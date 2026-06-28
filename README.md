# HiAPI Go SDK

Zero-dependency Go client for the [HiAPI](https://hiapi.ai) **unified async task API** (`/v1/tasks`) — submit an image / video / audio generation task, poll it to completion, and read the output in **one call**.

- **Zero dependencies.** Standard library only (`net/http`, `crypto/hmac`, `encoding/json`).
- **One-call workflow.** `client.Tasks.Run(...)` submits and waits for you.
- **Context-aware.** Every call takes a `context.Context` for cancellation and deadlines.
- **Idiomatic errors.** Inspect with `errors.Is` / `errors.As`.
- **Webhook verification.** HMAC-SHA256 signature + replay check, built in.

> For OpenAI-compatible chat/image endpoints, point an OpenAI-compatible client at
> `https://api.hiapi.ai/v1`. This SDK focuses on what that can't do: the
> asynchronous **submit → poll → download** lifecycle.

## Install

```bash
go get github.com/HiAPIAI/hiapi-go
```

Requires Go 1.21+.

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"log"

	hiapi "github.com/HiAPIAI/hiapi-go"
)

func main() {
	client, err := hiapi.New("sk-...") // or set HIAPI_API_KEY and pass ""
	if err != nil {
		log.Fatal(err)
	}

	task, err := client.Tasks.Run(context.Background(), hiapi.RunParams{
		Model: "seedance-2-0",
		Input: map[string]any{"prompt": "a cyan glass data center", "resolution": "1080p"},
		OnUpdate: func(t *hiapi.Task) { log.Println("status:", t.Status) },
	})
	if err != nil {
		log.Fatal(err)
	}

	for _, out := range task.Output {
		fmt.Println(out.Type, out.URL) // e.g. "video https://cdn.hiapi.ai/tasks/..."
	}
}
```

`Run` blocks until the task reaches a terminal state. It returns a
`*TaskFailedError` if the task fails and a `*PollTimeoutError` if it doesn't
finish within `Timeout` (default 600s).

## Lower-level control

```go
ctx := context.Background()

created, err := client.Tasks.Create(ctx, hiapi.CreateParams{
	Model:    "seedance-2-0",
	Input:    map[string]any{"prompt": "...", "resolution": "720p"},
	Callback: map[string]any{"url": "https://your-app.com/hiapi/callback", "when": "final"},
})
// created.TaskID

task, err := client.Tasks.Retrieve(ctx, created.TaskID) // one status check
task, err := client.Tasks.Wait(ctx, created.TaskID, hiapi.WaitParams{
	PollInterval: 3 * time.Second,
	Timeout:      15 * time.Minute,
})
page, err := client.Tasks.List(ctx, hiapi.ListParams{Page: 1, Size: 20}) // newest first
```

`Input` fields are **defined per model** — see the relevant
[model page](https://docs.hiapi.ai/models/). Don't put callback fields inside
`Input`; pass `Callback` separately.

## Webhooks

If you set a **Webhook signing key** in the HiAPI console, terminal callbacks are
signed. Verify them against the **raw request body**:

```go
client, _ := hiapi.New("sk-...", hiapi.WithWebhookSecret("whsec_..."))

func handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	task, err := client.Webhooks.Verify(body, r.Header, hiapi.VerifyParams{})
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if task.Succeeded() {
		log.Println(task.Output[0].URL)
	}
	w.WriteHeader(http.StatusOK) // ack with 2xx; HiAPI retries non-2xx
}
```

Callbacks are delivered **at least once** — deduplicate by `task.TaskID`.

## Errors

Match categories with `errors.Is`; reach for `*APIError` (status, error code,
raw body) with `errors.As`.

| Error | When |
| --- | --- |
| `ErrAuthentication` | 401 — bad/missing API key |
| `ErrNotFound` | 404 — unknown task or not yours |
| `ErrInvalidRequest` | `INVALID_REQUEST` — fix the request |
| `ErrModelUnavailable` | `MODEL_UNAVAILABLE` — retry or switch model |
| `ErrTaskTimeout` / `ErrStorageUnavailable` | retryable upstream errors |
| `ErrServiceUnavailable` | 503 — platform busy (auto-retried) |
| `*APIError` | any non-2xx response (wraps the sentinels above) |
| `*ConnectionError` | network failure (auto-retried for reads only — **not** `Create`) |
| `*TaskFailedError` | a polled task ended in `status=fail` |
| `*PollTimeoutError` | `Run` / `Wait` exceeded its timeout |
| `*WebhookError` | bad signature or stale timestamp |

```go
task, err := client.Tasks.Run(ctx, params)
switch {
case errors.Is(err, hiapi.ErrModelUnavailable):
	// retry later or switch models
case errors.As(err, new(*hiapi.TaskFailedError)):
	// the task ran but failed
case err != nil:
	var apiErr *hiapi.APIError
	if errors.As(err, &apiErr) {
		log.Printf("HTTP %d: %s", apiErr.Status, apiErr.Message)
	}
}
```

429/503 are retried automatically with exponential backoff (`WithMaxRetries`,
default 2; honors `Retry-After`). Network errors are retried **only for
idempotent GETs** (`Retrieve` / `List`, and the polling inside `Wait` / `Run`).
The `POST` that `Create` issues is **never** retried on a network failure, so a
dropped connection can't silently create a second, double-charged task — if
`Create` returns a `*ConnectionError`, confirm with `List` before retrying.

## Configuration

```go
client, err := hiapi.New(
	"sk-...",                                  // or "" to read HIAPI_API_KEY
	hiapi.WithBaseURL("https://api.hiapi.ai/v1"),
	hiapi.WithTimeout(60*time.Second),         // per-request
	hiapi.WithMaxRetries(2),
	hiapi.WithWebhookSecret("whsec_..."),      // or HIAPI_WEBHOOK_SECRET
	hiapi.WithHTTPClient(customClient),        // proxies, custom TLS, instrumentation
)
```

The `Client` is safe for concurrent use by multiple goroutines.

## License

MIT
