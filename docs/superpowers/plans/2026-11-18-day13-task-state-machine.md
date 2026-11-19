# Day 13 Task State Machine Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Minimal Go app where task stages are changed only through a code-validated state machine, with assistant proposals confirmed by the user.

**Architecture:** Single Go binary with embedded HTML UI, JSON persistence in `data/tasks.json`, DeepSeek chat client, and a store that owns all task transitions.

**Tech Stack:** Go 1.27, `net/http`, `embed`, DeepSeek API, plain HTML/CSS/JS.

**Spec:** `docs/superpowers/specs/2026-11-18-day13-task-state-machine-design.md`

## Global Constraints

- Port `8093`.
- Copy DeepSeek key from Day 12 without prompting.
- No profiles or memory layers.
- Fixed stages: `planning`, `execution`, `validation`, `done`.
- Fixed actions: `approve_plan`, `submit_validation`, `validation_passed`, `validation_failed`.
- Stage changes only through `TaskStore.Transition` / `Confirm`.
- Invalid transition: HTTP 409, stage unchanged, history event `rejected`, chat system message.
- No commits unless explicitly requested.

---

### Task 1: Scaffold project and DeepSeek client

**Files:**
- Create: `go.mod`, `.env`, `.env.example`, `.gitignore`, `api_key.txt`, `llm.go`

**Interfaces:**
- Produces: `Message`, `Usage`, `LLMClient`, `LLMResponse`, `NewDeepSeekClient`.

- [ ] Copy `llm.go` from Day 12.
- [ ] Copy `api_key.txt` from Day 12.
- [ ] Create `.env` with `PORT=8093`, `DEEPSEEK_BASE_URL=https://api.deepseek.com`, `DEEPSEEK_MODEL=deepseek-flash`, `DEEPSEEK_API_KEY_FILE=api_key.txt`.
- [ ] Run `go test ./...`; expected: no test files, build succeeds.

### Task 2: Task model and transition table

**Files:**
- Create: `task.go`
- Test: `task_test.go`

**Interfaces:**
- Produces: `Stage`, `Action`, `Task`, `Proposal`, `HistoryEvent`, `Transition(from Stage, action Action) (Stage, bool)`, `AllowedActions(stage Stage) []Action`, `StageInfo(stage Stage) (step, expected string)`.

- [ ] Test all valid transitions and reject `planning → done`, `planning → validation`, `execution → done`.
- [ ] Implement transition map.
- [ ] Test stage labels.
- [ ] Implement `StageInfo`.
- [ ] Run `go test -run TestTransition ./...`; expected PASS.

### Task 3: Task store and persistence

**Files:**
- Create: `store.go`
- Test: `store_test.go`

**Interfaces:**
- Produces: `NewStore(dir string) (*Store, error)`, `Store.State() State`, `Store.Create(title, description string) (*Task, error)`, `Store.Select(id string) error`, `Store.Delete(id string) error`, `Store.ResetDemo() error`, `Store.ClearAll() error`, `Store.AddMessage(id string, msg Message)`, `Store.Propose(id string, action Action, reason string) (*Proposal, error)`, `Store.Confirm(id, proposalID string) error`, `Store.Transition(id string, action Action) error`.

- [ ] Test first-run demo task and persistence across store reload.
- [ ] Test delete last active task is rejected.
- [ ] Test valid proposal becomes pending; invalid proposal appends `rejected` history and system message.
- [ ] Test confirm revalidates and applies; stale proposal rejected.
- [ ] Implement JSON load/save with atomic rename.
- [ ] Run `go test ./...`; expected PASS.

### Task 4: Assistant proposal client

**Files:**
- Modify: `task.go` or create `agent.go`
- Test: `agent_test.go`

**Interfaces:**
- Produces: `ProposalResult`, `AskAssistant(ctx, llm, task, user string) (ProposalResult, Usage, error)`.

- [ ] Test parsing valid JSON with `reply`, `proposedAction`, `reason`.
- [ ] Test assistant prompt includes current stage, allowed actions, and fixed action meanings.
- [ ] Implement prompt and JSON extraction.
- [ ] Run `go test ./...`; expected PASS.

### Task 5: HTTP API

**Files:**
- Create: `main.go`
- Test: `api_test.go`

**Interfaces:**
- Produces routes:
  - `GET /api/state`
  - `POST /api/tasks`
  - `POST /api/tasks/select`
  - `DELETE /api/tasks/{id}`
  - `POST /api/tasks/{id}/chat`
  - `POST /api/tasks/{id}/confirm`
  - `POST /api/tasks/{id}/transition`
  - `POST /api/demo/reset`
  - `POST /api/clear-all`

- [ ] Test invalid transition returns 409 with `from`, `action`, `allowed`.
- [ ] Test valid transition changes stage.
- [ ] Test confirm with stale proposal returns 409.
- [ ] Test chat stores user and assistant messages.
- [ ] Implement handlers and embedded `web/index.html`.
- [ ] Run `go test ./...`; expected PASS.

### Task 6: UI

**Files:**
- Create: `web/index.html`

**Interfaces:**
- Consumes all API routes.

- [ ] Task list, active task, create task form.
- [ ] Stage strip with `planning → execution → validation → done`.
- [ ] Current step, expected action, allowed action buttons.
- [ ] Chat with user/assistant/system messages.
- [ ] Proposal card with Confirm/Cancel.
- [ ] History panel and rejected-transition messages.
- [ ] Reset demo, clear all, delete task.
- [ ] Verify element IDs match JS selectors.

### Task 7: Integration and acceptance

**Files:**
- Modify only if tests reveal defects.

- [ ] Run `go test ./...` and `go vet ./...`.
- [ ] Start app on `8093`.
- [ ] Create task; verify `planning`.
- [ ] Attempt `planning → done` via chat and API; verify no stage change and rejection logged.
- [ ] Approve plan; verify `execution`.
- [ ] Submit validation; verify `validation`.
- [ ] Validation failed; verify `execution`.
- [ ] Validation passed; verify `done`.
- [ ] Restart app; verify state/chat/history persisted.
- [ ] Clear all; verify only demo task remains.
