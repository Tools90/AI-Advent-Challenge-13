package main

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web/index.html
var webFS embed.FS

type config struct {
	keyFile string
	apiKey  string
	baseURL string
	model   string
	addr    string
}

type server struct {
	cfg    config
	store  *Store
	llm    LLMClient
	chatMu sync.Mutex
}

func main() {
	if err := loadDotEnv(".env"); err != nil {
		log.Printf("config: %v", err)
	}
	cfg := loadConfig()
	if cfg.apiKey == "" {
		log.Printf("deepseek: ключ не найден в %s", cfg.keyFile)
	}
	store, err := NewStore("data")
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	s := &server{cfg: cfg, store: store, llm: NewDeepSeekClient(cfg.baseURL, cfg.apiKey, cfg.model)}

	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Printf("сервер запущен: http://localhost%s (модель: %s)", cfg.addr, cfg.model)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()
	<-ctx.Done()
	log.Println("остановка...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.index)
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("POST /api/tasks", s.createTask)
	mux.HandleFunc("POST /api/tasks/select", s.selectTask)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.deleteTask)
	mux.HandleFunc("POST /api/tasks/{id}/chat", s.chat)
	mux.HandleFunc("POST /api/tasks/{id}/confirm", s.confirm)
	mux.HandleFunc("POST /api/tasks/{id}/transition", s.transition)
	mux.HandleFunc("POST /api/tasks/{id}/cancel", s.cancelProposal)
	mux.HandleFunc("POST /api/demo/reset", s.resetDemo)
	mux.HandleFunc("POST /api/clear-all", s.clearAll)
	return logRequests(mux)
}

func (s *server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "index.html не найден", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "model": s.cfg.model, "configured": s.cfg.apiKey != ""})
}

func (s *server) state(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"model":      s.cfg.model,
		"configured": s.cfg.apiKey != "",
		"state":      s.store.State(),
	})
}

func (s *server) createTask(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	task, err := s.store.Create(body.Title, body.Description)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"task": task, "state": s.store.State()})
}

func (s *server) selectTask(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.Select(body.ID); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": s.store.State()})
}

func (s *server) deleteTask(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Delete(r.PathValue("id")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": s.store.State()})
}

func (s *server) chat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Message string `json:"message"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	message := strings.TrimSpace(body.Message)
	if message == "" {
		writeError(w, http.StatusBadRequest, "сообщение пустое")
		return
	}
	if len([]rune(message)) > 4000 {
		writeError(w, http.StatusBadRequest, "сообщение слишком длинное")
		return
	}
	id := r.PathValue("id")
	task := s.store.Task(id)
	if task == nil {
		writeError(w, http.StatusNotFound, "задача не найдена")
		return
	}

	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	userIndex := len(task.Messages)
	if err := s.store.AddMessage(id, Message{Role: "user", Content: message}); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	task = s.store.Task(id)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	result, usage, err := AskAssistant(ctx, s.llm, task, message)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	assistantIndex := len(task.Messages)
	if err := s.store.AddMessage(id, Message{Role: "assistant", Content: result.Reply}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.AddTurn(id, TurnMeta{UserIndex: userIndex, AssistantIndex: assistantIndex, Request: result.Request}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var proposal *Proposal
	var rejection *TransitionError
	if result.ProposedAction != "" {
		proposal, err = s.store.Propose(id, result.ProposedAction, result.Reason)
		if err != nil {
			rejection, _ = err.(*TransitionError)
			if rejection != nil {
				s.reactToRejection(ctx, id, rejectionFromError(rejection, s.store.Task(id), "server"))
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"reply":     result.Reply,
		"proposal":  proposal,
		"rejection": rejection,
		"usage":     usage,
		"task":      s.store.Task(id),
		"state":     s.store.State(),
	})
}

func (s *server) confirm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProposalID string `json:"proposalId"`
	}
	_ = decodeJSON(w, r, &body)
	id := r.PathValue("id")
	err := s.store.Confirm(id, body.ProposalID)
	if err != nil {
		source := "server"
		if strings.Contains(err.Error(), "устарело") || strings.Contains(err.Error(), "нет предложенного") {
			source = "stale"
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		s.reactToRejection(ctx, id, rejectionFromError(err, s.store.Task(id), source))
		writeTaskError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": s.store.State(), "task": s.store.Task(id)})
}

func (s *server) transition(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string `json:"action"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := r.PathValue("id")
	action, _ := validateAction(body.Action)
	if err := s.store.Transition(id, action); err != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		s.reactToRejection(ctx, id, rejectionFromError(err, s.store.Task(id), "server"))
		writeTaskError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": s.store.State(), "task": s.store.Task(id)})
}

