package authoring

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultAPIBase              = "https://api.typesafe.ai/v1"
	DefaultModel                = "jev-latest"
	operationSelectionThreshold = 0.60
	primaryCandidateThreshold   = 0.18
	dependencyThreshold         = 0.65
	approvalThreshold           = 0.60
)

var ErrDisabled = errors.New("drafting is disabled: set TYPESAFE_API_KEY to enable BYOK authoring")

func Enabled(apiKey string) bool { return strings.TrimSpace(apiKey) != "" }

type Client struct {
	APIBase    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

type question struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

type answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Noul          float64            `json:"noul,omitempty"`
	Score         float64            `json:"score,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

type systemOneResponse struct {
	Model   string            `json:"model"`
	Answers map[string]answer `json:"answers"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (c Client) Generate(ctx context.Context, intent, requestedName, timezone string) (Draft, error) {
	intent = strings.TrimSpace(intent)
	if len(intent) < 10 {
		return Draft{}, errors.New("automation intent must contain at least 10 bytes")
	}
	if len(intent) > MaxIntentBytes {
		return Draft{}, fmt.Errorf("automation intent exceeds %d bytes", MaxIntentBytes)
	}
	if !Enabled(c.APIKey) {
		return Draft{}, ErrDisabled
	}
	model := strings.TrimSpace(c.Model)
	if model == "" {
		model = DefaultModel
	}
	base, err := validatedAPIBase(c.APIBase)
	if err != nil {
		return Draft{}, err
	}
	questions := planningQuestions()
	requestBody, err := json.Marshal(map[string]any{
		"state": map[string]any{
			"automation_intent": intent,
			"instruction":       "Select only capabilities needed to implement this bounded automation. Prefer the smallest sufficient workflow.",
		},
		"model": model, "questions": questions,
	})
	if err != nil {
		return Draft{}, err
	}
	response, err := c.evaluate(ctx, base+"/systemone", requestBody)
	if err != nil {
		return Draft{}, err
	}
	if err := validateAnswers(response.Answers, questions); err != nil {
		return Draft{}, fmt.Errorf("validate Jev decisions: %w", err)
	}
	draft, err := compileDraft(intent, requestedName, timezone, response)
	if err != nil {
		return Draft{}, err
	}
	if err := draft.Validate(requestedName); err != nil {
		return Draft{}, fmt.Errorf("validate compiled draft: %w", err)
	}
	return draft, nil
}

func validatedAPIBase(value string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(value), "/")
	if base == "" {
		base = DefaultAPIBase
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("TypeSafe API base must be an absolute URL without credentials, query, or fragment")
	}
	loopback := parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1"
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback) {
		return "", errors.New("TypeSafe API base must use HTTPS (HTTP is allowed only for loopback development)")
	}
	return base, nil
}

func (c Client) evaluate(ctx context.Context, endpoint string, body []byte) (systemOneResponse, error) {
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	for attempt := 0; attempt < 3; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return systemOneResponse{}, err
		}
		request.Header.Set("Authorization", "Bearer "+c.APIKey)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("User-Agent", "Werkt-Authoring/1.0")
		response, err := client.Do(request)
		if err != nil {
			return systemOneResponse{}, fmt.Errorf("request Jev decisions: %w", err)
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		_ = response.Body.Close()
		if readErr != nil {
			return systemOneResponse{}, fmt.Errorf("read Jev response: %w", readErr)
		}
		if (response.StatusCode == http.StatusTooManyRequests || response.StatusCode == 529) && attempt < 2 {
			if err := waitForRetry(ctx, response.Header.Get("Retry-After"), attempt); err != nil {
				return systemOneResponse{}, err
			}
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return systemOneResponse{}, fmt.Errorf("TypeSafe API returned HTTP %d: %s", response.StatusCode, apiErrorMessage(responseBody, response.StatusCode))
		}
		var value systemOneResponse
		if err := json.Unmarshal(responseBody, &value); err != nil {
			return systemOneResponse{}, errors.New("TypeSafe API returned invalid JSON")
		}
		if strings.TrimSpace(value.Model) == "" || value.Answers == nil {
			return systemOneResponse{}, errors.New("TypeSafe API response omitted model or answers")
		}
		return value, nil
	}
	return systemOneResponse{}, errors.New("TypeSafe API remained overloaded after 3 attempts")
}

