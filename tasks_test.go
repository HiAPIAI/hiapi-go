package hiapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient builds a client pointed at srv with fast retries (no real
// backoff sleeps in unit tests because we keep maxRetries small).
func newTestClient(t *testing.T, srv *httptest.Server, opts ...Option) *Client {
	t.Helper()
	all := append([]Option{WithBaseURL(srv.URL), WithMaxRetries(0)}, opts...)
	c, err := New("sk-test", all...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestCreate(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/tasks" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("auth header: %q", got)
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "hiapi-go/") {
			t.Errorf("user-agent: %q", r.Header.Get("User-Agent"))
		}
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		gotBody = string(buf)
		w.Write([]byte(`{"code":0,"data":{"taskId":"created-1"}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	ct, err := c.Tasks.Create(context.Background(), CreateParams{
		Model: "seedance-2-0",
		Input: map[string]any{"prompt": "hi"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if ct.TaskID != "created-1" {
		t.Fatalf("taskId: %q", ct.TaskID)
	}
	if !strings.Contains(gotBody, `"model":"seedance-2-0"`) || !strings.Contains(gotBody, `"prompt":"hi"`) {
		t.Fatalf("request body: %s", gotBody)
	}
}

func TestRetrieve(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tasks/task-42" {
			t.Errorf("path: %s", r.URL.Path)
		}
		w.Write([]byte(`{"code":0,"data":{"taskId":"task-42","model":"m","status":"success","created":1,"completed":2,"storage":"temp","output":[{"url":"u","type":"image"}]}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	task, err := c.Tasks.Retrieve(context.Background(), "task-42")
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if task.TaskID != "task-42" || !task.Succeeded() || task.Output[0].URL != "u" {
		t.Fatalf("task: %+v", task)
	}
}

func TestList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "2" || r.URL.Query().Get("size") != "5" {
			t.Errorf("query: %s", r.URL.RawQuery)
		}
		w.Write([]byte(`{"code":0,"data":{"tasks":[{"taskId":"a"},{"taskId":"b"}],"page":2,"size":5,"total":7}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	page, err := c.Tasks.List(context.Background(), ListParams{Page: 2, Size: 5})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 2 || page.Total != 7 {
		t.Fatalf("page: %+v", page)
	}
}

func TestList_DefaultsAndBareArrayData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" || r.URL.Query().Get("size") != "20" {
			t.Errorf("defaults not applied: %s", r.URL.RawQuery)
		}
		// "data" is a bare array, not a page object.
		w.Write([]byte(`{"code":0,"data":[{"taskId":"a"}]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	page, err := c.Tasks.List(context.Background(), ListParams{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].TaskID != "a" {
		t.Fatalf("bare array data: %+v", page.Items)
	}
}

func TestWait_PollsToSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		status := "handling"
		if n >= 3 {
			status = "success"
		}
		fmt.Fprintf(w, `{"code":0,"data":{"taskId":"t","model":"m","status":%q}}`, status)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	var updates []string
	task, err := c.Tasks.Wait(context.Background(), "t", WaitParams{
		PollInterval: time.Millisecond,
		Timeout:      5 * time.Second,
		OnUpdate:     func(tk *Task) { updates = append(updates, tk.Status) },
	})
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !task.Succeeded() {
		t.Fatalf("final status: %s", task.Status)
	}
	if atomic.LoadInt32(&calls) < 3 {
		t.Fatalf("expected >=3 polls, got %d", calls)
	}
	// OnUpdate fires only on status change: handling, then success.
	if len(updates) != 2 || updates[0] != "handling" || updates[1] != "success" {
		t.Fatalf("updates: %v", updates)
	}
}

func TestWait_FailReturnsTaskFailedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"data":{"taskId":"t","status":"fail","error":{"code":"BAD","message":"nope"}}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.Tasks.Wait(context.Background(), "t", WaitParams{PollInterval: time.Millisecond})
	var tfe *TaskFailedError
	if !errors.As(err, &tfe) {
		t.Fatalf("expected *TaskFailedError, got %v", err)
	}
	if tfe.Code != "BAD" || tfe.Message != "nope" || tfe.Task.TaskID != "t" {
		t.Fatalf("failed error: %+v", tfe)
	}
}

func TestWait_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"data":{"taskId":"t","status":"handling"}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.Tasks.Wait(context.Background(), "t", WaitParams{
		PollInterval: time.Millisecond,
		Timeout:      20 * time.Millisecond,
	})
	var pte *PollTimeoutError
	if !errors.As(err, &pte) {
		t.Fatalf("expected *PollTimeoutError, got %v", err)
	}
	if pte.TaskID != "t" {
		t.Fatalf("timeout error: %+v", pte)
	}
}

func TestWait_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"data":{"taskId":"t","status":"handling"}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	_, err := c.Tasks.Wait(ctx, "t", WaitParams{PollInterval: 5 * time.Millisecond, Timeout: time.Hour})
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context error, got %v", err)
	}
}

func TestRun_CreateThenWait(t *testing.T) {
	var posts, gets int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			atomic.AddInt32(&posts, 1)
			w.Write([]byte(`{"code":0,"data":{"taskId":"run-1"}}`))
			return
		}
		atomic.AddInt32(&gets, 1)
		w.Write([]byte(`{"code":0,"data":{"taskId":"run-1","status":"success","output":[{"url":"final"}]}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	task, err := c.Tasks.Run(context.Background(), RunParams{
		Model:        "m",
		Input:        map[string]any{"p": 1},
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if task.Output[0].URL != "final" || posts != 1 || gets < 1 {
		t.Fatalf("run flow: task=%+v posts=%d gets=%d", task, posts, gets)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		sentinel error
	}{
		{"401", 401, `{"message":"bad key"}`, ErrAuthentication},
		{"404", 404, `{"message":"missing"}`, ErrNotFound},
		{"503", 503, `{"message":"down"}`, ErrServiceUnavailable},
		{"invalid_request", 400, `{"error_code":"INVALID_REQUEST","message":"x"}`, ErrInvalidRequest},
		{"model_unavailable", 400, `{"error_code":"MODEL_UNAVAILABLE","message":"x"}`, ErrModelUnavailable},
		{"task_timeout", 400, `{"error_code":"TASK_TIMEOUT","message":"x"}`, ErrTaskTimeout},
		{"storage", 400, `{"error_code":"STORAGE_UNAVAILABLE","message":"x"}`, ErrStorageUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			_, err := c.Tasks.Retrieve(context.Background(), "x")
			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("errors.Is(%v) = false", tc.sentinel)
			}
			var ae *APIError
			if !errors.As(err, &ae) || ae.Status != tc.status {
				t.Fatalf("APIError: %+v", ae)
			}
		})
	}
}

func TestRetryOn503ThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(503)
			w.Write([]byte(`{"message":"transient"}`))
			return
		}
		w.Write([]byte(`{"code":0,"data":{"taskId":"t","status":"success"}}`))
	}))
	defer srv.Close()

	// maxRetries=2; backoff for attempt 0 is 0.5s — acceptable for one retry.
	c := newTestClient(t, srv, WithMaxRetries(2))
	task, err := c.Tasks.Retrieve(context.Background(), "t")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !task.Succeeded() || calls != 2 {
		t.Fatalf("expected 2 calls then success, got calls=%d task=%+v", calls, task)
	}
}

// countingRT is an http.RoundTripper that counts attempts and always fails with
// a synthetic network error — letting us assert retry behavior deterministically
// without hijacking real sockets.
type countingRT struct{ calls int32 }

func (rt *countingRT) RoundTrip(*http.Request) (*http.Response, error) {
	atomic.AddInt32(&rt.calls, 1)
	return nil, fmt.Errorf("synthetic network failure")
}

func clientWithRT(t *testing.T, rt http.RoundTripper) *Client {
	t.Helper()
	c, err := New("sk-test",
		WithBaseURL("https://api.hiapi.ai/v1"),
		WithMaxRetries(3),
		WithHTTPClient(&http.Client{Transport: rt}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestPostNotRetriedOnNetworkError(t *testing.T) {
	rt := &countingRT{}
	c := clientWithRT(t, rt)
	_, err := c.Tasks.Create(context.Background(), CreateParams{Model: "m", Input: map[string]any{}})
	var ce *ConnectionError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ConnectionError, got %v", err)
	}
	// A POST must NOT be retried on a network error — exactly one attempt.
	if n := atomic.LoadInt32(&rt.calls); n != 1 {
		t.Fatalf("POST retried on network error: %d attempts", n)
	}
}

func TestGetRetriedOnNetworkError(t *testing.T) {
	rt := &countingRT{}
	c := clientWithRT(t, rt)
	_, err := c.Tasks.Retrieve(context.Background(), "t")
	var ce *ConnectionError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ConnectionError, got %v", err)
	}
	// GET is idempotent: initial attempt + 3 retries = 4 attempts.
	if n := atomic.LoadInt32(&rt.calls); n != 4 {
		t.Fatalf("expected 4 attempts (1 + 3 retries), got %d", n)
	}
}

func TestRetryDelay_HTTPDateAndNumeric(t *testing.T) {
	// Numeric seconds.
	h := http.Header{}
	h.Set("Retry-After", "2")
	if d := retryDelay(0, h); d != 2*time.Second {
		t.Fatalf("numeric Retry-After: got %s", d)
	}
	// A past HTTP-date clamps to 0 (not negative).
	h.Set("Retry-After", "Wed, 21 Oct 2000 07:28:00 GMT")
	if d := retryDelay(0, h); d != 0 {
		t.Fatalf("past HTTP-date should clamp to 0, got %s", d)
	}
	// A far-future HTTP-date clamps to the ceiling.
	h.Set("Retry-After", "Wed, 21 Oct 2099 07:28:00 GMT")
	if d := retryDelay(0, h); d != maxRetryAfter {
		t.Fatalf("future HTTP-date should clamp to %s, got %s", maxRetryAfter, d)
	}
	// Garbage falls back to exponential backoff.
	h.Set("Retry-After", "soon")
	if d := retryDelay(1, h); d != backoffDelay(1) {
		t.Fatalf("garbage Retry-After should back off, got %s", d)
	}
}

// shortBodyRT returns a response whose body errors partway through reading.
type shortBodyRT struct{}

type erroringBody struct{}

func (erroringBody) Read([]byte) (int, error) { return 0, fmt.Errorf("connection reset mid-body") }
func (erroringBody) Close() error             { return nil }

func (shortBodyRT) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: 200,
		Body:       erroringBody{},
		Header:     http.Header{},
	}, nil
}

func TestReadError_SurfacesAsConnectionError(t *testing.T) {
	c, err := New("sk-test", WithMaxRetries(0), WithHTTPClient(&http.Client{Transport: shortBodyRT{}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.Tasks.Retrieve(context.Background(), "t")
	var ce *ConnectionError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ConnectionError on body read failure, got %v", err)
	}
}

func TestNew_MissingKey(t *testing.T) {
	t.Setenv("HIAPI_API_KEY", "")
	if _, err := New(""); err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestNew_EnvKeyFallback(t *testing.T) {
	t.Setenv("HIAPI_API_KEY", "sk-from-env")
	c, err := New("")
	if err != nil {
		t.Fatalf("env fallback: %v", err)
	}
	if c.apiKey != "sk-from-env" {
		t.Fatalf("key: %q", c.apiKey)
	}
}

func TestBaseURLNormalization(t *testing.T) {
	cases := map[string]string{
		"https://api.hiapi.ai":     "https://api.hiapi.ai/v1",
		"https://api.hiapi.ai/":    "https://api.hiapi.ai/v1",
		"https://api.hiapi.ai/v1":  "https://api.hiapi.ai/v1",
		"https://api.hiapi.ai/v1/": "https://api.hiapi.ai/v1",
	}
	for in, want := range cases {
		if got := normalizeBaseURL(in); got != want {
			t.Errorf("normalizeBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}
