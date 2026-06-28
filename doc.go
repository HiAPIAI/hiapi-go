// Package hiapi is a zero-dependency Go client for the HiAPI unified async
// task API (https://api.hiapi.ai/v1).
//
// HiAPI exposes one lifecycle for every model — image, video, and audio
// generation alike: submit a task, poll until it finishes, then read its
// output. The [Client.Tasks] service mirrors that lifecycle with
// [Tasks.Create], [Tasks.Retrieve], [Tasks.List], [Tasks.Wait], and the
// one-call [Tasks.Run].
//
// # Quick start
//
//	client, err := hiapi.New("sk-...") // or set HIAPI_API_KEY
//	if err != nil {
//		log.Fatal(err)
//	}
//	task, err := client.Tasks.Run(context.Background(), hiapi.RunParams{
//		Model: "seedance-2-0",
//		Input: map[string]any{"prompt": "a cyan glass data center", "resolution": "1080p"},
//		OnUpdate: func(t *hiapi.Task) { log.Println(t.Status) },
//	})
//	if err != nil {
//		log.Fatal(err)
//	}
//	fmt.Println(task.Output[0].URL)
//
// # Errors
//
// Non-2xx HTTP responses surface as [*APIError], which unwraps to a category
// sentinel — [ErrAuthentication], [ErrNotFound], [ErrInvalidRequest], and the
// rest — so you can branch with [errors.Is]. A task that polls to a terminal
// fail state returns [*TaskFailedError]; exceeding the client-side timeout
// returns [*PollTimeoutError]; a network failure returns [*ConnectionError];
// an unverifiable callback returns [*WebhookError]. Reach for any of these
// concrete types with [errors.As] to read their fields.
package hiapi
