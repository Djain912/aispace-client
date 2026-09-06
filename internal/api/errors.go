package api

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Error is an API-level failure (non-2xx response) or a transport failure.
type Error struct {
	Status     int             `json:"status,omitempty"`
	Code       string          `json:"code"`
	Message    string          `json:"message"`
	Details    json.RawMessage `json:"details,omitempty"`
	RetryAfter int             `json:"retry_after,omitempty"`
	cause      error
}

func (e *Error) Error() string { return e.Message }

// Unwrap exposes the transport cause, if any.
func (e *Error) Unwrap() error { return e.cause }

// ExitCode maps the error to the CLI exit code contract:
// 3 auth (401), 4 quota (402/413), 5 rate limited (429), 1 otherwise.
func (e *Error) ExitCode() int {
	switch e.Status {
	case http.StatusUnauthorized:
		return 3
	case http.StatusPaymentRequired, http.StatusRequestEntityTooLarge:
		return 4
	case http.StatusTooManyRequests:
		return 5
	}
	return 1
}

type wireError struct {
	Error struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Details json.RawMessage `json:"details"`
	} `json:"error"`
}

func errorFromResponse(resp *http.Response, body []byte) *Error {
	e := &Error{Status: resp.StatusCode}
	var w wireError
	if json.Unmarshal(body, &w) == nil && w.Error.Code != "" {
		e.Code = w.Error.Code
		e.Message = w.Error.Message
		e.Details = w.Error.Details
	}
	if e.Code == "" {
		e.Code = fallbackCode(resp.StatusCode)
	}
	if e.Message == "" {
		e.Message = fmt.Sprintf("%s (HTTP %d)", http.StatusText(resp.StatusCode), resp.StatusCode)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		e.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	}
	return e
}

func fallbackCode(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "unauthenticated"
	case http.StatusPaymentRequired:
		return "quota_exceeded"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusRequestEntityTooLarge:
		return "file_too_large"
	case http.StatusTooManyRequests:
		return "rate_limited"
	}
	if status >= 500 {
		return "internal"
	}
	return "http_error"
}
