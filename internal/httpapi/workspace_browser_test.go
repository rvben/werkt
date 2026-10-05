package httpapi

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceBrowserKeyboardFocusAndResponsiveModality(t *testing.T) {
	if os.Getenv("WERKT_BROWSER_TESTS") == "" {
		t.Skip("set WERKT_BROWSER_TESTS=1 to run the headless workspace contract")
	}
	chrome := findChrome(t)
	server := httptest.NewServer(http.HandlerFunc(workspaceBrowserFixture))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, chrome,
		"--headless=new",
		"--disable-background-networking",
		"--disable-default-apps",
		"--disable-extensions",
		"--disable-gpu",
		"--no-first-run",
		"--no-sandbox",
		"--user-data-dir="+t.TempDir(),
		"--virtual-time-budget=3000",
		"--window-size=390,844",
		"--dump-dom",
		server.URL+"/app/",
	)
	output, err := command.CombinedOutput()
	match := regexp.MustCompile(`<pre id="browser-test-results">([^<]+)</pre>`).FindSubmatch(output)
	if len(match) != 2 {
		t.Fatalf("browser contract did not publish results: %v\n%s", err, output)
	}
	var results map[string]bool
	if err := json.Unmarshal([]byte(html.UnescapeString(string(match[1]))), &results); err != nil {
		t.Fatalf("decode browser results: %v\n%s", err, match[1])
	}
	for _, contract := range []string{
		"backCleared",
		"backgroundInert",
		"diagnosisFocused",
		"diagnosisModal",
		"diagnosisRouted",
		"flowExactLedger",
		"flowExactChronology",
		"flowAsyncFocus",
		"flowLiveEvidence",
		"flowLedgerPrecedesJourney",
		"flowReadOnly",
		"flowConnectionsMeetPorts",
		"flowInspectorAction",
		"flowTabPanelsResolve",
		"helpShortcut",
		"journeyAccessible",
		"logsDirect",
		"mobileSearchEscape",
		"operatorScopeVisible",
		"approvalFieldTyped",
		"approvalTargetProtected",
		"identifierHierarchy",
		"diagnosticAdvancedClosed",
		"runShortcutReviews",
		"runTargetScoped",
		"tableHeadersRetained",
	} {
		if !results[contract] {
			t.Errorf("browser contract %q failed: %#v", contract, results)
		}
	}
}

func TestWorkspaceBrowserEmptyStateDraftHandoff(t *testing.T) {
	if os.Getenv("WERKT_BROWSER_TESTS") == "" {
		t.Skip("set WERKT_BROWSER_TESTS=1 to run the headless workspace contract")
	}
	chrome := findChrome(t)
	server := httptest.NewServer(http.HandlerFunc(workspaceEmptyBrowserFixture))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, chrome,
		"--headless=new",
		"--disable-background-networking",
		"--disable-default-apps",
		"--disable-extensions",
		"--disable-gpu",
		"--no-first-run",
		"--no-sandbox",
		"--user-data-dir="+t.TempDir(),
		"--virtual-time-budget=3000",
		"--window-size=390,844",
		"--dump-dom",
		server.URL+"/app/",
	)
	output, err := command.CombinedOutput()
	match := regexp.MustCompile(`<pre id="browser-test-results">([^<]+)</pre>`).FindSubmatch(output)
	if len(match) != 2 {
		t.Fatalf("empty-state browser contract did not publish results: %v\n%s", err, output)
	}
	var results map[string]bool
	if err := json.Unmarshal([]byte(html.UnescapeString(string(match[1]))), &results); err != nil {
		t.Fatalf("decode empty-state browser results: %v\n%s", err, match[1])
	}
	for _, contract := range []string{"draftVisible", "deployVisible", "copyActionsNamed", "reviewBoundaryVisible", "mobileSingleColumn", "noHorizontalOverflow"} {
		if !results[contract] {
			t.Errorf("empty-state browser contract %q failed: %#v", contract, results)
		}
	}
}

