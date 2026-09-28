package runner

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rvben/werkt/internal/domain"
	"github.com/rvben/werkt/internal/provenance"
)

const (
	defaultProvisionTimeout = 2 * time.Minute
	defaultCleanupTimeout   = 30 * time.Second
	// Husker permits 1 MiB decoded writes and 120 sensitive writes/minute.
	// Using the full bounded write avoids exhausting that limit while staging a
	// checksum-pinned installer such as mise; base64 still fits below Husker's
	// 2 MiB HTTP request ceiling.
	defaultUploadChunkSize   = 1024 * 1024
	defaultDownloadChunkSize = 512 * 1024
	defaultBuildTimeout      = 15 * time.Minute
	guestCommandGrace        = 30 * time.Second
	maxAutomationResultBytes = 16 * 1024 * 1024
	// A husker request is sent at most this many times. Retries cover a rate
	// limit on any request and a dropped connection or unavailable gateway on
	// a request that is safe to repeat.
	maxHuskerAttempts = 5
	// Husker's rate limit refills per minute, so it never asks for a longer
	// wait. A longer Retry-After is returned as the error rather than slept.
	maxHuskerRetryAfter = time.Minute
	// defaultRequestCeiling bounds a husker request whose caller set no
	// deadline, so an unresponsive husker cannot hold a worker forever.
	defaultRequestCeiling = 5 * time.Minute
)

type HuskerConfig struct {
	URL                     string
	Token                   string
	RootFS                  string
	Kernel                  string
	VCPUs                   uint32
	MemoryMiB               uint32
	BuildNetwork            string
	BuildTimeout            time.Duration
	ProvisionTimeout        time.Duration
	CleanupTimeout          time.Duration
	UploadChunkSize         int
	DownloadChunkSize       int
	HTTPClient              *http.Client
	Secrets                 SecretResolver
	ToolBaseImage           string
	ToolBaseDigest          string
	ToolPlatform            string
	MisePath                string
	MiseVersion             string
	MiseDigest              string
	ToolPrepareTimeout      time.Duration
	MaxAutomationStateBytes int
}

type HuskerRunner struct {
	baseURL           string
	token             string
	rootFS            string
	kernel            string
	vcpus             uint32
	memoryMiB         uint32
	buildNetwork      string
	buildTimeout      time.Duration
	provisionTimeout  time.Duration
	cleanupTimeout    time.Duration
	uploadChunkSize   int
	downloadChunkSize int
	client            *http.Client
	secrets           SecretResolver
	maxStateBytes     int
	imageMu           sync.Mutex
	resolvedImages    map[string]string
	toolConfig        toolEnvironmentConfig
	requestCeiling    time.Duration
	sleep             func(context.Context, time.Duration) error
}

