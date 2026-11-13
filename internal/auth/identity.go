package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

type (
	identity struct{ Subject, Email, AccountID string }
	claims   struct {
		Issuer          string          `json:"iss"`
		Subject         string          `json:"sub"`
		Email           string          `json:"email"`
		Audience        json.RawMessage `json:"aud"`
		AuthorizedParty string          `json:"azp"`
		Expires         int64           `json:"exp"`
		Issued          int64           `json:"iat"`
		NotBefore       int64           `json:"nbf"`
		Nonce           string          `json:"nonce"`
		AccountID       string          `json:"chatgpt_account_id"`
		Auth            struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	jwk struct {
		KeyType   string `json:"kty"`
		KeyID     string `json:"kid"`
		Algorithm string `json:"alg"`
		Use       string `json:"use"`
		N         string `json:"n"`
		E         string `json:"e"`
	}
)

func (s *Service) verifyIdentity(ctx context.Context, token, clientID, nonce string) (identity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return identity{}, authError("invalid ID token")
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return identity{}, authError("invalid ID token header")
	}
	var meta struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}
	if json.Unmarshal(header, &meta) != nil || meta.Algorithm != "RS256" || meta.KeyID == "" {
		return identity{}, authError("unsupported ID token signature")
	}
	key, err := s.identityKey(ctx, meta.KeyID)
	if err != nil {
		return identity{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return identity{}, authError("invalid ID token signature")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature) != nil {
		return identity{}, authError("ID token signature verification failed")
	}
	return s.validateClaims(parts[1], clientID, nonce)
}

func (s *Service) validateClaims(encoded, clientID, nonce string) (identity, error) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return identity{}, authError("invalid ID token claims")
	}
	var c claims
	if json.Unmarshal(data, &c) != nil {
		return identity{}, authError("invalid ID token claims")
	}
	if c.Issuer != s.config.Issuer || !validLifetime(c) {
		return identity{}, authError("ID token identity or lifetime is invalid")
	}
	if nonce != "" && c.Nonce != nonce {
		return identity{}, authError("ID token nonce mismatch")
	}
	if !validAudience(c, clientID) {
		return identity{}, authError("ID token audience mismatch")
	}
	account := c.AccountID
	if account == "" {
		account = c.Auth.AccountID
	}
	return identity{Subject: c.Subject, Email: c.Email, AccountID: account}, nil
}

func validAudience(c claims, clientID string) bool {
	if c.AuthorizedParty != "" && c.AuthorizedParty != clientID {
		return false
	}
	var audience string
	if json.Unmarshal(c.Audience, &audience) == nil {
		return audience == clientID
	}
	var audiences []string
	if json.Unmarshal(c.Audience, &audiences) != nil {
		return false
	}
	return slices.Contains(audiences, clientID) && (len(audiences) == 1 || c.AuthorizedParty == clientID)
}

func (s *Service) identityKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	var discovery struct {
		Issuer string `json:"issuer"`
		JWKS   string `json:"jwks_uri"`
	}
	if err := s.getJSON(ctx, s.config.Issuer+"/.well-known/openid-configuration", &discovery); err != nil {
		return nil, err
	}
	endpoint, err := url.Parse(discovery.JWKS)
	issuer, issuerErr := url.Parse(s.config.Issuer)
	if err != nil || issuerErr != nil || discovery.Issuer != s.config.Issuer || !sameOrigin(endpoint, issuer) {
		return nil, authError("untrusted identity discovery")
	}
	var keys struct {
		Keys []jwk `json:"keys"`
	}
	if err = s.getJSON(ctx, discovery.JWKS, &keys); err != nil {
		return nil, err
	}
	for _, key := range keys.Keys {
		if validKey(key, kid) {
			return rsaKey(key)
		}
	}
	return nil, authError("identity verification key unavailable")
}

func rsaKey(key jwk) (*rsa.PublicKey, error) {
	n, nErr := base64.RawURLEncoding.DecodeString(key.N)
	e, eErr := base64.RawURLEncoding.DecodeString(key.E)
	if nErr != nil || eErr != nil || len(n) < 256 || len(e) > 4 || len(e) == 0 {
		return nil, authError("invalid identity key")
	}
	exponent := new(big.Int).SetBytes(e).Int64()
	if exponent < 3 || exponent > 2147483647 {
		return nil, authError("invalid identity exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exponent)}, nil
}

func (s *Service) getJSON(ctx context.Context, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return authError("build identity discovery request")
	}
	response, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return authError("identity verification is temporarily unavailable")
	}
	defer response.Body.Close() //nolint:errcheck // Identity decoding already determines the read result.
	if response.StatusCode != http.StatusOK {
		return authError("identity verification is temporarily unavailable")
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target) != nil {
		return authError("invalid identity discovery response")
	}
	return nil
}

func validLifetime(c claims) bool {
	return c.Subject != "" && c.Expires > time.Now().Unix() && c.Issued > 0 && c.Issued <= time.Now().Add(5*time.Second).Unix() && c.NotBefore <= time.Now().Add(5*time.Second).Unix()
}

func sameOrigin(a, b *url.URL) bool { return a.Scheme == b.Scheme && a.Host == b.Host && a.User == nil }

func validKey(key jwk, kid string) bool {
	return key.KeyID == kid && key.KeyType == "RSA" && (key.Algorithm == "" || key.Algorithm == "RS256") && (key.Use == "" || key.Use == "sig")
}
