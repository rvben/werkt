package domain

import (
	"strings"
	"testing"
	"time"
)

func TestInvestigationSubmissionAndHandoff(t *testing.T) {
	now := time.Now().UTC()
	reserved := InvestigationState{Status: "reserved"}
	unknown := InvestigationState{Status: "submission_unknown"}
	running := InvestigationState{Status: "running", TaskID: "task_example", TaskURL: "https://example.com/tasks/example", Attempt: 1}
	review := running
	review.Status = "review"
	review.CloudStatus = "ready"
	review.CloudUpdatedAt, review.ObservedAt = &now, &now
	review.ActualBaseSHA = strings.Repeat("a", 40)
	review.Handoff = "Reproduced, changed parser, regression tests passed; awaiting review."
	review.PatchRef, review.PatchSHA256 = "artifacts/example.patch", strings.Repeat("b", 64)
	local := review
	local.Status = "local"
	completed := local
	completed.Status, completed.DeliveryEvidence = "completed", "Reviewed and delivered in commit example; release pending."
	for _, pair := range [][2]InvestigationState{{reserved, unknown}, {unknown, running}, {running, review}, {review, local}, {local, completed}} {
		if err := pair[1].ValidateTransition(pair[0]); err != nil {
			t.Fatalf("%s -> %s: %v", pair[0].Status, pair[1].Status, err)
		}
	}
	tests := map[string]struct{ old, next InvestigationState }{
		"launch without durable uncertainty": {reserved, running},
		"retry ambiguous submission":         {unknown, reserved},
		"reopen completed":                   {completed, running},
		"drop unknown reservation":           {unknown, InvestigationState{Status: "abandoned"}},
		"running local takeover":             {running, local},
	}
	add := func(name string, edit func(*InvestigationState)) {
		next := local
		edit(&next)
		tests[name] = struct{ old, next InvestigationState }{review, next}
	}
	add("no written handoff", func(s *InvestigationState) { s.Handoff = "" })
	add("no patch snapshot", func(s *InvestigationState) { s.PatchSHA256 = "" })
	add("cloud still running", func(s *InvestigationState) { s.CloudStatus = "pending" })
	add("no actual base", func(s *InvestigationState) { s.ActualBaseSHA = "" })
	add("replace task", func(s *InvestigationState) { s.TaskID = "task_other" })
	add("unsupported attempt", func(s *InvestigationState) { s.Attempt = 0 })
	add("credentials in task URL", func(s *InvestigationState) { s.TaskURL = "https://secret@example.com/task" })
	add("oversized findings", func(s *InvestigationState) { s.Handoff = strings.Repeat("x", 65537) })
	add("future observation", func(s *InvestigationState) { future := now.Add(time.Hour); s.ObservedAt = &future })
	add("erase observation", func(s *InvestigationState) { s.ObservedAt = nil })
	add("unsupported completion", func(s *InvestigationState) { s.Status = "completed" })
	stale := review
	past := now.Add(-time.Hour)
	stale.ObservedAt, stale.CloudUpdatedAt = &past, &past
	staleLocal := stale
	staleLocal.Status = "local"
	tests["stale cloud observation"] = struct{ old, next InvestigationState }{stale, staleLocal}
	changed := local
	changed.PatchSHA256 = strings.Repeat("c", 64)
	tests["replace local snapshot"] = struct{ old, next InvestigationState }{local, changed}
	for name, pair := range tests {
		t.Run(name, func(t *testing.T) {
			if err := pair.next.ValidateTransition(pair.old); err == nil {
				t.Fatal("unsafe transition accepted")
			}
		})
	}
}
