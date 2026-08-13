// Package main wires the daemon for both local `go run` and the Android JNI entry.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"openyou/daemon/internal/ai/llm"
	"openyou/daemon/internal/ai/thinktank"
	"openyou/daemon/internal/infra/bridge"
	"openyou/daemon/internal/infra/config"
	"openyou/daemon/internal/infra/crypto"
	dbpkg "openyou/daemon/internal/infra/db"
	"openyou/daemon/internal/infra/notify"
	"openyou/daemon/internal/infra/privacy"
	"openyou/daemon/internal/infra/tool"
	"openyou/daemon/internal/server"
	"openyou/daemon/internal/service/background"
	"openyou/daemon/internal/service/importer"
	"openyou/daemon/internal/service/matcher"
	"openyou/daemon/internal/store/consult"
	"openyou/daemon/internal/store/memory"
	"openyou/daemon/internal/store/task"
)

const listenAddr = "127.0.0.1:8765"

// Run wires all stores and starts the HTTP server, blocking until it exits.
func Run(dataDir string) error {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	dbPath := filepath.Join(dataDir, "openyou.db")
	db, err := sql.Open("sqlite3", dbPath+"?_busy_timeout=5000&_journal_mode=WAL")
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	// Close on every exit path — the Android retry loop re-calls Run, so an
	// open *sql.DB would leak per attempt.
	defer db.Close()
	db.SetMaxOpenConns(1) // SQLite: serialize writes to avoid "database is locked"

	// Schema is unreleased: every table is created by its store's CREATE TABLE, so no legacy upgrade paths.
	migrations := []dbpkg.Migration{
		{Version: 1, Up: `SELECT 1`},
	}
	if err := dbpkg.Migrate(db, migrations); err != nil {
		return fmt.Errorf("migration: %w", err)
	}
	cfgStore, err := config.NewStore(db)
	if err != nil {
		return err
	}
	// Encrypt api_key with a master key in the data dir; a future Android step
	// will source it from the hardware Keystore.
	if key, kerr := loadOrCreateKey(filepath.Join(dataDir, "master.key")); kerr == nil {
		if c, cerr := crypto.NewCipher(key); cerr == nil {
			cfgStore.EnableEncryption(c)
		} else {
			log.Printf("crypto: cipher init failed, encryption disabled: %v", cerr)
		}
	} else {
		log.Printf("crypto: key load failed, encryption disabled: %v", kerr)
	}
	consultStore, err := consult.NewStore(db)
	if err != nil {
		return err
	}
	memStore, err := memory.NewStore(db)
	if err != nil {
		return err
	}
	taskStore, err := task.NewStore(db)
	if err != nil {
		return err
	}

	// First-boot import; skipped on later runs (idempotent).
	if res, err := importer.Import(cfgStore); err != nil {
		log.Printf("importer: %v", err)
	} else if !res.Skipped {
		log.Printf("importer: context-imported flag set")
	}

	// The matcher loads skill docs from the embedded userdata FS plus the runtime dir.
	userdataDir := filepath.Join(dataDir, "userdata")
	// Rename legacy plural userdata dirs to singular before the matcher loads.
	userdataSingularize(userdataDir)
	if err := os.MkdirAll(filepath.Join(userdataDir, "memory"), 0755); err == nil {
		memory.MarkdownDir = filepath.Join(userdataDir, "memory")
	}
	skillMatcher, err := matcher.NewMatcherWithDir(userdataDir, importer.SeedFS)
	if err != nil {
		log.Printf("userdata: %v (falling back to embedded)", err)
		skillMatcher, err = matcher.NewMatcher()
		if err != nil {
			return fmt.Errorf("userdata matcher: %w", err)
		}
	}

	// userdata/openyou.json(.jsonc) as defaults; Settings-page values win and are never overwritten.
	applyUserDataConfig(cfgStore, userdataDir)

	// Pseudonymizer seeded from inside file names; "trust" mode disables it.
	var names []string
	if c, err := skillMatcher.ReadDir("inside"); err == nil {
		for _, line := range strings.Split(c, "\n") {
			if strings.HasPrefix(line, "# ") {
				names = append(names, strings.TrimSpace(strings.TrimPrefix(line, "# ")))
			}
		}
	}
	pseudo := privacy.New(names)
	if mode, _ := cfgStore.Get("privacy_mode"); mode == "trust" {
		pseudo.SetEnabled(false)
	}

	// Every prompt is pseudonymized and every response un-pseudonymized here —
	// the single choke point for chat, thinktank, and memory.
	rawLLM := llm.NewClient(cfgStore)
	llmClient := llm.NewMaskingClient(rawLLM, pseudo)

	hub := bridge.NewHub()
	notifyQueue := notify.NewQueue(32)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	extractor := memory.NewLLMMemoryExtractor(ctx, llmClient)
	memQueue := memory.NewQueue(extractor, memStore, 2, notifyQueue)

	bt := thinktank.New(
		llmClient,
		thinktank.NewSkillAdapter(skillMatcher),
		thinktank.NewMemoryAdapter(memStore),
		skillMatcher,
	)

	// Background workers (star 6h, health 24h, decay 24h).
	background.StartWithTasks(ctx, memStore, taskStore, skillMatcher, hub, notifyQueue)

	deps := server.Deps{
		DB:            db,
		LLM:           llmClient,
		Tools:         tool.NewRegistry(),
		Config:        cfgStore,
		Sessions:      consultStore,
		Hub:           hub,
		Memory:        memStore,
		MemoryQueue:   memQueue,
		SkillMatcher:  skillMatcher,
		Skills:        skillMatcher,
		ThinkTank:     bt,
		Feedback:      consultStore,
		Pseudonymizer: pseudo,
		Tasks:         taskStore,
		Notify:        notifyQueue,
		DocsDir:       userdataDir,
	}

	handler := server.NewRouter(deps)
	log.Printf("openyou daemon listening on http://%s", listenAddr)
	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// runDone makes the signal goroutine exit when Run returns for any other
	// reason (Android retry loop) so handlers don't pile up across retried runs.
	runDone := make(chan struct{})
	defer close(runDone)
	shutdownComplete := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case <-sigCh:
		case <-runDone:
			return
		}
		log.Println("shutting down...")
		cancel()
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = srv.Shutdown(shutCtx)
		shutCancel()
		// Stop the queue after HTTP shutdown, so no jobs enqueue post-drain.
		memQueue.Stop()
		close(shutdownComplete)
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("server error: %v", err)
		// The Android retry loop restarts only on non-nil errors.
		return fmt.Errorf("listen: %w", err)
	}
	// Returning early on ErrServerClosed would let main exit while Shutdown is
	// still draining — dropping queued extractions.
	<-shutdownComplete
	return nil
}

