package hiapi

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Default polling configuration for [Tasks.Wait] and [Tasks.Run].
const (
	DefaultPollInterval = 3 * time.Second
	DefaultPollTimeout  = 600 * time.Second
)

// OnUpdate is called once per status change while waiting, with the latest task.
type OnUpdate func(*Task)

// Tasks is the client.Tasks service — the unified async task API (/v1/tasks).
type Tasks struct {
	transport *transport
}

// CreateParams are the inputs to [Tasks.Create].
type CreateParams struct {
	// Model is the model id, e.g. "seedance-2-0".
	Model string
	// Input holds the model's business parameters (fields vary per model).
	Input map[string]any
	// Callback optionally configures a webhook, e.g.
	// {"url": ..., "when": "final"}. Do not put callback fields inside Input.
	Callback map[string]any
	// Route optionally selects a model route (e.g. "pro") without writing the
	// model@route suffix form: Model "x" + Route "pro" is the preferred
	// spelling of Model "x@pro". Empty/"default" both mean the default route.
	// An unknown route is rejected with a 400 listing the available routes.
	Route string
	// IdempotencyKey (sent as the Idempotency-Key header, at most 255 bytes)
	// makes retries safe: resending the same key + body returns the original
	// task (CreatedTask.IdempotentReplay is then true) instead of creating and
	// billing a second one. The transport also becomes willing to retry this
	// POST on network errors. Reusing a key with a different body fails with
	// error_code IDEMPOTENCY_KEY_MISMATCH ([ErrIdempotencyKeyMismatch]).
	IdempotencyKey string
}

// Create submits a task and returns immediately with its task id.
func (t *Tasks) Create(ctx context.Context, params CreateParams) (*CreatedTask, error) {
	body := map[string]any{"model": params.Model, "input": params.Input}
	if params.Route != "" {
		body["route"] = params.Route
	}
	if params.Callback != nil {
		body["callback"] = params.Callback
	}
	var reqHeaders map[string]string
	if params.IdempotencyKey != "" {
		reqHeaders = map[string]string{idempotencyKeyHeader: params.IdempotencyKey}
	}
	env, respHeaders, err := t.transport.requestWithHeaders(ctx, "POST", "/tasks", body, nil, reqHeaders)
	if err != nil {
		return nil, err
	}
	created := createdTaskFromMap(dataObject(env))
	created.IdempotentReplay = strings.EqualFold(respHeaders.Get("Idempotent-Replay"), "true")
	return created, nil
}

// Retrieve fetches a task's current status and (when success) its output.
func (t *Tasks) Retrieve(ctx context.Context, taskID string) (*Task, error) {
	env, err := t.transport.request(ctx, "GET", "/tasks/"+url.PathEscape(taskID), nil, nil)
	if err != nil {
		return nil, err
	}
	return taskFromMap(dataObject(env)), nil
}

// ListParams are the inputs to [Tasks.List]. Zero values use page 1, size 20.
type ListParams struct {
	Page int
	Size int
}

// List returns your tasks, newest first (paginated).
func (t *Tasks) List(ctx context.Context, params ListParams) (*TaskPage, error) {
	page, size := params.Page, params.Size
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("size", strconv.Itoa(size))
	env, err := t.transport.request(ctx, "GET", "/tasks", nil, q)
	if err != nil {
		return nil, err
	}
	// "data" is normally the page object; tolerate a bare array under data too.
	if arr, ok := env["data"].([]any); ok {
		return taskPageFromMap(map[string]any{"items": arr, "page": page, "size": size}), nil
	}
	return taskPageFromMap(dataObject(env)), nil
}

// WaitParams tunes [Tasks.Wait] / [Tasks.Run]. Zero PollInterval/Timeout use
// the package defaults; OnUpdate may be nil.
type WaitParams struct {
	PollInterval time.Duration
	Timeout      time.Duration
	OnUpdate     OnUpdate
}

func (p WaitParams) pollInterval() time.Duration {
	if p.PollInterval <= 0 {
		return DefaultPollInterval
	}
	return p.PollInterval
}

// timeout treats the zero value (unset) as the default, per Go convention.
func (p WaitParams) timeout() time.Duration {
	if p.Timeout <= 0 {
		return DefaultPollTimeout
	}
	return p.Timeout
}

// Wait polls taskID until it reaches a terminal state.
//
// It returns the task on success, a [*TaskFailedError] on fail, and a
// [*PollTimeoutError] once the timeout has actually elapsed. The poll loop also
// stops early if ctx is cancelled.
func (t *Tasks) Wait(ctx context.Context, taskID string, params WaitParams) (*Task, error) {
	interval := params.pollInterval()
	timeout := params.timeout()
	deadline := time.Now().Add(timeout)
	lastStatus := ""

	for {
		task, err := t.Retrieve(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if params.OnUpdate != nil && task.Status != lastStatus {
			lastStatus = task.Status
			params.OnUpdate(task)
		}
		switch task.Status {
		case StatusSuccess:
			return task, nil
		case StatusFail:
			return nil, taskFailedError(task)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, &PollTimeoutError{TaskID: taskID, Timeout: timeout}
		}
		// Sleep the smaller of the poll interval and the time left, so the last
		// poll lands right at the deadline rather than before it.
		wait := interval
		if remaining < wait {
			wait = remaining
		}
		// Returns ctx.Err() if the caller cancels mid-wait.
		if err := sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

// RunParams are the inputs to [Tasks.Run] — Create plus Wait in one call.
// Route and IdempotencyKey are forwarded to Create (see [CreateParams]).
type RunParams struct {
	Model          string
	Input          map[string]any
	Callback       map[string]any
	Route          string
	IdempotencyKey string
	PollInterval   time.Duration
	Timeout        time.Duration
	OnUpdate       OnUpdate
}

// Run creates a task and blocks until it finishes — the one-call workflow.
//
// It is equivalent to Create followed by Wait. Callback is optional and
// independent of polling; set it if you also want a webhook.
func (t *Tasks) Run(ctx context.Context, params RunParams) (*Task, error) {
	created, err := t.Create(ctx, CreateParams{
		Model:          params.Model,
		Input:          params.Input,
		Callback:       params.Callback,
		Route:          params.Route,
		IdempotencyKey: params.IdempotencyKey,
	})
	if err != nil {
		return nil, err
	}
	return t.Wait(ctx, created.TaskID, WaitParams{
		PollInterval: params.PollInterval,
		Timeout:      params.Timeout,
		OnUpdate:     params.OnUpdate,
	})
}

func taskFailedError(task *Task) *TaskFailedError {
	e := &TaskFailedError{Task: task}
	if task.Error != nil {
		e.Code = task.Error.Code
		e.Message = task.Error.Message
	}
	return e
}
