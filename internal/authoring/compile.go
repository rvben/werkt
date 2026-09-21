package authoring

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

type selectedOperation struct {
	Spec        operationSpec
	Probability float64
	DependsOn   []string
}

func compileDraft(intent, requestedName, timezone string, response systemOneResponse) (Draft, error) {
	primary := response.Answers["primary_operation"]
	if _, ok := operationByID(primary.Choice); !ok {
		return Draft{}, errors.New("Jev selected an unavailable primary operation")
	}
	selected := selectOperations(primary, response.Answers)
	selected = resolveOperationConflicts(selected, primary.Choice)
	if len(selected) == 0 {
		return Draft{}, errors.New("Jev did not select an operation")
	}
	for index := range selected {
		for earlier := range selected {
			if selected[earlier].Spec.Stage >= selected[index].Spec.Stage {
				continue
			}
			if response.Answers[dependencyQuestion(selected[index].Spec.ID, selected[earlier].Spec.ID)].Noul >= dependencyThreshold {
				selected[index].DependsOn = append(selected[index].DependsOn, selected[earlier].Spec.ID)
			}
		}
	}
	triggerAnswer := response.Answers["trigger"]
	trigger, triggerWarnings := compileTrigger(triggerAnswer.Choice, intent, timezone)
	approvalProbability := response.Answers["requires_approval"].Noul
	risk := response.Answers["risk"].Score
	requiresApproval := approvalProbability >= approvalThreshold || risk >= 2 || hasDestructiveOperation(selected)

	steps := make([]Step, 0, len(selected)+1)
	for _, operation := range selected {
		steps = append(steps, Step{
			ID: operation.Spec.ID, Title: operation.Spec.Title, Connector: operation.Spec.Connector,
			Operation: operation.Spec.Operation, Effect: operation.Spec.Effect, DependsOn: append([]string{}, operation.DependsOn...),
			Inputs: operationInputs(operation.Spec.ID), Outputs: []string{operation.Spec.ID + "-result"},
		})
	}
	if requiresApproval {
		steps = insertApproval(steps)
	}
	name := requestedName
	if name == "" {
		name = draftName(intent, primary.Choice)
	}
	warnings := append([]string{}, triggerWarnings...)
	confidence := math.Min(math.Min(primary.Confidence, triggerAnswer.Confidence), response.Answers["risk"].Confidence)
	if confidence < 0.65 {
		warnings = append(warnings, fmt.Sprintf("Jev's lowest decision confidence is %.2f; review the selected trigger, operations, and risk assessment carefully.", confidence))
	}
	warnings = append(warnings, "Runtime values such as spreadsheet IDs, ranges, prompts, models, and file paths must be supplied in the triggering event and reviewed before deployment.")
	if requiresApproval {
		warnings = append(warnings, "Werkt inserted a durable approval gate before execution because Jev or policy classified the workflow as consequential.")
	}

	selectedIDs := make(map[string]bool, len(selected))
	for _, operation := range selected {
		selectedIDs[operation.Spec.ID] = true
	}
	decisionOperations := make([]OperationDecision, 0, len(operationCatalog))
	for _, operation := range operationCatalog {
		decisionOperations = append(decisionOperations, OperationDecision{
			ID: operation.ID, Selected: selectedIDs[operation.ID],
			RelevanceProbability: response.Answers[relevanceQuestion(operation.ID)].Noul,
			PrimaryProbability:   primary.Probabilities[operation.ID],
		})
	}
	draft := Draft{
		Name: name, Description: draftDescription(selected), Trigger: trigger, Steps: steps,
		Secrets: collectSecrets(selected), Egress: collectEgress(selected), Warnings: warnings,
		Decision: DecisionSummary{
			Provider: "typesafe", Model: response.Model, Confidence: confidence,
			Trigger: triggerAnswer.Choice, TriggerConfidence: triggerAnswer.Confidence,
			RiskScore: risk, ApprovalProbability: approvalProbability, Operations: decisionOperations,
			InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens,
		},
	}
	draft.Files = renderFiles(draft, selected, requiresApproval)
	return draft, nil
}

func selectOperations(primary answer, answers map[string]answer) []selectedOperation {
	selected := make([]selectedOperation, 0, len(operationCatalog))
	for _, operation := range operationCatalog {
		probability := answers[relevanceQuestion(operation.ID)].Noul
		if operation.ID == primary.Choice || probability >= operationSelectionThreshold || primary.Probabilities[operation.ID] >= primaryCandidateThreshold {
			selected = append(selected, selectedOperation{Spec: operation, Probability: probability})
		}
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].Spec.Stage == selected[j].Spec.Stage {
			return selected[i].Spec.ID < selected[j].Spec.ID
		}
		return selected[i].Spec.Stage < selected[j].Spec.Stage
	})
	if len(selected) > 6 {
		selected = selected[:6]
	}
	return selected
}

func resolveOperationConflicts(selected []selectedOperation, primary string) []selectedOperation {
	left, right := -1, -1
	for index, operation := range selected {
		switch operation.Spec.ID {
		case "sheets-update":
			left = index
		case "sheets-clear":
			right = index
		}
	}
	if left < 0 || right < 0 {
		return selected
	}
	remove := right
	if primary == "sheets-clear" || (primary != "sheets-update" && selected[right].Probability > selected[left].Probability) {
		remove = left
	}
	return append(selected[:remove], selected[remove+1:]...)
}

