package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/werkt/internal/domain"
)

// recordingSleeper stands in for the wait between husker retries, so tests
// observe the delay a retry asked for without spending it.
type recordingSleeper struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (s *recordingSleeper) sleep(ctx context.Context, delay time.Duration) error {
	s.mu.Lock()
	s.delays = append(s.delays, delay)
	s.mu.Unlock()
	return ctx.Err()
}

func (s *recordingSleeper) recorded() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.delays...)
}

func newTestHuskerRunner(t *testing.T, server *httptest.Server, config HuskerConfig) (*HuskerRunner, *recordingSleeper) {
	t.Helper()
	config.URL = server.URL
	config.HTTPClient = server.Client()
	if config.CleanupTimeout == 0 {
		config.CleanupTimeout = 5 * time.Second
	}
	runner, err := NewHuskerRunner(config)
	if err != nil {
		t.Fatal(err)
	}
	sleeper := &recordingSleeper{}
	runner.sleep = sleeper.sleep
	return runner, sleeper
}

// dropConnection closes the connection without an HTTP response, which the
// client sees as EOF: what a restarted SSH tunnel does to a request in flight.
func dropConnection(t *testing.T, response http.ResponseWriter) {
	t.Helper()
	hijacker, ok := response.(http.Hijacker)
	if !ok {
		t.Fatal("test server cannot hijack connections")
	}
	connection, _, err := hijacker.Hijack()
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
}

func writeRateLimited(t *testing.T, response http.ResponseWriter, retryAfter string) {
	t.Helper()
	if retryAfter != "" {
		response.Header().Set("Retry-After", retryAfter)
	}
	writeJSONStatus(t, response, http.StatusTooManyRequests, map[string]string{
		"kind": "rate_limited", "message": "too many requests",
	})
}

func TestHuskerRequestWaitsOutTheRateLimitHuskerAnnounces(t *testing.T) {
	var mu sync.Mutex
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		writes++
		if writes == 1 {
			writeRateLimited(t, response, "7")
			return
		}
		writeJSON(t, response, map[string]any{"bytes_written": 3})
	}))
	defer server.Close()
	runner, sleeper := newTestHuskerRunner(t, server, HuskerConfig{})

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := runner.uploadFile(ctx, "werkt-test", "/tmp/file", []byte("abc"), 0o600); err != nil {
		t.Fatalf("upload after a rate limit: %v", err)
	}
	if writes != 2 {
		t.Fatalf("write requests = %d, want 2", writes)
	}
	if delays := sleeper.recorded(); len(delays) != 1 || delays[0] != 7*time.Second {
		t.Fatalf("retry delays = %v, want [7s]", delays)
	}
}

func TestHuskerRateLimitWithoutRetryAfterBacksOffExponentially(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		if requests <= 3 {
			writeRateLimited(t, response, "")
			return
		}
		writeJSON(t, response, execResponse{ExitCode: 0})
	}))
	defer server.Close()
	runner, sleeper := newTestHuskerRunner(t, server, HuskerConfig{})

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := runner.exec(ctx, "werkt-test", execRequest{Command: "/bin/true"}); err != nil {
		t.Fatalf("exec after rate limits: %v", err)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if delays := sleeper.recorded(); !equalDurations(delays, want) {
		t.Fatalf("retry delays = %v, want %v", delays, want)
	}
}

func TestHuskerRateLimitIsReturnedWhenTheWaitWouldOutliveTheDeadline(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		writeRateLimited(t, response, "7")
	}))
	defer server.Close()
	runner, sleeper := newTestHuskerRunner(t, server, HuskerConfig{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := runner.uploadFile(ctx, "werkt-test", "/tmp/file", []byte("abc"), 0o600)
	var apiErr *huskerAPIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want the 429", err)
	}
	if requests != 1 || len(sleeper.recorded()) != 0 {
		t.Fatalf("requests = %d, delays = %v; want one request and no wait", requests, sleeper.recorded())
	}
}

