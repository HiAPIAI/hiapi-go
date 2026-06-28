package hiapi

import (
	"encoding/json"
	"testing"
)

// decode is a test helper that parses a JSON literal into a map.
func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return m
}

func TestTaskFromMap_SuccessWithOutput(t *testing.T) {
	m := decode(t, `{
		"taskId": "abc123",
		"model": "seedance-2-0",
		"status": "success",
		"created": 1781492551,
		"completed": 1781493577,
		"storage": "temp",
		"output": [{"url": "https://cdn/x.mp4", "type": "video", "expireAt": 1781500000}]
	}`)
	task := taskFromMap(m)

	if task.TaskID != "abc123" || task.Model != "seedance-2-0" {
		t.Fatalf("ids: %+v", task)
	}
	if task.Status != "success" || !task.Succeeded() || !task.IsTerminal() {
		t.Fatalf("status: %+v", task)
	}
	if task.CreatedAt != 1781492551 || task.CompletedAt != 1781493577 {
		t.Fatalf("timestamps: created=%d completed=%d", task.CreatedAt, task.CompletedAt)
	}
	if task.Storage != "temp" {
		t.Fatalf("storage: %q", task.Storage)
	}
	if len(task.Output) != 1 {
		t.Fatalf("output len: %d", len(task.Output))
	}
	o := task.Output[0]
	if o.URL != "https://cdn/x.mp4" || o.Type != "video" || o.ExpireAt != 1781500000 {
		t.Fatalf("output: %+v", o)
	}
}

func TestTaskFromMap_NonTerminalCompletedZero(t *testing.T) {
	// While a task is non-terminal the API sends completed:0; it must surface as 0.
	m := decode(t, `{"taskId":"t","model":"m","status":"handling","created":1781492551,"completed":0}`)
	task := taskFromMap(m)
	if task.CompletedAt != 0 {
		t.Fatalf("expected CompletedAt 0, got %d", task.CompletedAt)
	}
	if task.IsTerminal() {
		t.Fatalf("handling should not be terminal")
	}
}

func TestTaskFromMap_FailWithError(t *testing.T) {
	m := decode(t, `{"taskId":"t","model":"m","status":"fail","error":{"code":"E","message":"boom"}}`)
	task := taskFromMap(m)
	if !task.IsTerminal() || task.Succeeded() {
		t.Fatalf("fail flags: %+v", task)
	}
	if task.Error == nil || task.Error.Code != "E" || task.Error.Message != "boom" {
		t.Fatalf("error: %+v", task.Error)
	}
}

func TestTaskFromMap_UnknownStatusParses(t *testing.T) {
	// A status the server adds later must parse rather than break.
	m := decode(t, `{"taskId":"t","model":"m","status":"reticulating"}`)
	task := taskFromMap(m)
	if task.Status != "reticulating" || task.IsTerminal() {
		t.Fatalf("unknown status: %+v", task)
	}
}

func TestCreatedTaskFromMap(t *testing.T) {
	ct := createdTaskFromMap(decode(t, `{"taskId":"new-id"}`))
	if ct.TaskID != "new-id" {
		t.Fatalf("created: %+v", ct)
	}
}

func TestTaskPageFromMap_TasksKey(t *testing.T) {
	m := decode(t, `{"tasks":[{"taskId":"a","status":"success"},{"taskId":"b","status":"fail"}],"page":2,"size":20,"total":41}`)
	p := taskPageFromMap(m)
	if len(p.Items) != 2 || p.Items[0].TaskID != "a" || p.Items[1].TaskID != "b" {
		t.Fatalf("items: %+v", p.Items)
	}
	if p.Page != 2 || p.Size != 20 || p.Total != 41 {
		t.Fatalf("pagination: %+v", p)
	}
}

func TestTaskPageFromMap_AlternateKeysAndPrecedence(t *testing.T) {
	// "tasks" wins over alternates when both present.
	m := decode(t, `{"tasks":[{"taskId":"real"}],"items":[{"taskId":"decoy"}]}`)
	p := taskPageFromMap(m)
	if len(p.Items) != 1 || p.Items[0].TaskID != "real" {
		t.Fatalf("precedence: %+v", p.Items)
	}

	// Falls back to "items" when "tasks" absent.
	p2 := taskPageFromMap(decode(t, `{"items":[{"taskId":"x"}]}`))
	if len(p2.Items) != 1 || p2.Items[0].TaskID != "x" {
		t.Fatalf("alternate: %+v", p2.Items)
	}
}

func TestTaskFromMap_DefensiveMissingFields(t *testing.T) {
	task := taskFromMap(decode(t, `{}`))
	if task.TaskID != "" || task.Status != "" || task.CreatedAt != 0 || task.Error != nil || len(task.Output) != 0 {
		t.Fatalf("empty parse not defensive: %+v", task)
	}
}
