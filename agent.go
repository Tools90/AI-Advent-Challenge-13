package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type ProposalResult struct {
	Reply          string
	ProposedAction Action
	Reason         string
	Request        []Message
}

type RejectionInfo struct {
	Source  string   `json:"source"`
	Reason  string   `json:"reason"`
	Action  Action   `json:"action"`
	From    Stage    `json:"from"`
	Target  Stage    `json:"target,omitempty"`
	Allowed []Action `json:"allowed"`
}
type assistantJSON struct {
	Reply          string `json:"reply"`
	ProposedAction string `json:"proposedAction"`
	Reason         string `json:"reason"`
}

func AskAssistant(ctx context.Context, llm LLMClient, task *Task, user string) (ProposalResult, Usage, error) {
	messages := buildAssistantMessages(task, user)
	response, err := llm.Chat(ctx, messages)
	if err != nil {
		return ProposalResult{}, Usage{}, err
	}
	result := parseAssistant(response.Message.Content)
	result.Request = append([]Message{}, messages...)
	if strings.TrimSpace(result.Reply) == "" {
		result.Reply = strings.TrimSpace(response.Message.Content)
	}
	if result.ProposedAction != "" && !hasTransitionIntent(user, task.Stage) {
		result.ProposedAction = ""
	}
	if result.ProposedAction == "" {
		if action := contextualNextAction(user, task.Stage); action != "" {
			result.ProposedAction = action
			if result.Reason == "" {
				result.Reason = "запрос относится к следующему этапу; предлагаю " + string(action)
			}
		}
	}
	if result.ProposedAction == "" && claimsProposal(result.Reply) {
		result.Reply = strings.TrimSpace(result.Reply) + "\n\nПереход не предложен: в запросе нет явной просьбы менять этап."
	}
	return result, response.Usage, nil
}

func claimsProposal(reply string) bool {
	lower := strings.ToLower(reply)
	return containsAny(lower, "предлагаю действие", "предлагаю выполнить", "предлагаю перейти", "предлагаю сдать", "предлагаю утвердить")
}

func hasTransitionIntent(user string, stage Stage) bool {
	lower := strings.ToLower(user)
	explicit := []string{
		"перейд", "переход", "переведи", "утверд", "утвержд", "подтверд", "подтвержд", "одобр",
		"принима", "согласен", "согласна", "начина", "приступа", "запуск", "продолж",
		"сдаю", "сдать", "сдаем", "сдаём", "на валидацию", "валидация пройдена",
		"валидация не пройдена", "заверш", "следующий этап", "готово", "план готов", "поехали",
		"approve_plan", "submit_validation", "validation_passed", "validation_failed",
	}
	if containsAny(lower, explicit...) || isAffirmative(lower) {
		return true
	}
	switch stage {
	case StagePlanning:
		return containsAny(lower, "реализ", "разработ", "напиши код", "написать код", "сделай api", "сделай mvp", "внедр", "программируй", "проведи валидацию", "валидаци", "протестируй", "запусти тесты")
	case StageExecution:
		return containsAny(lower, "проведи валидацию", "валидаци", "протестируй", "запусти тесты", "прогони тесты", "проверь результат", "проверь работу", "проверь api", "заверш", "финал", "выпускай")
	case StageValidation:
		return containsAny(lower, "заверш", "финал", "выпускай", "готово", "прими", "исправь", "доработ", "верни", "провал")
	default:
		return false
	}
}

func isAffirmative(text string) bool {
	value := strings.TrimSpace(strings.ToLower(text))
	value = strings.Trim(value, " .,!?…")
	switch value {
	case "да", "да давай", "да, давай", "да, го", "да го", "давай", "ок", "ok", "окей", "делай", "подтверждаю", "согласен", "согласна", "поехали", "го":
		return true
	default:
		return false
	}
}

func contextualNextAction(user string, stage Stage) Action {
	lower := strings.ToLower(user)
	if containsAny(lower, "сразу", "пропуст", "через этап") {
		return ""
	}
	if strings.Contains(lower, "план") && !containsAny(lower, "реализуй", "реализовать", "начни", "начина", "приступа", "выполн", "сделай") {
		return ""
	}
	wantsExecution := containsAny(lower, "реализуй", "реализовать", "разработ", "напиши код", "написать код", "сделай api", "сделай mvp", "внедр", "программируй", "начни", "начина", "приступа")
	wantsValidation := containsAny(lower, "проведи валидацию", "валидаци", "протестируй", "запусти тесты", "прогони тесты", "проверь результат", "проверь работу", "проверь api", "сдаю", "сдать")
	wantsDone := containsAny(lower, "заверш", "финал", "выпускай", "готово", "закончи")
	wantsFix := containsAny(lower, "исправь", "доработ", "верни", "провал", "не пройдена")
	switch stage {
	case StagePlanning:
		if wantsExecution || wantsValidation {
			return ActionApprovePlan
		}
	case StageExecution:
		if wantsValidation || wantsDone {
			return ActionSubmitValidation
		}
	case StageValidation:
		if containsAny(lower, "валидация пройдена") {
			return ActionValidationPassed
		}
		if wantsDone {
			return ActionValidationPassed
		}
		if wantsFix || wantsExecution || containsAny(lower, "валидация не пройдена") {
			return ActionValidationFailed
		}
	}
	return ""
}

