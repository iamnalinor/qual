package diagnostic

import (
	"strings"
	"unicode/utf8"
)

// GitCause retains a bounded Git diagnostic and the original subprocess error.
// Environment-supplied configuration and credentials may appear in Git stderr.
func GitCause(err error, stderr string, environment []string) error {
	var secrets []string
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		name = strings.ToUpper(name)
		if ok && sensitiveEnvironment(name) {
			secrets = append(secrets, value)
			fields := strings.Fields(value)
			if len(fields) == 3 && strings.EqualFold(fields[0], "Authorization:") {
				secrets = append(secrets, fields[2])
			}
		}
	}
	message := err.Error()
	if detail := strings.TrimSpace(stderr); detail != "" {
		message += ": " + detail
	}
	message = sanitize(message, secrets)
	const limit = 4096
	if len(message) > limit {
		message = message[:limit-3]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
		message += "..."
	}
	return &safeCause{message: message, cause: err}
}

func sensitiveEnvironment(name string) bool {
	if strings.HasPrefix(name, "GIT_CONFIG_VALUE_") {
		return true
	}
	for _, suffix := range []string{"KEY", "TOKEN", "PASSWORD", "SECRET", "AUTH"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}