// A worker releases an attempt interrupted by shutdown instead of failing it,
// which it can only do if the request reports the cancellation rather than the
// rate limit it was waiting out.
func TestHuskerRetryWaitInterruptedByCancellationReportsIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		writeRateLimited(t, response, "7")
	}))
	defer server.Close()
	runner, _ := newTestHuskerRunner(t, server, HuskerConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner.sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}

	err := runner.uploadFile(ctx, "werkt-test", "/tmp/file", []byte("abc"), 0o600)
	var apiErr *huskerAPIError
	if !errors.Is(err, context.Canceled) || !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want both the cancellation and the 429 it interrupted", err)
	}
}

func TestHuskerRateLimitAnnouncingAnUnreasonableWaitIsReturned(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		writeRateLimited(t, response, "120")
	}))
	defer server.Close()
	runner, sleeper := newTestHuskerRunner(t, server, HuskerConfig{})

	// The deadline would hold the wait; the cap on an announced wait is what
	// returns the 429.
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	err := runner.uploadFile(ctx, "werkt-test", "/tmp/file", []byte("abc"), 0o600)
	var apiErr *huskerAPIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests || apiErr.RetryAfter != 120*time.Second {
		t.Fatalf("error = %v, want the 429 announcing 120s", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 || len(sleeper.recorded()) != 0 {
		t.Fatalf("requests = %d, delays = %v; want one request and no wait", requests, sleeper.recorded())
	}
}

func TestHuskerRetriesAreBounded(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		writeJSONStatus(t, response, http.StatusServiceUnavailable, map[string]string{"kind": "unavailable"})
	}))
	defer server.Close()
	// The default cleanup budget holds every backoff, so the attempt cap is
	// what stops the retries here, not the deadline.
	runner, sleeper := newTestHuskerRunner(t, server, HuskerConfig{CleanupTimeout: defaultCleanupTimeout})

	err := runner.cleanupVM("werkt-test")
	var apiErr *huskerAPIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("cleanup error = %v, want the final 503", err)
	}
	if requests != maxHuskerAttempts || len(sleeper.recorded()) != maxHuskerAttempts-1 {
		t.Fatalf("requests = %d, delays = %v; want %d requests", requests, sleeper.recorded(), maxHuskerAttempts)
	}
}

func TestHuskerCleanupRetriesADroppedConnection(t *testing.T) {
	var mu sync.Mutex
	deletes := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if request.Method != http.MethodDelete || request.URL.Path != "/v1/vms/werkt-test" {
			http.Error(response, "unexpected", http.StatusBadRequest)
			return
		}
		deletes++
		if deletes == 1 {
			dropConnection(t, response)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	runner, _ := newTestHuskerRunner(t, server, HuskerConfig{})

	if err := runner.cleanupVM("werkt-test"); err != nil {
		t.Fatalf("cleanup error = %v", err)
	}
	if deletes != 2 {
		t.Fatalf("delete requests = %d, want 2", deletes)
	}
}

func TestHuskerCleanupTreatsAMissingVMAsRemoved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		writeJSONStatus(t, response, http.StatusNotFound, map[string]string{"kind": "vm_not_found"})
	}))
	defer server.Close()
	runner, _ := newTestHuskerRunner(t, server, HuskerConfig{})
	if err := runner.cleanupVM("werkt-test"); err != nil {
		t.Fatalf("cleanup of a missing VM = %v, want nil", err)
	}
}

func TestHuskerNeverRepeatsAPostWhoseConnectionDropped(t *testing.T) {
	var mu sync.Mutex
	execs := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		execs++
		dropConnection(t, response)
	}))
	defer server.Close()
	runner, sleeper := newTestHuskerRunner(t, server, HuskerConfig{})

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := runner.exec(ctx, "werkt-test", execRequest{Command: "/bin/true"}); err == nil {
		t.Fatal("exec over a dropped connection succeeded")
	}
	if execs != 1 || len(sleeper.recorded()) != 0 {
		t.Fatalf("exec requests = %d, delays = %v; a command may already have run, so it is sent once", execs, sleeper.recorded())
	}
}