func (s *server) cancelProposal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	info, err := s.store.CancelProposal(id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if info != nil {
		s.reactToRejection(ctx, id, *info)
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": s.store.State(), "task": s.store.Task(id)})
}

func (s *server) resetDemo(w http.ResponseWriter, r *http.Request) {
	if err := s.store.ResetDemo(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": s.store.State()})
}

func (s *server) clearAll(w http.ResponseWriter, r *http.Request) {
	if err := s.store.ClearAll(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": s.store.State()})
}

func (s *server) reactToRejection(ctx context.Context, id string, info RejectionInfo) {
	task := s.store.Task(id)
	if task == nil {
		return
	}
	comment, _, err := AskRejectionComment(ctx, s.llm, task, info)
	if err != nil || strings.TrimSpace(comment) == "" {
		comment = fallbackRejectionComment(info)
	}
	_ = s.store.AddMessage(id, Message{Role: "assistant", Content: comment})
}

func fallbackRejectionComment(info RejectionInfo) string {
	allowed := "нет"
	if len(info.Allowed) > 0 {
		actions := make([]string, 0, len(info.Allowed))
		for _, action := range info.Allowed {
			actions = append(actions, string(action))
		}
		allowed = strings.Join(actions, ", ")
	}
	switch info.Source {
	case "user":
		return "Понял, переход не применяю. Остаёмся на текущем этапе. Разрешено: " + allowed + ". Если нужно, могу предложить другой переход."
	case "stale":
		return "Предложение устарело: этап уже изменился. Переход не применяю. Разрешено: " + allowed + "."
	default:
		return "Переход отклонён сервером: " + info.Reason + ". Остаёмся на текущем этапе. Разрешено: " + allowed + "."
	}
}

func rejectionFromError(err error, task *Task, source string) RejectionInfo {
	info := RejectionInfo{Source: source, Reason: err.Error()}
	if task != nil {
		info.From = task.Stage
		info.Allowed = AllowedActions(task.Stage)
	}
	if rejection, ok := err.(*TransitionError); ok {
		info.Source = "server"
		info.Reason = rejection.Error()
		info.Action = rejection.Action
		info.From = rejection.From
		info.Allowed = rejection.Allowed
	}
	return info
}

func writeTaskError(w http.ResponseWriter, err error) {
	if rejection, ok := err.(*TransitionError); ok {
		writeJSON(w, http.StatusConflict, rejection)
		return
	}
	status := http.StatusBadRequest
	if strings.Contains(err.Error(), "устарело") || strings.Contains(err.Error(), "нет предложенного") {
		status = http.StatusConflict
	}
	writeError(w, status, err.Error())
}

func loadConfig() config {
	keyFile := envOr("DEEPSEEK_API_KEY_FILE", "api_key.txt")
	port := envOr("PORT", "8093")
	return config{
		keyFile: keyFile,
		apiKey:  readSecret(keyFile, "sk-your-key-here"),
		baseURL: strings.TrimRight(envOr("DEEPSEEK_BASE_URL", "https://api.deepseek.com"), "/"),
		model:   envOr("DEEPSEEK_MODEL", "deepseek-flash"),
		addr:    ":" + port,
	}
}

func readSecret(path, placeholder string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	value := unquote(strings.TrimSpace(strings.TrimPrefix(string(data), "\ufeff")))
	if value == placeholder || strings.Contains(value, "your-") {
		return ""
	}
	return value
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		return fmt.Errorf("неверный JSON: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("файл %s не найден, используем переменные окружения", path)
		}
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
		value = unquote(strings.TrimSpace(value))
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, value)
		}
	}
	return scanner.Err()
}

func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}