// Stress the same rendered interface with API-shaped records, not DOM-only mocks.
func TestWorkspaceBrowserFlowEdgeCases(t *testing.T) {
	if os.Getenv("WERKT_BROWSER_TESTS") == "" {
		t.Skip("set WERKT_BROWSER_TESTS=1 to run the headless workspace contract")
	}
	chrome := findChrome(t)
	server := httptest.NewServer(http.HandlerFunc(workspaceEdgeBrowserFixture))
	defer server.Close()
	for _, size := range []string{"390,844", "1440,1000"} {
		t.Run(size, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, chrome, "--headless=new", "--disable-background-networking", "--disable-default-apps", "--disable-extensions", "--disable-gpu", "--no-first-run", "--no-sandbox", "--user-data-dir="+t.TempDir(), "--virtual-time-budget=12000", "--window-size="+size, "--dump-dom", server.URL+"/app/")
			output, err := command.CombinedOutput()
			match := regexp.MustCompile(`<pre id="browser-test-results">([^<]+)</pre>`).FindSubmatch(output)
			if len(match) != 2 {
				t.Fatalf("edge-case contract did not publish results: %v\n%s", err, output)
			}
			var results map[string]any
			if err := json.Unmarshal([]byte(html.UnescapeString(string(match[1]))), &results); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"longOverview", "manySources", "portsAligned", "historicalSource", "missingEvidence", "longDiagnosis", "manualDefinition"} {
				if results[key] != true {
					t.Errorf("%s failed: %#v", key, results)
				}
			}
		})
	}
}

const edgeAutomationID = "recording-complete-with-a-very-long-unbroken-identifier-for-the-weekly-international-operations-reconciliation-and-summary"

func workspaceEdgeBrowserFixture(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/test-driver.js" {
		response.Header().Set("Content-Type", "text/javascript")
		_, _ = response.Write([]byte(workspaceEdgeBrowserDriver))
		return
	}
	copyRequest := request.Clone(request.Context())
	copyURL := *request.URL
	copyURL.Path = strings.ReplaceAll(copyURL.Path, edgeAutomationID, "test-automation")
	copyRequest.URL = &copyURL
	recorder := httptest.NewRecorder()
	workspaceBrowserFixture(recorder, copyRequest)
	body := recorder.Body.String()
	if strings.HasPrefix(request.URL.Path, "/api/v1/") {
		body = strings.ReplaceAll(body, "test-automation", edgeAutomationID)
		var value any
		if json.Unmarshal([]byte(body), &value) == nil {
			var amend func(any)
			amend = func(value any) {
				switch item := value.(type) {
				case []any:
					for _, child := range item {
						amend(child)
					}
				case map[string]any:
					if _, exists := item["manifest"]; exists {
						triggers := []any{}
						if request.URL.Query().Get("edge") != "manual" {
							for n := 0; n < 20; n++ {
								triggers = append(triggers, map[string]any{"id": strings.Repeat("international-reconciliation-", 4) + string(rune('a'+n)), "type": "webhook", "enabled": true, "config": map[string]any{"provider": "zoom"}})
							}
						}
						item["triggers"] = triggers
					}
					if item["id"] == "run_test" {
						item["revisionId"] = "rev_retired_" + strings.Repeat("0123456789abcdef", 4)
						delete(item, "logs")
						delete(item, "result")
						item["error"] = "Provider rejected request: " + strings.Repeat("unbroken-upstream-error-context-", 12)
					}
					for _, child := range item {
						amend(child)
					}
				}
			}
			amend(value)
			encoded, _ := json.Marshal(value)
			body = string(encoded)
		}
	}
	for key, values := range recorder.Header() {
		for _, value := range values {
			response.Header().Add(key, value)
		}
	}
	response.WriteHeader(recorder.Code)
	_, _ = response.Write([]byte(body))
}

