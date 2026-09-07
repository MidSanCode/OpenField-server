package auth

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"
)

// Provider defines an OAuth2/OIDC provider interface.
type Provider interface {
	Name() string
	Config() *oauth2.Config
	ExchangeToken(ctx context.Context, code string) (*oauth2.Token, error)
	GetUserInfo(ctx context.Context, token *oauth2.Token) (*UserInfo, error)
}

// UserInfo holds the user information returned by an OIDC provider.
type UserInfo struct {
	OAuth2ID  string
	Email     string
	Username  string
	AvatarURL string
}

// BaseProvider provides common OAuth2 functionality.
type BaseProvider struct {
	name   string
	config *oauth2.Config
}

// Name returns the provider's identifier (e.g. "oidc").
func (p *BaseProvider) Name() string {
	return p.name
}

// Config returns the OAuth2 configuration used for authorization redirects
// and token exchange.
func (p *BaseProvider) Config() *oauth2.Config {
	return p.config
}

// ExchangeToken swaps an OAuth2 authorization code for an access token,
// wrapping transport failures with the exchange context.
func (p *BaseProvider) ExchangeToken(ctx context.Context, code string) (*oauth2.Token, error) {
	token, err := p.config.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange token: %w", err)
	}
	return token, nil
}
