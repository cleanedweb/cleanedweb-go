package cleanedweb

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrorKind classifies an error. The values are shared with the other CleanedWeb SDKs and the
// conformance cases.
type ErrorKind string

// Error kinds.
const (
	KindMissingAPIKey        ErrorKind = "missing_api_key"
	KindInvalidArgument      ErrorKind = "invalid_argument"
	KindConnection           ErrorKind = "connection"
	KindAuthentication       ErrorKind = "authentication"
	KindPermissionDenied     ErrorKind = "permission_denied"
	KindNotFound             ErrorKind = "not_found"
	KindVersionNotFound      ErrorKind = "version_not_found"
	KindInsufficientCredit   ErrorKind = "insufficient_credit"
	KindUsageBudgetExceeded  ErrorKind = "usage_budget_exceeded"
	KindIdempotencyConflict  ErrorKind = "idempotency_conflict"
	KindVersionConflict      ErrorKind = "version_conflict"
	KindInvalidRequest       ErrorKind = "invalid_request"
	KindRateLimited          ErrorKind = "rate_limited"
	KindServer               ErrorKind = "server"
	KindRedirect             ErrorKind = "redirect"
	KindAPIStatus            ErrorKind = "api_status"
	markerRunFailed                    = "run_failed"
	markerFailedRunReplayed            = "failed_run_replayed"
	definitionVersionCodeKey           = "builder_definition_version"
)

// kindError is a sentinel for errors.Is.
type kindError struct{ kind ErrorKind }

func (e *kindError) Error() string { return "cleanedweb: " + string(e.kind) }

// Sentinels for errors.Is(err, cleanedweb.ErrRateLimited) and friends. Every error returned by
// this package matches exactly one of them.
var (
	ErrMissingAPIKey       error = &kindError{KindMissingAPIKey}
	ErrInvalidArgument     error = &kindError{KindInvalidArgument}
	ErrConnection          error = &kindError{KindConnection}
	ErrAuthentication      error = &kindError{KindAuthentication}
	ErrPermissionDenied    error = &kindError{KindPermissionDenied}
	ErrNotFound            error = &kindError{KindNotFound}
	ErrVersionNotFound     error = &kindError{KindVersionNotFound}
	ErrInsufficientCredit  error = &kindError{KindInsufficientCredit}
	ErrUsageBudgetExceeded error = &kindError{KindUsageBudgetExceeded}
	ErrIdempotencyConflict error = &kindError{KindIdempotencyConflict}
	ErrVersionConflict     error = &kindError{KindVersionConflict}
	ErrInvalidRequest      error = &kindError{KindInvalidRequest}
	ErrRateLimited         error = &kindError{KindRateLimited}
	ErrServer              error = &kindError{KindServer}
	ErrRedirect            error = &kindError{KindRedirect}
	ErrAPIStatus           error = &kindError{KindAPIStatus}
)

// NotFound reports whether err is a 404 (also a catalog_version_not_found).
func NotFound(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrVersionNotFound)
}

// APIError is an error answer of the API: an HTTP status of 400 or more, or a redirect.
//
// Deciding what to do with an error that belongs to a run:
//
//   - RunFailed is true: the run failed, final for that idempotency key (the same answer is
//     replayed for 7 days). Use a new key to run again.
//   - Unsure is true: a gateway error or timeout that does not show whether the run failed. Keep the
//     key and read the result with it (Catalog.Result) before you decide.
type APIError struct {
	// Kind classifies the error; see the Kind constants.
	Kind ErrorKind
	// StatusCode is the HTTP status.
	StatusCode int
	// Code is detail.code (or detail.error_code) when the body has one, for example
	// "insufficient_units".
	Code string
	// Message is the human message of the body.
	Message string
	// Detail is the parsed "detail" of the body (a string, an object or a list).
	Detail any
	// Body is the raw response body (at most 64 KiB).
	Body []byte
	// Header holds the response headers.
	Header http.Header
	// RetryAfter is the wait the server asked for (Retry-After header, then detail.retry_after_seconds,
	// retryAfterSeconds or retry_after). Zero when none; see HasRetryAfter.
	RetryAfter time.Duration
	// IdempotencyKey is the key of the request, when it carried one.
	IdempotencyKey string

	hasRetryAfter bool
}

// HasRetryAfter reports whether the server asked for a wait (which may be zero).
func (e *APIError) HasRetryAfter() bool { return e.hasRetryAfter }