func findChrome(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"google-chrome",
		"chromium",
		"chromium-browser",
	} {
		if filepath.IsAbs(candidate) {
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	t.Skip("Chrome or Chromium is not installed")
	return ""
}

func workspaceBrowserFixture(response http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/app/":
		contents, _ := workspaceFiles.ReadFile("workspace/index.html")
		page := strings.Replace(string(contents), "</body>", `<script src="/test-driver.js" defer></script></body>`, 1)
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = response.Write([]byte(page))
	case "/app/workspace.css":
		contents, _ := workspaceFiles.ReadFile("workspace/workspace.css")
		response.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = response.Write(contents)
	case "/app/workspace.js":
		contents, _ := workspaceFiles.ReadFile("workspace/workspace.js")
		response.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = response.Write(contents)
	case "/app/config.js":
		response.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = response.Write([]byte(`window.WERKT_CONFIG = Object.freeze({draftingEnabled: true});`))
	case "/test-driver.js":
		response.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = response.Write([]byte(workspaceBrowserDriver))
	case "/api/v1/automations":
		writeBrowserJSON(response, `[{
			"id":"test-automation","project":"Operations","folder":"Tests","description":"Browser contract fixture","labels":[],"enabled":true,"activeRevisionId":"rev_test",
			"latestRun":{"id":"run_test","status":"failed","createdAt":"2026-08-23T18:42:00Z"}
		}]`)
	case "/api/v1/automations/test-automation":
		writeBrowserJSON(response, `{
			"id":"test-automation","project":"Operations","folder":"Tests","description":"Browser contract fixture","labels":[],"enabled":true,"activeRevisionId":"rev_test",
			"latestRun":{"id":"run_test","status":"failed","createdAt":"2026-08-23T18:42:00Z"},
			"manifest":{"runtime":{"language":"Go","image":"local"},"execution":{"concurrency":"forbid","timeout":"5m"}},
			"triggers":[{"id":"recording-complete","type":"webhook","enabled":true,"config":{"provider":"zoom"}}],"revisions":[{"id":"rev_test","contentHash":"sha256:test","createdAt":"2026-08-23T18:42:00Z","active":true,"provenance":{"artifactDigest":"sha256:artifact"}}]
		}`)
	case "/api/v1/runs":
		writeBrowserJSON(response, `[{"id":"run_test","automationId":"test-automation","revisionId":"rev_test","eventId":"evt_test","status":"failed","attempt":2,"maxAttempts":2,"createdAt":"2026-08-23T18:42:00Z","startedAt":"2026-08-23T18:42:00Z","finishedAt":"2026-08-23T18:42:01Z","error":"fixture failure","logs":"fixture log"}]`)
	case "/api/v1/runs/run_test":
		writeBrowserJSON(response, `{"id":"run_test","automationId":"test-automation","revisionId":"rev_test","eventId":"evt_test","status":"failed","attempt":2,"maxAttempts":2,"createdAt":"2026-08-23T18:42:00Z","startedAt":"2026-08-23T18:42:00Z","finishedAt":"2026-08-23T18:42:01Z","error":"fixture failure","logs":"fixture log","event":{"id":"evt_test","occurredAt":"2026-08-23T18:41:59Z","receivedAt":"2026-08-23T18:42:00Z","trigger":{"automation":"test-automation","id":"recording-complete","type":"webhook"},"metadata":{"source":"webhook"}}}`)
	case "/api/v1/approvals":
		writeBrowserJSON(response, `[{"id":"apr_test","automationId":"test-automation","revisionId":"rev_test","requestedByRunId":"run_test","key":"publish","status":"pending","title":"Publish recording?","description":"Confirm the final title.","fields":[{"id":"title","label":"Title","type":"text","required":true,"value":"Sunday service"}],"actions":[{"id":"approve","label":"Publish","style":"primary","requiresFields":true},{"id":"reject","label":"Skip","style":"neutral"}],"expiresAt":"2026-08-31T18:42:00Z","createdAt":"2026-08-30T18:42:00Z"}]`)
	case "/api/v1/approvals/apr_test":
		writeBrowserJSON(response, `{"id":"apr_test","automationId":"test-automation","revisionId":"rev_test","requestedByRunId":"run_test","key":"publish","status":"pending","title":"Publish recording?","description":"Confirm the final title.","fields":[{"id":"title","label":"Title","type":"text","required":true,"value":"Sunday service"}],"actions":[{"id":"approve","label":"Publish","style":"primary","requiresFields":true},{"id":"reject","label":"Skip","style":"neutral"}],"expiresAt":"2026-08-31T18:42:00Z","createdAt":"2026-08-30T18:42:00Z"}`)
	case "/api/v1/deployments", "/api/v1/audit":
		writeBrowserJSON(response, `[]`)
	case "/api/v1/auth/session":
		writeBrowserJSON(response, `{"configured":false,"authenticated":false,"scope":{"environment":"development","instance":"browser-fixture","actor":"workspace:local"}}`)
	default:
		http.NotFound(response, request)
	}
}

