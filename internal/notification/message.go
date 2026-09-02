package notification

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type Message struct {
	EventType    string     `json:"eventType"`
	AutomationID string     `json:"automationId"`
	SubjectID    string     `json:"subjectId"`
	Title        string     `json:"title"`
	Body         string     `json:"body"`
	URL          string     `json:"url"`
	Priority     string     `json:"priority"`
	Tags         []string   `json:"tags"`
	OccurredAt   time.Time  `json:"occurredAt"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
}

func Render(eventType, automationID, subjectID string, payload json.RawMessage, publicURL string, occurredAt time.Time) (Message, error) {
	var data struct {
		ApprovalID  string    `json:"approvalId"`
		RunID       string    `json:"runId"`
		Title       string    `json:"title"`
		Body        string    `json:"body"`
		Priority    string    `json:"priority"`
		Description string    `json:"description"`
		Status      string    `json:"status"`
		ResolvedBy  string    `json:"resolvedBy"`
		ExpiresAt   time.Time `json:"expiresAt"`
		Attempts    int       `json:"attempts"`
	}
	if err := json.Unmarshal(payload, &data); err != nil {
		return Message{}, fmt.Errorf("decode notification payload: %w", err)
	}
	message := Message{
		EventType: eventType, AutomationID: automationID, SubjectID: subjectID,
		Priority: "default", OccurredAt: occurredAt.UTC(),
	}
	switch eventType {
	case "notification.test":
		message.Title = "Werkt notification test"
		message.Body = "The durable notification delivery path is working."
		message.URL = strings.TrimRight(publicURL, "/") + "/app/"
		message.Tags = []string{"white_check_mark"}
	case "automation.notification":
		message.Title = fallback(data.Title, automationID)
		message.Body = compactBody(data.Body, "Open Werkt to inspect this automation run.")
		message.URL = runURL(publicURL, fallback(data.RunID, subjectID))
		message.Priority = fallback(data.Priority, "default")
		message.Tags = []string{"automation"}
	case "approval.requested":
		message.Title = "Approval needed: " + fallback(data.Title, automationID)
		message.Body = compactBody(data.Description, "Open Werkt to review this request.")
		if !data.ExpiresAt.IsZero() {
			message.Body += " Expires " + data.ExpiresAt.UTC().Format(time.RFC3339) + "."
		}
		message.URL = approvalURL(publicURL)
		message.Priority = "default"
		message.Tags = []string{"approval", "inbox_tray"}
		if !data.ExpiresAt.IsZero() {
			expiresAt := data.ExpiresAt.UTC()
			message.ExpiresAt = &expiresAt
		}
	case "approval.expiring":
		message.Title = "Approval expiring: " + fallback(data.Title, automationID)
		message.Body = "This request still needs a decision."
		if !data.ExpiresAt.IsZero() {
			message.Body += " It expires " + data.ExpiresAt.UTC().Format(time.RFC3339) + "."
		}
		message.URL = approvalURL(publicURL)
		message.Priority = "high"
		message.Tags = []string{"approval", "hourglass_flowing_sand"}
		if !data.ExpiresAt.IsZero() {
			expiresAt := data.ExpiresAt.UTC()
			message.ExpiresAt = &expiresAt
		}
	case "approval.resolved":
		message.Title = "Approval " + fallback(data.Status, "resolved") + ": " + fallback(data.Title, automationID)
		message.Body = "The decision was recorded in Werkt."
		message.URL = approvalURL(publicURL)
		message.Tags = []string{"approval", "white_check_mark"}
	case "run.failed":
		message.Title = "Automation failed: " + automationID
		message.Body = fmt.Sprintf("Run %s exhausted %d attempt(s). Open Werkt for redacted diagnostics.", fallback(data.RunID, subjectID), data.Attempts)
		message.URL = runURL(publicURL, fallback(data.RunID, subjectID))
		message.Priority = "high"
		message.Tags = []string{"warning", "automation"}
	default:
		return Message{}, fmt.Errorf("unsupported notification event %q", eventType)
	}
	return message, nil
}

func approvalURL(publicURL string) string {
	return appURL(publicURL, map[string]string{"view": "approvals", "approvalStatus": "pending"})
}

func runURL(publicURL, runID string) string {
	return appURL(publicURL, map[string]string{"view": "runs", "run": runID})
}

func appURL(publicURL string, values map[string]string) string {
	base, err := url.Parse(strings.TrimRight(publicURL, "/") + "/app/")
	if err != nil {
		return ""
	}
	query := base.Query()
	for key, value := range values {
		if value != "" {
			query.Set(key, value)
		}
	}
	base.RawQuery = query.Encode()
	return base.String()
}

func fallback(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func compactBody(value, fallback string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return fallback
	}
	const maximum = 500
	if len([]rune(value)) <= maximum {
		return value
	}
	return string([]rune(value)[:maximum-1]) + "…"
}
