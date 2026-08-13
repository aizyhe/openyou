package server_test

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"openyou/daemon/internal/infra/bridge"
	"openyou/daemon/internal/infra/tool"
	"openyou/daemon/internal/server"
	"openyou/daemon/internal/service/matcher"
	"openyou/daemon/internal/store/consult"
)

func newFeedbackTestRouter(t *testing.T) (http.Handler, string) {
	t.Helper()

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	fbStore, err := consult.NewStore(db)
	if err != nil {
		t.Fatalf("consult.NewStore: %v", err)
	}

	dir := t.TempDir()
	sk, err := matcher.NewMatcherWithDir(dir, nil)
	if err != nil {
		t.Fatalf("matcher: %v", err)
	}

	deps := server.Deps{
		Feedback: fbStore,
		Skills:   sk,
		Hub:      bridge.NewHub(),
		Tools:    tool.NewRegistry(),
		DocsDir:  dir,
	}

	return server.NewRouter(deps), dir
}

// ruleDocPath returns the /api/docs path for the current month's rule file.
func ruleDocPath() string {
	return "/api/docs/rule/" + time.Now().Format("2006-01") + ".md"
}

func TestFeedbackInaccurateCreatesRule(t *testing.T) {
	handler, _ := newFeedbackTestRouter(t)

	// Seed an existing rule file via the docs API.
	body1 := []byte(`{"content": "#1. [2026-07-25] Be nice. — from: onboarding"}`)
	w1 := doRequest(t, handler, http.MethodPut, ruleDocPath(), body1)
	if w1.Code != http.StatusOK {
		t.Errorf("put rules: status = %d, want %d", w1.Code, http.StatusOK)
	}

	body := []byte(`{"session_id":"ses_test","turn_index":0,"rating":"inaccurate","correction":"Never trust Bob with deadlines"}`)
	w := doRequest(t, handler, http.MethodPost, "/api/feedback", body)
	if w.Code != http.StatusOK {
		t.Errorf("save feedback: status = %d, want %d", w.Code, http.StatusOK)
	}

	// Read back the rule file via the docs API (returns raw markdown).
	w2 := doRequest(t, handler, http.MethodGet, ruleDocPath(), nil)
	if w2.Code != http.StatusOK {
		t.Errorf("get rules: status = %d, want %d", w2.Code, http.StatusOK)
	}
	content := w2.Body.String()
	if !strings.Contains(content, "Never trust Bob with deadlines") {
		t.Errorf("rules should contain correction text, got: %q", content)
	}
	if !strings.Contains(content, "- [") {
		t.Errorf("rules should contain appended correction line, got: %q", content)
	}
	if !strings.Contains(content, "from: session ses_test feedback") {
		t.Errorf("rule should cite source session, got: %q", content)
	}
}

func TestFeedbackUsefulDoesNotCreateUnnecessaryRules(t *testing.T) {
	handler, _ := newFeedbackTestRouter(t)

	// Seed an existing rule file via the docs API.
	body1 := []byte(`{"content": "#1. [2026-07-25] Be nice. — from: onboarding"}`)
	w1 := doRequest(t, handler, http.MethodPut, ruleDocPath(), body1)
	if w1.Code != http.StatusOK {
		t.Errorf("put rules: status = %d, want %d", w1.Code, http.StatusOK)
	}

	body := []byte(`{"session_id":"ses_test","turn_index":0,"rating":"useful","correction":""}`)
	w := doRequest(t, handler, http.MethodPost, "/api/feedback", body)
	if w.Code != http.StatusOK {
		t.Errorf("save feedback: status = %d, want %d", w.Code, http.StatusOK)
	}

	w2 := doRequest(t, handler, http.MethodGet, ruleDocPath(), nil)
	if w2.Code != http.StatusOK {
		t.Errorf("get rules: status = %d, want %d", w2.Code, http.StatusOK)
	}
	content := w2.Body.String()
	if strings.Contains(content, "#2.") {
		t.Errorf("no new rule should be created for useful feedback, got: %q", content)
	}
}

func TestFeedbackEmptyCorrectionDoesNotCreateRule(t *testing.T) {
	handler, _ := newFeedbackTestRouter(t)

	// Seed an existing rule file via the docs API.
	body1 := []byte(`{"content": "#1. [2026-07-25] Be nice. — from: onboarding"}`)
	w1 := doRequest(t, handler, http.MethodPut, ruleDocPath(), body1)
	if w1.Code != http.StatusOK {
		t.Errorf("put rules: status = %d, want %d", w1.Code, http.StatusOK)
	}

	body := []byte(`{"session_id":"ses_test","turn_index":0,"rating":"inaccurate","correction":""}`)
	w := doRequest(t, handler, http.MethodPost, "/api/feedback", body)
	if w.Code != http.StatusOK {
		t.Errorf("save feedback: status = %d, want %d", w.Code, http.StatusOK)
	}

	w2 := doRequest(t, handler, http.MethodGet, ruleDocPath(), nil)
	if w2.Code != http.StatusOK {
		t.Errorf("get rules: status = %d, want %d", w2.Code, http.StatusOK)
	}
	content := w2.Body.String()
	if strings.Contains(content, "#2.") {
		t.Errorf("no new rule should be created for empty correction, got: %q", content)
	}
}