func workspaceEmptyBrowserFixture(response http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/app/":
		contents, _ := workspaceFiles.ReadFile("workspace/index.html")
		page := strings.Replace(string(contents), "</body>", `<script src="/empty-test-driver.js" defer></script></body>`, 1)
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = response.Write([]byte(page))
	case "/app/workspace.css":
		contents, _ := workspaceFiles.ReadFile("workspace/workspace.css")
		response.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = response.Write(contents)
	case "/app/workspace.js":
		contents, _ := workspaceFiles.ReadFile("workspace/workspace.js")
		response.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = response.Write(contents)
	case "/app/config.js":
		response.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = response.Write([]byte(`window.WERKT_CONFIG = Object.freeze({draftingEnabled: true});`))
	case "/empty-test-driver.js":
		response.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = response.Write([]byte(workspaceEmptyBrowserDriver))
	case "/api/v1/auth/session":
		writeBrowserJSON(response, `{"configured":false,"authenticated":false,"scope":{"environment":"development","instance":"browser-fixture","actor":"workspace:local"}}`)
	case "/api/v1/automations", "/api/v1/approvals", "/api/v1/runs", "/api/v1/deployments", "/api/v1/audit":
		writeBrowserJSON(response, `[]`)
	default:
		http.NotFound(response, request)
	}
}

func writeBrowserJSON(response http.ResponseWriter, body string) {
	response.Header().Set("Content-Type", "application/json")
	_, _ = response.Write([]byte(body))
}

