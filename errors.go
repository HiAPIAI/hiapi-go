package hiapi

import (
	"errors"
	"fmt"
	"time"
)

// Sentinel errors for the categories an [*APIError] can represent. Match them
// with errors.Is, e.g. errors.Is(err, hiapi.ErrNotFound). An *APIError unwraps
// to exactly one of these (when its status or error_code is recognized), so the
// concrete *APIError is still available via errors.As for status/body detail.
var (
	// ErrAuthentication is a 401 — the API key is missing or invalid.
	ErrAuthentication = errors.New("hiapi: authentication failed")
	// ErrNotFound is a 404 — the task does not exist or is not yours.
	ErrNotFound = errors.New("hiapi: not found")
	// ErrServiceUnavailable is a 503 — the platform is temporarily down.
	ErrServiceUnavailable = errors.New("hiapi: service unavailable")
	// ErrInvalidRequest maps error_code=INVALID_REQUEST — fix the request.
	ErrInvalidRequest = errors.New("hiapi: invalid request")
	// ErrModelUnavailable maps error_code=MODEL_UNAVAILABLE — retry or switch models.
	ErrModelUnavailable = errors.New("hiapi: model unavailable")
	// ErrTaskFailedSync maps error_code=TASK_FAILED on a synchronous response.
	ErrTaskFailedSync = errors.New("hiapi: task failed")
	// ErrTaskTimeout maps error_code=TASK_TIMEOUT — the upstream task timed out.
	ErrTaskTimeout = errors.New("hiapi: task timeout")
	// ErrStorageUnavailable maps error_code=STORAGE_UNAVAILABLE — output storage error.
	ErrStorageUnavailable = errors.New("hiapi: storage unavailable")
	// ErrIdempotencyKeyProcessing maps error_code=IDEMPOTENCY_KEY_PROCESSING
	// (409) — the first request with this Idempotency-Key is still in flight.
	// Retryable: wait Retry-After seconds and resend the identical request; it
	// will replay the original task once created. The transport already retries
	// this automatically up to maxRetries before letting it surface.
	ErrIdempotencyKeyProcessing = errors.New("hiapi: idempotency key still processing")
	// ErrIdempotencyKeyMismatch maps error_code=IDEMPOTENCY_KEY_MISMATCH (422) —
	// this Idempotency-Key was already used with a different request body. Not
	// retryable: the key construction is buggy (two distinct requests derived
	// the same key).
	ErrIdempotencyKeyMismatch = errors.New("hiapi: idempotency key reused with a different body")
)

// APIError is returned when the server responds with a non-2xx status.
//
// Inspect it with errors.As. Status is the HTTP status code; ErrorCode is the
// business error code from the response envelope (may be empty); Body is the
// best-effort raw response body. The error unwraps to a matching sentinel
// (see ErrNotFound and friends) so errors.Is works for category checks.
type APIError struct {
	Status    int
	ErrorCode string
	Message   string
	Body      string
}

func (e *APIError) Error() string {
	if e.ErrorCode != "" {
		return fmt.Sprintf("hiapi: HTTP %d (%s): %s", e.Status, e.ErrorCode, e.Message)
	}
	return fmt.Sprintf("hiapi: HTTP %d: %s", e.Status, e.Message)
}

// Unwrap returns the sentinel error matching this response's error_code (or, as
// a fallback, its HTTP status), enabling errors.Is category checks. It returns
// nil when neither is recognized.
func (e *APIError) Unwrap() error {
	if s, ok := errorCodeToSentinel[e.ErrorCode]; ok {
		return s
	}
	return statusToSentinel[e.Status]
}

// ConnectionError is returned when a network-level failure (DNS, connection
// reset, timeout) prevents the request from completing. The underlying error
// is available via errors.Unwrap.
type ConnectionError struct {
	// URL is the request URL that failed.
	URL string
	// Err is the underlying transport error.
	Err error
}

func (e *ConnectionError) Error() string {
	return fmt.Sprintf("hiapi: request to %s failed: %v", e.URL, e.Err)
}

func (e *ConnectionError) Unwrap() error { return e.Err }

// TaskFailedError is returned by [Tasks.Wait] / [Tasks.Run] when a polled task
// reaches the terminal fail status. Task holds the full failed task; Code and
// Message echo task.Error when present.
type TaskFailedError struct {
	Task    *Task
	Code    string
	Message string
}

func (e *TaskFailedError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = "task failed"
	}
	id := "?"
	if e.Task != nil {
		id = e.Task.TaskID
	}
	return fmt.Sprintf("hiapi: task %s failed: %s", id, msg)
}

// PollTimeoutError is returned by [Tasks.Wait] / [Tasks.Run] when the task did
// not reach a terminal state before the client-side timeout elapsed. The task
// may still complete — retrieve it again later by TaskID.
type PollTimeoutError struct {
	TaskID  string
	Timeout time.Duration
}

func (e *PollTimeoutError) Error() string {
	return fmt.Sprintf(
		"hiapi: task %s did not finish within %s; it may still complete — "+
			"call Tasks.Retrieve(%q) later",
		e.TaskID, e.Timeout, e.TaskID,
	)
}

// WebhookError is returned when a callback cannot be verified (missing/blank
// headers, bad or stale timestamp, or signature mismatch).
type WebhookError struct {
	// Reason is a short description of why verification failed.
	Reason string
	// Err is an optional underlying cause (e.g. a JSON parse error).
	Err error
}

func (e *WebhookError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("hiapi: webhook verification failed: %s: %v", e.Reason, e.Err)
	}
	return fmt.Sprintf("hiapi: webhook verification failed: %s", e.Reason)
}

func (e *WebhookError) Unwrap() error { return e.Err }

// errorCodeToSentinel maps the API error_code enum onto a sentinel error.
var errorCodeToSentinel = map[string]error{
	"INVALID_REQUEST":            ErrInvalidRequest,
	"MODEL_UNAVAILABLE":          ErrModelUnavailable,
	"TASK_FAILED":                ErrTaskFailedSync,
	"TASK_TIMEOUT":               ErrTaskTimeout,
	"STORAGE_UNAVAILABLE":        ErrStorageUnavailable,
	"IDEMPOTENCY_KEY_PROCESSING": ErrIdempotencyKeyProcessing,
	"IDEMPOTENCY_KEY_MISMATCH":   ErrIdempotencyKeyMismatch,
}

// statusToSentinel falls back to a status-based sentinel when no (recognized)
// error_code is present.
var statusToSentinel = map[int]error{
	400: ErrInvalidRequest,
	401: ErrAuthentication,
	404: ErrNotFound,
	503: ErrServiceUnavailable,
}
