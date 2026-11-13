package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/iamnalinor/qual/internal/diagnostic"
	"github.com/iamnalinor/qual/internal/domain"
)

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int64  `json:"expires_in"`
}

func (s *Service) tokenRequest(ctx context.Context, kind string, form url.Values) (tokenResponse, error) {
	secrets := make([]string, 0, len(form))
	for _, values := range form {
		secrets = append(secrets, values...)
	}
	path := "/api/accounts/oauth/token"
	if kind == "codex" {
		path = "/oauth/token"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.config.Issuer+path, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, &domain.Error{Code: domain.ErrAuth, Message: "build token exchange", Cause: diagnostic.Cause(err, secrets...)}
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return tokenResponse{}, ctx.Err()
		}
		return tokenResponse{}, &domain.Error{Code: domain.ErrAuth, Message: "token exchange failed; restart authorization rather than retrying the same grant", Cause: diagnostic.Cause(err, secrets...)}
	}
	defer response.Body.Close() //nolint:errcheck // Token decoding already determines the read result.
	if response.StatusCode != http.StatusOK {
		return tokenResponse{}, authError("token grant rejected; sign in again")
	}
	var result tokenResponse
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return result, &domain.Error{Code: domain.ErrAuth, Message: "invalid token response", Cause: diagnostic.Cause(err, secrets...)}
	}
	if kind == "codex" && result.ExpiresIn == 0 {
		result.ExpiresIn = 3600
	}
	if err = validateToken(result, kind); err != nil {
		return result, err
	}
	return result, nil
}

func validateToken(t tokenResponse, kind string) error {
	if kind == "codex" {
		return validateLegacyToken(t)
	}
	// Identity-only consent is still a valid signed-in account. No inference is
	// authorized unless the server explicitly grants the sharing scope.
	if identityOnly(t) {
		return nil
	}
	if t.AccessToken == "" || t.ExpiresIn <= 0 || t.ExpiresIn > 86400 || !strings.EqualFold(t.TokenType, "Bearer") || t.Scope == "" {
		return authError("incomplete token response")
	}
	if hasScope(strings.Fields(t.Scope), "offline_access") && t.RefreshToken == "" {
		return authError("refresh credential missing")
	}
	return nil
}

func (s *Service) refresh(ctx context.Context, c Credentials) (Credentials, error) {
	if c.RefreshToken == "" {
		return Credentials{}, authError("refresh credential missing; sign in again")
	}
	// Persist intent before sending a rotating grant. If the transport outcome is
	// unknown or persistence fails, a later request cannot replay the old token.
	c.RefreshBlocked = true
	if err := s.saveLocked(ctx, c); err != nil {
		return Credentials{}, err
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {c.ClientID}, "refresh_token": {c.RefreshToken}}
	if c.Kind == "chatgpt" {
		form.Set("resource", resource)
	}
	tokens, err := s.tokenRequest(ctx, c.Kind, form)
	if err != nil {
		return Credentials{}, err
	}
	if err = s.refreshIdentity(ctx, tokens, &c); err != nil {
		return Credentials{}, err
	}
	c.AccessToken = tokens.AccessToken
	if tokens.RefreshToken != "" {
		c.RefreshToken = tokens.RefreshToken
	}
	c.ExpiresAt = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	if tokens.Scope != "" {
		c.Scopes = strings.Fields(tokens.Scope)
	}
	c.RefreshBlocked = false
	if err = s.saveLocked(ctx, c); err != nil {
		return Credentials{}, err
	}
	if c.Kind == "chatgpt" && !hasScope(c.Scopes, SharingScope) {
		return Credentials{}, authError("ChatGPT sharing consent is no longer granted")
	}
	return c, nil
}

func validateLegacyToken(t tokenResponse) error {
	if t.AccessToken == "" || t.RefreshToken == "" {
		return authError("incomplete legacy token response")
	}
	return nil
}

func identityOnly(t tokenResponse) bool {
	return t.IDToken != "" && t.AccessToken == "" && !hasScope(strings.Fields(t.Scope), SharingScope) && !hasScope(strings.Fields(t.Scope), "offline_access")
}

func (s *Service) refreshIdentity(ctx context.Context, tokens tokenResponse, c *Credentials) error {
	if tokens.IDToken == "" {
		return nil
	}
	claims, err := s.verifyIdentity(ctx, tokens.IDToken, c.ClientID, "")
	if err != nil {
		return err
	}
	if claims.Subject != c.Subject {
		return authError("refresh returned a different identity")
	}
	c.IDToken = tokens.IDToken
	return nil
}