const workspaceBrowserDriver = `
(() => {
  const waitFor = async (test) => {
    const deadline = Date.now() + 1800;
    while (!test() && Date.now() < deadline) await new Promise((resolve) => setTimeout(resolve, 20));
    if (!test()) throw new Error("browser contract timed out waiting for " + String(test));
  };
  window.addEventListener("load", async () => {
    const results = {};
    try {
      await waitFor(() => document.querySelector("[data-run]"));
      results.tableHeadersRetained = getComputedStyle(document.querySelector(".data-table thead")).display !== "none";
      document.querySelector("[data-run-logs]").click();
      await waitFor(() => document.querySelector("#diagnosis-pane").getAttribute("aria-modal") === "true");
      await waitFor(() => document.querySelector('[data-diagnosis-tab="logs"]')?.getAttribute("aria-selected") === "true");
      results.diagnosisModal = document.querySelector("#diagnosis-pane").getAttribute("role") === "dialog";
      results.backgroundInert = document.querySelector("#workspace-main").hasAttribute("inert");
      results.diagnosisFocused = document.activeElement?.hasAttribute("data-close-diagnosis") === true;
      results.logsDirect = document.querySelector('[data-diagnosis-tab="logs"]').getAttribute("aria-selected") === "true"
        && document.querySelector("#diagnosis-panel-logs").textContent.includes("fixture log");
      results.diagnosisRouted = new URL(location.href).searchParams.get("run") === "run_test" && new URL(location.href).searchParams.get("tab") === "logs";
      results.operatorScopeVisible = document.querySelector("#operator-scope").textContent.includes("development") && document.querySelector("#operator-scope").textContent.includes("Local session");
      document.querySelector("[data-close-diagnosis]").click();
      document.querySelector('[data-view="approvals"]').click();
      await waitFor(() => document.querySelector("[data-approval]"));
      document.querySelector("[data-approval]").click();
      await waitFor(() => document.querySelector("#approval-dialog").open);
      results.approvalTargetProtected = document.querySelector("#approval-scope-revision").textContent === "rev_test"
        && document.querySelector("#approval-scope-automation").textContent === "test-automation"
        && document.querySelector("#approval-scope-revision").closest("details").open === false;
      results.approvalFieldTyped = document.querySelector('[data-approval-field="title"]') instanceof HTMLInputElement
        && document.querySelector('[data-resolve-approval="approve"]').textContent === "Publish";
      document.querySelector("#approval-dialog").close();
      document.querySelector('[data-view="automations"]').click();
      document.querySelector("[data-mobile-back]").click();
      results.backCleared = !document.querySelector("#app-shell").classList.contains("has-selection") && location.hash === "";
      document.querySelector("[data-automation]").click();
      await waitFor(() => document.querySelector("[data-open-run]"));
      results.identifierHierarchy = !document.querySelector(".inventory-item").textContent.includes("rev_test")
        && document.querySelector(".automation-technical").open === false
        && document.querySelector("[data-open-run]").textContent.includes("Test run")
        && document.querySelector(".revision-table").textContent.includes("Current");
      document.querySelector('[data-automation-tab="flow"]').click();
      await waitFor(() => document.querySelector(".flow-map"));
      results.flowReadOnly = !document.querySelector(".flow-view input") && !document.querySelector("[contenteditable]");
      results.flowTabPanelsResolve = [...document.querySelectorAll("[data-automation-tab]")].every((tab) => document.getElementById(tab.getAttribute("aria-controls")));
      results.flowExactLedger = document.querySelector(".flow-ledger").textContent.includes("Definition facts")
        && document.querySelector(".flow-ledger").textContent.includes("recording-complete");
      document.querySelector('[data-flow-layer="live"]').click();
      await waitFor(() => document.querySelector(".flow-source.is-observed"));
      results.flowLiveEvidence = document.querySelector(".flow-map").getAttribute("aria-label").includes("run_test")
        && document.querySelector(".flow-source.is-observed").textContent.includes("recording-complete");
      results.flowAsyncFocus = Boolean(document.activeElement?.dataset.flowLayer === "live"
        && document.querySelector('[data-flow-focus="diagnosis"]')
        && document.querySelector('[data-flow-focus="ledger-summary"]'));
      await waitFor(() => document.querySelectorAll('.flow-wires path').length === 4);
      const svg = document.querySelector('.flow-wires');
      const graphBounds = svg.getBoundingClientRect();
      const portPoint = (key, side) => {
        const node = [...document.querySelectorAll('[data-flow-evidence]')].find(item => item.dataset.flowEvidence === key);
        const port = node.querySelector('[data-flow-port="' + side + '"]').getBoundingClientRect();
        return {x: (port.left + port.right) / 2 - graphBounds.left, y: (port.top + port.bottom) / 2 - graphBounds.top};
      };
      results.flowConnectionsMeetPorts = [...svg.querySelectorAll('path')].every(path => {
        const start = path.getPointAtLength(0);
        const end = path.getPointAtLength(path.getTotalLength());
        const source = portPoint(path.dataset.from, 'out');
        const target = portPoint(path.dataset.to, 'in');
        return Math.hypot(start.x - source.x, start.y - source.y) < 1
          && Math.hypot(end.x - target.x, end.y - target.y) < 1;
      });
      document.querySelector('[data-flow-evidence="logs"]').click();
      results.flowInspectorAction = document.querySelector('#flow-selection-title').textContent === 'Execution logs'
        && document.querySelector('[data-flow-evidence="logs"]').getAttribute('aria-controls') === 'flow-selection';
      document.querySelector('[data-flow-focus="inspect-selected"]').click();
      await waitFor(() => document.querySelector('#diagnosis-tab-logs')?.getAttribute('aria-selected') === 'true');
      results.flowInspectorAction = results.flowInspectorAction && document.querySelector('#diagnosis-panel-logs').textContent.includes('fixture log');
      document.querySelector('[data-close-diagnosis]').click();
      results.flowExactLedger = results.flowExactLedger && document.querySelector(".flow-ledger time")?.textContent.includes("2026-08-23T");
      results.flowExactChronology = ["Event occurred", "Event received", "Run created", "Revision started"].every((label) => document.querySelector(".flow-ledger").textContent.includes(label));
      results.flowLedgerPrecedesJourney = Boolean(document.querySelector(".flow-ledger").compareDocumentPosition(document.querySelector(".actor-journey")) & Node.DOCUMENT_POSITION_FOLLOWING);
      document.querySelector('[data-automation-tab="overview"]').click();
      document.dispatchEvent(new KeyboardEvent("keydown", {key: "r", bubbles: true}));
      await waitFor(() => document.querySelector("#run-dialog").open);
      results.runShortcutReviews = document.querySelector("#run-dialog").open;
      results.diagnosticAdvancedClosed = document.querySelector(".run-advanced").open === false
        && document.activeElement === document.querySelector('#run-form button[type="submit"]');
      results.runTargetScoped = document.querySelector("#run-target-environment").textContent === "development"
        && document.querySelector("#run-target-version").textContent.includes("Deployed")
        && document.querySelector("#run-target-instance").textContent.length > 0
        && document.querySelector("#run-target-actor").textContent === "workspace:local";
      document.querySelector("#run-dialog").close();
      document.querySelector("[data-run]").click();
      await waitFor(() => document.querySelector('[data-diagnosis-tab="journey"]'));
      document.querySelector('[data-diagnosis-tab="journey"]').click();
      results.journeyAccessible = document.querySelector("#diagnosis-panel-journey").textContent.includes("Approval requested")
        && document.querySelector("#diagnosis-panel-journey").textContent.includes("Operator");
      document.querySelector("[data-close-diagnosis]").click();
      document.dispatchEvent(new KeyboardEvent("keydown", {key: "?", bubbles: true}));
      results.helpShortcut = document.querySelector("#help-dialog").open;
      document.querySelector("#help-dialog").close();
      document.querySelector("#mobile-search-button").click();
      document.querySelector("#automation-search").dispatchEvent(new KeyboardEvent("keydown", {key: "Escape", bubbles: true}));
      results.mobileSearchEscape = document.querySelector("#mobile-search-button").getAttribute("aria-expanded") === "false";
    } catch (error) {
      results.driverCompleted = false;
      results.driverError = String(error);
    }
    const output = document.createElement("pre");
    output.id = "browser-test-results";
    output.textContent = JSON.stringify(results);
    document.body.append(output);
  });
})();
`

