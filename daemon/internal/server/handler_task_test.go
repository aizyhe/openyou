package server_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestHandleListTasks verifies GET /api/tasks returns 200 with a tasks list.
func TestHandleListTasks(t *testing.T) {
	h, deps := e2eDeps(t, &fakeLLM{replyFor: func(string) string { return "" }})
	deps.Tasks.Create("write report", "", "2026-08-01", "user", "", 0)

	w := doRequest(t, h, http.MethodGet, "/api/tasks", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list tasks: want 200, got %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Tasks) != 1 {
		t.Errorf("want 1 task, got %d", len(out.Tasks))
	}
}

// TestHandleCreateTask_MissingTitle verifies POST /api/tasks returns 400 when title is empty.
func TestHandleCreateTask_MissingTitle(t *testing.T) {
	h, _ := e2eDeps(t, &fakeLLM{replyFor: func(string) string { return "" }})

	w := doRequest(t, h, http.MethodPost, "/api/tasks", []byte(`{"title":""}`))
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty title want 400, got %d", w.Code)
	}
}

// TestHandleCreateTask_BadJSON verifies POST /api/tasks returns 400 for invalid JSON.
func TestHandleCreateTask_BadJSON(t *testing.T) {
	h, _ := e2eDeps(t, &fakeLLM{replyFor: func(string) string { return "" }})

	w := doRequest(t, h, http.MethodPost, "/api/tasks", []byte(`{bad`))
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad json want 400, got %d", w.Code)
	}
}

// TestHandleCreateTask_BadDate verifies POST /api/tasks returns 400 for an invalid date.
func TestHandleCreateTask_BadDate(t *testing.T) {
	h, _ := e2eDeps(t, &fakeLLM{replyFor: func(string) string { return "" }})

	w := doRequest(t, h, http.MethodPost, "/api/tasks", []byte(`{"title":"x","end_date":"not-a-date"}`))
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid date want 400, got %d", w.Code)
	}
}

// TestHandleCreateTask_Success verifies POST /api/tasks creates a task and returns 200.
func TestHandleCreateTask_Success(t *testing.T) {
	h, _ := e2eDeps(t, &fakeLLM{replyFor: func(string) string { return "" }})

	body := `{"title":"write report","end_date":"2026-08-01"}`
	w := doRequest(t, h, http.MethodPost, "/api/tasks", []byte(body))
	if w.Code != http.StatusOK {
		t.Fatalf("create task want 200, got %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Task map[string]any `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Task["title"] != "write report" {
		t.Errorf("task title want %q, got %v", "write report", out.Task["title"])
	}
}

// TestHandleUpdateTask_NotFound verifies PUT /api/tasks/{id} returns 404 for a
// missing id (store returns "not found" error, handler maps it to 404).
func TestHandleUpdateTask_NotFound(t *testing.T) {
	h, _ := e2eDeps(t, &fakeLLM{replyFor: func(string) string { return "" }})

	body := `{"title":"x"}`
	w := doRequest(t, h, http.MethodPut, "/api/tasks/nonexistent_id", []byte(body))
	if w.Code != http.StatusNotFound {
		t.Errorf("update missing task want 404, got %d", w.Code)
	}
}

// TestHandleUpdateTask_MissingTitle verifies PUT /api/tasks/{id} returns 400 for empty title.
func TestHandleUpdateTask_MissingTitle(t *testing.T) {
	h, deps := e2eDeps(t, &fakeLLM{replyFor: func(string) string { return "" }})
	task, _ := deps.Tasks.Create("original", "", "", "user", "", 0)

	w := doRequest(t, h, http.MethodPut, "/api/tasks/"+task.ID, []byte(`{"title":""}`))
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty title update want 400, got %d", w.Code)
	}
}

// TestHandleUpdateTask_Success verifies PUT /api/tasks/{id} updates a task and returns 200.
func TestHandleUpdateTask_Success(t *testing.T) {
	h, deps := e2eDeps(t, &fakeLLM{replyFor: func(string) string { return "" }})
	task, _ := deps.Tasks.Create("original", "", "", "user", "", 0)

	body := `{"title":"updated","done":true}`
	w := doRequest(t, h, http.MethodPut, "/api/tasks/"+task.ID, []byte(body))
	if w.Code != http.StatusOK {
		t.Errorf("update task want 200, got %d %s", w.Code, w.Body.String())
	}
}