// Error implements error.
func (e *APIError) Error() string {
	var b strings.Builder
	if e.Kind == KindRedirect {
		return fmt.Sprintf("HTTP %d redirect: nothing ran; check the base URL (the API origin is %s)", e.StatusCode, DefaultBaseURL)
	}
	fmt.Fprintf(&b, "HTTP %d", e.StatusCode)
	if e.Code != "" {
		b.WriteString(" " + e.Code)
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	if id := e.RunID(); id != "" {
		b.WriteString(" (run " + id + ")")
	}
	return b.String()
}

// Is makes errors.Is(err, cleanedweb.ErrRateLimited) and the other sentinels work.
func (e *APIError) Is(target error) bool {
	k, ok := target.(*kindError)
	return ok && k.kind == e.Kind
}

func (e *APIError) detailMap() map[string]any {
	m, _ := e.Detail.(map[string]any)
	return m
}

func (e *APIError) detailString(key string) string {
	if v, ok := e.detailMap()[key].(string); ok {
		return v
	}
	return ""
}

// RunID is the failed run's id, when the error is a failed run's.
func (e *APIError) RunID() string { return e.detailString("run_id") }

// ErrorCode is "run_failed" or "failed_run_replayed" when the error marks a failed run, else "".
func (e *APIError) ErrorCode() string {
	switch c := e.detailString("error_code"); c {
	case markerRunFailed, markerFailedRunReplayed:
		return c
	}
	return ""
}

// RunFailed is true when the run failed: final for the idempotency key; use a new key to run again.
func (e *APIError) RunFailed() bool { return e.ErrorCode() != "" }

// Replayed is true when the error answers a replay of the key (detail.replayed).
func (e *APIError) Replayed() bool {
	v, _ := e.detailMap()["replayed"].(bool)
	return v
}

// Unsure is true when the error does not show whether the run failed (a gateway error or a
// timeout): the run may have started.
func (e *APIError) Unsure() bool {
	return !e.RunFailed() && (e.StatusCode >= 500 || e.StatusCode == http.StatusRequestTimeout)
}

// Required is the Units a run needs, on insufficient_units (a decimal string).
func (e *APIError) Required() string { return e.detailScalar("required") }

// Available is the Units the balance has, on insufficient_units (a decimal string).
func (e *APIError) Available() string {
	if _, isList := e.detailMap()["available"].([]any); isList {
		return ""
	}
	return e.detailScalar("available")
}

// AvailableVersions lists the majors that exist, on catalog_version_not_found.
func (e *APIError) AvailableVersions() []int {
	list, _ := e.detailMap()["available"].([]any)
	var out []int
	for _, v := range list {
		if n, ok := v.(float64); ok {
			out = append(out, int(n))
		}
	}
	return out
}

func (e *APIError) detailScalar(key string) string {
	switch v := e.detailMap()[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

// pendingResult reports a 409 that only means "not ready yet": read the result again with the
// same key. The current engine answers 202 instead; older paths answer these codes.
func (e *APIError) pendingResult() bool {
	return e.StatusCode == http.StatusConflict &&
		(e.Code == "result_not_available" || e.Code == "result_not_complete" || e.Code == "result_not_running")
}

// ConnectionError means the request did not get an answer (timeout, lost connection, DNS, TLS).
// The outcome is unknown: for a run it may have started. IdempotencyKey is set when the request
// carried one; reuse it to recover (Catalog.Result). It wraps the transport error.
type ConnectionError struct {
	Err            error
	IdempotencyKey string
}

// Error implements error.
func (e *ConnectionError) Error() string {
	msg := "cleanedweb: no answer: " + e.Err.Error() + ". The request may have reached the server"
	if e.IdempotencyKey != "" {
		msg += "; read the result with the same idempotency key to recover it"
	}
	return msg
}

// Unwrap returns the transport error (context.DeadlineExceeded, a net.Error, ...).
func (e *ConnectionError) Unwrap() error { return e.Err }

// Is matches ErrConnection.
func (e *ConnectionError) Is(target error) bool { return target == ErrConnection }

// Unsure is always true: nothing proves whether the run started.
func (e *ConnectionError) Unsure() bool { return true }

// InvalidArgumentError is a bad argument, found before any request was sent.
type InvalidArgumentError struct{ Message string }

func invalidArgument(format string, args ...any) error {
	return &InvalidArgumentError{Message: fmt.Sprintf(format, args...)}
}

// Error implements error.
func (e *InvalidArgumentError) Error() string { return "cleanedweb: invalid argument: " + e.Message }

// Is matches ErrInvalidArgument.
func (e *InvalidArgumentError) Is(target error) bool { return target == ErrInvalidArgument }

// MissingAPIKeyError means a call that needs a key was made without one. Pass WithAPIKey or set
// CLEANEDWEB_API_KEY. Catalog reads need no key.
type MissingAPIKeyError struct{}

// Error implements error.
func (e *MissingAPIKeyError) Error() string {
	return "cleanedweb: this call needs an API key: use WithAPIKey or set CLEANEDWEB_API_KEY (catalog reads need none)"
}

// Is matches ErrMissingAPIKey.
func (e *MissingAPIKeyError) Is(target error) bool { return target == ErrMissingAPIKey }

// IsRunFailed reports whether err is a failed run's error: final for its idempotency key.
func IsRunFailed(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.RunFailed()
}

// IsUnsure reports whether err leaves it unknown whether the run started or failed: a connection
// error, or a gateway error or timeout without a failed-run marker. Keep the idempotency key and
// read the result with it.
func IsUnsure(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Unsure()
	}
	var conn *ConnectionError
	return errors.As(err, &conn)
}

// newAPIError maps an error answer to the typed error the documentation describes.
func newAPIError(status int, body []byte, header http.Header, key string) *APIError {
	e := &APIError{StatusCode: status, Body: body, Header: header, IdempotencyKey: key}
	var parsed any
	if len(body) > 0 && json.Unmarshal(body, &parsed) == nil {
		e.Detail, e.Code, e.Message = detailAndCode(parsed)
	} else if len(body) > 0 {
		e.Message = truncate(strings.TrimSpace(string(body)), 300)
	}
	if secs, ok := retryAfterFrom(header, e.Detail); ok {
		e.RetryAfter, e.hasRetryAfter = secs, true
	}
	e.Kind = classify(e)
	return e
}

func detailAndCode(body any) (detail any, code, message string) {
	m, isMap := body.(map[string]any)
	if isMap {
		if d, ok := m["detail"]; ok {
			detail = d
		} else {
			detail = m
		}
	} else {
		detail = body
	}
	switch d := detail.(type) {
	case map[string]any:
		if c, ok := d["code"].(string); ok && c != "" {
			code = c
		} else if c, ok := d["error_code"].(string); ok {
			code = c
		}
		message, _ = d["message"].(string)
	case string:
		message = d
	case []any:
		var parts []string
		for i, item := range d {
			if i == 3 {
				break
			}
			if im, ok := item.(map[string]any); ok {
				if msg, ok := im["msg"].(string); ok {
					parts = append(parts, msg)
					continue
				}
			}
			parts = append(parts, fmt.Sprint(item))
		}
		message = strings.Join(parts, "; ")
	}
	return detail, code, message
}

func retryAfterFrom(header http.Header, detail any) (time.Duration, bool) {
	if raw := strings.TrimSpace(header.Get("Retry-After")); raw != "" {
		if secs, err := strconv.ParseFloat(raw, 64); err == nil && secs >= 0 {
			return seconds(secs), true
		}
		// An HTTP date: not worth parsing here.
	}
	if d, ok := detail.(map[string]any); ok {
		for _, key := range []string{"retry_after_seconds", "retryAfterSeconds", "retry_after"} {
			if secs, ok := d[key].(float64); ok && secs >= 0 {
				return seconds(secs), true
			}
		}
	}
	return 0, false
}

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

func classify(e *APIError) ErrorKind {
	switch {
	case e.StatusCode == http.StatusUnauthorized:
		return KindAuthentication
	case e.StatusCode == http.StatusForbidden:
		return KindPermissionDenied
	case e.StatusCode == http.StatusPaymentRequired || e.Code == "insufficient_units":
		return KindInsufficientCredit
	case e.StatusCode == http.StatusNotFound:
		if e.Code == "catalog_version_not_found" {
			return KindVersionNotFound
		}
		return KindNotFound
	case e.StatusCode == http.StatusConflict:
		switch {
		case e.Code == "usage_budget_exceeded":
			return KindUsageBudgetExceeded
		case e.Code == "delivery_identity_conflict" || e.Code == "schedule_idempotency_conflict":
			return KindIdempotencyConflict
		case strings.HasPrefix(e.Code, definitionVersionCodeKey):
			return KindVersionConflict
		}
		return KindAPIStatus
	case e.StatusCode == http.StatusTooManyRequests:
		return KindRateLimited
	case e.StatusCode == http.StatusBadRequest || e.StatusCode == http.StatusUnsupportedMediaType || e.StatusCode == http.StatusUnprocessableEntity:
		return KindInvalidRequest
	case e.StatusCode >= 500:
		return KindServer
	}
	return KindAPIStatus
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