const workspaceEmptyBrowserDriver = `
(() => {
  window.addEventListener("load", async () => {
    const deadline = Date.now() + 1800;
    while (!document.querySelector(".command-stack") && Date.now() < deadline) {
      await new Promise((resolve) => setTimeout(resolve, 20));
    }
    const text = document.querySelector("#workspace-content")?.textContent || "";
    const buttons = [...document.querySelectorAll(".command-stack button")].map((button) => button.getAttribute("aria-label"));
    const columns = getComputedStyle(document.querySelector(".onboarding-flow")).gridTemplateColumns.split(" ").filter(Boolean);
    const results = {
      draftVisible: text.includes("werkt draft") && text.includes("Draft"),
      deployVisible: text.includes("werkt deploy") && text.includes("Deploy"),
      copyActionsNamed: buttons.includes("Copy draft command") && buttons.includes("Copy deployment command"),
      reviewBoundaryVisible: text.includes("Inspect permissions, side effects, and assumptions") && text.includes("Nothing becomes active"),
      mobileSingleColumn: columns.length === 1,
      noHorizontalOverflow: document.documentElement.scrollWidth <= document.documentElement.clientWidth,
    };
    const output = document.createElement("pre");
    output.id = "browser-test-results";
    output.textContent = JSON.stringify(results);
    document.body.append(output);
  });
})();
`