func NewHuskerRunner(config HuskerConfig) (*HuskerRunner, error) {
	parsed, err := url.Parse(config.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("husker URL must be an absolute http or https URL")
	}
	if config.BuildNetwork == "" {
		config.BuildNetwork = "nat"
	}
	if config.BuildNetwork != "none" && config.BuildNetwork != "nat" && config.BuildNetwork != "bridged" {
		return nil, errors.New("husker build network must be none, nat, or bridged")
	}
	if config.BuildTimeout <= 0 {
		config.BuildTimeout = defaultBuildTimeout
	}
	if config.ProvisionTimeout <= 0 {
		config.ProvisionTimeout = defaultProvisionTimeout
	}
	if config.CleanupTimeout <= 0 {
		config.CleanupTimeout = defaultCleanupTimeout
	}
	if config.UploadChunkSize <= 0 {
		config.UploadChunkSize = defaultUploadChunkSize
	}
	if config.UploadChunkSize > 1024*1024 {
		return nil, errors.New("husker upload chunk size cannot exceed 1 MiB")
	}
	if config.DownloadChunkSize <= 0 {
		config.DownloadChunkSize = defaultDownloadChunkSize
	}
	if config.DownloadChunkSize > 1024*1024 {
		return nil, errors.New("husker download chunk size cannot exceed 1 MiB")
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{}
	}
	return &HuskerRunner{
		baseURL:           strings.TrimRight(config.URL, "/"),
		token:             config.Token,
		rootFS:            config.RootFS,
		kernel:            config.Kernel,
		vcpus:             config.VCPUs,
		memoryMiB:         config.MemoryMiB,
		buildNetwork:      config.BuildNetwork,
		buildTimeout:      config.BuildTimeout,
		provisionTimeout:  config.ProvisionTimeout,
		cleanupTimeout:    config.CleanupTimeout,
		uploadChunkSize:   config.UploadChunkSize,
		downloadChunkSize: config.DownloadChunkSize,
		client:            config.HTTPClient,
		secrets:           config.Secrets,
		maxStateBytes:     automationStateLimit(config.MaxAutomationStateBytes),
		resolvedImages:    make(map[string]string),
		toolConfig: toolEnvironmentConfig{
			BaseImage: config.ToolBaseImage, BaseDigest: config.ToolBaseDigest,
			Platform: config.ToolPlatform, MisePath: config.MisePath,
			MiseVersion: config.MiseVersion, MiseDigest: config.MiseDigest,
			PrepareTimeout: config.ToolPrepareTimeout,
		},
		requestCeiling: defaultRequestCeiling,
		sleep:          sleepContext,
	}, nil
}

func (r *HuskerRunner) PinImages(value domain.Manifest) (domain.Manifest, error) {
	if len(value.Runtime.Tools) > 0 {
		resolved, err := resolveToolEnvironment(value.Runtime.Tools, r.toolConfig)
		if err != nil {
			return domain.Manifest{}, err
		}
		value.Runtime.ResolvedTools = &resolved
		return value, nil
	}
	return provenance.PinHuskerImages(value)
}

