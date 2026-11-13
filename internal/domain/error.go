package domain

import (
	"errors"
	"time"
)

// Error associates a stable machine code with contextual diagnostic text.
type Error struct {
	Code       ErrorCode     `json:"code"`
	Message    string        `json:"message"`
	Cause      error         `json:"-"`
	Retryable  bool          `json:"retryable,omitempty"`
	RetryAfter time.Duration `json:"retry_after,omitempty"`
	HTTPStatus int           `json:"http_status,omitempty"`
}

func (e *Error) Error() string {
	message := string(e.Code) + ": " + e.Message
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}

// Unwrap preserves the underlying failure for errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.Cause }

// CodeOf extracts the nearest domain error code, or an empty code for uncoded errors.
func CodeOf(err error) ErrorCode {
	var coded *Error
	if errors.As(err, &coded) && coded != nil {
		return coded.Code
	}
	return ""
}
