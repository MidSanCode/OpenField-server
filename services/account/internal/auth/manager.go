// Package auth implements the account service's authentication: the OIDC
// provider abstraction, callback handling and user provisioning for OAuth2
// sign-ups.
package auth

import (
	"context"
	"fmt"

	"github.com/openfield/server/pkg/config"
	"github.com/openfield/server/pkg/logger"
	"github.com/openfield/server/pkg/model"
	"github.com/openfield/server/pkg/repository"
)

// Manager manages the OIDC provider and authentication logic.
type Manager struct {
	provider Provider
	config   *config.Config
}

// NewManager creates a new auth manager with the configured OIDC provider.
func NewManager(cfg *config.Config) *Manager {
	provider := NewOIDCProvider(cfg.OIDC)

	return &Manager{
		provider: provider,
		config:   cfg,
	}
}

// GetProvider returns the OIDC provider.
func (m *Manager) GetProvider() Provider {
	return m.provider
}

// ResolveIdentity handles the OIDC callback exchange: swap the authorization
// code for tokens, fetch the user info, and return the identity together with
// every non-deleted OpenField account it is currently bound to (empty when
// none yet). No account is created here — the caller decides between
// first-account creation, direct login and the multi-account picker.
func (m *Manager) ResolveIdentity(ctx context.Context, code string) (*UserInfo, []*model.User, error) {
	token, err := m.provider.ExchangeToken(ctx, code)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to exchange token: %w", err)
	}

	userInfo, err := m.provider.GetUserInfo(ctx, token)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get user info: %w", err)
	}

	logger.Log.Info("oidc user info received", "email", userInfo.Email)

	if userInfo.OAuth2ID == "" {
		return nil, nil, fmt.Errorf("oauth provider returned no subject (sub) claim")
	}

	accounts, err := findUsersByOAuth2(m.provider.Name(), userInfo.OAuth2ID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to find accounts: %w", err)
	}
	return userInfo, accounts, nil
}

// CreateAccountFromOAuth2 provisions a brand-new OpenField account bound to
// the given OAuth identity (first login, or "add a new account" from the
// multi-account picker). New accounts must complete registration.
func (m *Manager) CreateAccountFromOAuth2(info *UserInfo) (*model.User, error) {
	user, err := createUserFromOAuth2(info)
	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}
	logger.Log.Info("new user created", "user_id", user.ID)
	return user, nil
}

// QuotaExceeded reports whether the identity already holds the maximum number
// of bound accounts (MaxOAuth2Accounts).
func (m *Manager) QuotaExceeded(provider, oauth2ID string) (bool, error) {
	n, err := countUsersByOAuth2(provider, oauth2ID)
	if err != nil {
		return false, err
	}
	return n >= MaxOAuth2Accounts, nil
}

// Bind handles the OIDC callback for an account-binding flow: exchange code,
// get user info, then link the OAuth2 identity to the given user account.
// One identity may be bound to several accounts (multi-account login), up to
// MaxOAuth2Accounts; an account itself stays bound to a single identity.
func (m *Manager) Bind(ctx context.Context, code string, userID int64) (*model.User, error) {
	token, err := m.provider.ExchangeToken(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange token: %w", err)
	}

	userInfo, err := m.provider.GetUserInfo(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("failed to get user info: %w", err)
	}

	logger.Log.Info("oidc bind user info received", "email", userInfo.Email)

	if userInfo.OAuth2ID == "" {
		return nil, fmt.Errorf("oauth provider returned no subject (sub) claim")
	}

	user, err := repository.NewUserRepository().GetByID(userID)
	if err != nil || user == nil {
		return nil, fmt.Errorf("failed to load user: %w", err)
	}
	// An account can be linked to only one identity; binding again must
	// target the same identity (idempotent) or be rejected.
	if user.OAuth2Provider != "" && user.OAuth2ID != "" {
		if user.OAuth2Provider == m.provider.Name() && user.OAuth2ID == userInfo.OAuth2ID {
			logger.Log.Info("oauth identity already bound", "user_id", userID)
			return user, nil
		}
		return nil, ErrOAuth2AlreadyBound
	}

	// One identity → several accounts, but never past the quota.
	exceeded, err := m.QuotaExceeded(m.provider.Name(), userInfo.OAuth2ID)
	if err != nil {
		return nil, fmt.Errorf("failed to check oauth quota: %w", err)
	}
	if exceeded {
		return nil, ErrOAuth2QuotaExceeded
	}

	user, err = repository.NewUserRepository().BindOAuth(userID, m.provider.Name(), userInfo.OAuth2ID, userInfo.Username)
	if err != nil {
		return nil, fmt.Errorf("failed to bind oauth identity: %w", err)
	}
	logger.Log.Info("oauth identity bound", "user_id", user.ID)
	return user, nil
}
