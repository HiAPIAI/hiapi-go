package hiapi

// Tests for the route parameter and Idempotency-Key support on Tasks.Create /
// Tasks.Run (route spelling, replay flag, 409/422 semantics, retry safety).

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCreateWithRouteSendsRouteField(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		gotBody = string(buf)
		w.Write([]byte(`{"code":0,"data":{"taskId":"created-1"}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.Tasks.Create(context.Background(), CreateParams{
		Model: "gpt-image-2/text-to-image",
		Input: map[string]any{"prompt": "x"},
		Route: "pro",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(gotBody, `"route":"pro"`) {
		t.Fatalf("route missing from body: %s", gotBody)
	}
}

func TestCreateWithoutRouteOmitsField(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		gotBody = string(buf)
		w.Write([]byte(`{"code":0,"data":{"taskId":"created-1"}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if _, err := c.Tasks.Create(context.Background(), CreateParams{
		Model: "m", Input: map[string]any{"prompt": "x"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if strings.Contains(gotBody, `"route"`) {
		t.Fatalf("route must be omitted when unset: %s", gotBody)
	}
}

func TestRetrieveParsesRouteEcho(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"data":{"taskId":"tk-1","model":"gpt-image-2/text-to-image@pro","status":"success","route":"pro"}}`))
	}))
	defer srv.Close()

	task, err := newTestClient(t, srv).Tasks.Retrieve(context.Background(), "tk-1")
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if task.Route != "pro" {
		t.Fatalf("route echo: %q", task.Route)
	}
	if task.Model != "gpt-image-2/text-to-image@pro" {
		t.Fatalf("model should be the resolved full name: %q", task.Model)
	}
}

func TestCreateSendsIdempotencyKeyHeader(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Idempotency-Key")
		w.Write([]byte(`{"code":0,"data":{"taskId":"created-1"}}`))
	}))
	defer srv.Close()

	ct, err := newTestClient(t, srv).Tasks.Create(context.Background(), CreateParams{
		Model: "m", Input: map[string]any{"prompt": "x"}, IdempotencyKey: "wf-1:submit",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if gotKey != "wf-1:submit" {
		t.Fatalf("Idempotency-Key header: %q", gotKey)
	}
	if ct.IdempotentReplay {
		t.Fatal("IdempotentReplay must be false without the replay header")
	}
}

func TestCreateReplaySetsIdempotentReplay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Idempotent-Replay", "true")
		w.Write([]byte(`{"code":0,"data":{"taskId":"created-1"}}`))
	}))
	defer srv.Close()

	ct, err := newTestClient(t, srv).Tasks.Create(context.Background(), CreateParams{
		Model: "m", Input: map[string]any{"prompt": "x"}, IdempotencyKey: "k",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !ct.IdempotentReplay {
		t.Fatal("IdempotentReplay must be true on a replayed response")
	}
	if ct.TaskID != "created-1" {
		t.Fatalf("taskId: %q", ct.TaskID)
	}
}

func TestCreateIdempotencyMismatchNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"code":422,"error_code":"IDEMPOTENCY_KEY_MISMATCH","message":"key reused"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, WithMaxRetries(2))
	_, err := c.Tasks.Create(context.Background(), CreateParams{
		Model: "m", Input: map[string]any{"prompt": "x"}, IdempotencyKey: "k",
	})
	if !errors.Is(err, ErrIdempotencyKeyMismatch) {
		t.Fatalf("want ErrIdempotencyKeyMismatch, got %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("422 must not be retried, got %d calls", got)
	}
}

func TestCreate409ProcessingRetriesThenReplays(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"code":409,"error_code":"IDEMPOTENCY_KEY_PROCESSING","message":"in progress"}`))
			return
		}
		w.Header().Set("Idempotent-Replay", "true")
		w.Write([]byte(`{"code":0,"data":{"taskId":"created-1"}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, WithMaxRetries(1))
	ct, err := c.Tasks.Create(context.Background(), CreateParams{
		Model: "m", Input: map[string]any{"prompt": "x"}, IdempotencyKey: "k",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !ct.IdempotentReplay {
		t.Fatal("expected the retried request to hit the replay path")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected one automatic 409 retry, got %d calls", got)
	}
}

func TestCreate409ProcessingExhaustsRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"code":409,"error_code":"IDEMPOTENCY_KEY_PROCESSING","message":"in progress"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, WithMaxRetries(1))
	_, err := c.Tasks.Create(context.Background(), CreateParams{
		Model: "m", Input: map[string]any{"prompt": "x"}, IdempotencyKey: "k",
	})
	if !errors.Is(err, ErrIdempotencyKeyProcessing) {
		t.Fatalf("want ErrIdempotencyKeyProcessing, got %v", err)
	}
	if got := calls.Load(); got != 2 { // initial attempt + 1 retry
		t.Fatalf("calls: %d", got)
	}
}

func TestCreate409WithoutKeyNotRetried(t *testing.T) {
	// A 409 on a keyless POST is not the idempotency-processing contract; it
	// must surface immediately rather than being retried.
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"code":409,"error_code":"IDEMPOTENCY_KEY_PROCESSING","message":"in progress"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, WithMaxRetries(2))
	_, err := c.Tasks.Create(context.Background(), CreateParams{
		Model: "m", Input: map[string]any{"prompt": "x"},
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Fatalf("want 409 APIError, got %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("keyless 409 must not be retried, got %d calls", got)
	}
}

func TestCreateNetworkErrorRetriedOnlyWithIdempotencyKey(t *testing.T) {
	// First request: connection dropped mid-response; second: success. Without
	// a key the POST must fail immediately; with a key it retries and succeeds.
	newFlaky := func() *httptest.Server {
		var calls atomic.Int32
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				hj, ok := w.(http.Hijacker)
				if !ok {
					t.Fatal("server does not support hijacking")
				}
				conn, _, _ := hj.Hijack()
				conn.Close() // simulate a reset before any response bytes
				return
			}
			w.Write([]byte(`{"code":0,"data":{"taskId":"created-1"}}`))
		}))
	}

	srv1 := newFlaky()
	defer srv1.Close()
	c1 := newTestClient(t, srv1, WithMaxRetries(2))
	_, err := c1.Tasks.Create(context.Background(), CreateParams{
		Model: "m", Input: map[string]any{"prompt": "x"},
	})
	var connErr *ConnectionError
	if !errors.As(err, &connErr) {
		t.Fatalf("keyless create must surface the network error, got %v", err)
	}

	srv2 := newFlaky()
	defer srv2.Close()
	c2 := newTestClient(t, srv2, WithMaxRetries(2))
	ct, err := c2.Tasks.Create(context.Background(), CreateParams{
		Model: "m", Input: map[string]any{"prompt": "x"}, IdempotencyKey: "k",
	})
	if err != nil {
		t.Fatalf("keyed create should retry through the network error: %v", err)
	}
	if ct.TaskID != "created-1" {
		t.Fatalf("taskId: %q", ct.TaskID)
	}
}

func TestRunForwardsRouteAndIdempotencyKey(t *testing.T) {
	var gotBody, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			buf := make([]byte, r.ContentLength)
			r.Body.Read(buf)
			gotBody = string(buf)
			gotKey = r.Header.Get("Idempotency-Key")
			w.Write([]byte(`{"code":0,"data":{"taskId":"tk-1"}}`))
			return
		}
		w.Write([]byte(`{"code":0,"data":{"taskId":"tk-1","model":"m@ext","status":"success","route":"ext"}}`))
	}))
	defer srv.Close()

	task, err := newTestClient(t, srv).Tasks.Run(context.Background(), RunParams{
		Model:          "m",
		Input:          map[string]any{"prompt": "x"},
		Route:          "ext",
		IdempotencyKey: "job-42",
		PollInterval:   time.Millisecond,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(gotBody, `"route":"ext"`) || gotKey != "job-42" {
		t.Fatalf("create request: body=%s key=%q", gotBody, gotKey)
	}
	if task.Route != "ext" {
		t.Fatalf("route echo: %q", task.Route)
	}
}
