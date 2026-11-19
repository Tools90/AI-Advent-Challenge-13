package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTransitionTable(t *testing.T) {
	valid := []struct {
		from   Stage
		action Action
		to     Stage
	}{
		{StagePlanning, ActionApprovePlan, StageExecution},
		{StageExecution, ActionSubmitValidation, StageValidation},
		{StageValidation, ActionValidationPassed, StageDone},
		{StageValidation, ActionValidationFailed, StageExecution},
	}
	for _, test := range valid {
		to, ok := Transition(test.from, test.action)
		if !ok || to != test.to {
			t.Errorf("Transition(%s, %s) = %s, %v; want %s", test.from, test.action, to, ok, test.to)
		}
	}
	invalid := []struct {
		from   Stage
		action Action
	}{
		{StagePlanning, ActionSubmitValidation},
		{StagePlanning, ActionValidationPassed},
		{StagePlanning, ActionValidationFailed},
		{StageExecution, ActionValidationPassed},
		{StageExecution, ActionApprovePlan},
		{StageValidation, ActionApprovePlan},
		{StageDone, ActionApprovePlan},
	}
	for _, test := range invalid {
		if to, ok := Transition(test.from, test.action); ok {
			t.Errorf("Transition(%s, %s) = %s; want rejected", test.from, test.action, to)
		}
	}
}

func TestStageInfo(t *testing.T) {
	for _, stage := range []Stage{StagePlanning, StageExecution, StageValidation, StageDone} {
		step, expected := StageInfo(stage)
		if step == "" || expected == "" {
			t.Errorf("StageInfo(%s) = %q, %q", stage, step, expected)
		}
	}
	if len(AllowedActions(StageDone)) != 0 {
		t.Errorf("done must have no allowed actions")
	}
}

func TestStoreInitialDemo(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := store.State()
	if len(state.Tasks) != 1 || state.ActiveID != "demo" {
		t.Fatalf("state = %+v", state)
	}
	task := state.Tasks[0]
	if task.Stage != StagePlanning || task.CurrentStep == "" || task.ExpectedAction == "" {
		t.Fatalf("task = %+v", task)
	}
	if err := store.Delete("demo"); err == nil {
		t.Fatal("last task deletion must fail")
	}
}

