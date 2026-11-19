package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Stage string

const (
	StagePlanning   Stage = "planning"
	StageExecution  Stage = "execution"
	StageValidation Stage = "validation"
	StageDone       Stage = "done"
)

type Action string

const (
	ActionApprovePlan      Action = "approve_plan"
	ActionSubmitValidation Action = "submit_validation"
	ActionValidationPassed Action = "validation_passed"
	ActionValidationFailed Action = "validation_failed"
)

var transitionTable = map[Stage]map[Action]Stage{
	StagePlanning: {
		ActionApprovePlan: StageExecution,
	},
	StageExecution: {
		ActionSubmitValidation: StageValidation,
	},
	StageValidation: {
		ActionValidationPassed: StageDone,
		ActionValidationFailed: StageExecution,
	},
}

type Task struct {
	ID             string         `json:"id"`
	Title          string         `json:"title"`
	Description    string         `json:"description"`
	Stage          Stage          `json:"stage"`
	CurrentStep    string         `json:"currentStep"`
	ExpectedAction string         `json:"expectedAction"`
	AllowedActions []Action       `json:"allowedActions"`
	Messages       []Message      `json:"messages"`
	Turns          []TurnMeta     `json:"turns"`
	History        []HistoryEvent `json:"history"`
	Pending        *Proposal      `json:"-"`
	CreatedAt      string         `json:"createdAt"`
	UpdatedAt      string         `json:"updatedAt"`
}

type TurnMeta struct {
	UserIndex      int       `json:"userIndex"`
	AssistantIndex int       `json:"assistantIndex"`
	Request        []Message `json:"request"`
}

type Proposal struct {
	ID        string `json:"id"`
	Action    Action `json:"action"`
	From      Stage  `json:"from"`
	Target    Stage  `json:"target"`
	Reason    string `json:"reason"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
}

type HistoryEvent struct {
	ID     string `json:"id"`
	At     string `json:"at"`
	From   Stage  `json:"from"`
	Action Action `json:"action"`
	To     Stage  `json:"to"`
	Result string `json:"result"`
	Reason string `json:"reason,omitempty"`
}

func (t Task) MarshalJSON() ([]byte, error) {
	type taskAlias Task
	return json.Marshal(struct {
		taskAlias
		PendingProposal any `json:"pendingProposal"`
	}{
		taskAlias:       taskAlias(t),
		PendingProposal: pendingValue(t.Pending),
	})
}

func (t *Task) UnmarshalJSON(data []byte) error {
	type taskAlias Task
	var aux struct {
		taskAlias
		PendingProposal json.RawMessage `json:"pendingProposal"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*t = Task(aux.taskAlias)
	raw := strings.TrimSpace(string(aux.PendingProposal))
	if raw == "" || raw == "null" || raw == `"NONE"` {
		t.Pending = nil
		return nil
	}
	var proposal Proposal
	if err := json.Unmarshal([]byte(raw), &proposal); err != nil {
		return err
	}
	t.Pending = &proposal
	return nil
}

func pendingValue(proposal *Proposal) any {
	if proposal == nil {
		return "NONE"
	}
	return proposal
}

type State struct {
	Version  int     `json:"version"`
	ActiveID string  `json:"activeId"`
	Tasks    []*Task `json:"tasks"`
}

type TransitionError struct {
	ErrorMsg string   `json:"error"`
	From     Stage    `json:"from"`
	Action   Action   `json:"action"`
	Allowed  []Action `json:"allowed"`
}

func (e *TransitionError) Error() string {
	return e.ErrorMsg
}

func Transition(from Stage, action Action) (Stage, bool) {
	next, ok := transitionTable[from][action]
	return next, ok
}

func AllowedActions(stage Stage) []Action {
	actions := make([]Action, 0, len(transitionTable[stage]))
	for action := range transitionTable[stage] {
		actions = append(actions, action)
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i] < actions[j] })
	return actions
}

func StageInfo(stage Stage) (string, string) {
	switch stage {
	case StagePlanning:
		return "Сформировать и утвердить план", "Утвердить план"
	case StageExecution:
		return "Выполнить работы по плану", "Сдать на валидацию"
	case StageValidation:
		return "Проверить результат", "Валидация пройдена или не пройдена"
	case StageDone:
		return "Задача завершена", "—"
	default:
		return "Неизвестный этап", "—"
	}
}

func actionLabel(action Action) string {
	switch action {
	case ActionApprovePlan:
		return "Утвердить план"
	case ActionSubmitValidation:
		return "Сдать на валидацию"
	case ActionValidationPassed:
		return "Валидация пройдена"
	case ActionValidationFailed:
		return "Валидация не пройдена"
	default:
		return string(action)
	}
}

func stageLabel(stage Stage) string {
	switch stage {
	case StagePlanning:
		return "Планирование"
	case StageExecution:
		return "Реализация"
	case StageValidation:
		return "Валидация"
	case StageDone:
		return "Готово"
	default:
		return string(stage)
	}
}

func newTransitionError(from Stage, action Action) *TransitionError {
	return &TransitionError{
		ErrorMsg: "переход запрещён",
		From:     from,
		Action:   action,
		Allowed:  AllowedActions(from),
	}
}

func newID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func nowString() string {
	return time.Now().Format(time.RFC3339)
}

func newDemoTask() *Task {
	task := &Task{
		ID:          "demo",
		Title:       "Демо: API доставки еды",
		Description: "Собрать MVP API доставки: утвердить план, реализовать, провести валидацию.",
		Stage:       StagePlanning,
		Messages:    []Message{},
		Turns:       []TurnMeta{},
		History:     []HistoryEvent{},
		CreatedAt:   nowString(),
		UpdatedAt:   nowString(),
	}
	refreshTask(task)
	return task
}

func refreshTask(task *Task) {
	step, expected := StageInfo(task.Stage)
	task.CurrentStep = step
	task.ExpectedAction = expected
	task.AllowedActions = AllowedActions(task.Stage)
	if task.Messages == nil {
		task.Messages = []Message{}
	}
	if task.History == nil {
		task.History = []HistoryEvent{}
	}
	if task.Turns == nil {
		task.Turns = []TurnMeta{}
	}
}

func (t *Task) addEvent(from Stage, action Action, to Stage, result, reason string) {
	t.History = append(t.History, HistoryEvent{
		ID:     newID(),
		At:     nowString(),
		From:   from,
		Action: action,
		To:     to,
		Result: result,
		Reason: reason,
	})
	t.UpdatedAt = nowString()
}

func (t *Task) addSystemMessage(content string) {
	t.Messages = append(t.Messages, Message{Role: "system", Content: content})
	t.UpdatedAt = nowString()
}

func (t *Task) applyTransition(action Action) error {
	from := t.Stage
	to, ok := Transition(from, action)
	if !ok {
		return newTransitionError(from, action)
	}
	t.Stage = to
	t.Pending = nil
	t.addEvent(from, action, to, "applied", "")
	t.addSystemMessage(fmt.Sprintf("Этап изменён: %s → %s.", stageLabel(from), stageLabel(to)))
	refreshTask(t)
	return nil
}

func validateAction(value string) (Action, bool) {
	action := Action(strings.TrimSpace(value))
	switch action {
	case ActionApprovePlan, ActionSubmitValidation, ActionValidationPassed, ActionValidationFailed:
		return action, true
	default:
		return action, false
	}
}