func containsAny(text string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func AskRejectionComment(ctx context.Context, llm LLMClient, task *Task, rejection RejectionInfo) (string, Usage, error) {
	var b strings.Builder
	b.WriteString("Ты — ассистент, который ведёт задачу. Только что переход был отклонён.\n")
	b.WriteString("Не меняй этап, не применяй переход и не предлагай новый автоматически.\n")
	b.WriteString("Дай короткий комментарий по-русски: что произошло, почему отказ и что можно сделать дальше.\n\n")
	b.WriteString("Задача: " + task.Title + "\n")
	b.WriteString("Текущий этап: " + string(task.Stage) + " (" + stageLabel(task.Stage) + ")\n")
	b.WriteString("Ожидаемое действие: " + task.ExpectedAction + "\n")
	b.WriteString("Разрешённые действия: " + allowedText(task.Stage) + "\n\n")
	b.WriteString("Отклонение:\n")
	b.WriteString("- источник: " + rejection.Source + "\n")
	b.WriteString("- действие: " + string(rejection.Action) + "\n")
	b.WriteString("- из этапа: " + string(rejection.From) + "\n")
	if rejection.Target != "" {
		b.WriteString("- целевой этап: " + string(rejection.Target) + "\n")
	}
	b.WriteString("- причина: " + rejection.Reason + "\n")
	if len(rejection.Allowed) > 0 {
		allowed := make([]string, 0, len(rejection.Allowed))
		for _, action := range rejection.Allowed {
			allowed = append(allowed, string(action))
		}
		b.WriteString("- разрешено: " + strings.Join(allowed, ", ") + "\n")
	}
	messages := []Message{{Role: "system", Content: b.String()}}
	start := len(task.Messages) - 8
	if start < 0 {
		start = 0
	}
	for _, message := range task.Messages[start:] {
		messages = append(messages, message)
	}
	response, err := llm.Chat(ctx, messages)
	if err != nil {
		return "", Usage{}, err
	}
	return strings.TrimSpace(response.Message.Content), response.Usage, nil
}
func buildAssistantMessages(task *Task, user string) []Message {
	var b strings.Builder
	b.WriteString("Ты — ассистент, который ведёт задачу по фиксированному процессу.\n")
	b.WriteString("Ты НЕ меняешь этап сам. Ты только предлагаешь действие, сервер проверит его.\n")
	b.WriteString("Актуальное состояние задачи — единственный источник истины. Игнорируй устаревшие утверждения об этапе в истории сообщений.\n\n")
	b.WriteString("Задача: " + task.Title + "\n")
	if task.Description != "" {
		b.WriteString("Описание: " + task.Description + "\n")
	}
	b.WriteString("Текущий этап: " + string(task.Stage) + " (" + stageLabel(task.Stage) + ")\n")
	b.WriteString("Текущий шаг: " + task.CurrentStep + "\n")
	b.WriteString("Ожидаемое действие: " + task.ExpectedAction + "\n")
	b.WriteString("Разрешённые действия: " + allowedText(task.Stage) + "\n\n")
	b.WriteString("Действия и переходы:\n")
	b.WriteString("- approve_plan: planning → execution\n")
	b.WriteString("- submit_validation: execution → validation\n")
	b.WriteString("- validation_passed: validation → done\n")
	b.WriteString("- validation_failed: validation → execution\n\n")
	b.WriteString("Правила:\n")
	b.WriteString("1. Если пользователь явно просит перейти на этап или выполнить действие (например: «перейди», «утверди», «сдаю», «подтверждаю», «валидация пройдена»), верни proposedAction с действием, ведущим на этот этап, даже если переход запрещён. Не отказывайся сам — сервер проверит и отклонит.\n")
	b.WriteString("2. Если из запроса ясно, что работа относится к другому этапу (в planning просят реализовать, в execution — проверить/валидировать, в validation — завершить или исправить), предложи соответствующий переход, даже если нет слова «перейди». Если запрос относится к текущему этапу, это вопрос или правка плана — не предлагай переход.\n")
	b.WriteString("3. Не предлагай переход только потому, что текущий шаг этого ожидает. Если пользователь составляет план, добавляет пункты, задаёт вопрос или обсуждает задачу — proposedAction должен быть пустым.\n")
	b.WriteString("4. После отклонения не предлагай переход снова без явной просьбы пользователя.\n")
	b.WriteString("5. Если переход не нужен, верни proposedAction пустой строкой.\n")
	b.WriteString("6. Не утверждай, что этап уже изменён.\n")
	b.WriteString("7. Пример: пользователь просит «сразу в done» — верни proposedAction validation_passed, даже если сейчас planning. Сервер отклонит; не подменяй это на approve_plan.\n")
	b.WriteString("8. Если в reply ты пишешь, что предлагаешь действие, поле proposedAction обязательно должно быть заполнено. Нельзя писать «предлагаю» без proposedAction.\n")
	b.WriteString("9. Верни только JSON без markdown: {\"reply\":\"...\",\"proposedAction\":\"...\",\"reason\":\"...\"}.\n")

	if len(task.History) > 0 {
		b.WriteString("\nПоследние события:\n")
		start := len(task.History) - 10
		if start < 0 {
			start = 0
		}
		for _, event := range task.History[start:] {
			b.WriteString(fmt.Sprintf("- %s: %s, %s → %s, %s", event.At, event.Action, event.From, event.To, event.Result))
			if event.Reason != "" {
				b.WriteString(" (" + event.Reason + ")")
			}
			b.WriteString("\n")
		}
	}

	messages := []Message{{Role: "system", Content: b.String()}}
	history := task.Messages
	var lastUser *Message
	if len(history) > 0 && history[len(history)-1].Role == "user" {
		last := history[len(history)-1]
		lastUser = &last
		history = history[:len(history)-1]
	}
	start := len(history) - 20
	if start < 0 {
		start = 0
	}
	for _, message := range history[start:] {
		if message.Role == "system" {
			continue
		}
		messages = append(messages, message)
	}
	messages = append(messages, Message{Role: "system", Content: stateReminder(task)})
	if lastUser != nil {
		messages = append(messages, *lastUser)
	} else if user != "" {
		messages = append(messages, Message{Role: "user", Content: user})
	}
	return messages
}

func stateReminder(task *Task) string {
	return "АКТУАЛЬНОЕ СОСТОЯНИЕ (источник истины):\n" +
		"- этап: " + string(task.Stage) + " (" + stageLabel(task.Stage) + ")\n" +
		"- шаг: " + task.CurrentStep + "\n" +
		"- ожидается: " + task.ExpectedAction + "\n" +
		"- разрешено: " + allowedText(task.Stage) + "\n" +
		"Не утверждай, что этап другой. Если в истории есть старые сообщения о другом этапе, они устарели."
}

func parseAssistant(content string) ProposalResult {
	content = strings.TrimSpace(content)
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end < start {
		return inferProposal(ProposalResult{Reply: content})
	}
	var parsed assistantJSON
	if err := json.Unmarshal([]byte(content[start:end+1]), &parsed); err != nil {
		return inferProposal(ProposalResult{Reply: content})
	}
	result := ProposalResult{
		Reply:  strings.TrimSpace(parsed.Reply),
		Reason: strings.TrimSpace(parsed.Reason),
	}
	if parsed.ProposedAction != "" {
		if action, ok := validateAction(parsed.ProposedAction); ok {
			result.ProposedAction = action
		} else {
			result.ProposedAction = Action(strings.TrimSpace(parsed.ProposedAction))
		}
	}
	return inferProposal(result)
}

func inferProposal(result ProposalResult) ProposalResult {
	if result.ProposedAction != "" {
		return result
	}
	action := inferActionFromText(result.Reply)
	if action != "" {
		result.ProposedAction = action
		if result.Reason == "" {
			result.Reason = "действие распознано из текста ответа ассистента"
		}
	}
	return result
}

func inferActionFromText(text string) Action {
	lower := strings.ToLower(text)
	intent := []string{"предлага", "предложу", "сдаю", "перехожу", "запускаю", "отправляю", "подтверждаю"}
	hasIntent := false
	for _, marker := range intent {
		if strings.Contains(lower, marker) {
			hasIntent = true
			break
		}
	}
	if !hasIntent {
		return ""
	}
	actions := []Action{ActionApprovePlan, ActionSubmitValidation, ActionValidationPassed, ActionValidationFailed}
	found := make([]Action, 0, 1)
	for _, action := range actions {
		if strings.Contains(lower, string(action)) {
			found = append(found, action)
		}
	}
	if len(found) == 1 {
		return found[0]
	}
	if len(found) > 1 {
		return ""
	}
	switch {
	case strings.Contains(lower, "утвердить план") || strings.Contains(lower, "утверждаю план"):
		return ActionApprovePlan
	case strings.Contains(lower, "сдать на валидацию") || strings.Contains(lower, "сдаю на валидацию") || strings.Contains(lower, "сдаю работу на валидацию"):
		return ActionSubmitValidation
	case strings.Contains(lower, "валидация не пройдена") || strings.Contains(lower, "валидацию не пройдена"):
		return ActionValidationFailed
	case strings.Contains(lower, "валидация пройдена") || strings.Contains(lower, "валидацию пройдена"):
		return ActionValidationPassed
	default:
		return ""
	}
}
