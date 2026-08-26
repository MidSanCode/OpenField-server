package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/openfield/server/pkg/config"
	"golang.org/x/oauth2"
)

// OIDCProvider implements the generic OIDC provider.
type OIDCProvider struct {
	*BaseProvider
	userInfoURL string
}

// NewOIDCProvider creates a new generic OIDC provider.
func NewOIDCProvider(cfg config.OIDCConfig) *OIDCProvider {
	base := &BaseProvider{
		name: "oidc",
		config: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Scopes:       cfg.Scopes,
			Endpoint: oauth2.Endpoint{
				AuthURL:  cfg.IssuerURL + "/auth",
				TokenURL: cfg.IssuerURL + "/token",
			},
		},
	}
	return &OIDCProvider{
		BaseProvider: base,
		userInfoURL:  cfg.IssuerURL + "/me",
	}
}

// GetUserInfo fetches the user profile from the OIDC provider.
func (p *OIDCProvider) GetUserInfo(ctx context.Context, token *oauth2.Token) (*UserInfo, error) {
	client := p.Config().Client(ctx, token)
	resp, err := client.Get(p.userInfoURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get user info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo endpoint returned status %d", resp.StatusCode)
	}

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to decode user info: %w", err)
	}

	// Standard OIDC userinfo identifies the end user by the "sub" claim;
	// some non-conforming providers expose "id" instead.
	subject := getString(data, "sub")
	if subject == "" {
		subject = getString(data, "id")
	}

	userInfo := &UserInfo{
		OAuth2ID:  subject,
		Username:  getString(data, "name"),
		Email:     getString(data, "email"),
		AvatarURL: getString(data, "picture"),
	}

	return userInfo, nil
}

func getString(data map[string]interface{}, key string) string {
	if v, ok := data[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