func (r *HuskerRunner) Execute(parent context.Context, run domain.RunnableRun) (Result, error) {
	if len(run.Manifest.Runtime.Command) == 0 {
		return Result{}, errors.New("runtime command is empty")
	}
	toolEnvironment := run.Manifest.Runtime.ResolvedTools
	rootFS := ""
	if len(run.Manifest.Runtime.Tools) > 0 {
		if toolEnvironment == nil {
			return Result{}, errors.New("runtime.tools revision is missing its resolved environment")
		}
		rootFS = toolEnvironment.Image
	} else {
		if _, err := provenance.PinHuskerImages(run.Manifest); err != nil {
			return Result{}, err
		}
		rootFS = run.Manifest.Runtime.Image
		if rootFS == "" {
			rootFS = r.rootFS
		}
		if strings.TrimSpace(rootFS) == "" {
			return Result{}, errors.New("runtime.image or the husker rootfs fallback is required")
		}
	}
	resolved, err := resolveRuntimeEnvironment(parent, r.secrets, run.Manifest.Runtime)
	if err != nil {
		return Result{}, err
	}
	redactor := newLogRedactor(resolved.secrets)
	artifact, err := archiveDirectory(run.ArtifactPath)
	if err != nil {
		return Result{}, fmt.Errorf("package automation artifact: %w", err)
	}
	eventJSON, err := json.Marshal(run.Event)
	if err != nil {
		return Result{}, fmt.Errorf("encode event: %w", err)
	}

	identity := executionIdentity(run)
	vmName := attemptVMName(run.ID, run.Attempt)
	guestRoot := "/tmp/werkt-" + identity
	archivePath := guestRoot + ".tar.gz"
	workspacePath := guestRoot + "/work"
	eventPath := guestRoot + "/event.json"
	resultPath := guestRoot + "/result.json"
	controlPath := guestRoot + "/control.json"
	statePath := ""
	runtimeTimeout := run.Manifest.Execution.TimeoutDuration()
	lifetime := r.provisionTimeout + runtimeTimeout + r.cleanupTimeout + guestCommandGrace

	provisionContext, cancelProvision := context.WithTimeout(parent, r.provisionTimeout)
	defer cancelProvision()
	if toolEnvironment != nil {
		if err := r.verifyToolImage(provisionContext, toolEnvironment); err != nil {
			return Result{}, err
		}
	} else {
		rootFS, err = r.ensureOCIImage(provisionContext, rootFS)
		if err != nil {
			return Result{}, fmt.Errorf("prepare husker runtime image: %w", err)
		}
	}
	network := runtimeNetwork(run.Manifest.Runtime)
	err = r.createVM(provisionContext, vmName, "werkt/"+run.ID, rootFS, network, run.Manifest.Runtime.Egress, lifetime)
	if mayOwnVM(err) {
		defer r.cleanupVM(vmName) //nolint:errcheck // cleanupVM logs its own failure; husker's expiry is the backstop
	}
	if err != nil {
		return Result{}, fmt.Errorf("create husker VM: %w", err)
	}

	if err := r.waitReady(provisionContext, vmName); err != nil {
		return Result{}, fmt.Errorf("wait for husker VM: %w", err)
	}
	if err := r.uploadFile(provisionContext, vmName, archivePath, artifact, 0o600); err != nil {
		return Result{}, fmt.Errorf("upload automation artifact: %w", err)
	}
	if _, err := r.exec(provisionContext, vmName, execRequest{
		Command: "/bin/mkdir",
		Args:    []string{"-p", workspacePath},
		Timeout: 30,
	}); err != nil {
		return Result{}, fmt.Errorf("prepare guest workspace: %w", err)
	}
	if _, err := r.exec(provisionContext, vmName, execRequest{
		Command: "/bin/tar",
		Args:    []string{"-xzf", archivePath, "-C", workspacePath},
		Timeout: 30,
	}); err != nil {
		return Result{}, fmt.Errorf("extract automation artifact: %w", err)
	}
	if err := r.uploadFile(provisionContext, vmName, eventPath, eventJSON, 0o600); err != nil {
		return Result{}, fmt.Errorf("upload event: %w", err)
	}
	if err := r.uploadFile(provisionContext, vmName, resultPath, []byte("{}"), 0o600); err != nil {
		return Result{}, fmt.Errorf("initialize result: %w", err)
	}
	if err := r.uploadFile(provisionContext, vmName, controlPath, []byte("{}"), 0o600); err != nil {
		return Result{}, fmt.Errorf("initialize run control: %w", err)
	}
	if run.Manifest.Execution.State.Enabled {
		statePath = guestRoot + "/state.json"
		state := run.State
		if len(state) == 0 {
			state = json.RawMessage(`{}`)
		}
		if _, err := validateAutomationState(state, r.maxStateBytes); err != nil {
			return Result{}, fmt.Errorf("initialize automation state: %w", err)
		}
		if err := r.uploadFile(provisionContext, vmName, statePath, state, 0o600); err != nil {
			return Result{}, fmt.Errorf("upload automation state: %w", err)
		}
	}
	cancelProvision()

	executionContext, cancelExecution := context.WithTimeout(parent, runtimeTimeout+guestCommandGrace)
	defer cancelExecution()
	command := run.Manifest.Runtime.Command
	resolved.values["WERKT_STATE_MAX_BYTES"] = fmt.Sprint(r.maxStateBytes)
	response, executeErr := r.exec(executionContext, vmName, execRequest{
		Command:    command[0],
		Args:       command[1:],
		WorkingDir: workspacePath,
		Environment: runtimeEnvironmentMap(
			run,
			eventPath,
			resultPath,
			controlPath,
			statePath,
			resolved.values,
		),
		Timeout: durationSeconds(runtimeTimeout),
	})
	logs := formatLogs(redactor.Redact(response.Stdout), redactor.Redact(response.Stderr))
	if executeErr != nil {
		executeErr = redactor.Error(executeErr)
		if executionContext.Err() != nil {
			return Result{Logs: logs}, fmt.Errorf("automation exceeded timeout %s: %w", runtimeTimeout, executionContext.Err())
		}
		return Result{Logs: logs}, fmt.Errorf("execute automation in husker VM: %w", executeErr)
	}
	if response.ExitCode == 124 {
		return Result{Logs: logs}, fmt.Errorf("automation exceeded timeout %s", runtimeTimeout)
	}
	if response.ExitCode != 0 {
		return Result{Logs: logs}, fmt.Errorf("automation process exited with code %d", response.ExitCode)
	}

	resultJSON, err := r.readFile(executionContext, vmName, resultPath)
	if err != nil {
		return Result{Logs: logs}, fmt.Errorf("read automation result: %w", err)
	}
	if !json.Valid(resultJSON) {
		return Result{Logs: logs}, errors.New("automation result is not valid JSON")
	}
	result := Result{Output: resultJSON, Logs: logs}
	controlJSON, err := r.readFileLimited(executionContext, vmName, controlPath, MaxRunControlBytes)
	if err != nil {
		return Result{Logs: logs}, fmt.Errorf("read run control: %w", err)
	}
	result.Control, err = validateRunControl(controlJSON, time.Now().UTC())
	if err != nil {
		return Result{Logs: logs}, err
	}
	if statePath != "" {
		stateJSON, err := r.readFileLimited(executionContext, vmName, statePath, uint64(r.maxStateBytes))
		if err != nil {
			return Result{Logs: logs}, fmt.Errorf("read automation state: %w", err)
		}
		result.State, err = validateAutomationState(stateJSON, r.maxStateBytes)
		if err != nil {
			return Result{Logs: logs}, err
		}
	}
	return result, nil
}