func TestStorePersistenceAndMessages(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create("Новая задача", "Описание")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddMessage(created.ID, Message{Role: "user", Content: "привет"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition("demo", ActionApprovePlan); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	state := reloaded.State()
	if len(state.Tasks) != 2 || state.ActiveID != created.ID {
		t.Fatalf("state = %+v", state)
	}
	if findTask(&state, "demo").Stage != StageExecution {
		t.Fatalf("demo stage = %s", findTask(&state, "demo").Stage)
	}
	if len(findTask(&state, created.ID).Messages) != 1 {
		t.Fatalf("messages = %+v", findTask(&state, created.ID).Messages)
	}
}

func TestProposalRejectsInvalidAndConfirmsValid(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Propose("demo", ActionValidationPassed, "сразу done"); err == nil {
		t.Fatal("invalid proposal must fail")
	}
	task := store.Task("demo")
	if task.Stage != StagePlanning || len(task.History) == 0 || task.History[len(task.History)-1].Result != "rejected" {
		t.Fatalf("task = %+v", task)
	}
	if len(task.Messages) == 0 || !strings.Contains(task.Messages[len(task.Messages)-1].Content, "отклонён") {
		t.Fatalf("rejection not logged in chat: %+v", task.Messages)
	}

	proposal, err := store.Propose("demo", ActionApprovePlan, "план готов")
	if err != nil {
		t.Fatal(err)
	}
	if proposal == nil || task.Pending != nil {
		t.Fatal("proposal must be pending")
	}
	if err := store.Confirm("demo", proposal.ID); err != nil {
		t.Fatal(err)
	}
	task = store.Task("demo")
	if task.Stage != StageExecution || task.Pending != nil {
		t.Fatalf("task = %+v", task)
	}
	if task.History[len(task.History)-1].Result != "applied" {
		t.Fatalf("history = %+v", task.History)
	}
}

func TestConfirmRejectsStaleProposal(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := store.Propose("demo", ActionApprovePlan, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transition("demo", ActionApprovePlan); err != nil {
		t.Fatal(err)
	}
	if err := store.Confirm("demo", proposal.ID); err == nil {
		t.Fatal("stale confirmation must fail")
	}
	if store.Task("demo").Stage != StageExecution {
		t.Fatal("stage changed by stale confirmation")
	}
}

func TestCancelResetAndClearAll(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := store.Propose("demo", ActionApprovePlan, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelProposal("demo"); err != nil {
		t.Fatal(err)
	}
	if store.Task("demo").Pending != nil {
		t.Fatal("pending not cleared")
	}
	if proposal == nil {
		t.Fatal("proposal missing")
	}

	if err := store.Transition("demo", ActionApprovePlan); err != nil {
		t.Fatal(err)
	}
	if err := store.ResetDemo(); err != nil {
		t.Fatal(err)
	}
	task := store.Task("demo")
	if task.Stage != StagePlanning || len(task.History) != 0 {
		t.Fatalf("demo not reset: %+v", task)
	}

	if _, err := store.Create("Вторая", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.ClearAll(); err != nil {
		t.Fatal(err)
	}
	state := store.State()
	if len(state.Tasks) != 1 || state.Tasks[0].ID != "demo" || state.ActiveID != "demo" {
		t.Fatalf("clear all state = %+v", state)
	}
}

type fakeLLM struct {
	content  string
	reaction string
	last     []Message
}

func (f *fakeLLM) Chat(ctx context.Context, messages []Message) (LLMResponse, error) {
	f.last = append([]Message(nil), messages...)
	content := f.content
	if f.reaction != "" && len(messages) > 0 && strings.Contains(messages[0].Content, "отклонён") {
		content = f.reaction
	}
	return LLMResponse{Message: Message{Role: "assistant", Content: content}, Usage: Usage{TotalTokens: 3}}, nil
}

func (f *fakeLLM) Model() string { return "fake" }

func TestAskAssistantParsesProposal(t *testing.T) {
	task := newDemoTask()
	task.Messages = append(task.Messages, Message{Role: "user", Content: "утверди план"})
	llm := &fakeLLM{content: `{"reply":"Можно утвердить.","proposedAction":"approve_plan","reason":"план готов"}`}
	result, usage, err := AskAssistant(context.Background(), llm, task, "утверди план")
	if err != nil {
		t.Fatal(err)
	}
	if result.ProposedAction != ActionApprovePlan || result.Reply == "" || usage.TotalTokens != 3 {
		t.Fatalf("result = %+v usage=%+v", result, usage)
	}
	if len(llm.last) == 0 || !strings.Contains(llm.last[0].Content, "approve_plan") || !strings.Contains(llm.last[0].Content, "planning") {
		t.Fatalf("prompt missing state: %+v", llm.last)
	}
}

func TestAskAssistantRejectsUnknownActionFromModel(t *testing.T) {
	result := parseAssistant(`{"reply":"ok","proposedAction":"jump_to_done","reason":"x"}`)
	if result.ProposedAction != Action("jump_to_done") {
		t.Fatalf("action = %q", result.ProposedAction)
	}
}

func TestAPIInvalidTransitionReturnsConflict(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &server{cfg: config{model: "fake"}, store: store, llm: &fakeLLM{content: "ok"}}
	handler := s.handler()
	body := strings.NewReader(`{"action":"validation_passed"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/tasks/demo/transition", body)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var rejection TransitionError
	if err := json.Unmarshal(recorder.Body.Bytes(), &rejection); err != nil {
		t.Fatal(err)
	}
	if rejection.From != StagePlanning || rejection.Action != ActionValidationPassed || len(rejection.Allowed) != 1 {
		t.Fatalf("rejection = %+v", rejection)
	}
	if store.Task("demo").Stage != StagePlanning {
		t.Fatal("stage changed on invalid transition")
	}
}

func TestAPIChatProposesAndConfirms(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	llm := &fakeLLM{content: `{"reply":"Предлагаю утвердить план.","proposedAction":"approve_plan","reason":"готов"}`}
	s := &server{cfg: config{model: "fake"}, store: store, llm: llm}
	handler := s.handler()

	chatBody := strings.NewReader(`{"message":"утверди план"}`)
	chatRequest := httptest.NewRequest(http.MethodPost, "/api/tasks/demo/chat", chatBody)
	chatRequest.Header.Set("Content-Type", "application/json")
	chatRecorder := httptest.NewRecorder()
	handler.ServeHTTP(chatRecorder, chatRequest)
	if chatRecorder.Code != http.StatusOK {
		t.Fatalf("chat status = %d, body = %s", chatRecorder.Code, chatRecorder.Body.String())
	}
	task := store.Task("demo")
	if task.Pending == nil {
		t.Fatal("pending proposal missing")
	}

	confirmBody := strings.NewReader(`{"proposalId":"` + task.Pending.ID + `"}`)
	confirmRequest := httptest.NewRequest(http.MethodPost, "/api/tasks/demo/confirm", confirmBody)
	confirmRequest.Header.Set("Content-Type", "application/json")
	confirmRecorder := httptest.NewRecorder()
	handler.ServeHTTP(confirmRecorder, confirmRequest)
	if confirmRecorder.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, body = %s", confirmRecorder.Code, confirmRecorder.Body.String())
	}
	if store.Task("demo").Stage != StageExecution {
		t.Fatal("stage not changed after confirm")
	}
}

func TestAPIChatRejectsForbiddenProposal(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	llm := &fakeLLM{content: `{"reply":"Пробую сразу done.","proposedAction":"validation_passed","reason":"просьба"}`, reaction: "Переход отклонён. Остаёмся в планировании."}
	s := &server{cfg: config{model: "fake"}, store: store, llm: llm}
	handler := s.handler()
	body := strings.NewReader(`{"message":"Перейди сразу в done"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/tasks/demo/chat", body)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var result struct {
		Rejection *TransitionError `json:"rejection"`
		Task      *Task            `json:"task"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Rejection == nil || result.Rejection.Action != ActionValidationPassed {
		t.Fatalf("rejection = %+v", result.Rejection)
	}
	if result.Task.Stage != StagePlanning || result.Task.Pending != nil {
		t.Fatalf("task = %+v", result.Task)
	}
	if len(result.Task.History) == 0 || result.Task.History[len(result.Task.History)-1].Result != "rejected" {
		t.Fatalf("history = %+v", result.Task.History)
	}
	if len(result.Task.Messages) == 0 || result.Task.Messages[len(result.Task.Messages)-1].Content != llm.reaction {
		t.Fatalf("reaction not added: %+v", result.Task.Messages)
	}
}

func TestAPICancelProposalReactsInChat(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Propose("demo", ActionApprovePlan, "план готов"); err != nil {
		t.Fatal(err)
	}
	llm := &fakeLLM{content: "ok", reaction: "Понял отказ. Остаёмся на планировании."}
	s := &server{cfg: config{model: "fake"}, store: store, llm: llm}
	handler := s.handler()
	request := httptest.NewRequest(http.MethodPost, "/api/tasks/demo/cancel", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	task := store.Task("demo")
	if task.Stage != StagePlanning || task.Pending != nil {
		t.Fatalf("task = %+v", task)
	}
	if len(task.Messages) == 0 || task.Messages[len(task.Messages)-1].Role != "assistant" || task.Messages[len(task.Messages)-1].Content != llm.reaction {
		t.Fatalf("reaction not added: %+v", task.Messages)
	}
	if len(task.History) == 0 || task.History[len(task.History)-1].Result != "rejected" {
		t.Fatalf("history = %+v", task.History)
	}
}

func TestParseAssistantInfersActionFromPlainText(t *testing.T) {
	result := parseAssistant("Отлично, тогда сдаю работу на валидацию — предлагаю действие submit_validation (execution → validation). Сервер проверит переход.")
	if result.ProposedAction != ActionSubmitValidation {
		t.Fatalf("action = %q", result.ProposedAction)
	}
	if got := parseAssistant("Сейчас этап execution, ожидается submit_validation."); got.ProposedAction != "" {
		t.Fatalf("state description must not become a proposal: %+v", got)
	}
}

func TestAPIChatCreatesProposalFromPlainText(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transition("demo", ActionApprovePlan); err != nil {
		t.Fatal(err)
	}
	llm := &fakeLLM{content: "Отлично, тогда сдаю работу на валидацию — предлагаю действие submit_validation (execution → validation). Сервер проверит переход."}
	s := &server{cfg: config{model: "fake"}, store: store, llm: llm}
	handler := s.handler()
	body := strings.NewReader(`{"message":"сдаю работу"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/tasks/demo/chat", body)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	task := store.Task("demo")
	if task.Pending == nil || task.Pending.Action != ActionSubmitValidation {
		t.Fatalf("pending = %+v", task.Pending)
	}
	if task.Stage != StageExecution {
		t.Fatalf("stage = %s", task.Stage)
	}
}

func TestAskAssistantSuppressesProposalWithoutIntent(t *testing.T) {
	task := newDemoTask()
	task.Messages = append(task.Messages, Message{Role: "user", Content: "добавь еще один пункт плана"})
	llm := &fakeLLM{content: `{"reply":"Добавил пункт.","proposedAction":"approve_plan","reason":"шаг требует"}`}
	result, _, err := AskAssistant(context.Background(), llm, task, "добавь еще один пункт плана")
	if err != nil {
		t.Fatal(err)
	}
	if result.ProposedAction != "" {
		t.Fatalf("proposal must be suppressed: %+v", result)
	}
}

func TestHasTransitionIntentForms(t *testing.T) {
	yes := []struct {
		text  string
		stage Stage
	}{
		{"утверди план", StagePlanning},
		{"план утверждаю", StagePlanning},
		{"план утверждён", StagePlanning},
		{"подтверждаю", StagePlanning},
		{"переходим к реализации", StagePlanning},
		{"сдаю работу", StageExecution},
		{"реализуй API", StagePlanning},
		{"проведи валидацию", StageExecution},
		{"заверши задачу", StageValidation},
	}
	for _, test := range yes {
		if !hasTransitionIntent(test.text, test.stage) {
			t.Errorf("intent not detected: %q at %s", test.text, test.stage)
		}
	}
	no := []struct {
		text  string
		stage Stage
	}{
		{"составь план", StagePlanning},
		{"добавь еще один пункт плана", StagePlanning},
		{"что такое HTTP", StagePlanning},
		{"где мы сейчас", StageExecution},
		{"реализуй API", StageExecution},
	}
	for _, test := range no {
		if hasTransitionIntent(test.text, test.stage) {
			t.Errorf("false intent: %q at %s", test.text, test.stage)
		}
	}
}

func TestContextualNextAction(t *testing.T) {
	cases := []struct {
		text  string
		stage Stage
		want  Action
	}{
		{"Реализуй API", StagePlanning, ActionApprovePlan},
		{"Проведи валидацию", StagePlanning, ActionApprovePlan},
		{"Проведи валидацию", StageExecution, ActionSubmitValidation},
		{"Заверши задачу", StageExecution, ActionSubmitValidation},
		{"Заверши задачу", StageValidation, ActionValidationPassed},
		{"Исправь замечания", StageValidation, ActionValidationFailed},
		{"Составь план", StagePlanning, ""},
		{"Добавь пункт плана", StagePlanning, ""},
		{"Реализуй API", StageExecution, ""},
	}
	for _, test := range cases {
		if got := contextualNextAction(test.text, test.stage); got != test.want {
			t.Errorf("contextualNextAction(%q, %s) = %q; want %q", test.text, test.stage, got, test.want)
		}
	}
}

func TestAskAssistantAddsContextualProposal(t *testing.T) {
	task := newDemoTask()
	llm := &fakeLLM{content: `{"reply":"Сначала утвердим план.","proposedAction":"","reason":""}`}
	result, _, err := AskAssistant(context.Background(), llm, task, "Реализуй API")
	if err != nil {
		t.Fatal(err)
	}
	if result.ProposedAction != ActionApprovePlan {
		t.Fatalf("action = %q", result.ProposedAction)
	}
}

func TestPendingProposalIsAlwaysPresent(t *testing.T) {
	task := newDemoTask()
	data, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"pendingProposal":"NONE"`) {
		t.Fatalf("missing NONE: %s", data)
	}
	task.Pending = &Proposal{ID: "x", Action: ActionApprovePlan, From: StagePlanning, Target: StageExecution, Status: "pending"}
	data, err = json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"pendingProposal":{"id":"x"`) {
		t.Fatalf("proposal not serialized: %s", data)
	}
	var restored Task
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Pending == nil || restored.Pending.Action != ActionApprovePlan {
		t.Fatalf("pending = %+v", restored.Pending)
	}
}

func TestClaimsProposal(t *testing.T) {
	if !claimsProposal("Предлагаю действие submit_validation.") {
		t.Fatal("proposal claim not detected")
	}
	if claimsProposal("Сейчас этап execution, ожидается submit_validation.") {
		t.Fatal("state description treated as proposal")
	}
}
