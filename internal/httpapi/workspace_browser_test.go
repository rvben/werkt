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
		"helpShortcut",
		"logsDirect",
		"mobileSearchEscape",
		"operatorScopeVisible",
		"runShortcutReviews",
		"runTargetScoped",
		"tableHeadersRetained",
	} {
		if !results[contract] {
			t.Errorf("browser contract %q failed: %#v", contract, results)
		}
	}
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
			"manifest":{"runtime":{"language":"Go","image":"local"},"execution":{"concurrency":"forbid","timeout":"5m"}},
			"triggers":[],"revisions":[{"id":"rev_test","contentHash":"sha256:test","createdAt":"2026-08-23T18:42:00Z","active":true}]
		}`)
	case "/api/v1/runs":
		writeBrowserJSON(response, `[{"id":"run_test","automationId":"test-automation","revisionId":"rev_test","eventId":"evt_test","status":"failed","attempt":2,"maxAttempts":2,"createdAt":"2026-08-23T18:42:00Z","startedAt":"2026-08-23T18:42:00Z","finishedAt":"2026-08-23T18:42:01Z","error":"fixture failure","logs":"fixture log"}]`)
	case "/api/v1/runs/run_test":
		writeBrowserJSON(response, `{"id":"run_test","automationId":"test-automation","revisionId":"rev_test","eventId":"evt_test","status":"failed","attempt":2,"maxAttempts":2,"createdAt":"2026-08-23T18:42:00Z","startedAt":"2026-08-23T18:42:00Z","finishedAt":"2026-08-23T18:42:01Z","error":"fixture failure","logs":"fixture log"}`)
	case "/api/v1/deployments", "/api/v1/audit":
		writeBrowserJSON(response, `[]`)
	case "/api/v1/auth/session":
		writeBrowserJSON(response, `{"configured":false,"authenticated":false,"scope":{"environment":"development","instance":"browser-fixture","actor":"workspace:local"}}`)
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
    if (!test()) throw new Error("browser contract timed out");
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
      results.operatorScopeVisible = document.querySelector("#operator-scope").textContent.includes("development") && document.querySelector("#operator-scope").textContent.includes("workspace:local");
      document.querySelector("[data-close-diagnosis]").click();
      document.querySelector("[data-mobile-back]").click();
      results.backCleared = !document.querySelector("#app-shell").classList.contains("has-selection") && location.hash === "";
      document.querySelector("[data-automation]").click();
      await waitFor(() => document.querySelector("[data-open-run]"));
      document.dispatchEvent(new KeyboardEvent("keydown", {key: "r", bubbles: true}));
      await waitFor(() => document.querySelector("#run-dialog").open);
      results.runShortcutReviews = document.querySelector("#run-dialog").open;
      results.runTargetScoped = document.querySelector("#run-target-environment").textContent === "development"
        && document.querySelector("#run-target-instance").textContent.length > 0
        && document.querySelector("#run-target-actor").textContent === "workspace:local";
      document.querySelector("#run-dialog").close();
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
