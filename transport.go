package hiapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// retryStatuses are worth retrying: rate limiting and transient platform
// unavailability. These mean the server rejected the request *before*
// processing, so retrying is safe even for a non-idempotent POST.
var retryStatuses = map[int]bool{429: true, 503: true}

// idempotentMethods are safe to retry after a network error, where the outcome
// is unknown. A POST /tasks must NOT be retried on a network error — it might
// have created a task the SDK never saw the response for, double-charging.
var idempotentMethods = map[string]bool{
	http.MethodGet:    true,
	http.MethodHead:   true,
	http.MethodPut:    true,
	http.MethodDelete: true,
}

const (
	maxBackoff    = 8 * time.Second
	maxRetryAfter = 60 * time.Second
)

// transport performs JSON request/response framing, authentication, retries
// with exponential backoff (honoring Retry-After), and maps non-2xx responses
// onto the typed errors in errors.go.
type transport struct {
	apiKey     string
	baseURL    string
	maxRetries int
	httpClient *http.Client
}

// request performs an HTTP request and returns the parsed JSON envelope.
func (t *transport) request(ctx context.Context, method, path string, body map[string]any, params url.Values) (map[string]any, error) {
	fullURL := t.buildURL(path, params)

	var rawBody []byte
	if body != nil {
		var err error
		rawBody, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("hiapi: encoding request body: %w", err)
		}
	}

	for attempt := 0; ; attempt++ {
		var reader io.Reader
		if rawBody != nil {
			reader = bytes.NewReader(rawBody)
		}
		req, err := http.NewRequestWithContext(ctx, method, fullURL, reader)
		if err != nil {
			return nil, &ConnectionError{URL: fullURL, Err: err}
		}
		req.Header.Set("Authorization", "Bearer "+t.apiKey)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "hiapi-go/"+Version)
		if rawBody != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := t.httpClient.Do(req)
		if err != nil {
			// Never retry if the caller's context was cancelled or timed out.
			if ctx.Err() != nil {
				return nil, &ConnectionError{URL: fullURL, Err: ctx.Err()}
			}
			// Only retry network failures for idempotent methods; a retried POST
			// could silently create a second task.
			if idempotentMethods[strings.ToUpper(method)] && attempt < t.maxRetries {
				if waitErr := sleep(ctx, backoffDelay(attempt)); waitErr != nil {
					return nil, &ConnectionError{URL: fullURL, Err: waitErr}
				}
				continue
			}
			return nil, &ConnectionError{URL: fullURL, Err: err}
		}

		data, status, headers, readErr := readResponse(resp)
		if readErr != nil {
			// A failure reading the body is a network-level failure. Honor
			// cancellation, and retry only for idempotent methods (a POST may
			// have already created a task server-side).
			if ctx.Err() != nil {
				return nil, &ConnectionError{URL: fullURL, Err: ctx.Err()}
			}
			if idempotentMethods[strings.ToUpper(method)] && attempt < t.maxRetries {
				if waitErr := sleep(ctx, backoffDelay(attempt)); waitErr != nil {
					return nil, &ConnectionError{URL: fullURL, Err: waitErr}
				}
				continue
			}
			return nil, &ConnectionError{URL: fullURL, Err: readErr}
		}

		if status >= 200 && status < 300 {
			return decodeJSON(data, status)
		}

		if retryStatuses[status] && attempt < t.maxRetries {
			if waitErr := sleep(ctx, retryDelay(attempt, headers)); waitErr != nil {
				return nil, &ConnectionError{URL: fullURL, Err: waitErr}
			}
			continue
		}
		return nil, errorFromResponse(status, data)
	}
}

func (t *transport) buildURL(path string, params url.Values) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := t.baseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	return u
}

// readResponse reads and closes the body, returning the bytes, status, headers,
// and any read error (a truncated/reset body must not be parsed as empty data).
func readResponse(resp *http.Response) ([]byte, int, http.Header, error) {
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, resp.Header, err
}

// sleep waits for d, or returns early if the context is cancelled.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// retryDelay honors a Retry-After header — numeric seconds or an HTTP-date —
// else falls back to exponential backoff. The result is clamped to
// [0, maxRetryAfter].
func retryDelay(attempt int, headers http.Header) time.Duration {
	if headers != nil {
		if ra := headers.Get("Retry-After"); ra != "" {
			if secs, err := strconv.ParseFloat(ra, 64); err == nil {
				return clampRetryAfter(time.Duration(secs * float64(time.Second)))
			}
			// HTTP-date form, e.g. "Wed, 21 Oct 2026 07:28:00 GMT".
			if when, err := http.ParseTime(ra); err == nil {
				return clampRetryAfter(time.Until(when))
			}
		}
	}
	return backoffDelay(attempt)
}

// clampRetryAfter bounds a Retry-After delay to [0, maxRetryAfter] so a negative
// (past) date or an absurdly large value can't break the retry loop.
func clampRetryAfter(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}

func backoffDelay(attempt int) time.Duration {
	d := time.Duration(0.5 * math.Pow(2, float64(attempt)) * float64(time.Second))
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

func decodeJSON(raw []byte, status int) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, &APIError{
			Status:  status,
			Message: "could not decode response body as JSON: " + err.Error(),
			Body:    string(raw),
		}
	}
	obj, ok := parsed.(map[string]any)
	if !ok {
		return nil, &APIError{
			Status:  status,
			Message: "expected a JSON object response",
			Body:    string(raw),
		}
	}
	return obj, nil
}

func errorFromResponse(status int, raw []byte) *APIError {
	var errorCode, message string
	if len(raw) > 0 {
		var parsed map[string]any
		if err := json.Unmarshal(raw, &parsed); err == nil {
			errorCode = asString(parsed["error_code"])
			message = asString(parsed["message"])
		}
	}
	if message == "" {
		message = fmt.Sprintf("HTTP %d", status)
	}
	return &APIError{
		Status:    status,
		ErrorCode: errorCode,
		Message:   message,
		Body:      string(raw),
	}
}

// normalizeBaseURL strips trailing slashes and ensures the /v1 prefix is
// present, accepting both https://api.hiapi.ai and https://api.hiapi.ai/v1.
func normalizeBaseURL(base string) string {
	base = strings.TrimRight(base, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base
}

// dataObject extracts the "data" object from a response envelope.
func dataObject(env map[string]any) map[string]any {
	if d, ok := env["data"].(map[string]any); ok {
		return d
	}
	return map[string]any{}
}
