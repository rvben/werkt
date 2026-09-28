package database_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/domain"
	"github.com/rvben/werkt/internal/runner"
	"github.com/rvben/werkt/internal/service"
)

type leaseRow struct {
	status      string
	attempt     int
	errorText   string
	logs        string
	owner       *string
	expiresAt   *time.Time
	availableIn time.Duration
}

type runFixture struct {
	store *database.Store
	admin *pgxpool.Pool
	runID string
}

func newRunFixture(t *testing.T, ctx context.Context, maxAttempts int) runFixture {
	t.Helper()
	databaseURL := os.Getenv("WERKT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("WERKT_TEST_DATABASE_URL is not set")
	}
	dataDir := t.TempDir()
	store, err := database.Open(ctx, databaseURL, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	reset := func() {
		if _, err := admin.Exec(context.Background(), `TRUNCATE notification_deliveries, notification_events, audit_events, runs, events, triggers, revisions, automations CASCADE`); err != nil {
			t.Errorf("reset database: %v", err)
		}
	}
	reset()
	t.Cleanup(reset)

	manifest := domain.Manifest{
		APIVersion: "werkt.dev/v1", Kind: "Automation",
		Metadata:  domain.Metadata{Name: "release-example", Project: "integration"},
		Runtime:   domain.Runtime{Language: "go", Command: []string{"./automation"}},
		Execution: domain.Execution{Retries: maxAttempts - 1},
	}
	contentHash := strings.Repeat("c", 64)
	artifact := filepath.Join(dataDir, "artifacts", contentHash)
	writeRetentionFixture(t, artifact)
	revisionID, err := store.Deploy(ctx, manifest, contentHash, artifact, retentionArtifactProvenance(manifest, contentHash))
	if err != nil {
		t.Fatal(err)
	}
	runID, created, err := store.EnqueueManualRun(ctx, manifest.Metadata.Name, "release-run", revisionID, json.RawMessage(`{}`), "agent:test")
	if err != nil || !created {
		t.Fatalf("enqueue created=%v err=%v", created, err)
	}
	return runFixture{store: store, admin: admin, runID: runID}
}

func (f runFixture) row(t *testing.T, ctx context.Context) leaseRow {
	t.Helper()
	var row leaseRow
	var availableIn float64
	err := f.admin.QueryRow(ctx, `
		SELECT status, attempt, error, logs, lease_owner, lease_expires_at,
			extract(epoch from available_at - now())
		FROM runs WHERE id = $1`, f.runID).Scan(
		&row.status, &row.attempt, &row.errorText, &row.logs, &row.owner, &row.expiresAt, &availableIn)
	if err != nil {
		t.Fatal(err)
	}
	row.availableIn = time.Duration(availableIn * float64(time.Second))
	return row
}

func TestReleaseRunRequeuesTheSameAttemptForImmediatePickup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRunFixture(t, ctx, 1)
	run, err := fixture.store.AcquireRun(ctx, "worker-1", time.Minute)
	if err != nil || run == nil {
		t.Fatalf("acquire run=%#v err=%v", run, err)
	}

	if err := fixture.store.ReleaseRun(ctx, run.Run, "worker-1", "partial logs", "interrupted by control-plane shutdown"); err != nil {
		t.Fatal(err)
	}
	row := fixture.row(t, ctx)
	if row.status != string(domain.RunQueued) || row.attempt != 1 || row.owner != nil || row.expiresAt != nil ||
		row.availableIn > 0 || row.errorText != "interrupted by control-plane shutdown" || row.logs != "partial logs" {
		t.Fatalf("released row = %+v, want queued at attempt 1, available now, lease cleared, reason and logs kept", row)
	}

	// A single-attempt run is still picked up again: an interruption is not a
	// failure, so it cannot end the run.
	again, err := fixture.store.AcquireRun(ctx, "worker-2", time.Minute)
	if err != nil || again == nil || again.ID != run.ID || again.Attempt != 2 {
		t.Fatalf("reacquire run=%#v err=%v; want the released run as attempt 2", again, err)
	}
}