// TestHandleDeleteTask_NotFound verifies DELETE /api/tasks/{id} returns 404 for a missing id.
func TestHandleDeleteTask_NotFound(t *testing.T) {
	h, _ := e2eDeps(t, &fakeLLM{replyFor: func(string) string { return "" }})

	w := doRequest(t, h, http.MethodDelete, "/api/tasks/nonexistent_id", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("delete missing task want 404, got %d", w.Code)
	}
}

// TestHandleDeleteTask_Success verifies DELETE /api/tasks/{id} deletes a task and returns 200.
func TestHandleDeleteTask_Success(t *testing.T) {
	h, deps := e2eDeps(t, &fakeLLM{replyFor: func(string) string { return "" }})
	task, _ := deps.Tasks.Create("to delete", "", "", "user", "", 0)

	w := doRequest(t, h, http.MethodDelete, "/api/tasks/"+task.ID, nil)
	if w.Code != http.StatusOK {
		t.Errorf("delete task want 200, got %d %s", w.Code, w.Body.String())
	}
}

// Regression for the full-replace bug: the UI sends only {title,end_date,done}
// and the old handler zeroed every omitted field on each toggle.
func TestHandleUpdateTask_PartialKeepsOmittedFields(t *testing.T) {
	h, deps := e2eDeps(t, &fakeLLM{replyFor: func(string) string { return "" }})
	task, err := deps.Tasks.CreateFull("star action", "full action plan", "2026-08-01", "2026-08-10", "high", "conversation", "ses1", 0)
	if err != nil {
		t.Fatalf("CreateFull: %v", err)
	}

	w := doRequest(t, h, http.MethodPut, "/api/tasks/"+task.ID,
		[]byte(`{"title":"star action","end_date":"2026-08-10","done":true}`))
	if w.Code != http.StatusOK {
		t.Fatalf("partial update: %d %s", w.Code, w.Body.String())
	}
	got, err := deps.Tasks.Get(task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Done {
		t.Error("done flag not applied")
	}
	if got.Description != "full action plan" {
		t.Errorf("description must survive a toggle, got %q", got.Description)
	}
	if got.Priority != "high" {
		t.Errorf("priority must survive a toggle, got %q", got.Priority)
	}
	if got.StartDate != "2026-08-01" {
		t.Errorf("start_date must survive a toggle, got %q", got.StartDate)
	}

	// An explicitly-present field with empty value still clears (presence semantics).
	w = doRequest(t, h, http.MethodPut, "/api/tasks/"+task.ID,
		[]byte(`{"title":"star action","description":""}`))
	if w.Code != http.StatusOK {
		t.Fatalf("clear description: %d %s", w.Code, w.Body.String())
	}
	got, _ = deps.Tasks.Get(task.ID)
	if got.Description != "" {
		t.Errorf("explicit empty description should clear, got %q", got.Description)
	}
	if got.Priority != "high" {
		t.Errorf("priority must survive description clear, got %q", got.Priority)
	}
}

// TestHandleCreateTask_SourceValidation pins the source field contract:
// default "user", explicit "conversation" accepted, unknown rejected.
func TestHandleCreateTask_SourceValidation(t *testing.T) {
	h, deps := e2eDeps(t, &fakeLLM{replyFor: func(string) string { return "" }})

	create := func(body string) string {
		w := doRequest(t, h, http.MethodPost, "/api/tasks", []byte(body))
		if w.Code != http.StatusOK {
			t.Fatalf("create %s: %d %s", body, w.Code, w.Body.String())
		}
		var out struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out.Task.ID
	}

	id := create(`{"title":"manual"}`)
	if tk, err := deps.Tasks.Get(id); err != nil || tk.Source != "user" {
		t.Errorf("default source want user, got %+v err=%v", tk, err)
	}
	id = create(`{"title":"from star","source":"conversation"}`)
	if tk, err := deps.Tasks.Get(id); err != nil || tk.Source != "conversation" {
		t.Errorf("explicit conversation source, got %+v err=%v", tk, err)
	}
	if w := doRequest(t, h, http.MethodPost, "/api/tasks", []byte(`{"title":"x","source":"bogus"}`)); w.Code != http.StatusBadRequest {
		t.Errorf("invalid source want 400, got %d", w.Code)
	}
}