func waitForRetry(ctx context.Context, header string, attempt int) error {
	delay := time.Duration(100*(1<<attempt)) * time.Millisecond
	if seconds, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && seconds >= 0 && seconds <= 30 {
		delay = time.Duration(seconds) * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func apiErrorMessage(body []byte, status int) string {
	var value struct {
		Error   any    `json:"error"`
		Detail  string `json:"detail"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &value) == nil {
		for _, candidate := range []string{value.Detail, value.Message} {
			if candidate = strings.TrimSpace(candidate); candidate != "" && len(candidate) <= 500 {
				return candidate
			}
		}
		if text, ok := value.Error.(string); ok && strings.TrimSpace(text) != "" && len(text) <= 500 {
			return strings.TrimSpace(text)
		}
	}
	return http.StatusText(status)
}

func planningQuestions() map[string]question {
	operationCriteria := make(map[string]string, len(operationCatalog))
	for _, operation := range operationCatalog {
		operationCriteria[operation.ID] = operation.Description
	}
	questions := map[string]question{
		"trigger": {Type: "choice", Instructions: "Which trigger shape does the requested automation explicitly call for? Choose webhook when no timing or inbound channel is stated.", Criteria: map[string]string{
			"webhook": "An on-demand or generic signed HTTP event", "schedule": "A recurring or time-based run", "email": "An inbound email", "ntfy": "An inbound ntfy topic message",
		}},
		"primary_operation": {Type: "choice", Instructions: "Which available operation is the main outcome or indispensable capability requested by the automation?", Criteria: operationCriteria},
		"requires_approval": {Type: "noul", Instructions: "Should a human explicitly approve this automation before it performs an external write, deletion, publication, or other consequential action?"},
		"risk":              {Type: "score", Instructions: "How consequential would an incorrect execution of this automation be?", Criteria: []string{"Read-only or readily reversible", "Low impact external change", "Meaningful publication or data mutation", "Destructive, sensitive, or difficult to reverse"}},
	}
	for _, operation := range operationCatalog {
		questions[relevanceQuestion(operation.ID)] = question{Type: "noul", Instructions: "Does the requested automation need this operation: " + operation.Description + "?"}
	}
	for _, later := range operationCatalog {
		for _, earlier := range operationCatalog {
			if earlier.Stage >= later.Stage {
				continue
			}
			questions[dependencyQuestion(later.ID, earlier.ID)] = question{Type: "noul", Instructions: map[string]any{
				"question":          "To fulfill the request, does `later_operation` need the output or completion of `earlier_operation`?",
				"earlier_operation": earlier.Description, "later_operation": later.Description,
			}}
		}
	}
	return questions
}

func validateAnswers(answers map[string]answer, questions map[string]question) error {
	for id, expected := range questions {
		value, ok := answers[id]
		if !ok {
			return fmt.Errorf("answer %q is missing", id)
		}
		if value.Type != expected.Type {
			return fmt.Errorf("answer %q has type %q, expected %q", id, value.Type, expected.Type)
		}
		switch expected.Type {
		case "noul":
			if !unit(value.Noul) {
				return fmt.Errorf("answer %q has an invalid noul", id)
			}
		case "choice":
			criteria := expected.Criteria.(map[string]string)
			if _, exists := criteria[value.Choice]; !exists || !unit(value.Confidence) {
				return fmt.Errorf("answer %q has an invalid choice or confidence", id)
			}
			if err := validateDistribution(value.Probabilities, criteria); err != nil {
				return fmt.Errorf("answer %q: %w", id, err)
			}
		case "score":
			levels := expected.Criteria.([]string)
			if math.IsNaN(value.Score) || value.Score < 0 || value.Score > float64(len(levels)-1) || !unit(value.Confidence) {
				return fmt.Errorf("answer %q has an invalid score or confidence", id)
			}
		}
	}
	return nil
}

func validateDistribution(values map[string]float64, criteria map[string]string) error {
	if len(values) != len(criteria) {
		return errors.New("probability distribution does not match the choices")
	}
	total := 0.0
	for option := range criteria {
		probability, ok := values[option]
		if !ok || !unit(probability) {
			return errors.New("probability distribution contains an invalid choice or value")
		}
		total += probability
	}
	if math.Abs(total-1) > 0.02 {
		return errors.New("probabilities do not sum to one")
	}
	return nil
}

func unit(value float64) bool { return !math.IsNaN(value) && value >= 0 && value <= 1 }

func relevanceQuestion(id string) string { return "use_" + strings.ReplaceAll(id, "-", "_") }

func dependencyQuestion(later, earlier string) string {
	return "depends_" + strings.ReplaceAll(later, "-", "_") + "__" + strings.ReplaceAll(earlier, "-", "_")
}