func runtimeNetwork(runtime domain.Runtime) string {
	if len(runtime.Egress) > 0 {
		return "filtered"
	}
	return "none"
}

// ensureOCIImage imports a digest-pinned OCI reference under a bounded,
// deterministic catalog name before VM creation. Husker otherwise derives a
// catalog name directly from the reference, which can exceed its 64-character
// resource-name limit for immutable references.
func (r *HuskerRunner) ensureOCIImage(ctx context.Context, reference string) (string, error) {
	r.imageMu.Lock()
	defer r.imageMu.Unlock()

	if name, ok := r.resolvedImages[reference]; ok {
		return name, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	name := huskerImageName(reference)
	var existing imageResponse
	err := r.doJSON(ctx, http.MethodGet, "/v1/images/"+url.PathEscape(name), nil, http.StatusOK, &existing)
	if err == nil {
		if err := verifyHuskerImage(existing, name, reference); err != nil {
			return "", err
		}
		r.resolvedImages[reference] = name
		return name, nil
	}
	var apiErr *huskerAPIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		return "", err
	}

	request := importOCIImageRequest{Name: name, Reference: reference}
	var imported imageResponse
	err = r.doJSON(ctx, http.MethodPost, "/v1/images/import-oci", request, http.StatusCreated, &imported)
	if err != nil {
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusConflict || apiErr.Kind != "image_already_exists" {
			return "", err
		}
		if err := r.doJSON(ctx, http.MethodGet, "/v1/images/"+url.PathEscape(name), nil, http.StatusOK, &imported); err != nil {
			return "", err
		}
	}
	if err := verifyHuskerImage(imported, name, reference); err != nil {
		return "", err
	}
	r.resolvedImages[reference] = name
	return name, nil
}

func huskerImageName(reference string) string {
	digest := sha256.Sum256([]byte(reference))
	return "werkt-oci-" + hex.EncodeToString(digest[:24])
}

func verifyHuskerImage(image imageResponse, name, reference string) error {
	if image.Name != name {
		return fmt.Errorf("husker returned image %q while resolving %q", image.Name, name)
	}
	wantSource := "oci://" + strings.TrimPrefix(reference, "oci://")
	if image.SourcePath != wantSource {
		return fmt.Errorf("husker image %q resolves to unexpected source %q", name, image.SourcePath)
	}
	return nil
}