func TestHuskerRequestWithoutADeadlineIsBounded(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	runner, _ := newTestHuskerRunner(t, server, HuskerConfig{})
	runner.requestCeiling = 100 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		done <- runner.doJSON(context.Background(), http.MethodPost, "/v1/vms", map[string]string{}, http.StatusCreated, nil)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want the request ceiling", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a husker request without a caller deadline never returned")
	}
}

// fakeHusker answers every VM lifecycle call a creator makes. createStatus
// decides how VM creation answers; failAfterCreate makes the first request
// after creation fail, so the creator abandons the VM it just made.
type fakeHusker struct {
	t               *testing.T
	mu              sync.Mutex
	createStatus    int
	createKind      string
	failAfterCreate bool
	dropFirstDelete bool
	created         []string
	deleted         []string
	deleteAttempts  int
	calls           []string
}

func (h *fakeHusker) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, request.Method+" "+request.URL.Path)
	if serveTestHuskerImageImport(h.t, response, request, nil) {
		return
	}
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/v1/images/minimal-base":
		writeJSON(h.t, response, imageResponse{Name: "minimal-base", Kind: "rootfs", ContentDigest: testDigest("minimal-base-image")})
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/images/werkt-tools-"):
		writeJSONStatus(h.t, response, http.StatusNotFound, map[string]string{"kind": "image_not_found"})
	case request.Method == http.MethodPost && request.URL.Path == "/v1/vms":
		var body createVMRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			h.t.Errorf("decode create: %v", err)
		}
		h.created = append(h.created, body.Name)
		if h.createStatus != 0 && h.createStatus != http.StatusCreated {
			writeJSONStatus(h.t, response, h.createStatus, map[string]string{"kind": h.createKind, "message": "create failed"})
			return
		}
		response.WriteHeader(http.StatusCreated)
	case request.Method == http.MethodDelete && strings.HasPrefix(request.URL.Path, "/v1/vms/"):
		h.deleteAttempts++
		if h.dropFirstDelete && h.deleteAttempts == 1 {
			dropConnection(h.t, response)
			return
		}
		h.deleted = append(h.deleted, strings.TrimPrefix(request.URL.Path, "/v1/vms/"))
		response.WriteHeader(http.StatusNoContent)
	case h.failAfterCreate:
		writeJSONStatus(h.t, response, http.StatusBadRequest, map[string]string{"kind": "invalid_request", "message": "refused"})
	case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/ready"):
		writeJSON(h.t, response, readyResponse{Ready: true})
	default:
		writeJSONStatus(h.t, response, http.StatusBadRequest, map[string]string{"kind": "unexpected", "message": request.Method + " " + request.URL.Path})
	}
}

func (h *fakeHusker) snapshot() (created, deleted []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.created...), append([]string(nil), h.deleted...)
}

// vmCreators drives each code path that creates a husker VM up to the point
// where it has asked for one.
var vmCreators = map[string]func(t *testing.T, runner *HuskerRunner) error{
	"run": func(t *testing.T, runner *HuskerRunner) error {
		_, err := runner.Execute(context.Background(), domain.RunnableRun{
			Run:          domain.Run{ID: "run_lifecycle", Attempt: 1},
			ArtifactPath: t.TempDir(),
			Manifest: domain.Manifest{
				Runtime:   domain.Runtime{Image: "alpine@sha256:" + strings.Repeat("c", 64), Command: []string{"./run"}},
				Execution: domain.Execution{Timeout: "5s"},
			},
		})
		return err
	},
	"build": func(t *testing.T, runner *HuskerRunner) error {
		return runner.Build(context.Background(), t.TempDir(), domain.Manifest{
			Metadata: domain.Metadata{Name: "lifecycle"},
			Runtime:  domain.Runtime{Image: "alpine@sha256:" + strings.Repeat("d", 64), Build: []string{"make"}},
		}, nil)
	},
	"tool preparation": func(t *testing.T, runner *HuskerRunner) error {
		_, err := runner.PrepareManifest(context.Background(), domain.Manifest{Runtime: domain.Runtime{
			Language: "python", Tools: map[string]string{"python": "3.13.7"}, Command: []string{"python3", "main.py"},
		}})
		return err
	},
}

