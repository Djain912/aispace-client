package mcp

import (
	"context"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/aispace-sh/aispace-client/internal/api"
)

type ToolError struct {
	OK                bool   `json:"ok"`
	Category          string `json:"category"`
	Code              string `json:"code"`
	Message           string `json:"message"`
	Retryable         bool   `json:"retryable"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
	RequestID         string `json:"request_id,omitempty"`
}

var (
	credentialPattern = regexp.MustCompile(`(?i)(ask_[A-Za-z0-9_-]+|age-secret-key-[A-Za-z0-9_-]+|bearer\s+[A-Za-z0-9._~-]+)`)
	urlPattern        = regexp.MustCompile(`https?://[^\s]+`)
)

func mapError(err error, fallbackCategory, fallbackCode string) ToolError {
	out := ToolError{OK: false, Category: fallbackCategory, Code: fallbackCode, Message: safeMessage(err), Retryable: false}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		out.Category, out.Code, out.Message = "cancelled", "cancelled", "operation cancelled"
		return out
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		out.Category, out.Code, out.Message, out.Retryable = "network", "network", "network request failed", true
		return out
	}
	var ae *api.Error
	if !errors.As(err, &ae) {
		return out
	}
	out.Code = ae.Code
	out.RequestID = bounded(ae.RequestID, 256)
	out.Message = safeMessage(errors.New(ae.Message))
	switch ae.Code {
	case "bad_request", "invalid_expires", "file_too_large", "missing_content_length", "checksum_mismatch", "bad_response", "config":
		out.Category = "input"
	case "unauthenticated", "invalid_key", "key_revoked":
		out.Category, out.Message = "auth", "aispace authentication failed; configure a valid AISPACE_KEY"
	case "forbidden":
		out.Category = "permission"
	case "not_found":
		out.Category = "not_found"
	case "quota_exceeded", "allowance_exceeded", "payment_required", "monthly_upload_cap", "monthly_download_cap":
		out.Category = "quota"
	case "rate_limited":
		out.Category, out.Retryable = "rate_limit", true
	case "conflict":
		out.Category, out.Retryable = "conflict", ae.RetryAfter > 0
	case "network":
		out.Category, out.Message, out.Retryable = "network", "network request failed", true
	case "meter_unavailable", "internal":
		out.Category, out.Retryable = "service", ae.RetryAfter > 0
	default:
		if ae.Status == 401 {
			out.Category, out.Message = "auth", "aispace authentication failed; configure a valid AISPACE_KEY"
		} else if ae.Status == 403 {
			out.Category = "permission"
		} else if ae.Status == 404 {
			out.Category = "not_found"
		} else if ae.Status >= 500 {
			out.Category = "service"
		}
	}
	if ae.RetryAfter > 0 && (out.Category == "rate_limit" || out.Category == "conflict" || out.Category == "service") {
		out.RetryAfterSeconds = ae.RetryAfter
	}
	return out
}

func safeMessage(err error) string {
	if err == nil {
		return "operation failed"
	}
	msg := credentialPattern.ReplaceAllString(err.Error(), "[redacted]")
	msg = urlPattern.ReplaceAllStringFunc(msg, func(raw string) string {
		u, parseErr := url.Parse(strings.TrimRight(raw, ".,);"))
		if parseErr != nil {
			return "[redacted URL]"
		}
		return u.Scheme + "://" + u.Host + "/[redacted]"
	})
	return bounded(msg, 1024)
}

func bounded(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
