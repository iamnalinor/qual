package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/iamnalinor/qual/internal/diagnostic"
	"github.com/iamnalinor/qual/internal/domain"
)

// DeviceCode is the user-visible legacy Codex authorization challenge.
type (
	DeviceCode     struct{ VerificationURL, UserCode string }
	deviceResponse struct {
		DeviceID string          `json:"device_auth_id"`
		UserCode string          `json:"user_code"`
		Interval json.RawMessage `json:"interval"`
		Code     string          `json:"authorization_code"`
		Verifier string          `json:"code_verifier"`
	}
)

// DeviceLogin explicitly selects the legacy Codex device grant. It never switches
// a token-sharing authorization into the legacy billing route.
func (s *Service) DeviceLogin(ctx context.Context, show func(DeviceCode) error) (Credentials, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	device, status, err := s.deviceRequest(ctx, "usercode", map[string]string{"client_id": legacyClientID})
	if err != nil {
		return Credentials{}, err
	}
	if status != http.StatusOK || device.DeviceID == "" || device.UserCode == "" {
		return Credentials{}, authError("device authorization unavailable")
	}
	if show == nil {
		return Credentials{}, authError("device-code display callback required")
	}
	if err = show(DeviceCode{VerificationURL: s.config.Issuer + "/codex/device", UserCode: device.UserCode}); err != nil {
		return Credentials{}, err
	}
	result, err := s.pollDevice(ctx, device)
	if err != nil {
		return Credentials{}, err
	}
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {legacyClientID}, "code": {result.Code}, "code_verifier": {result.Verifier}, "redirect_uri": {s.config.Issuer + "/deviceauth/callback"}}
	tokens, err := s.tokenRequest(ctx, "codex", form)
	if err != nil {
		return Credentials{}, err
	}
	c, err := s.registration(ctx, tokens, "codex", legacyClientID, "", "", Credentials{})
	if err != nil {
		return Credentials{}, err
	}
	if err = s.Save(ctx, c); err != nil {
		return Credentials{}, err
	}
	return c, nil
}

func deviceInterval(data json.RawMessage) time.Duration {
	var text string
	if json.Unmarshal(data, &text) != nil {
		text = string(data)
	}
	seconds, err := strconv.Atoi(text)
	if err != nil {
		seconds = 5
	}
	return time.Duration(min(max(seconds, 1), 300)+3) * time.Second
}

func (s *Service) pollDevice(ctx context.Context, device deviceResponse) (deviceResponse, error) {
	timer := time.NewTimer(deviceInterval(device.Interval))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return deviceResponse{}, ctx.Err()
		case <-timer.C:
		}
		result, status, err := s.deviceRequest(ctx, "token", map[string]string{"device_auth_id": device.DeviceID, "user_code": device.UserCode})
		if err != nil {
			return deviceResponse{}, err
		}
		if status == http.StatusOK {
			if result.Code == "" || result.Verifier == "" {
				return deviceResponse{}, authError("invalid device grant")
			}
			return result, nil
		}
		if status != http.StatusForbidden && status != http.StatusNotFound {
			return deviceResponse{}, authError("device authorization rejected")
		}
		timer.Reset(deviceInterval(device.Interval))
	}
}

func (s *Service) deviceRequest(ctx context.Context, operation string, body map[string]string) (deviceResponse, int, error) {
	secrets := make([]string, 0, len(body))
	for _, value := range body {
		secrets = append(secrets, value)
	}
	data, err := json.Marshal(body)
	if err != nil {
		return deviceResponse{}, 0, &domain.Error{Code: domain.ErrAuth, Message: "encode device authorization", Cause: diagnostic.Cause(err, secrets...)}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.config.Issuer+"/api/accounts/deviceauth/"+operation, bytes.NewReader(data))
	if err != nil {
		return deviceResponse{}, 0, &domain.Error{Code: domain.ErrAuth, Message: "build device authorization", Cause: diagnostic.Cause(err, secrets...)}
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return deviceResponse{}, 0, ctx.Err()
		}
		return deviceResponse{}, 0, &domain.Error{Code: domain.ErrAuth, Message: "device authorization transport failed", Cause: diagnostic.Cause(err, secrets...)}
	}
	defer response.Body.Close() //nolint:errcheck // Device grant decoding already determines the read result.
	var result deviceResponse
	if response.StatusCode == http.StatusOK {
		if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
			return result, response.StatusCode, &domain.Error{Code: domain.ErrAuth, Message: "invalid device authorization response", Cause: diagnostic.Cause(err, secrets...)}
		}
	}
	return result, response.StatusCode, nil
}