func (r *HuskerRunner) createVM(ctx context.Context, name, owner, rootFS, network string, egress []domain.EgressRule, lifetime time.Duration) error {
	policy := make([]egressRuleRequest, len(egress))
	for index, rule := range egress {
		policy[index] = egressRuleRequest{
			Host:     rule.Host,
			Port:     rule.Port,
			Protocol: rule.EffectiveProtocol(),
		}
	}
	request := createVMRequest{
		Name:             name,
		RootFSPath:       rootFS,
		KernelPath:       r.kernel,
		VCPUs:            r.vcpus,
		MemoryMiB:        r.memoryMiB,
		Network:          network,
		Egress:           policy,
		ExpiresAfterSecs: durationSeconds(lifetime),
		Owner:            owner,
	}
	return r.doJSON(ctx, http.MethodPost, "/v1/vms", request, http.StatusCreated, nil)
}

// mayOwnVM reports whether a VM may exist under the name a create request
// asked for. A create that failed after reaching husker, or whose response was
// lost, can still have made the VM, so only a name husker reports as already
// taken is known not to be ours to delete.
func mayOwnVM(createErr error) bool {
	var apiErr *huskerAPIError
	return !errors.As(createErr, &apiErr) || apiErr.StatusCode != http.StatusConflict || apiErr.Kind != "vm_already_exists"
}

func (r *HuskerRunner) waitReady(ctx context.Context, name string) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		var response readyResponse
		err := r.doJSON(ctx, http.MethodGet, vmPath(name)+"/ready", nil, http.StatusOK, &response)
		if err != nil {
			return err
		}
		if response.Ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *HuskerRunner) uploadFile(ctx context.Context, vmName, path string, data []byte, mode uint32) error {
	if len(data) == 0 {
		return r.writeChunk(ctx, vmName, path, nil, mode, false)
	}
	for offset := 0; offset < len(data); offset += r.uploadChunkSize {
		end := min(offset+r.uploadChunkSize, len(data))
		if err := r.writeChunk(ctx, vmName, path, data[offset:end], mode, offset > 0); err != nil {
			return err
		}
	}
	return nil
}

func (r *HuskerRunner) writeChunk(ctx context.Context, vmName, path string, data []byte, mode uint32, appendData bool) error {
	request := writeFileRequest{
		Path:   path,
		Data:   base64.StdEncoding.EncodeToString(data),
		Mode:   mode,
		Append: appendData,
	}
	return r.doJSON(ctx, http.MethodPost, vmPath(vmName)+"/files/write", request, http.StatusOK, nil)
}

func (r *HuskerRunner) readFile(ctx context.Context, vmName, path string) ([]byte, error) {
	return r.readFileLimited(ctx, vmName, path, maxAutomationResultBytes)
}

func (r *HuskerRunner) readFileLimited(ctx context.Context, vmName, path string, maxBytes uint64) ([]byte, error) {
	var result []byte
	var expectedSize *uint64
	var expectedModified *uint64
	for {
		offset := uint64(len(result))
		var response readFileResponse
		if err := r.doJSON(ctx, http.MethodPost, vmPath(vmName)+"/files/read", map[string]any{
			"path":   path,
			"offset": offset,
			"len":    r.downloadChunkSize,
		}, http.StatusOK, &response); err != nil {
			return nil, err
		}
		chunk, err := base64.StdEncoding.DecodeString(response.Data)
		if err != nil {
			return nil, fmt.Errorf("decode husker file response: %w", err)
		}
		if response.Size != uint64(len(chunk)) {
			return nil, fmt.Errorf("husker file response reported %d bytes but contained %d", response.Size, len(chunk))
		}
		if response.TotalSize == nil {
			if uint64(len(chunk)) > maxBytes {
				return nil, fmt.Errorf("guest file exceeds the %d-byte download limit", maxBytes)
			}
			return append(result, chunk...), nil
		}
		if *response.TotalSize > maxBytes {
			return nil, fmt.Errorf("guest file is %d bytes, exceeding the %d-byte download limit", *response.TotalSize, maxBytes)
		}
		if expectedSize == nil {
			total := *response.TotalSize
			expectedSize = &total
			if response.ModifiedNanos != nil {
				modified := *response.ModifiedNanos
				expectedModified = &modified
			}
		} else if *expectedSize != *response.TotalSize || !sameOptionalUint64(expectedModified, response.ModifiedNanos) {
			return nil, errors.New("guest file changed while it was being downloaded")
		}
		result = append(result, chunk...)
		if uint64(len(result)) > *expectedSize {
			return nil, fmt.Errorf("guest returned more than the reported file size of %d bytes", *expectedSize)
		}
		if uint64(len(result)) == *expectedSize {
			return result, nil
		}
		if len(chunk) == 0 {
			return nil, fmt.Errorf("guest returned no data at offset %d of a %d-byte file", offset, *expectedSize)
		}
	}
}

