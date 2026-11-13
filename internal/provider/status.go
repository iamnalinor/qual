package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/iamnalinor/qual/internal/diagnostic"
	"github.com/iamnalinor/qual/internal/domain"
)

// Bound both the response read and the printable reason. Oversized or unknown
// bodies are omitted entirely; only recognized structured error fields are used.
const (
	maxErrorBody   = 16 << 10
	maxErrorReason = 1024
)

func statusError(r *http.Response, secrets ...string) *domain.Error {
	code := domain.ErrorCode("provider_http")
	if r.StatusCode == http.StatusUnauthorized || r.StatusCode == http.StatusForbidden {
		code = domain.ErrAuth
	}
	retry := r.StatusCode == http.StatusTooManyRequests || r.StatusCode >= 500
	return &domain.Error{Code: code, Message: http.StatusText(r.StatusCode), HTTPStatus: r.StatusCode, Retryable: retry, RetryAfter: retryAfter(r.Header.Get("Retry-After")), Cause: responseReason(r.Body, secrets)}
}

func responseReason(body io.Reader, secrets []string) error {
	if body == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(body, maxErrorBody+1))
	if err != nil {
		return errors.New(capReason(diagnostic.Cause(fmt.Errorf("read provider error response: %w", err), secrets...).Error()))
	}
	if len(data) > maxErrorBody {
		return nil
	}
	reason := structuredReason(data)
	if reason == "" {
		return nil
	}
	safe := diagnostic.Cause(errors.New(reason), secrets...).Error()
	// Do not retain the raw API reason in the error chain.
	return errors.New(capReason(safe))
}

func structuredReason(data []byte) string {
	var envelope struct {
		Error *struct {
			Message string          `json:"message"`
			Code    json.RawMessage `json:"code"`
			Type    string          `json:"type"`
			Status  string          `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.Error == nil {
		return ""
	}
	value := envelope.Error
	// Gemini code is numeric HTTP status; its named status is more useful. The
	// OpenAI-compatible schemas use a string code alongside message/type.
	var code string
	if err := json.Unmarshal(value.Code, &code); err != nil {
		code = ""
	}
	parts := make([]string, 0, 4)
	for _, part := range []string{value.Type, code, value.Status, value.Message} {
		if part != "" && !slices.Contains(parts, part) {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ": ")
}

func capReason(safe string) string {
	// Redact before truncating so a credential crossing the boundary cannot leave
	// a visible prefix. Do not retain the raw API reason in the error chain.
	if len(safe) > maxErrorReason {
		end := maxErrorReason - len("…")
		for !utf8.ValidString(safe[:end]) {
			end--
		}
		safe = safe[:end] + "…"
	}
	return safe
}
