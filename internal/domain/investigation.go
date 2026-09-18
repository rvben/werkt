package domain

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var ErrInvalidInvestigation = errors.New("invalid investigation")

// InvestigationSpec is the immutable input to a manually selected investigation.
// RepositoryID, rather than a mutable repository name, identifies the subject.
type InvestigationSpec struct {
	RequestID        string `json:"requestId"`
	RepositoryID     string `json:"repositoryId"`
	Repository       string `json:"repository"`
	IssueNumber      int64  `json:"issueNumber"`
	IssueFingerprint string `json:"issueFingerprint"`
	EnvironmentID    string `json:"environmentId"`
	Branch           string `json:"branch"`
	ExpectedBaseSHA  string `json:"expectedBaseSha"`
}

// InvestigationState is replaced atomically, guarded by the record version.
// Cloud status is evidence, not a workflow transition or issue resolution.
type InvestigationState struct {
	Status           string     `json:"status"`
	TaskID           string     `json:"taskId,omitempty"`
	TaskURL          string     `json:"taskUrl,omitempty"`
	Attempt          int        `json:"attempt,omitempty"`
	ActualBaseSHA    string     `json:"actualBaseSha,omitempty"`
	CloudStatus      string     `json:"cloudStatus,omitempty"`
	CloudUpdatedAt   *time.Time `json:"cloudUpdatedAt,omitempty"`
	ObservedAt       *time.Time `json:"observedAt,omitempty"`
	Handoff          string     `json:"handoff,omitempty"`
	PatchRef         string     `json:"patchRef,omitempty"`
	PatchSHA256      string     `json:"patchSha256,omitempty"`
	DeliveryEvidence string     `json:"deliveryEvidence,omitempty"`
	Reason           string     `json:"reason,omitempty"`
}

type Investigation struct {
	InvestigationSpec
	State     InvestigationState `json:"state"`
	Version   int64              `json:"version"`
	CreatedAt time.Time          `json:"createdAt"`
	UpdatedAt time.Time          `json:"updatedAt"`
}

var investigationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var repositoryID = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
var repositoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var sha256Value = regexp.MustCompile(`^[a-f0-9]{64}$`)
var commitSHA = regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`)

func ValidateInvestigationSubject(repository string, issue int64) error {
	if !repositoryID.MatchString(repository) || issue <= 0 {
		return fmt.Errorf("%w: positive repository ID and issue number are required", ErrInvalidInvestigation)
	}
	return nil
}

func (s InvestigationSpec) Validate() error {
	if err := ValidateInvestigationSubject(s.RepositoryID, s.IssueNumber); err != nil {
		return err
	}
	if !investigationID.MatchString(s.RequestID) || !repositoryName.MatchString(s.Repository) || len(s.Repository) > 256 ||
		!sha256Value.MatchString(s.IssueFingerprint) || !commitSHA.MatchString(s.ExpectedBaseSHA) ||
		!shortText(s.EnvironmentID, 256) || !shortText(s.Branch, 256) {
		return fmt.Errorf("%w: request ID, repository, fingerprint, environment, branch and full base SHA are required", ErrInvalidInvestigation)
	}
	return nil
}

func shortText(s string, max int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= max && !strings.ContainsAny(s, "\x00\r\n")
}

func (s InvestigationState) Terminal() bool {
	return s.Status == "completed" || s.Status == "abandoned"
}

// ValidateTransition also protects immutable cloud identity and handoff snapshots.
// An uncertain submission never returns to reserved: only reconciliation may
// attach its task, or explicitly abandon the reservation with a reason.
func (s InvestigationState) ValidateTransition(previous InvestigationState) error {
	invalid := func(reason string) error { return fmt.Errorf("%w: %s", ErrInvalidInvestigation, reason) }
	transitions := map[string][]string{
		"reserved":           {"submission_unknown", "abandoned"},
		"submission_unknown": {"running", "needs_input", "review", "failed", "abandoned"},
		"running":            {"needs_input", "review", "failed"},
		"needs_input":        {"running", "review", "failed", "abandoned"},
		"failed":             {"running", "review", "abandoned"},
		"review":             {"running", "local", "completed", "abandoned"},
		"local":              {"review", "completed", "abandoned"},
	}
	allowed := false
	if options, ok := transitions[previous.Status]; ok {
		allowed = s.Status == previous.Status
		for _, next := range options {
			allowed = allowed || s.Status == next
		}
	}
	if !allowed {
		return invalid("invalid or terminal workflow transition")
	}
	if len(s.Handoff) > 65536 || len(s.Reason) > 4096 || len(s.DeliveryEvidence) > 8192 || len(s.PatchRef) > 2048 ||
		len(s.TaskURL) > 2048 || len(s.CloudStatus) > 128 || strings.ContainsRune(s.Handoff+s.Reason+s.DeliveryEvidence+s.PatchRef, 0) {
		return invalid("field exceeds its documented limit or contains NUL")
	}
	if s.TaskID != "" {
		u, err := url.Parse(s.TaskURL)
		if !investigationID.MatchString(s.TaskID) || err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || s.Attempt < 1 || s.Attempt > 100 {
			return invalid("task requires a valid ID, HTTPS URL and selected attempt")
		}
	} else if s.TaskURL != "" || s.Attempt != 0 || s.CloudStatus != "" || s.CloudUpdatedAt != nil || s.ObservedAt != nil || s.ActualBaseSHA != "" {
		return invalid("cloud evidence requires an attached task")
	}
	if previous.TaskID != "" && (s.TaskID != previous.TaskID || s.TaskURL != previous.TaskURL) {
		return invalid("attached cloud task identity is immutable")
	}
	if (s.Status == "reserved" || s.Status == "submission_unknown") && s.TaskID != "" {
		return invalid("reconcile attached task into a non-reservation state")
	}
	if s.Status != "reserved" && s.Status != "submission_unknown" && s.Status != "abandoned" && s.TaskID == "" {
		return invalid("this state requires a task")
	}
	if s.ActualBaseSHA != "" && !commitSHA.MatchString(s.ActualBaseSHA) {
		return invalid("actual base must be a full SHA")
	}
	if previous.ActualBaseSHA != "" && s.ActualBaseSHA != previous.ActualBaseSHA {
		return invalid("actual base is immutable")
	}
	if (s.PatchRef == "") != (s.PatchSHA256 == "") || (s.PatchSHA256 != "" && !sha256Value.MatchString(s.PatchSHA256)) {
		return invalid("patch reference and SHA-256 must be supplied together")
	}
	if s.CloudUpdatedAt != nil && s.ObservedAt == nil {
		return invalid("cloud timestamp requires observation time")
	}
	if s.ObservedAt != nil && (s.ObservedAt.After(time.Now().Add(time.Minute)) || (previous.ObservedAt != nil && s.ObservedAt.Before(*previous.ObservedAt))) {
		return invalid("observation is in the future or older than recorded evidence")
	}
	if s.CloudUpdatedAt != nil && (s.CloudUpdatedAt.After(s.ObservedAt.Add(time.Minute)) || (previous.CloudUpdatedAt != nil && s.CloudUpdatedAt.Before(*previous.CloudUpdatedAt))) {
		return invalid("cloud timestamp regressed or is in the future")
	}
	if previous.ObservedAt != nil && s.ObservedAt == nil || previous.CloudUpdatedAt != nil && s.CloudUpdatedAt == nil {
		return invalid("recorded observation cannot be erased")
	}
	if s.Status == "local" {
		if s.CloudStatus != "ready" || s.CloudUpdatedAt == nil || s.ObservedAt == nil || time.Since(*s.ObservedAt) > 5*time.Minute || s.ActualBaseSHA == "" || s.PatchSHA256 == "" || strings.TrimSpace(s.Handoff) == "" {
			return invalid("local handoff requires a recently observed idle task, base, saved patch digest and written findings")
		}
		if previous.Status == "local" && (s.CloudUpdatedAt == nil || !s.CloudUpdatedAt.Equal(*previous.CloudUpdatedAt) || s.PatchSHA256 != previous.PatchSHA256 || s.Attempt != previous.Attempt) {
			return invalid("cloud or snapshot changed: return to review before continuing locally")
		}
	}
	if s.Status == "completed" && (strings.TrimSpace(s.DeliveryEvidence) == "" || strings.TrimSpace(s.Handoff) == "") {
		return invalid("completion requires findings and explicit delivery or resolution evidence")
	}
	if s.Status == "abandoned" && strings.TrimSpace(s.Reason) == "" {
		return invalid("abandonment requires a reconciliation reason; it does not cancel remote work")
	}
	return nil
}
