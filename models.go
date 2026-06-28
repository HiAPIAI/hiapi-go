package hiapi

// Task status values reported by the API. Status is kept as a plain string
// (not a closed enum) on purpose: a status the server adds later must still
// parse rather than break an older SDK.
const (
	StatusQueued    = "queued"
	StatusHandling  = "handling"
	StatusArchiving = "archiving"
	StatusSuccess   = "success"
	StatusFail      = "fail"
)

// terminalStatuses are the states a task can never leave once reached.
var terminalStatuses = map[string]bool{
	StatusSuccess: true,
	StatusFail:    true,
}

// Output is one generated artifact produced by a successful task.
type Output struct {
	URL      string
	Type     string
	ExpireAt int64 // 0 when absent
	// Raw is the full decoded JSON object for this output.
	Raw map[string]any
}

func outputFromMap(m map[string]any) Output {
	return Output{
		URL:      asString(m["url"]),
		Type:     asString(m["type"]),
		ExpireAt: asInt64(m["expireAt"]),
		Raw:      m,
	}
}

// TaskError is the failure reason attached to a task in the fail state.
type TaskError struct {
	Code    string
	Message string
	// Raw is the full decoded JSON object for this error.
	Raw map[string]any
}

func taskErrorFromMap(m map[string]any) *TaskError {
	return &TaskError{
		Code:    asString(m["code"]),
		Message: asString(m["message"]),
		Raw:     m,
	}
}

// Task is an async generation task and (when finished) its output.
//
// The wire format uses camelCase (taskId, expireAt) and epoch-second integers;
// these fields expose Go-idiomatic names and parse defensively — unknown fields
// stay in Raw so a server-side addition never breaks an older SDK.
type Task struct {
	TaskID string
	Model  string
	Status string
	// CreatedAt and CompletedAt are Unix epoch seconds; 0 means absent. The API
	// sends completed:0 while a task is non-terminal, normalized to 0 here.
	CreatedAt   int64
	CompletedAt int64
	Storage     string
	Output      []Output
	Error       *TaskError
	// Raw is the full decoded JSON object for this task.
	Raw map[string]any
}

// IsTerminal reports whether the task has reached a state it can never leave.
func (t *Task) IsTerminal() bool { return terminalStatuses[t.Status] }

// Succeeded reports whether the task finished successfully.
func (t *Task) Succeeded() bool { return t.Status == StatusSuccess }

func taskFromMap(m map[string]any) *Task {
	t := &Task{
		TaskID:      asString(m["taskId"]),
		Model:       asString(m["model"]),
		Status:      asString(m["status"]),
		CreatedAt:   asInt64(m["created"]),
		CompletedAt: asInt64(m["completed"]),
		Storage:     asString(m["storage"]),
		Raw:         m,
	}
	if outs, ok := m["output"].([]any); ok {
		for _, o := range outs {
			if om, ok := o.(map[string]any); ok {
				t.Output = append(t.Output, outputFromMap(om))
			}
		}
	}
	if em, ok := m["error"].(map[string]any); ok {
		t.Error = taskErrorFromMap(em)
	}
	return t
}

// CreatedTask is the thin response of POST /v1/tasks — just the new task id.
type CreatedTask struct {
	TaskID string
	// Raw is the full decoded JSON object.
	Raw map[string]any
}

func createdTaskFromMap(m map[string]any) *CreatedTask {
	return &CreatedTask{TaskID: asString(m["taskId"]), Raw: m}
}

// TaskPage is one page of GET /v1/tasks (newest first).
type TaskPage struct {
	Items []*Task
	Page  int
	Size  int
	Total int
	// Raw is the full decoded JSON object for the page.
	Raw map[string]any
}

func taskPageFromMap(m map[string]any) *TaskPage {
	p := &TaskPage{
		Page:  asInt(m["page"]),
		Size:  asInt(m["size"]),
		Total: asInt(m["total"]),
		Raw:   m,
	}
	// The API returns the page array under "tasks"; accept common alternates too
	// so a paginated list never silently parses to empty.
	for _, key := range []string{"tasks", "items", "list", "data", "records"} {
		if arr, ok := m[key].([]any); ok {
			for _, it := range arr {
				if im, ok := it.(map[string]any); ok {
					p.Items = append(p.Items, taskFromMap(im))
				}
			}
			break
		}
	}
	return p
}

// asString coerces a decoded JSON value to a string ("" when absent/non-string).
func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// asInt64 coerces a decoded JSON number to int64 (0 when absent/non-numeric).
// JSON numbers decode as float64 via encoding/json.
func asInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}

// asInt coerces a decoded JSON number to int (0 when absent/non-numeric).
func asInt(v any) int { return int(asInt64(v)) }
