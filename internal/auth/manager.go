package auth

import (
	"context"
	"fmt"

	"github.com/openfield/server/internal/config"
	"github.com/openfield/server/internal/logger"
	"github.com/openfield/server/internal/model"
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

// Authenticate handles the OIDC callback: exchange code, get user info, find/create user.
func (m *Manager) Authenticate(ctx context.Context, code string) (*model.User, error) {
	token, err := m.provider.ExchangeToken(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange token: %w", err)
	}

	userInfo, err := m.provider.GetUserInfo(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("failed to get user info: %w", err)
	}

	logger.Log.Info("oidc user info received", "email", userInfo.Email)

	// Try to find existing user by oidc id
	user, err := findUserByOAuth2(m.provider.Name(), userInfo.OAuth2ID)
	if err != nil {
		return nil, fmt.Errorf("failed to find user: %w", err)
	}

	if user == nil {
		// Create new user
		user, err = createUserFromOAuth2(userInfo)
		if err != nil {
			return nil, fmt.Errorf("failed to create user: %w", err)
		}
		logger.Log.Info("new user created", "user_id", user.ID)
	} else {
		logger.Log.Info("existing user authenticated", "user_id", user.ID)
	}

	return user, nil
}
