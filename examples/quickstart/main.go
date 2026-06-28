// Command quickstart submits a task and blocks until it finishes.
//
// Run it with your API key in the environment:
//
//	HIAPI_API_KEY=sk-... go run ./examples/quickstart
package main

import (
	"context"
	"fmt"
	"log"

	hiapi "github.com/HiAPIAI/hiapi-go"
)

func main() {
	client, err := hiapi.New("") // reads HIAPI_API_KEY
	if err != nil {
		log.Fatal(err)
	}

	task, err := client.Tasks.Run(context.Background(), hiapi.RunParams{
		Model: "seedance-2-0",
		Input: map[string]any{
			"prompt":     "a cyan glass data center, slow dolly in",
			"resolution": "1080p",
		},
		OnUpdate: func(t *hiapi.Task) {
			log.Printf("status: %s", t.Status)
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	for _, o := range task.Output {
		fmt.Printf("%s (%s)\n", o.URL, o.Type)
	}
}
