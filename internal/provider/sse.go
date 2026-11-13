package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/iamnalinor/qual/internal/diagnostic"
	"github.com/iamnalinor/qual/internal/domain"
)

// readSSE supports comments, CRLF, multi-line data and arbitrarily split TCP reads.
func readSSE(ctx context.Context, r io.Reader, consume func([]byte) error, secrets ...string) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	var lines []string
	dispatch := func() error {
		if len(lines) == 0 {
			return nil
		}
		data := []byte(strings.Join(lines, "\n"))
		lines = nil
		return consume(data)
	}
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := scanner.Err(); err != nil {
		return &domain.Error{Code: domain.ErrIncomplete, Message: "provider stream read failed", Cause: diagnostic.Cause(err, secrets...)}
	}
	return dispatch()
}

func retryAfter(value string) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		return max(time.Until(date), 0)
	}
	return 0
}

type object map[string]json.RawMessage

func decode(data []byte) (object, error) {
	var v object
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, problem(domain.ErrIncomplete, "invalid provider event JSON")
	}
	return v, nil
}

func (o object) str(k string) string {
	var v string
	if json.Unmarshal(o[k], &v) != nil {
		return ""
	}
	return v
}

func (o object) num(k string) int64 {
	var v int64
	if json.Unmarshal(o[k], &v) != nil {
		return 0
	}
	return v
}

func (o object) obj(k string) object {
	var v object
	if json.Unmarshal(o[k], &v) != nil {
		return nil
	}
	return v
}

func (o object) list(k string) []object {
	var v []object
	if json.Unmarshal(o[k], &v) != nil {
		return nil
	}
	return v
}

func raw(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic("invalid internal provider JSON value")
	}
	return data
}