func sameOptionalUint64(left, right *uint64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (r *HuskerRunner) exec(ctx context.Context, vmName string, request execRequest) (execResponse, error) {
	var response execResponse
	err := r.doJSON(ctx, http.MethodPost, vmPath(vmName)+"/exec", request, http.StatusOK, &response)
	return response, err
}

// cleanupVM removes a VM this runner created. It runs on a fresh context so a
// cancelled run still removes its VM, and it logs its own failure because it
// usually runs deferred.
func (r *HuskerRunner) cleanupVM(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), r.cleanupTimeout)
	defer cancel()
	if _, err := r.deleteVM(ctx, name); err != nil {
		slog.Error("husker VM cleanup failed; husker's expiry will remove it", "vm", name, "error", err)
		return err
	}
	return nil
}

// deleteVM removes a VM and reports whether one existed. A VM that is already
// gone is not an error.
func (r *HuskerRunner) deleteVM(ctx context.Context, name string) (bool, error) {
	err := r.doJSON(ctx, http.MethodDelete, vmPath(name), nil, http.StatusNoContent, nil)
	var apiErr *huskerAPIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return false, nil
	}
	return err == nil, err
}

// ReapPriorAttempts removes the VMs of every earlier attempt of a run. An
// attempt whose worker stopped without cleaning up, or lost its lease while
// still executing, leaves its VM behind, and a live one would go on running
// the automation beside this attempt. The attempt proceeds only once every
// earlier VM is confirmed gone.
func (r *HuskerRunner) ReapPriorAttempts(parent context.Context, run domain.RunnableRun) error {
	ctx, cancel := context.WithTimeout(parent, r.cleanupTimeout)
	defer cancel()
	for attempt := 1; attempt < run.Attempt; attempt++ {
		name := attemptVMName(run.ID, attempt)
		removed, err := r.deleteVM(ctx, name)
		if err != nil {
			return fmt.Errorf("remove VM %s left by attempt %d: %w", name, attempt, err)
		}
		if removed {
			slog.Warn("removed husker VM left by an earlier attempt", "run", run.ID, "attempt", run.Attempt, "vm", name, "vm_attempt", attempt)
		}
	}
	return nil
}

// doJSON sends one husker API call, retrying it within the caller's deadline
// when that is safe. Husker rejects a rate-limited request before handling it,
// so a 429 is retried for every method after the wait husker announces. A
// dropped connection or an unavailable gateway may hide a request husker
// already acted on, so those are retried only for GET and DELETE, which can be
// repeated without effect.
func (r *HuskerRunner) doJSON(ctx context.Context, method, path string, requestBody any, expectedStatus int, responseBody any) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.requestCeiling)
		defer cancel()
	}
	var encoded []byte
	if requestBody != nil {
		var err error
		if encoded, err = json.Marshal(requestBody); err != nil {
			return err
		}
	}
	for attempt := 1; ; attempt++ {
		err := r.sendJSON(ctx, method, path, encoded, expectedStatus, responseBody)
		delay, retry := r.retryDelay(ctx, method, attempt, err)
		if !retry {
			return withContextErr(ctx, err)
		}
		slog.Warn("retrying husker request", "method", method, "path", path, "attempt", attempt, "delay", delay, "error", err)
		if sleepErr := r.sleep(ctx, delay); sleepErr != nil {
			return withContextErr(ctx, err)
		}
	}
}

