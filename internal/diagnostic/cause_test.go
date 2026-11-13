package diagnostic

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
)

func TestCauseRedactsQuotedCredentials(t *testing.T) {
	secret := "private\ncredential"
	original := fmt.Errorf("proxy rejected %q, escaped %s", secret, url.QueryEscape(secret))
	err := Cause(original, secret)
	if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "credential") || !strings.Contains(err.Error(), "proxy rejected") {
		t.Fatalf("unsafe quoted secret: %v", err)
	}
}

func TestCauseUnwrapsURLRetainingNetworkType(t *testing.T) {
	dns := &net.DNSError{Err: "no such host", Name: "missing.test", IsNotFound: true}
	original := &url.Error{Op: "Post", URL: "https://user:pass@host.test/token?key=private", Err: dns} //nolint:gosec // Deliberately fake URL credentials prove the entire URL is omitted.
	err := Cause(original)
	var actual *net.DNSError
	if !errors.As(err, &actual) || actual != dns || !errors.Is(err, dns) || !strings.Contains(err.Error(), "no such host") || strings.Contains(err.Error(), "private") {
		t.Fatalf("DNS diagnostic: %v", err)
	}
}

func TestCauseRedactsURLsContainingQuotes(t *testing.T) {
	err := Cause(errors.New(`proxy refused https://user:pass'word@proxy.test/private-path?key=hidden`))
	for _, secret := range []string{"word", "private-path", "hidden"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("URL secret leaked: %v", err)
		}
	}
	if !strings.Contains(err.Error(), "proxy refused") {
		t.Fatalf("lost cause: %v", err)
	}
}

func TestCauseRedactsOverlappingCredentials(t *testing.T) {
	err := Cause(errors.New("rejected prefix-sensitive-suffix"), "prefix", "prefix-sensitive-suffix")
	if strings.Contains(err.Error(), "sensitive-suffix") {
		t.Fatalf("partial secret leaked: %v", err)
	}
}
