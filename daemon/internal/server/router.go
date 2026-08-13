package server

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"openyou/daemon/internal/ai/agent"
	"openyou/daemon/internal/ai/llm"
	"openyou/daemon/internal/ai/thinktank"
	"openyou/daemon/internal/infra/bridge"
	"openyou/daemon/internal/infra/config"
	"openyou/daemon/internal/infra/notify"
	"openyou/daemon/internal/infra/privacy"
	"openyou/daemon/internal/infra/tool"
	"openyou/daemon/internal/server/web"
	"openyou/daemon/internal/service/matcher"
	"openyou/daemon/internal/store/consult"
	"openyou/daemon/internal/store/memory"
	"openyou/daemon/internal/store/task"
)

func handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Write(web.IndexHTML)
}

// requireJSONBody is a CSRF guard: mutating requests must declare
// application/json so a cross-origin page can't rewrite config via a simple
// text/plain request (JSON triggers a CORS preflight this server never allows).
func requireJSONBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			if r.ContentLength != 0 && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				writeErr(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ChatStreamer is the LLM interface the chat handler needs; both *llm.Client and
// *llm.MaskingClient satisfy it, so privacy masking stays transparent.
type ChatStreamer interface {
	Stream(ctx context.Context, messages []llm.Message, onDelta func(string)) (string, error)
	Chat(ctx context.Context, systemPrompt, userMessage string) (string, error)
}

type Deps struct {
	DB              *sql.DB
	LLM             ChatStreamer
	Tools           *tool.Registry
	Config          *config.Store
	Sessions        *consult.Store
	Hub             *bridge.Hub
	Memory          *memory.Store
	MemoryExtractor memory.MemoryExtractor
	MemoryQueue     *memory.Queue
	SkillMatcher    agent.SkillMatcher
	Skills          *matcher.Matcher
	ThinkTank       *thinktank.ThinkTank
	Feedback        *consult.Store
	Pseudonymizer   *privacy.Pseudonymizer
	Tasks           *task.Store
	Notify          *notify.Queue
	DocsDir         string
	// LLMSem caps concurrent LLM requests (chat/analyze/star) to prevent unbounded fan-out.
	LLMSem chan struct{}
}

// NewRouter: default the semaphore here so callers that omit LLMSem still work correctly.
func NewRouter(deps Deps) http.Handler {
	if deps.LLMSem == nil {
		deps.LLMSem = make(chan struct{}, 3)
	}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", handleIndex)
	mux.HandleFunc("GET /api/health", handleHealth(deps))

	mux.HandleFunc("POST /api/chat", handleChat(deps))

	mux.HandleFunc("GET /api/sessions", handleListSessions(deps))
	mux.HandleFunc("GET /api/sessions/{id}", handleGetSession(deps))
	mux.HandleFunc("DELETE /api/sessions/{id}", handleDeleteSession(deps))

	mux.HandleFunc("GET /api/config", handleGetConfig(deps))
	mux.HandleFunc("PUT /api/config", handleUpdateConfig(deps))

	mux.HandleFunc("GET /api/skills", handleListSkills(deps))
	mux.HandleFunc("POST /api/skills", handleCreateSkill(deps))
	mux.HandleFunc("POST /api/skills/reload", handleReloadSkills(deps))
	mux.HandleFunc("GET /api/skills/{name}", handleGetSkill(deps))
	mux.HandleFunc("PUT /api/skills/{name}", handleUpdateSkill(deps))
	mux.HandleFunc("DELETE /api/skills/{name}", handleDeleteSkill(deps))

	mux.HandleFunc("GET /api/docs", handleListDocs(deps))
	mux.HandleFunc("GET /api/docs/{path...}", handleGetDoc(deps))
	mux.HandleFunc("PUT /api/docs/{path...}", handlePutDoc(deps))
	mux.HandleFunc("DELETE /api/docs/{path...}", handleDeleteDoc(deps))

	mux.HandleFunc("POST /api/analyze", handleAnalyze(deps))

	mux.HandleFunc("GET /api/memory", handleListMemory(deps))
	mux.HandleFunc("PUT /api/memory/{id}", handleUpdateMemory(deps))
	mux.HandleFunc("PUT /api/memory/{id}/confirm", handleConfirmMemory(deps))
	mux.HandleFunc("PUT /api/memory/{id}/dispute", handleDisputeMemory(deps))
	mux.HandleFunc("DELETE /api/memory/{id}", handleDeleteMemory(deps))

	mux.HandleFunc("GET /api/home", handleHome(deps))

	mux.HandleFunc("POST /api/feedback", handleSaveFeedback(deps))
	mux.HandleFunc("GET /api/feedback/pending", handlePendingFeedback(deps))

	// POST accepts {star_history, settings} so frontend-local data can ride along
	// in the manifest; both methods produce the same zip archive.
	mux.HandleFunc("GET /api/backup/export", handleBackupExport(deps))
	mux.HandleFunc("POST /api/backup/export", handleBackupExport(deps))
	mux.HandleFunc("POST /api/backup/import", handleBackupImport(deps))

	mux.HandleFunc("/ws", bridge.HandleWS(deps.Hub))

	mux.HandleFunc("GET /api/tasks", handleListTasks(deps))
	mux.HandleFunc("POST /api/tasks", handleCreateTask(deps))
	mux.HandleFunc("PUT /api/tasks/{id}", handleUpdateTask(deps))
	mux.HandleFunc("DELETE /api/tasks/{id}", handleDeleteTask(deps))

	mux.HandleFunc("POST /api/star", handleStar(deps))

	mux.HandleFunc("GET /api/notifications/poll", handleNotificationsPoll(deps))

	mux.HandleFunc("POST /api/skills/curate", handleCurateSkills(deps))

	return requireJSONBody(mux)
}
