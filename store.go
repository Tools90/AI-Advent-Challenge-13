package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Store struct {
	mu    sync.RWMutex
	path  string
	state State
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("создать каталог %s: %w", dir, err)
	}
	store := &Store{path: filepath.Join(dir, "tasks.json")}
	data, err := os.ReadFile(store.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("прочитать %s: %w", store.path, err)
		}
		store.state = initialState()
		if err := store.saveLocked(); err != nil {
			return nil, err
		}
		return store, nil
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if err := json.Unmarshal(data, &store.state); err != nil {
		return nil, fmt.Errorf("разобрать %s: %w", store.path, err)
	}
	normalizeState(&store.state)
	if err := store.saveLocked(); err != nil {
		return nil, err
	}
	return store, nil
}

func initialState() State {
	task := newDemoTask()
	return State{Version: 1, ActiveID: task.ID, Tasks: []*Task{task}}
}

func normalizeState(state *State) {
	if state.Version == 0 {
		state.Version = 1
	}
	if len(state.Tasks) == 0 {
		*state = initialState()
		return
	}
	seen := make(map[string]bool, len(state.Tasks))
	for _, task := range state.Tasks {
		if task.ID == "" || seen[task.ID] {
			task.ID = newID()
		}
		seen[task.ID] = true
		if task.Stage != StagePlanning && task.Stage != StageExecution && task.Stage != StageValidation && task.Stage != StageDone {
			task.Stage = StagePlanning
		}
		if task.CreatedAt == "" {
			task.CreatedAt = nowString()
		}
		if task.Pending != nil {
			if _, ok := Transition(task.Stage, task.Pending.Action); !ok {
				task.Pending = nil
			}
		}
		refreshTask(task)
	}
	if findTask(state, state.ActiveID) == nil {
		state.ActiveID = state.Tasks[0].ID
	}
}

func (s *Store) State() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneState(s.state)
}

func (s *Store) Task(id string) *Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	task := findTask(&s.state, id)
	if task == nil {
		return nil
	}
	return cloneTask(task)
}

func (s *Store) Create(title, description string) (*Task, error) {
	title = strings.TrimSpace(title)
	description = strings.TrimSpace(description)
	if title == "" {
		return nil, fmt.Errorf("название задачи обязательно")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task := &Task{
		ID:          newID(),
		Title:       title,
		Description: description,
		Stage:       StagePlanning,
		Messages:    []Message{},
		History:     []HistoryEvent{},
		CreatedAt:   nowString(),
		UpdatedAt:   nowString(),
	}
	refreshTask(task)
	s.state.Tasks = append(s.state.Tasks, task)
	s.state.ActiveID = task.ID
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return cloneTask(task), nil
}

func (s *Store) Select(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if findTask(&s.state, id) == nil {
		return fmt.Errorf("задача не найдена")
	}
	s.state.ActiveID = id
	return s.saveLocked()
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.state.Tasks) <= 1 {
		return fmt.Errorf("нельзя удалить последнюю задачу")
	}
	index := -1
	for i, task := range s.state.Tasks {
		if task.ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("задача не найдена")
	}
	s.state.Tasks = append(s.state.Tasks[:index], s.state.Tasks[index+1:]...)
	if s.state.ActiveID == id {
		s.state.ActiveID = s.state.Tasks[0].ID
	}
	return s.saveLocked()
}

func (s *Store) ResetDemo() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task := findTask(&s.state, "demo")
	if task == nil {
		task = newDemoTask()
		s.state.Tasks = append(s.state.Tasks, task)
	} else {
		fresh := newDemoTask()
		*task = *fresh
	}
	s.state.ActiveID = task.ID
	refreshTask(task)
	return s.saveLocked()
}

func (s *Store) ClearAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = initialState()
	return s.saveLocked()
}

func (s *Store) AddMessage(id string, message Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task := findTask(&s.state, id)
	if task == nil {
		return fmt.Errorf("задача не найдена")
	}
	task.Messages = append(task.Messages, message)
	task.UpdatedAt = nowString()
	return s.saveLocked()
}

func (s *Store) AddTurn(id string, turn TurnMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task := findTask(&s.state, id)
	if task == nil {
		return fmt.Errorf("задача не найдена")
	}
	task.Turns = append(task.Turns, turn)
	task.UpdatedAt = nowString()
	return s.saveLocked()
}

func (s *Store) Transition(id string, action Action) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task := findTask(&s.state, id)
	if task == nil {
		return fmt.Errorf("задача не найдена")
	}
	if err := task.applyTransition(action); err != nil {
		rejection, _ := err.(*TransitionError)
		task.addEvent(task.Stage, action, "", "rejected", err.Error())
		task.addSystemMessage(fmt.Sprintf("Переход отклонён: %s. Разрешено: %s.", err.Error(), allowedText(task.Stage)))
		_ = s.saveLocked()
		if rejection != nil {
			return rejection
		}
		return err
	}
	return s.saveLocked()
}