func main() {
	dataDir := os.Getenv("OPENYOU_DATA_DIR")
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".openyou")
	}
	if err := Run(dataDir); err != nil {
		log.Fatalf("daemon exited: %v", err)
	}
}

// loadOrCreateKey persists a 32-byte master key (0600); future Android wraps it in Keystore.
func loadOrCreateKey(path string) ([]byte, error) {
	if data, err := os.ReadFile(path); err == nil && len(data) == 32 {
		return data, nil
	}
	key, err := crypto.GenerateKey()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0600); err != nil {
		return nil, err
	}
	return key, nil
}

// applyUserDataConfig seeds defaults from userdata/openyou.json(.jsonc); a value
// set via the Settings page always wins.
func applyUserDataConfig(cfg *config.Store, userdataDir string) {
	path := ""
	for _, name := range []string{"openyou.json", "openyou.jsonc"} {
		p := filepath.Join(userdataDir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			path = p
			break
		}
	}
	if path == "" {
		log.Print("config: no userdata/openyou.json(.jsonc), using built-in defaults")
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("config: read %s: %v", path, err)
		return
	}
	var conf struct {
		APIKey      string  `json:"api_key"`
		BaseURL     string  `json:"base_url"`
		Model       string  `json:"model"`
		MaxTokens   int     `json:"max_tokens"`
		Temperature float64 `json:"temperature"`
		PrivacyMode string  `json:"privacy_mode"`
	}
	if err := json.Unmarshal(stripJSONComments(data), &conf); err != nil {
		log.Printf("config: parse %s: %v", path, err)
		return
	}
	// Only fill empty config keys — the Settings page is the higher-priority source.
	fill := func(k, v string) {
		if v == "" {
			return
		}
		if cur, _ := cfg.Get(k); cur == "" {
			if err := cfg.Set(k, v); err != nil {
				log.Printf("config: set %s from %s: %v", k, path, err)
			}
		}
	}
	fill("api_key", conf.APIKey)
	fill("base_url", conf.BaseURL)
	fill("model", conf.Model)
	fill("privacy_mode", conf.PrivacyMode)
	if conf.MaxTokens > 0 {
		fill("max_tokens", strconv.Itoa(conf.MaxTokens))
	}
	if conf.Temperature > 0 {
		fill("temperature", strconv.FormatFloat(conf.Temperature, 'f', -1, 64))
	}
	log.Printf("config: applied %s (api_key set=%t)", path, conf.APIKey != "")
}

func stripJSONComments(src []byte) []byte {
	out := make([]byte, 0, len(src))
	inStr := false
	esc := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inStr {
			out = append(out, c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			out = append(out, c)
			continue
		}
		if c == '/' && i+1 < len(src) && src[i+1] == '/' {
			for i < len(src) && src[i] != '\n' {
				i++
			}
			out = append(out, '\n')
			continue
		}
		if c == '/' && i+1 < len(src) && src[i+1] == '*' {
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i++
			out = append(out, '\n')
			continue
		}
		out = append(out, c)
	}
	return out
}

func userdataSingularize(root string) {
	pairs := []struct{ old, new string }{
		{"insides", "inside"}, {"rules", "rule"},
		{"skills", "skill"}, {"tasks", "task"}, {"memories", "memory"},
		{"stars", "star"}, {"inside/events", "inside/event"},
	}
	for _, p := range pairs {
		oldPath := filepath.Join(root, filepath.FromSlash(p.old))
		newPath := filepath.Join(root, filepath.FromSlash(p.new))
		if info, err := os.Stat(oldPath); err == nil && info.IsDir() {
			if _, err := os.Stat(newPath); os.IsNotExist(err) {
				if err := os.Rename(oldPath, newPath); err != nil {
					log.Printf("userdata: rename %s -> %s: %v", oldPath, newPath, err)
				} else {
					log.Printf("userdata: migrated %s -> %s", oldPath, newPath)
				}
			}
		}
	}
}