// withContextErr adds the context's error to a request error when the context
// ended first, so a caller can tell a request cut short by cancellation from
// one husker refused, even when the last response was a refusal.
func withContextErr(ctx context.Context, err error) error {
	contextErr := ctx.Err()
	if err == nil || contextErr == nil || errors.Is(err, contextErr) {
		return err
	}
	return errors.Join(err, contextErr)
}

func (r *HuskerRunner) retryDelay(ctx context.Context, method string, attempt int, err error) (time.Duration, bool) {
	if err == nil || attempt >= maxHuskerAttempts || ctx.Err() != nil {
		return 0, false
	}
	backoff := time.Duration(1<<(attempt-1)) * time.Second
	delay := time.Duration(0)
	var apiErr *huskerAPIError
	var transportErr *huskerTransportError
	idempotent := method == http.MethodGet || method == http.MethodDelete
	switch {
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests:
		delay = backoff
		if apiErr.RetryAfter > 0 {
			delay = apiErr.RetryAfter
		}
		if delay > maxHuskerRetryAfter {
			return 0, false
		}
	case errors.As(err, &apiErr) && idempotent && (apiErr.StatusCode == http.StatusBadGateway ||
		apiErr.StatusCode == http.StatusServiceUnavailable || apiErr.StatusCode == http.StatusGatewayTimeout):
		delay = backoff
	case errors.As(err, &transportErr) && idempotent:
		delay = backoff
	default:
		return 0, false
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= delay {
		return 0, false
	}
	return delay, true
}

func (r *HuskerRunner) sendJSON(ctx context.Context, method, path string, encoded []byte, expectedStatus int, responseBody any) error {
	var body io.Reader
	if encoded != nil {
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, r.baseURL+path, body)
	if err != nil {
		return err
	}
	if encoded != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if r.token != "" {
		request.Header.Set("Authorization", "Bearer "+r.token)
	}
	response, err := r.client.Do(request)
	if err != nil {
		return &huskerTransportError{err: err}
	}
	defer response.Body.Close() //nolint:errcheck // closing a read-only response body cannot change the result
	if response.StatusCode != expectedStatus {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
		apiErr := &huskerAPIError{
			StatusCode: response.StatusCode,
			Message:    strings.TrimSpace(string(payload)),
			RetryAfter: parseRetryAfter(response.Header.Get("Retry-After")),
		}
		var envelope struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
		}
		if json.Unmarshal(payload, &envelope) == nil {
			apiErr.Kind = envelope.Kind
			if envelope.Message != "" {
				apiErr.Message = envelope.Message
			}
		}
		return apiErr
	}
	if responseBody == nil || expectedStatus == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(responseBody); err != nil {
		return fmt.Errorf("decode husker response: %w", err)
	}
	return nil
}

// parseRetryAfter reads the delay-seconds form husker sends. Anything else
// yields zero, which falls back to exponential backoff.
func parseRetryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func archiveDirectory(directory string) ([]byte, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	tarWriter := tar.NewWriter(gzipWriter)
	walkErr := filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == absolute {
			return nil
		}
		relative, err := filepath.Rel(absolute, path)
		if err != nil {
			return err
		}
		first, _, _ := strings.Cut(filepath.ToSlash(relative), "/")
		if first == provenance.MetadataDirectory {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return fmt.Errorf("reserved provenance metadata path %q is not a directory", provenance.MetadataDirectory)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact contains symbolic link: %s", relative)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("artifact contains unsupported entry: %s", relative)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		if info.IsDir() {
			header.Name += "/"
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tarWriter, file)
		closeErr := file.Close()
		return errors.Join(copyErr, closeErr)
	})
	closeErr := errors.Join(tarWriter.Close(), gzipWriter.Close())
	if walkErr != nil {
		return nil, walkErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return compressed.Bytes(), nil
}