func newCreatorRunner(t *testing.T, husker *fakeHusker) *HuskerRunner {
	t.Helper()
	misePath := filepath.Join(t.TempDir(), "mise")
	if err := os.WriteFile(misePath, []byte("verified-mise-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(husker)
	t.Cleanup(server.Close)
	runner, _ := newTestHuskerRunner(t, server, HuskerConfig{
		ProvisionTimeout: 5 * time.Second, BuildTimeout: 5 * time.Second,
		ToolBaseImage: "minimal-base", ToolBaseDigest: testDigest("minimal-base-image"), ToolPlatform: "linux-arm64",
		MisePath: misePath, MiseVersion: "2026.8.1", MiseDigest: testDigest("verified-mise-binary"),
		ToolPrepareTimeout: 5 * time.Second,
	})
	return runner
}

func TestEveryVMCreatorRemovesAVMWhoseCreationResponseWasLost(t *testing.T) {
	for name, create := range vmCreators {
		t.Run(name, func(t *testing.T) {
			husker := &fakeHusker{t: t, createStatus: http.StatusGatewayTimeout, createKind: "timeout"}
			if err := create(t, newCreatorRunner(t, husker)); err == nil {
				t.Fatal("creation that answered 504 succeeded")
			}
			created, deleted := husker.snapshot()
			if len(created) != 1 || !equalStrings(deleted, created) {
				t.Fatalf("created = %v, deleted = %v; a VM husker may have made must be removed", created, deleted)
			}
		})
	}
}

func TestEveryVMCreatorLeavesAVMItDidNotCreateAlone(t *testing.T) {
	for name, create := range vmCreators {
		t.Run(name, func(t *testing.T) {
			husker := &fakeHusker{t: t, createStatus: http.StatusConflict, createKind: "vm_already_exists"}
			if err := create(t, newCreatorRunner(t, husker)); err == nil {
				t.Fatal("creation that answered 409 succeeded")
			}
			if created, deleted := husker.snapshot(); len(created) != 1 || len(deleted) != 0 {
				t.Fatalf("created = %v, deleted = %v; a name already taken belongs to someone else", created, deleted)
			}
		})
	}
}

func TestEveryVMCreatorRemovesItsVMWhenTheFirstCleanupConnectionDrops(t *testing.T) {
	for name, create := range vmCreators {
		t.Run(name, func(t *testing.T) {
			husker := &fakeHusker{t: t, failAfterCreate: true, dropFirstDelete: true}
			if err := create(t, newCreatorRunner(t, husker)); err == nil {
				t.Fatal("creation whose VM refused every request succeeded")
			}
			created, deleted := husker.snapshot()
			if len(created) != 1 || !equalStrings(deleted, created) {
				t.Fatalf("created = %v, deleted = %v", created, deleted)
			}
		})
	}
}

func TestToolPreparationInspectsImagesWithinItsTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	runner, _ := newTestHuskerRunner(t, server, HuskerConfig{
		ToolBaseImage: "minimal-base", ToolBaseDigest: testDigest("minimal-base-image"), ToolPlatform: "linux-arm64",
		MisePath: "/opt/werkt/mise", MiseVersion: "2026.8.1", MiseDigest: testDigest("verified-mise-binary"),
		ToolPrepareTimeout: 100 * time.Millisecond,
	})

	done := make(chan error, 1)
	go func() {
		_, err := runner.PrepareManifest(context.Background(), domain.Manifest{Runtime: domain.Runtime{
			Language: "python", Tools: map[string]string{"python": "3.13.7"}, Command: []string{"python3"},
		}})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want the preparation timeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tool preparation waited past its timeout for an image inspection")
	}
}

func TestReapPriorAttemptsRemovesEveryEarlierAttemptsVM(t *testing.T) {
	var mu sync.Mutex
	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if request.Method != http.MethodDelete {
			http.Error(response, "unexpected", http.StatusBadRequest)
			return
		}
		name := strings.TrimPrefix(request.URL.Path, "/v1/vms/")
		deleted = append(deleted, name)
		if name == attemptVMName("run_reap", 2) {
			writeJSONStatus(t, response, http.StatusNotFound, map[string]string{"kind": "vm_not_found"})
			return
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	runner, _ := newTestHuskerRunner(t, server, HuskerConfig{})

	run := domain.RunnableRun{Run: domain.Run{ID: "run_reap", Attempt: 4}}
	if err := runner.ReapPriorAttempts(context.Background(), run); err != nil {
		t.Fatalf("reap = %v", err)
	}
	want := []string{attemptVMName("run_reap", 1), attemptVMName("run_reap", 2), attemptVMName("run_reap", 3)}
	if !equalStrings(deleted, want) {
		t.Fatalf("deleted = %v, want %v", deleted, want)
	}
	if want[2] != "werkt-"+executionIdentity(domain.RunnableRun{Run: domain.Run{ID: "run_reap", Attempt: 3}}) {
		t.Fatal("reaped name differs from the name the attempt ran under")
	}
}

func TestReapPriorAttemptsFailsWhenAnEarlierVMCannotBeRemoved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		writeJSONStatus(t, response, http.StatusInternalServerError, map[string]string{"kind": "internal"})
	}))
	defer server.Close()
	runner, _ := newTestHuskerRunner(t, server, HuskerConfig{})
	err := runner.ReapPriorAttempts(context.Background(), domain.RunnableRun{Run: domain.Run{ID: "run_reap", Attempt: 2}})
	if err == nil || !strings.Contains(err.Error(), attemptVMName("run_reap", 1)) {
		t.Fatalf("reap error = %v, want one naming the VM left behind", err)
	}
}

