package database

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rvben/werkt/internal/domain"
)

// approvalGuardFixture deploys one automation and opens a pending approval on
// its active revision, the state a deploy of a new revision would strand.
type approvalGuardFixture struct {
	store      *Store
	dataDir    string
	manifest   domain.Manifest
	revisionID string
	approvalID string
}

func newApprovalGuardFixture(t *testing.T, ctx context.Context) approvalGuardFixture {
	t.Helper()
	store, run, automation := acquiredApprovalRun(t, ctx)
	if err := store.CompleteRun(ctx, run, "worker-1", "", json.RawMessage(`{}`), nil, domain.RunControl{
		Approval: &domain.ApprovalRequest{
			Key: "publish", Title: "Publish Sunday?", ExpiresAt: time.Now().UTC().Add(time.Hour),
			Actions: []domain.ApprovalAction{{ID: "approve", Label: "Approve"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	listed, err := store.ListApprovalsPage(ctx, automation, "", ListCursor{}, 10)
	if err != nil || len(listed) != 1 || listed[0].Status != "pending" {
		t.Fatalf("approvals=%#v err=%v", listed, err)
	}
	var manifest domain.Manifest
	var manifestJSON []byte
	if err := store.pool.QueryRow(ctx, `SELECT manifest FROM revisions WHERE id = $1`, run.RevisionID).Scan(&manifestJSON); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		t.Fatal(err)
	}
	return approvalGuardFixture{store: store, dataDir: store.dataDir, manifest: manifest, revisionID: run.RevisionID, approvalID: listed[0].ID}
}

func (f approvalGuardFixture) deploy(t *testing.T, ctx context.Context, contentHash string) (string, error) {
	t.Helper()
	return f.store.Deploy(ctx, f.manifest, contentHash, testStorageFixture(t, f.dataDir, "artifacts", contentHash), testArtifactProvenance(f.manifest, contentHash))
}

func (f approvalGuardFixture) activeRevision(t *testing.T, ctx context.Context) string {
	t.Helper()
	var active string
	if err := f.store.pool.QueryRow(ctx, `SELECT active_revision_id FROM automations WHERE id = $1`, f.manifest.Metadata.Name).Scan(&active); err != nil {
		t.Fatal(err)
	}
	return active
}

func TestDeployRefusesToStrandAPendingApprovalIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newApprovalGuardFixture(t, ctx)

	_, err := f.deploy(t, ctx, strings.Repeat("d", 64))
	if !errors.Is(err, ErrPendingApprovals) {
		t.Fatalf("deploy of a new revision error=%v, want ErrPendingApprovals", err)
	}
	var pending *PendingApprovalsError
	if !errors.As(err, &pending) || len(pending.Approvals) != 1 || pending.Approvals[0].ID != f.approvalID ||
		pending.ActiveRevision != f.revisionID || !strings.Contains(err.Error(), f.approvalID) || !strings.Contains(err.Error(), "Publish Sunday?") {
		t.Fatalf("refusal=%#v (%v), want it to name approval %s on %s", pending, err, f.approvalID, f.revisionID)
	}
	if active := f.activeRevision(t, ctx); active != f.revisionID {
		t.Fatalf("active revision=%s after a refused deploy, want %s", active, f.revisionID)
	}
	var revisions int
	if err := f.store.pool.QueryRow(ctx, `SELECT count(*) FROM revisions WHERE automation_id = $1`, f.manifest.Metadata.Name).Scan(&revisions); err != nil || revisions != 1 {
		t.Fatalf("revisions=%d err=%v, want the refused revision rolled back", revisions, err)
	}
	// The point of refusing: the request can still be answered.
	if _, created, err := f.store.ResolveApproval(ctx, f.approvalID, "answer", f.revisionID, "approve", map[string]any{}, "user:test"); err != nil || !created {
		t.Fatalf("resolve after refused deploy created=%v err=%v", created, err)
	}
}

func TestDeployOfTheActiveRevisionKeepsPendingApprovalsAnswerableIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newApprovalGuardFixture(t, ctx)

	revisionID, err := f.deploy(t, ctx, strings.Repeat("c", 64))
	if err != nil || revisionID != f.revisionID {
		t.Fatalf("redeploy of the active revision=%s err=%v, want %s", revisionID, err, f.revisionID)
	}
	if _, created, err := f.store.ResolveApproval(ctx, f.approvalID, "answer", f.revisionID, "approve", map[string]any{}, "user:test"); err != nil || !created {
		t.Fatalf("resolve after unchanged redeploy created=%v err=%v", created, err)
	}
}

func TestDeployIgnoresApprovalsNoLongerAnswerableIntegration(t *testing.T) {
	for name, settle := range map[string]func(t *testing.T, ctx context.Context, f approvalGuardFixture){
		"resolved": func(t *testing.T, ctx context.Context, f approvalGuardFixture) {
			if _, _, err := f.store.ResolveApproval(ctx, f.approvalID, "answer", f.revisionID, "approve", map[string]any{}, "user:test"); err != nil {
				t.Fatal(err)
			}
		},
		// Past its expiry an approval is refused whatever the revision, so
		// nothing is lost by activating before the expiry sweep marks it.
		"past expiry": func(t *testing.T, ctx context.Context, f approvalGuardFixture) {
			if _, err := f.store.pool.Exec(ctx, `UPDATE approvals SET expires_at = now() - interval '1 minute' WHERE id = $1`, f.approvalID); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			f := newApprovalGuardFixture(t, ctx)
			settle(t, ctx, f)
			revisionID, err := f.deploy(t, ctx, strings.Repeat("d", 64))
			if err != nil || revisionID == f.revisionID || f.activeRevision(t, ctx) != revisionID {
				t.Fatalf("deploy=%s err=%v, want the new revision active", revisionID, err)
			}
		})
	}
}

// activateQueuedDeployment drives a queued deployment through to activation
// the way the deployment worker does.
func activateQueuedDeployment(t *testing.T, ctx context.Context, f approvalGuardFixture, deploymentID, worker, contentHash string) (string, error) {
	t.Helper()
	claimed, err := f.store.AcquireDeployment(ctx, worker, time.Minute)
	if err != nil || claimed == nil || claimed.ID != deploymentID {
		t.Fatalf("claimed=%#v err=%v, want %s", claimed, err, deploymentID)
	}
	if err := f.store.SetDeploymentStage(ctx, deploymentID, worker, domain.DeploymentActivating, f.manifest.Metadata.Name, contentHash); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RecordDeploymentStep(ctx, deploymentID, worker, domain.DeploymentStepUpdate{ID: "activate", Kind: "activate", Status: domain.DeploymentStepRunning}); err != nil {
		t.Fatal(err)
	}
	return f.store.ActivateDeployment(ctx, deploymentID, worker, f.manifest, contentHash,
		testStorageFixture(t, f.dataDir, "artifacts", contentHash), testArtifactProvenance(f.manifest, contentHash), "agent:operator")
}

func TestDeploymentStrandsApprovalsOnlyWhenRetriedWithTheOverrideIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newApprovalGuardFixture(t, ctx)
	contentHash := strings.Repeat("d", 64)

	original, created, err := f.store.CreateDeployment(ctx, "dep_guarded", "guarded-key", strings.Repeat("a", 64), "sha256:"+strings.Repeat("1", 64),
		testStorageFixture(t, f.dataDir, "deployment-sources", "dep_guarded"), "agent:operator")
	if err != nil || !created || original.StrandApprovals {
		t.Fatalf("deployment=%#v created=%v err=%v", original, created, err)
	}
	_, activateErr := activateQueuedDeployment(t, ctx, f, original.ID, "worker-1", contentHash)
	if !errors.Is(activateErr, ErrPendingApprovals) {
		t.Fatalf("activation error=%v, want ErrPendingApprovals", activateErr)
	}
	if err := f.store.FailDeployment(ctx, original.ID, "worker-1", activateErr); err != nil {
		t.Fatal(err)
	}
	failed, err := f.store.GetDeployment(ctx, original.ID)
	if err != nil || failed.Status != domain.DeploymentFailed || !strings.Contains(failed.Error, f.approvalID) {
		t.Fatalf("failed=%#v err=%v, want the error to name %s", failed, err, f.approvalID)
	}

	// A plain retry is refused for the same reason.
	plain, _, err := f.store.RetryDeployment(ctx, original.ID, "dep_plain", "plain-key", "agent:operator", false)
	if err != nil || plain.StrandApprovals {
		t.Fatalf("plain retry=%#v err=%v", plain, err)
	}
	if _, err := activateQueuedDeployment(t, ctx, f, plain.ID, "worker-2", contentHash); !errors.Is(err, ErrPendingApprovals) {
		t.Fatalf("plain retry activation error=%v, want ErrPendingApprovals", err)
	}
	if err := f.store.FailDeployment(ctx, plain.ID, "worker-2", errors.New("refused")); err != nil {
		t.Fatal(err)
	}

	// Reusing a key with the other answer would replay a deployment that does
	// not carry the decision the caller just made.
	if _, _, err := f.store.RetryDeployment(ctx, original.ID, "dep_flip", "plain-key", "agent:operator", true); !errors.Is(err, ErrDeploymentIdempotencyConflict) {
		t.Fatalf("flipped override on a used key error=%v, want ErrDeploymentIdempotencyConflict", err)
	}

	override, created, err := f.store.RetryDeployment(ctx, original.ID, "dep_override", "override-key", "agent:operator", true)
	if err != nil || !created || !override.StrandApprovals {
		t.Fatalf("override retry=%#v created=%v err=%v", override, created, err)
	}
	replayed, created, err := f.store.RetryDeployment(ctx, original.ID, "dep_override_again", "override-key", "agent:operator", true)
	if err != nil || created || replayed.ID != override.ID {
		t.Fatalf("override replay=%#v created=%v err=%v", replayed, created, err)
	}
	revisionID, err := activateQueuedDeployment(t, ctx, f, override.ID, "worker-3", contentHash)
	if err != nil || revisionID == f.revisionID || f.activeRevision(t, ctx) != revisionID {
		t.Fatalf("override activation=%s err=%v, want the new revision active", revisionID, err)
	}

	var details []byte
	if err := f.store.pool.QueryRow(ctx, `
		SELECT details FROM audit_events
		WHERE action = 'automation.deployed' AND details->>'deploymentId' = $1`, override.ID).Scan(&details); err != nil {
		t.Fatal(err)
	}
	var audit struct {
		StrandedApprovals []string `json:"strandedApprovals"`
	}
	if err := json.Unmarshal(details, &audit); err != nil || len(audit.StrandedApprovals) != 1 || audit.StrandedApprovals[0] != f.approvalID {
		t.Fatalf("audit details=%s err=%v, want strandedApprovals [%s]", details, err, f.approvalID)
	}
	if _, _, err := f.store.ResolveApproval(ctx, f.approvalID, "answer", f.revisionID, "approve", map[string]any{}, "user:test"); !errors.Is(err, ErrAutomationRevisionChanged) {
		t.Fatalf("resolve after override error=%v, want the approval stranded", err)
	}
}