func executionIdentity(run domain.RunnableRun) string {
	return attemptIdentity(run.ID, run.Attempt)
}

func attemptIdentity(runID string, attempt int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", runID, attempt)))
	return hex.EncodeToString(digest[:8])
}

// attemptVMName is the name of the VM an attempt of a run executes in.
func attemptVMName(runID string, attempt int) string {
	return "werkt-" + attemptIdentity(runID, attempt)
}

func vmPath(name string) string {
	return "/v1/vms/" + url.PathEscape(name)
}

func durationSeconds(value time.Duration) uint64 {
	if value <= 0 {
		return 1
	}
	return uint64((value + time.Second - 1) / time.Second)
}

func runtimeEnvironmentMap(run domain.RunnableRun, eventPath, resultPath, controlPath, statePath string, values map[string]string) map[string]string {
	values["WERKT_AUTOMATION_ID"] = run.AutomationID
	values["WERKT_REVISION_ID"] = run.RevisionID
	values["WERKT_RUN_ID"] = run.ID
	values["WERKT_EVENT_PATH"] = eventPath
	values["WERKT_RESULT_PATH"] = resultPath
	values["WERKT_CONTROL_PATH"] = controlPath
	if statePath != "" {
		values["WERKT_STATE_PATH"] = statePath
		values["WERKT_STATE_VERSION"] = fmt.Sprintf("%d", run.StateVersion)
	}
	return values
}

type createVMRequest struct {
	Name             string              `json:"name"`
	RootFSPath       string              `json:"rootfs_path"`
	KernelPath       string              `json:"kernel_path,omitempty"`
	VCPUs            uint32              `json:"vcpu_count,omitempty"`
	MemoryMiB        uint32              `json:"mem_size_mib,omitempty"`
	Network          string              `json:"network"`
	Egress           []egressRuleRequest `json:"egress,omitempty"`
	ExpiresAfterSecs uint64              `json:"expires_after_secs"`
	Owner            string              `json:"owner"`
}

type importOCIImageRequest struct {
	Name      string `json:"name"`
	Reference string `json:"reference"`
}

type imageResponse struct {
	Name          string `json:"name"`
	SourcePath    string `json:"source_path"`
	Kind          string `json:"kind"`
	ContentDigest string `json:"content_digest"`
	ParentImage   string `json:"parent_image"`
}

type egressRuleRequest struct {
	Host     string `json:"host"`
	Port     uint16 `json:"port"`
	Protocol string `json:"protocol"`
}

type readyResponse struct {
	Ready bool `json:"ready"`
}

type writeFileRequest struct {
	Path   string `json:"path"`
	Data   string `json:"data"`
	Mode   uint32 `json:"mode"`
	Append bool   `json:"append"`
}

type readFileResponse struct {
	Data          string  `json:"data"`
	Size          uint64  `json:"size"`
	TotalSize     *uint64 `json:"total_size"`
	ModifiedNanos *uint64 `json:"modified_nanos"`
}

type execRequest struct {
	Command     string            `json:"command"`
	Args        []string          `json:"args,omitempty"`
	WorkingDir  string            `json:"working_dir,omitempty"`
	Environment map[string]string `json:"env,omitempty"`
	Timeout     uint64            `json:"timeout_secs,omitempty"`
}

type execResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

type huskerAPIError struct {
	StatusCode int
	Kind       string
	Message    string
	RetryAfter time.Duration
}

func (e *huskerAPIError) Error() string {
	if e.Kind != "" {
		return fmt.Sprintf("husker API returned %d (%s): %s", e.StatusCode, e.Kind, e.Message)
	}
	return fmt.Sprintf("husker API returned %d: %s", e.StatusCode, e.Message)
}

// huskerTransportError is a request that got no HTTP response at all, so
// whether husker acted on it is unknown.
type huskerTransportError struct {
	err error
}

func (e *huskerTransportError) Error() string { return e.err.Error() }

func (e *huskerTransportError) Unwrap() error { return e.err }
