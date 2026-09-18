package database

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/werkt/internal/domain"
)

func TestInvestigationRegistryIntegration(t *testing.T) {
	url := os.Getenv("WERKT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WERKT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := Open(ctx, url, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `TRUNCATE investigations CASCADE`); err != nil {
		t.Fatal(err)
	}
	defer store.pool.Exec(context.Background(), `TRUNCATE investigations CASCADE`) //nolint:errcheck
	spec := domain.InvestigationSpec{RequestID: "request-a", RepositoryID: "123", Repository: "example/project", IssueNumber: 42, IssueFingerprint: strings.Repeat("a", 64), EnvironmentID: "env", Branch: "main", ExpectedBaseSHA: strings.Repeat("b", 40)}
	// Same-key concurrent replay must create one durable reservation only.
	var wg sync.WaitGroup
	created := make(chan bool, 8)
	failures := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			_, fresh, err := store.ReserveInvestigation(ctx, spec, "test")
			created <- fresh
			failures <- err
		})
	}
	wg.Wait()
	close(created)
	close(failures)
	n := 0
	for fresh := range created {
		if fresh {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("created %d reservations", n)
	}
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	other := spec
	other.RequestID = "request-b"
	if _, _, err := store.ReserveInvestigation(ctx, other, "test"); !errors.Is(err, ErrInvestigationConflict) {
		t.Fatalf("duplicate issue: %v", err)
	}
	other = spec
	other.Branch = "other"
	if _, _, err := store.ReserveInvestigation(ctx, other, "test"); !errors.Is(err, ErrInvestigationConflict) {
		t.Fatalf("conflicting idempotency key: %v", err)
	}
	r, err := store.UpdateInvestigation(ctx, spec.RequestID, 1, domain.InvestigationState{Status: "submission_unknown"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateInvestigation(ctx, spec.RequestID, 1, domain.InvestigationState{Status: "abandoned", Reason: "stale writer"}, "test"); !errors.Is(err, ErrInvestigationConflict) {
		t.Fatalf("lost update: %v", err)
	}
	if _, err := store.UpdateInvestigation(ctx, spec.RequestID, r.Version, domain.InvestigationState{Status: "reserved"}, "test"); !errors.Is(err, domain.ErrInvalidInvestigation) {
		t.Fatalf("ambiguous retry: %v", err)
	}
	r, err = store.UpdateInvestigation(ctx, spec.RequestID, r.Version, domain.InvestigationState{Status: "abandoned", Reason: "Manually reconciled: never submitted."}, "test")
	if err != nil {
		t.Fatal(err)
	}
	other = spec
	other.RequestID = "request-b"
	other.Repository = "example/renamed"
	if _, fresh, err := store.ReserveInvestigation(ctx, other, "test"); err != nil || !fresh {
		t.Fatalf("next generation: %v %v", fresh, err)
	}
	items, err := store.ListInvestigations(ctx, "123", 42, ListCursor{}, 100)
	if err != nil || len(items) != 2 || items[1].State.Status != "abandoned" {
		t.Fatalf("history lost after rename: %+v %v", items, err)
	}
	page, err := store.ListInvestigations(ctx, "123", 42, ListCursor{CreatedAt: items[0].CreatedAt, ID: items[0].RequestID}, 1)
	if err != nil || len(page) != 1 || page[0].RequestID != spec.RequestID {
		t.Fatalf("pagination: %+v %v", page, err)
	}
	var versions int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM investigation_versions WHERE request_id=$1`, spec.RequestID).Scan(&versions); err != nil || versions != 3 {
		t.Fatalf("history versions=%d err=%v", versions, err)
	}
	// Replayed intent retains its final state: it is never a new launch permit.
	replayed, fresh, err := store.ReserveInvestigation(ctx, spec, "test")
	if err != nil || fresh || replayed.State.Status != "abandoned" {
		t.Fatalf("terminal replay: %+v %v %v", replayed, fresh, err)
	}
	// Competing new request IDs for a different issue race at the unique index.
	errs := make(chan error, 2)
	for _, id := range []string{"race-a", "race-b"} {
		wg.Go(func() {
			candidate := spec
			candidate.RequestID = id
			candidate.IssueNumber = 43
			_, _, err := store.ReserveInvestigation(ctx, candidate, "test")
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	successes, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrInvestigationConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("ownership race: successes=%d conflicts=%d", successes, conflicts)
	}
	// A known cloud task may belong to only one issue, and failed attachment
	// must preserve the uncertain reservation and its previous version.
	b, err := store.UpdateInvestigation(ctx, "request-b", 1, domain.InvestigationState{Status: "submission_unknown"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	state := domain.InvestigationState{Status: "running", TaskID: "task_unique", TaskURL: "https://example.com/task", Attempt: 1}
	if _, err := store.UpdateInvestigation(ctx, b.RequestID, b.Version, state, "test"); err != nil {
		t.Fatal(err)
	}
	items, err = store.ListInvestigations(ctx, "123", 43, ListCursor{}, 1)
	if err != nil || len(items) != 1 {
		t.Fatalf("race owner: %v %v", items, err)
	}
	owner, err := store.UpdateInvestigation(ctx, items[0].RequestID, 1, domain.InvestigationState{Status: "submission_unknown"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateInvestigation(ctx, owner.RequestID, owner.Version, state, "test"); !errors.Is(err, ErrInvestigationConflict) {
		t.Fatalf("cloud identity collision: %v", err)
	}
	after, err := store.GetInvestigation(ctx, owner.RequestID)
	if err != nil || after.Version != owner.Version || after.State.Status != "submission_unknown" {
		t.Fatalf("failed attachment partially committed: %+v %v", after, err)
	}
}
