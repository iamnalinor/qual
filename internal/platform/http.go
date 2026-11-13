package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func sameOrigin(a, b *url.URL) bool { return a.Scheme == b.Scheme && a.Host == b.Host && b.User == nil }

func (s *Source) get(ctx context.Context, endpoint, host, kind string, out any) (headers http.Header, resultErr error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return nil, failure("source_unavailable", "invalid platform endpoint", nil)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, failure("source_unavailable", "create platform request", nil)
	}
	s.authorize(request, host, kind)
	request.Header.Set("Accept", "application/json")
	client := s.client(u)
	response, err := client.Do(request)
	if err != nil {
		return nil, failure("source_unavailable", "platform request failed", nil)
	}
	defer func() { resultErr = errors.Join(resultErr, response.Body.Close()) }()
	if response.StatusCode != http.StatusOK {
		code := "source_unavailable"
		if response.StatusCode == 401 || response.StatusCode == 403 {
			code = "auth"
		}
		return nil, failure(code, "platform read denied or unavailable", nil)
	}
	if err = decodeResponse(response.Body, out); err != nil {
		return nil, err
	}
	return response.Header, nil
}

func (s *Source) pages(ctx context.Context, endpoint, host, kind string) ([]json.RawMessage, error) {
	return s.paged(ctx, endpoint, host, kind, "")
}

func (s *Source) paged(ctx context.Context, endpoint, host, kind, field string) ([]json.RawMessage, error) {
	origin, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var result []json.RawMessage
	for page := 0; endpoint != ""; page++ {
		if page >= 100 || seen[endpoint] {
			return nil, failure("source_unavailable", "pagination limit or cycle", nil)
		}
		seen[endpoint] = true
		var raw json.RawMessage
		headers, err := s.get(ctx, endpoint, host, kind, &raw)
		if err != nil {
			return nil, err
		}
		items, err := pageItems(raw, field)
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
		next := nextPage(headers, endpoint)
		next, err = paginationURL(origin, next)
		if err != nil {
			return nil, err
		}
		endpoint = next
	}
	return result, nil
}

func nextPage(headers http.Header, current string) string {
	for _, link := range strings.Split(headers.Get("Link"), ",") {
		parts := strings.Split(link, ";")
		if len(parts) > 1 && strings.TrimSpace(parts[1]) == `rel="next"` {
			return strings.Trim(strings.TrimSpace(parts[0]), "<>")
		}
	}
	if next := headers.Get("X-Next-Page"); next != "" {
		u, err := url.Parse(current)
		if err == nil {
			q := u.Query()
			q.Set("page", next)
			u.RawQuery = q.Encode()
			return u.String()
		}
	}
	return ""
}

func pageItems(raw json.RawMessage, field string) ([]json.RawMessage, error) {
	if field != "" {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil, failure("source_unavailable", "invalid page envelope", err)
		}
		raw = object[field]
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, failure("source_unavailable", "invalid page items", err)
	}
	return items, nil
}

func (s *Source) client(u *url.URL) http.Client {
	client := http.Client{Timeout: 30 * time.Second}
	if s.config.HTTPClient != nil {
		client = *s.config.HTTPClient
		if client.Timeout == 0 {
			client.Timeout = 30 * time.Second
		}
	}
	client.CheckRedirect = func(next *http.Request, _ []*http.Request) error {
		if !sameOrigin(u, next.URL) {
			return failure("source_unavailable", "cross-origin redirect rejected", nil)
		}
		return http.ErrUseLastResponse
	}
	return client
}

func paginationURL(origin *url.URL, next string) (string, error) {
	if next == "" {
		return "", nil
	}
	parsed, err := url.Parse(next)
	if err != nil {
		return "", failure("source_unavailable", "invalid pagination URL", nil)
	}
	resolved := origin.ResolveReference(parsed)
	if !sameOrigin(origin, resolved) {
		return "", failure("source_unavailable", "cross-origin pagination rejected", nil)
	}
	return resolved.String(), nil
}

func (s *Source) authorize(request *http.Request, host, kind string) {
	if token := s.config.Tokens[host]; token != "" {
		if kind == "gitlab" {
			request.Header.Set("PRIVATE-TOKEN", token)
		} else {
			request.Header.Set("Authorization", "Bearer "+token)
		}
	}
}

func decodeResponse(bodyReader io.Reader, out any) error {
	body, err := io.ReadAll(io.LimitReader(bodyReader, 16<<20+1))
	if err != nil || len(body) > 16<<20 {
		return failure("source_unavailable", "platform response exceeded limit or failed", nil)
	}
	if err = json.Unmarshal(body, out); err != nil {
		return failure("source_unavailable", "invalid platform response", err)
	}
	return nil
}