func TestReleaseRunRefusesARunTheWorkerNoLongerOwns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRunFixture(t, ctx, 1)
	run, err := fixture.store.AcquireRun(ctx, "worker-1", time.Minute)
	if err != nil || run == nil {
		t.Fatalf("acquire run=%#v err=%v", run, err)
	}

	err = fixture.store.ReleaseRun(ctx, run.Run, "worker-2", "", "interrupted by control-plane shutdown")
	if !errors.Is(err, database.ErrRunLeaseLost) {
		t.Fatalf("release by another worker = %v, want ErrRunLeaseLost", err)
	}
	row := fixture.row(t, ctx)
	if row.status != string(domain.RunRunning) || row.owner == nil || *row.owner != "worker-1" {
		t.Fatalf("row after refused release = %+v, want it still running under worker-1", row)
	}
}

// shutdownExecutor blocks until its run is cancelled, then spends a moment
// cleaning up, as a husker run removing its VM does, before it returns.
type shutdownExecutor struct {
	started chan struct{}
	succeed bool
	failure error
	cleaned atomic.Bool
}

func (e *shutdownExecutor) Execute(ctx context.Context, _ domain.RunnableRun) (runner.Result, error) {
	close(e.started)
	<-ctx.Done()
	time.Sleep(200 * time.Millisecond)
	e.cleaned.Store(true)
	if e.succeed {
		return runner.Result{Logs: "finished as shutdown began", Output: json.RawMessage(`{}`)}, nil
	}
	if e.failure != nil {
		return runner.Result{Logs: "failed on its own"}, e.failure
	}
	return runner.Result{Logs: "stopped by shutdown"}, ctx.Err()
}

func runWorkerUntilShutdown(t *testing.T, fixture runFixture, executor *shutdownExecutor) {
	t.Helper()
	workerContext, stopWorker := context.WithCancel(context.Background())
	defer stopWorker()
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.NewWorker(fixture.store, executor, "worker-1", 10*time.Millisecond).Run(workerContext)
	}()
	select {
	case <-executor.started:
	case <-time.After(10 * time.Second):
		t.Fatal("worker never started the run")
	}
	stopWorker()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not return after shutdown")
	}
	if !executor.cleaned.Load() {
		t.Fatal("worker returned before the run finished cleaning up")
	}
}

func TestWorkerReleasesARunInterruptedByShutdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRunFixture(t, ctx, 3)

	runWorkerUntilShutdown(t, fixture, &shutdownExecutor{started: make(chan struct{})})

	row := fixture.row(t, ctx)
	if row.status != string(domain.RunQueued) || row.attempt != 1 || row.owner != nil || row.availableIn > 0 ||
		row.errorText != "interrupted by control-plane shutdown" || row.logs != "stopped by shutdown" {
		t.Fatalf("row after shutdown = %+v, want it released for immediate pickup, not failed or left leased", row)
	}
}

func TestWorkerRecordsARunThatSucceededAsShutdownBegan(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRunFixture(t, ctx, 3)

	runWorkerUntilShutdown(t, fixture, &shutdownExecutor{started: make(chan struct{}), succeed: true})

	row := fixture.row(t, ctx)
	if row.status != string(domain.RunSucceeded) || row.owner != nil || row.logs != "finished as shutdown began" {
		t.Fatalf("row after shutdown = %+v, want the success recorded", row)
	}
}

func TestWorkerFailsARunWhoseOwnFailureCoincidesWithShutdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRunFixture(t, ctx, 1)

	failure := errors.New("automation process failed: exit status 1")
	runWorkerUntilShutdown(t, fixture, &shutdownExecutor{started: make(chan struct{}), failure: failure})

	row := fixture.row(t, ctx)
	if row.status != string(domain.RunFailed) || row.owner != nil || row.errorText != failure.Error() || row.logs != "failed on its own" {
		t.Fatalf("row after shutdown = %+v, want the attempt's own failure recorded, not released as an interruption", row)
	}
}
