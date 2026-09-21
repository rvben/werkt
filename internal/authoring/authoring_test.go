package authoring

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/werkt/internal/manifest"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestClientRequestsTypedJevDecisionsAndCompilesDraft(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "http://127.0.0.1/v1/systemone" || request.Method != http.MethodPost {
			t.Errorf("request = %s %s", request.Method, request.URL)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		if got := request.Header.Get("User-Agent"); got != "Werkt-Authoring/1.0" {
			t.Errorf("user agent = %q", got)
		}
		var body struct {
			State     map[string]any      `json:"state"`
			Model     string              `json:"model"`
			Questions map[string]question `json:"questions"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "jev-latest" || !strings.Contains(body.State["automation_intent"].(string), "Friday") {
			t.Fatalf("request body = %#v", body)
		}
		if body.Questions["primary_operation"].Type != "choice" || body.Questions["requires_approval"].Type != "noul" || body.Questions["risk"].Type != "score" {
			t.Fatalf("questions = %#v", body.Questions)
		}
		return jsonResponse(http.StatusOK, validJevResponse()), nil
	})

	value, err := (Client{APIBase: "http://127.0.0.1/v1", APIKey: "test-key", Model: "jev-latest", HTTPClient: &http.Client{Transport: transport}}).Generate(
		context.Background(), "Every Friday, summarize the supplied sheet and notify the operator.", "release-brief", "Europe/Amsterdam",
	)
	if err != nil {
		t.Fatal(err)
	}
	if value.Name != "release-brief" || value.Trigger.Type != "schedule" || value.Trigger.Cron != "0 9 * * 5" {
		t.Fatalf("draft identity/trigger = %#v", value)
	}
	if value.Decision.Provider != "typesafe" || value.Decision.Model != "jev-1.13.0" || len(value.Decision.Operations) != len(operationCatalog) {
		t.Fatalf("decision = %#v", value.Decision)
	}
	ids := stepIDs(value.Steps)
	if strings.Join(ids, ",") != "sheets-read,openai-chat,review,operator-notify" {
		t.Fatalf("steps = %v", ids)
	}
	if !buildManifest(value, "drafts", "").Execution.State.Enabled {
		t.Fatal("approval workflow must enable transactional state")
	}
	assertPythonCompiles(t, value.Files)
}

func TestClientRejectsMissingJevAnswer(t *testing.T) {
	response := validJevResponse()
	delete(response.Answers, "risk")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, response), nil
	})}
	_, err := (Client{APIBase: "http://localhost/v1", APIKey: "test-key", HTTPClient: client}).Generate(
		context.Background(), "Create a weekly release brief from engineering systems.", "release-brief", "UTC",
	)
	if err == nil || !strings.Contains(err.Error(), `answer "risk" is missing`) {
		t.Fatalf("error = %v", err)
	}
}

func TestClientRetriesTypeSafeOverload(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		if attempts < 3 {
			return jsonResponse(529, map[string]string{"detail": "overloaded"}), nil
		}
		return jsonResponse(http.StatusOK, validJevResponse()), nil
	})}
	if _, err := (Client{APIBase: "http://localhost/v1", APIKey: "test-key", HTTPClient: client}).Generate(
		context.Background(), "Every Friday summarize a supplied sheet and notify the operator.", "release-brief", "UTC",
	); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d", attempts)
	}
}

func TestDraftValidateRejectsDependencyCycle(t *testing.T) {
	draft := validDraft()
	draft.Steps[0].DependsOn = []string{"publish"}
	if err := draft.Validate("release-brief"); err == nil || !strings.Contains(err.Error(), "dependency cycle") {
		t.Fatalf("error = %v", err)
	}
}

func TestInferCronRequiresAnExplicitClockAndMapsWeekdays(t *testing.T) {
	if cron, ok := inferCron("Every Friday at 14:30, prepare sheet 12"); !ok || cron != "30 14 * * 5" {
		t.Fatalf("cron = %q, ok = %v", cron, ok)
	}
	if cron, ok := inferCron("Every Friday, prepare sheet 12"); !ok || cron != "0 9 * * 5" {
		t.Fatalf("default cron = %q, ok = %v", cron, ok)
	}
}

func TestOperationConflictKeepsPrimaryMutation(t *testing.T) {
	update, _ := operationByID("sheets-update")
	clear, _ := operationByID("sheets-clear")
	selected := []selectedOperation{{Spec: clear, Probability: 0.99}, {Spec: update, Probability: 0.8}}
	resolved := resolveOperationConflicts(selected, "sheets-update")
	if len(resolved) != 1 || resolved[0].Spec.ID != "sheets-update" {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func TestSelectionUsesPrimaryDistributionForAmbiguousMultiStepIntent(t *testing.T) {
	answers := make(map[string]answer, len(operationCatalog))
	for _, operation := range operationCatalog {
		answers[relevanceQuestion(operation.ID)] = answer{Type: "noul", Noul: 0.05}
	}
	primary := answer{Choice: "operator-notify", Probabilities: map[string]float64{"operator-notify": 0.45, "openai-chat": 0.25}}
	selected := selectOperations(primary, answers)
	if strings.Join(selectedOperationIDs(selected), ",") != "openai-chat,operator-notify" {
		t.Fatalf("selected = %v", selectedOperationIDs(selected))
	}
}

func TestDraftValidateRejectsInvalidScheduleTimezone(t *testing.T) {
	draft := validDraft()
	draft.Trigger.Timezone = "Mars/Olympus"
	if err := draft.Validate("release-brief"); err == nil || !strings.Contains(err.Error(), "valid IANA timezone") {
		t.Fatalf("error = %v", err)
	}
}

func TestWritePackageBuildsValidatedReviewableArtifact(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "release-brief")
	result, err := WritePackage(validDraft(), PackageOptions{
		OutputDirectory: output, Project: "engineering", Folder: "release", Model: "jev-latest",
		IntentDigest: IntentDigest("confidential customer alpha phrase"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Automation != "release-brief" || result.Output != output || result.ContentHash == "" {
		t.Fatalf("result = %#v", result)
	}
	value, err := manifest.Load(output)
	if err != nil {
		t.Fatal(err)
	}
	if value.Metadata.Project != "engineering" || value.Metadata.Folder != "release" {
		t.Fatalf("metadata = %#v", value.Metadata)
	}
	if value.Execution.State.Enabled {
		t.Fatal("state should remain disabled without an approval step")
	}
	if value.Runtime.Secrets["GITHUB_TOKEN"] != "engineering/release-brief/github" {
		t.Fatalf("secrets = %#v", value.Runtime.Secrets)
	}
	if len(value.Runtime.Egress) != 1 || value.Runtime.Egress[0].Host != "api.github.com" {
		t.Fatalf("egress = %#v", value.Runtime.Egress)
	}
	planBody, err := os.ReadFile(filepath.Join(output, "werkt.plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(planBody), "confidential customer alpha phrase") || !strings.Contains(string(planBody), `"intentSha256"`) || !strings.Contains(string(planBody), `"provider": "typesafe"`) {
		t.Fatalf("plan leaks intent or omits provenance: %s", planBody)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	command := exec.Command(python, "-m", "unittest", "discover", "-s", ".", "-p", "test_*.py")
	command.Dir = output
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated tests: %v\n%s", err, combined)
	}
	if _, err := WritePackage(validDraft(), PackageOptions{OutputDirectory: output, Project: "engineering"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second write error = %v", err)
	}
}

func TestIntentDigestIsStableAndOpaque(t *testing.T) {
	first := IntentDigest("same intent")
	if first != IntentDigest("same intent") || first == IntentDigest("different intent") || strings.Contains(first, "intent") {
		t.Fatalf("unexpected digests: %q", first)
	}
}

func TestEnabledRequiresANonBlankBYOKKey(t *testing.T) {
	if Enabled("") || Enabled("  \t") || !Enabled("test-key") {
		t.Fatal("unexpected BYOK feature-gate result")
	}
}

func validJevResponse() systemOneResponse {
	questions := planningQuestions()
	response := systemOneResponse{Model: "jev-1.13.0", Answers: make(map[string]answer, len(questions))}
	response.Usage.InputTokens, response.Usage.OutputTokens = 420, 80
	for id, question := range questions {
		switch question.Type {
		case "noul":
			response.Answers[id] = answer{Type: "noul", Noul: 0.05}
		case "choice":
			criteria := question.Criteria.(map[string]string)
			choice := "webhook"
			if id == "trigger" {
				choice = "schedule"
			} else if id == "primary_operation" {
				choice = "operator-notify"
			}
			probabilities := make(map[string]float64, len(criteria))
			for option := range criteria {
				probabilities[option] = 0
			}
			probabilities[choice] = 1
			response.Answers[id] = answer{Type: "choice", Choice: choice, Confidence: 0.92, Probabilities: probabilities}
		case "score":
			response.Answers[id] = answer{Type: "score", Score: 2, Confidence: 0.88}
		}
	}
	for id, probability := range map[string]float64{
		relevanceQuestion("sheets-read"):                     0.96,
		relevanceQuestion("openai-chat"):                     0.91,
		relevanceQuestion("operator-notify"):                 0.98,
		"requires_approval":                                  0.82,
		dependencyQuestion("openai-chat", "sheets-read"):     0.93,
		dependencyQuestion("operator-notify", "openai-chat"): 0.89,
		dependencyQuestion("operator-notify", "sheets-read"): 0.12,
	} {
		value := response.Answers[id]
		value.Noul = probability
		response.Answers[id] = value
	}
	return response
}

func jsonResponse(status int, value any) *http.Response {
	body, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}
}

func stepIDs(steps []Step) []string {
	values := make([]string, len(steps))
	for index, step := range steps {
		values[index] = step.ID
	}
	return values
}

func selectedOperationIDs(operations []selectedOperation) []string {
	values := make([]string, len(operations))
	for index, operation := range operations {
		values[index] = operation.Spec.ID
	}
	return values
}

func assertPythonCompiles(t *testing.T, files []File) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	directory := t.TempDir()
	paths := []string{}
	for _, file := range files {
		if !strings.HasSuffix(file.Path, ".py") {
			continue
		}
		path := filepath.Join(directory, file.Path)
		if err := os.WriteFile(path, []byte(file.Content), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	command := exec.Command(python, append([]string{"-m", "py_compile"}, paths...)...)
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated Python syntax: %v\n%s", err, combined)
	}
	command = exec.Command(python, "-m", "unittest", "discover", "-s", directory, "-p", "test_*.py")
	sdk, err := filepath.Abs("../../sdk/python")
	if err != nil {
		t.Fatal(err)
	}
	command.Env = append(os.Environ(), "PYTHONPATH="+sdk)
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated Python tests: %v\n%s", err, combined)
	}
}

func validDraft() Draft {
	return Draft{
		Name: "release-brief", Description: "Draft a weekly release brief from engineering activity.",
		Trigger: Trigger{ID: "weekly", Type: "schedule", Cron: "0 9 * * 5", Timezone: "Europe/Amsterdam"},
		Steps: []Step{
			{ID: "read-issues", Title: "Read open GitHub issues", Connector: "github", Operation: "list-issues", Effect: "read", Inputs: []string{"repository"}, Outputs: []string{"issues"}},
			{ID: "publish", Title: "Publish the release brief", Connector: "github", Operation: "create-issue", Effect: "write", DependsOn: []string{"read-issues"}, Inputs: []string{"issues"}, Outputs: []string{"issue-url"}},
		},
		Secrets: []SecretBinding{{Environment: "GITHUB_TOKEN", Secret: "engineering/release-brief/github", Description: "GitHub API token"}},
		Egress:  []EgressRule{{Host: "api.github.com", Port: 443}},
		Files: []File{
			{Path: "main.py", Content: "def handler(event, context):\n    return {'ok': True}\n"},
			{Path: "workflow_plan.py", Content: "STEPS = []\n"},
			{Path: "test_workflow_plan.py", Content: "import unittest\n\nclass DraftTest(unittest.TestCase):\n    def test_placeholder(self):\n        self.assertTrue(True)\n"},
		},
		Warnings: []string{"Confirm that the destination issue repository is correct before deployment."},
		Decision: DecisionSummary{Provider: "typesafe", Model: "jev-1.13.0", Confidence: 0.9, Trigger: "schedule", TriggerConfidence: 0.9, RiskScore: 2, ApprovalProbability: 0.8, Operations: []OperationDecision{{ID: "operator-notify", Selected: true, RelevanceProbability: 0.9, PrimaryProbability: 0.8}}},
	}
}