const workspaceEdgeBrowserDriver = `
(() => {
  // Chrome's virtual clock does not advance compositor grid transitions.
  // Assert the resting layout rather than an intermediate drawer width.
  const style=document.createElement('style');
  style.textContent='.app-shell { transition: none !important; }';
  document.head.append(style);
  const waitFor = async test => {
    const deadline = Date.now() + 5000;
    while (!test() && Date.now() < deadline) await new Promise(resolve => setTimeout(resolve, 20));
    if (!test()) throw new Error("Timed out: " + test);
  };
  const fits = selector => [...document.querySelectorAll(selector)].every(e => e.scrollWidth <= e.clientWidth + 1);
  window.addEventListener("load", async () => {
    const results = {};
    try {
      await waitFor(() => document.querySelector('[data-automation]'));
      document.querySelector('[data-automation]').click();
      await waitFor(() => document.querySelector('.automation-technical'));
      results.longOverview = document.querySelector('.detail-title h2').textContent.length > 100 && fits('.detail-header, .detail-body');
      document.querySelector('[data-automation-tab="flow"]').click();
      await waitFor(() => document.querySelectorAll('.flow-wires path').length > 3);
      results.manySources = document.querySelectorAll('.flow-source').length === 20 && fits('.flow-map, .flow-inspector, .flow-node-copy, .flow-history-notice') && document.querySelector('.flow-source-list').clientHeight <= 384;
      if(!results.manySources) results.sourceOverflow=[...document.querySelectorAll('.flow-map, .flow-inspector, .flow-node-copy, .flow-history-notice')].filter(e=>e.scrollWidth>e.clientWidth+1).map(e=>({class:e.className,width:e.clientWidth,scroll:e.scrollWidth}));
      const aligned = () => {
        const svg = document.querySelector('.flow-wires'), bounds = svg.getBoundingClientRect();
        return [...svg.querySelectorAll('path')].every(path => {
          const port = (key, side) => {
            const node = [...document.querySelectorAll('[data-flow-evidence]')].find(e => e.dataset.flowEvidence === key);
            const r = node.querySelector('[data-flow-port="' + side + '"]').getBoundingClientRect();
            return {x:(r.left+r.right)/2-bounds.left,y:(r.top+r.bottom)/2-bounds.top};
          };
          const a=path.getPointAtLength(0),b=path.getPointAtLength(path.getTotalLength()),s=port(path.dataset.from,'out'),t=port(path.dataset.to,'in');
          return Math.hypot(a.x-s.x,a.y-s.y)<1 && Math.hypot(b.x-t.x,b.y-t.y)<1;
        });
      };
      results.portsAligned = aligned();
      const sources=document.querySelector('.flow-source-list');
      sources.scrollTop=sources.scrollHeight;
      sources.dispatchEvent(new Event('scroll'));
      results.portsAligned=results.portsAligned && aligned();
      sources.scrollTop=0;sources.dispatchEvent(new Event('scroll'));
      document.querySelector('[data-flow-layer="live"]').click();
      await waitFor(() => document.querySelector('[data-flow-evidence="recorded-source"]'));
      await waitFor(() => document.querySelector('[data-flow-evidence="logs"]').textContent.includes('No output recorded'));
      await waitFor(() => document.querySelector('.flow-wires path.is-observed') && document.querySelectorAll('.flow-wires path').length > 3);
      results.portsAligned = results.portsAligned && aligned();
      results.historicalSource = document.querySelector('.flow-core').textContent.includes('Historical manifest unavailable')
        && document.querySelectorAll('.flow-source.is-observed').length === 1
        && document.querySelector('[data-flow-evidence="recorded-source"]').classList.contains('is-observed');
      results.missingEvidence = !document.querySelector('[data-flow-evidence="logs"]').classList.contains('is-observed')
        && !document.querySelector('[data-flow-evidence="output"]').classList.contains('is-observed')
        && document.querySelector('[data-flow-evidence="output"]').textContent.includes('No result recorded');
      document.querySelector('[data-flow-evidence="recorded-source"]').click();
      results.historicalSource = results.historicalSource && document.querySelector('.flow-inspector').textContent.includes('Unavailable for this run');
      document.querySelector('[data-flow-focus="diagnosis"]').click();
      await waitFor(() => document.querySelector('.run-failure'));
      await waitFor(() => document.querySelector('.diagnosis-body').clientWidth > 250);
      results.longDiagnosis = fits('.run-failure, .diagnosis-body') && document.querySelector('.run-failure').textContent.includes('unbroken-upstream');
      if(!results.longDiagnosis) results.diagnosisOverflow=[...document.querySelectorAll('.run-failure, .diagnosis-body, .run-failure-actions .button')].map(e=>({class:e.className,width:e.clientWidth,scroll:e.scrollWidth}));
      document.querySelector('[data-close-diagnosis]').click();
      const originalFetch=window.fetch;
      window.fetch=(path,opts) => originalFetch(String(path).includes('/api/v1/automations/') ? String(path)+'?edge=manual' : path,opts);
      document.querySelector('[data-flow-layer="definition"]').click();
      document.querySelector('#refresh-button').click();
      await waitFor(() => document.querySelector('.flow-map')?.textContent.includes('No manifest triggers'));
      await waitFor(() => document.querySelectorAll('.flow-wires path').length === 3);
      results.manualDefinition = document.querySelector('.flow-map').textContent.includes('Manual runs remain available') && aligned();
    } catch(error) { results.error=String(error);results.pathCount=document.querySelectorAll(".flow-wires path").length; }
    const pre=document.createElement('pre');pre.id='browser-test-results';pre.textContent=JSON.stringify(results);document.body.append(pre);
  });
})();
`
