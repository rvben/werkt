package notification

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRenderApprovalUsesWorkspaceInboxAndBoundsDescription(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"title": "Publish Sunday sermon", "description": strings.Repeat("evidence ", 100),
		"expiresAt": time.Date(2026, 9, 8, 20, 0, 0, 0, time.UTC),
	})
	message, err := Render("approval.requested", "sermon-onliner", "approval_1", payload, "https://werkt.example/", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if message.Title != "Approval needed: Publish Sunday sermon" || message.Priority != "default" {
		t.Fatalf("message = %#v", message)
	}
	if message.URL != "https://werkt.example/app/?approvalStatus=pending&view=approvals" {
		t.Fatalf("url = %q", message.URL)
	}
	if len([]rune(message.Body)) > 550 || !strings.Contains(message.Body, "Expires") {
		t.Fatalf("body = %q", message.Body)
	}
	if message.ExpiresAt == nil || !message.ExpiresAt.Equal(time.Date(2026, 9, 8, 20, 0, 0, 0, time.UTC)) {
		t.Fatalf("expiresAt = %v", message.ExpiresAt)
	}
}

func TestRenderNotificationTest(t *testing.T) {
	message, err := Render("notification.test", "system", "test_1", json.RawMessage(`{}`), "https://werkt.example/", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if message.Title != "Werkt notification test" || message.Priority != "default" || message.URL != "https://werkt.example/app/" {
		t.Fatalf("message = %#v", message)
	}
}

func TestRenderAutomationNotificationUsesRunLinkAndDeclaredPriority(t *testing.T) {
	payload := json.RawMessage(`{"runId":"run_42","title":"Recording started","body":"Sunday service is now recording.","priority":"low"}`)
	message, err := Render("automation.notification", "sermon-onliner", "run_42:started", payload, "https://werkt.example/", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if message.Title != "Recording started" || message.Body != "Sunday service is now recording." || message.Priority != "low" {
		t.Fatalf("message = %#v", message)
	}
	if message.URL != "https://werkt.example/app/?run=run_42&view=runs" {
		t.Fatalf("url = %q", message.URL)
	}
}

func TestRenderRunFailureDoesNotExposeRuntimeError(t *testing.T) {
	payload := json.RawMessage(`{"runId":"run_1","attempts":3,"error":"secret should never be here"}`)
	message, err := Render("run.failed", "backup", "run_1", payload, "https://werkt.example", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(message.Body, "secret") || !strings.Contains(message.URL, "run=run_1") {
		t.Fatalf("message = %#v", message)
	}
}
