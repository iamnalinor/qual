// Package diagnostic preserves actionable adapter failures without exposing
// credential values or request URLs in diagnostic output.
package diagnostic

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

var requestURL = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s<>]+`)

type safeCause struct {
	message string
	cause   error
}

func (e *safeCause) Error() string { return e.message }
func (e *safeCause) Unwrap() error { return e.cause }

// Cause removes URL wrappers and redacts explicit sensitive values from the
// printable cause. The underlying failure remains available to errors.Is/As.
// Callers must supply credentials that a custom transport could echo.
func Cause(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	for {
		wrapped, ok := err.(*url.Error) //nolint:errorlint // Strip only direct URL layers; retain other wrappers and their operation context.
		if !ok || wrapped.Err == nil {
			break
		}
		err = wrapped.Err
	}
	message := err.Error()
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		message = fmt.Sprintf("%s at byte %d", message, syntax.Offset)
	}
	var field *json.UnmarshalTypeError
	if errors.As(err, &field) {
		message = fmt.Sprintf("%s at byte %d", message, field.Offset)
	}
	return &safeCause{message: sanitize(message, secrets), cause: err}
}

func sanitize(message string, secrets []string) string {
	// Replace complete credentials first, including escaped variants, in one pass.
	// Replacing shorter values first could leave a longer credential's suffix.
	var values []string
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		quoted := strconv.Quote(secret)
		values = append(values, secret, url.QueryEscape(secret), url.PathEscape(secret), quoted[1:len(quoted)-1])
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	pairs := make([]string, 0, 2*len(values))
	for _, value := range values {
		pairs = append(pairs, value, "[redacted]")
	}
	message = strings.NewReplacer(pairs...).Replace(message)
	message = requestURL.ReplaceAllString(message, "[redacted URL]")
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return ' '
		}
		return r
	}, message)
	return strings.TrimSpace(message)
}