type reapingExecutor struct {
	calls    []string
	reapErr  error
	attempts []int
}

func (e *reapingExecutor) ReapPriorAttempts(_ context.Context, run domain.RunnableRun) error {
	e.calls = append(e.calls, "reap")
	e.attempts = append(e.attempts, run.Attempt)
	return e.reapErr
}

func (e *reapingExecutor) Execute(context.Context, domain.RunnableRun) (Result, error) {
	e.calls = append(e.calls, "execute")
	return Result{Output: []byte(`{}`)}, nil
}

type recordingVerifier struct {
	calls *[]string
}

func (v recordingVerifier) Verify(string, domain.ArtifactProvenance) error {
	*v.calls = append(*v.calls, "verify")
	return nil
}

func TestVerifyingExecutorReapsEarlierAttemptsBeforeAnythingElse(t *testing.T) {
	next := &reapingExecutor{}
	executor := NewVerifyingExecutor(next, recordingVerifier{calls: &next.calls})
	if _, err := executor.Execute(context.Background(), domain.RunnableRun{Run: domain.Run{ID: "run", Attempt: 3}}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"reap", "verify", "execute"}; !equalStrings(next.calls, want) {
		t.Fatalf("calls = %v, want %v", next.calls, want)
	}

	first := &reapingExecutor{}
	executor = NewVerifyingExecutor(first, recordingVerifier{calls: &first.calls})
	if _, err := executor.Execute(context.Background(), domain.RunnableRun{Run: domain.Run{ID: "run", Attempt: 1}}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"verify", "execute"}; !equalStrings(first.calls, want) {
		t.Fatalf("first attempt calls = %v, want %v", first.calls, want)
	}
}

func TestVerifyingExecutorRunsNothingWhileAnEarlierAttemptsVMRemains(t *testing.T) {
	next := &reapingExecutor{reapErr: errors.New("husker unavailable")}
	executor := NewVerifyingExecutor(next, recordingVerifier{calls: &next.calls})
	_, err := executor.Execute(context.Background(), domain.RunnableRun{Run: domain.Run{ID: "run", Attempt: 2}})
	if err == nil || !strings.Contains(err.Error(), "husker unavailable") {
		t.Fatalf("error = %v", err)
	}
	if want := []string{"reap"}; !equalStrings(next.calls, want) {
		t.Fatalf("calls = %v, want %v", next.calls, want)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalDurations(left, right []time.Duration) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