func (s *Store) Propose(id string, action Action, reason string) (*Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task := findTask(&s.state, id)
	if task == nil {
		return nil, fmt.Errorf("задача не найдена")
	}
	to, ok := Transition(task.Stage, action)
	if !ok {
		err := newTransitionError(task.Stage, action)
		task.addEvent(task.Stage, action, "", "rejected", err.Error())
		task.addSystemMessage(fmt.Sprintf("Переход отклонён: %s. Разрешено: %s.", err.Error(), allowedText(task.Stage)))
		_ = s.saveLocked()
		return nil, err
	}
	proposal := &Proposal{
		ID:        newID(),
		Action:    action,
		From:      task.Stage,
		Target:    to,
		Reason:    strings.TrimSpace(reason),
		Status:    "pending",
		CreatedAt: nowString(),
	}
	task.Pending = proposal
	task.addEvent(task.Stage, action, to, "proposed", proposal.Reason)
	task.addSystemMessage(fmt.Sprintf("Ассистент предлагает: %s. Подтвердите переход.", actionLabel(action)))
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	copy := *proposal
	return &copy, nil
}

func (s *Store) Confirm(id, proposalID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task := findTask(&s.state, id)
	if task == nil {
		return fmt.Errorf("задача не найдена")
	}
	if task.Pending == nil {
		return fmt.Errorf("нет предложенного перехода")
	}
	if proposalID != "" && task.Pending.ID != proposalID {
		return fmt.Errorf("предложение устарело")
	}
	action := task.Pending.Action
	from := task.Stage
	to, ok := Transition(from, action)
	if !ok {
		err := newTransitionError(from, action)
		task.Pending = nil
		task.addEvent(from, action, "", "rejected", err.Error())
		task.addSystemMessage(fmt.Sprintf("Переход отклонён: %s. Разрешено: %s.", err.Error(), allowedText(from)))
		_ = s.saveLocked()
		return err
	}
	task.Pending = nil
	task.Stage = to
	task.addEvent(from, action, to, "applied", "")
	task.addSystemMessage(fmt.Sprintf("Этап изменён: %s → %s.", stageLabel(from), stageLabel(to)))
	refreshTask(task)
	return s.saveLocked()
}

func (s *Store) CancelProposal(id string) (*RejectionInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task := findTask(&s.state, id)
	if task == nil {
		return nil, fmt.Errorf("задача не найдена")
	}
	if task.Pending == nil {
		return nil, fmt.Errorf("нет предложенного перехода")
	}
	proposal := *task.Pending
	task.addEvent(task.Stage, proposal.Action, proposal.Target, "rejected", "пользователь отклонил")
	task.Pending = nil
	task.addSystemMessage("Предложенный переход отклонён пользователем.")
	info := &RejectionInfo{
		Source:  "user",
		Reason:  "пользователь отклонил предложенный переход",
		Action:  proposal.Action,
		From:    task.Stage,
		Target:  proposal.Target,
		Allowed: AllowedActions(task.Stage),
	}
	if err := s.saveLocked(); err != nil {
		return info, err
	}
	return info, nil
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	_ = os.Remove(s.path)
	return os.Rename(tmp, s.path)
}

func findTask(state *State, id string) *Task {
	for _, task := range state.Tasks {
		if task.ID == id {
			return task
		}
	}
	return nil
}

func cloneTask(task *Task) *Task {
	if task == nil {
		return nil
	}
	copy := *task
	copy.Messages = append([]Message{}, task.Messages...)
	copy.Turns = make([]TurnMeta, len(task.Turns))
	for i, turn := range task.Turns {
		turn.Request = append([]Message{}, turn.Request...)
		copy.Turns[i] = turn
	}
	copy.History = append([]HistoryEvent{}, task.History...)
	copy.AllowedActions = append([]Action{}, task.AllowedActions...)
	if task.Pending != nil {
		pending := *task.Pending
		copy.Pending = &pending
	}
	return &copy
}

func cloneState(state State) State {
	copy := State{Version: state.Version, ActiveID: state.ActiveID, Tasks: make([]*Task, 0, len(state.Tasks))}
	for _, task := range state.Tasks {
		copy.Tasks = append(copy.Tasks, cloneTask(task))
	}
	return copy
}

func allowedText(stage Stage) string {
	actions := AllowedActions(stage)
	if len(actions) == 0 {
		return "нет"
	}
	labels := make([]string, 0, len(actions))
	for _, action := range actions {
		labels = append(labels, string(action))
	}
	return strings.Join(labels, ", ")
}