func compileTrigger(choice, intent, timezone string) (Trigger, []string) {
	switch choice {
	case "email":
		return Trigger{ID: "inbound-email", Type: "email", TokenSecret: "trigger/email-token"}, nil
	case "ntfy":
		return Trigger{ID: "ntfy-message", Type: "ntfy", Server: "https://ntfy.sh", Topic: "replace-with-private-topic", TokenSecret: "trigger/ntfy-token"}, []string{"Replace the placeholder ntfy server/topic and corresponding egress host before deployment."}
	case "schedule":
		if cron, ok := inferCron(intent); ok {
			return Trigger{ID: "scheduled", Type: "schedule", Cron: cron, Timezone: timezone}, nil
		}
		return Trigger{ID: "on-demand", Type: "webhook", Secret: "trigger/webhook"}, []string{"Jev identified a schedule, but Werkt could not map the cadence safely; the draft uses a webhook until an explicit cron schedule is configured."}
	default:
		return Trigger{ID: "on-demand", Type: "webhook", Secret: "trigger/webhook"}, nil
	}
}

var clockPattern = regexp.MustCompile(`(?i)\b(?:at\s+([01]?\d|2[0-3])(?::([0-5]\d))?|([01]?\d|2[0-3]):([0-5]\d))\b`)

func inferCron(intent string) (string, bool) {
	lower := strings.ToLower(intent)
	if strings.Contains(lower, "every hour") || strings.Contains(lower, "hourly") {
		return "0 * * * *", true
	}
	hour, minute := "9", "0"
	if match := clockPattern.FindStringSubmatch(lower); match != nil {
		hourValue, minuteValue := match[1], match[2]
		if hourValue == "" {
			hourValue, minuteValue = match[3], match[4]
		}
		hour = strings.TrimLeft(hourValue, "0")
		if hour == "" {
			hour = "0"
		}
		if minuteValue != "" {
			minute = strings.TrimLeft(minuteValue, "0")
			if minute == "" {
				minute = "0"
			}
		}
	}
	weekdays := []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}
	for day, label := range weekdays {
		if strings.Contains(lower, label) {
			return fmt.Sprintf("%s %s * * %d", minute, hour, day), true
		}
	}
	if strings.Contains(lower, "every day") || strings.Contains(lower, "daily") {
		return fmt.Sprintf("%s %s * * *", minute, hour), true
	}
	return "", false
}

func hasDestructiveOperation(selected []selectedOperation) bool {
	for _, operation := range selected {
		if operation.Spec.ID == "sheets-clear" {
			return true
		}
	}
	return false
}

func insertApproval(steps []Step) []Step {
	firstWrite := len(steps)
	dependencies := []string{}
	for index, step := range steps {
		if step.Effect == "write" {
			firstWrite = index
			break
		}
		dependencies = append(dependencies, step.ID)
	}
	approval := Step{ID: "review", Title: "Review consequential execution", Connector: "werkt", Operation: "request-approval", Effect: "approval", DependsOn: dependencies, Inputs: []string{"run-input"}, Outputs: []string{"approval"}}
	result := append([]Step{}, steps[:firstWrite]...)
	result = append(result, approval)
	for _, step := range steps[firstWrite:] {
		step.DependsOn = appendUnique(step.DependsOn, "review")
		result = append(result, step)
	}
	return result
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func operationInputs(id string) []string {
	switch id {
	case "zoom-recording":
		return []string{"recording_uuid"}
	case "sheets-read", "sheets-update", "sheets-clear":
		return []string{"spreadsheet_id", "range"}
	case "openai-transcribe":
		return []string{"audio_path", "model"}
	case "openai-vision":
		return []string{"image_path", "prompt", "model"}
	case "openai-chat":
		return []string{"prompt", "model"}
	case "operator-notify":
		return []string{"notification_title"}
	default:
		return nil
	}
}

func collectSecrets(selected []selectedOperation) []SecretBinding {
	values := map[string]SecretBinding{}
	for _, operation := range selected {
		for _, secret := range operation.Spec.Secrets {
			values[secret.Environment] = secret
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]SecretBinding, 0, len(keys))
	for _, key := range keys {
		result = append(result, values[key])
	}
	return result
}

func collectEgress(selected []selectedOperation) []EgressRule {
	values := map[string]EgressRule{}
	for _, operation := range selected {
		for _, rule := range operation.Spec.Egress {
			values[fmt.Sprintf("%s:%d", rule.Host, rule.Port)] = rule
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]EgressRule, 0, len(keys))
	for _, key := range keys {
		result = append(result, values[key])
	}
	return result
}

func draftDescription(selected []selectedOperation) string {
	titles := make([]string, 0, len(selected))
	for _, operation := range selected {
		titles = append(titles, strings.ToLower(operation.Spec.Title))
	}
	return "Jev-planned workflow to " + strings.Join(titles, ", then ") + "."
}

func draftName(intent, fallback string) string {
	words := regexp.MustCompile(`[a-z0-9]+`).FindAllString(strings.ToLower(intent), 5)
	value := strings.Join(words, "-")
	value = strings.Trim(value, "-0123456789")
	if value == "" {
		value = fallback + "-workflow"
	}
	if len(value) > 63 {
		value = strings.TrimRight(value[:63], "-")
	}
	return value
}
