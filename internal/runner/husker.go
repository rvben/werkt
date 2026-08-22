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
	"strings"
	"time"

	"github.com/rvben/werkt/internal/domain"
)

const (
	defaultProvisionTimeout  = 2 * time.Minute
	defaultCleanupTimeout    = 30 * time.Second
	defaultUploadChunkSize   = 512 * 1024
	defaultDownloadChunkSize = 512 * 1024
	defaultBuildTimeout      = 15 * time.Minute
	guestCommandGrace        = 30 * time.Second
	maxAutomationResultBytes = 16 * 1024 * 1024
)

type HuskerConfig struct {
	URL               string
	Token             string
	RootFS            string
	Kernel            string
	VCPUs             uint32
	MemoryMiB         uint32
	BuildNetwork      string
	BuildTimeout      time.Duration
	ProvisionTimeout  time.Duration
	CleanupTimeout    time.Duration
	UploadChunkSize   int
	DownloadChunkSize int
	HTTPClient        *http.Client
	Secrets           SecretResolver
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
	}, nil
}

func (r *HuskerRunner) Execute(parent context.Context, run domain.RunnableRun) (Result, error) {
	if len(run.Manifest.Runtime.Command) == 0 {
		return Result{}, errors.New("runtime command is empty")
	}
	rootFS := run.Manifest.Runtime.Image
	if rootFS == "" {
		rootFS = r.rootFS
	}
	if strings.TrimSpace(rootFS) == "" {
		return Result{}, errors.New("runtime.image or the husker rootfs fallback is required")
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
	vmName := "werkt-" + identity
	guestRoot := "/tmp/werkt-" + identity
	archivePath := guestRoot + ".tar.gz"
	workspacePath := guestRoot + "/work"
	eventPath := guestRoot + "/event.json"
	resultPath := guestRoot + "/result.json"
	runtimeTimeout := run.Manifest.Execution.TimeoutDuration()
	lifetime := r.provisionTimeout + runtimeTimeout + r.cleanupTimeout + guestCommandGrace

	provisionContext, cancelProvision := context.WithTimeout(parent, r.provisionTimeout)
	defer cancelProvision()
	network := runtimeNetwork(run.Manifest.Runtime)
	if err := r.createVM(provisionContext, vmName, "werkt/"+run.ID, rootFS, network, run.Manifest.Runtime.Egress, lifetime); err != nil {
		return Result{}, fmt.Errorf("create husker VM: %w", err)
	}
	defer r.cleanupVM(vmName)

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
	cancelProvision()

	executionContext, cancelExecution := context.WithTimeout(parent, runtimeTimeout+guestCommandGrace)
	defer cancelExecution()
	command := run.Manifest.Runtime.Command
	response, executeErr := r.exec(executionContext, vmName, execRequest{
		Command:    command[0],
		Args:       command[1:],
		WorkingDir: workspacePath,
		Environment: runtimeEnvironmentMap(
			run,
			eventPath,
			resultPath,
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
	return Result{Output: resultJSON, Logs: logs}, nil
}

func runtimeNetwork(runtime domain.Runtime) string {
	if len(runtime.Egress) > 0 {
		return "filtered"
	}
	return "none"
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

func (r *HuskerRunner) cleanupVM(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), r.cleanupTimeout)
	defer cancel()
	err := r.doJSON(ctx, http.MethodDelete, vmPath(name), nil, http.StatusNoContent, nil)
	var apiErr *huskerAPIError
	if err != nil && !(errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound) {
		slog.Warn("husker VM cleanup failed; durable expiration will retry cleanup", "vm", name, "error", err)
	}
}

func (r *HuskerRunner) doJSON(ctx context.Context, method, path string, requestBody any, expectedStatus int, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, r.baseURL+path, body)
	if err != nil {
		return err
	}
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if r.token != "" {
		request.Header.Set("Authorization", "Bearer "+r.token)
	}
	response, err := r.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
		apiErr := &huskerAPIError{StatusCode: response.StatusCode, Message: strings.TrimSpace(string(payload))}
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
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", run.ID, run.Attempt)))
	return hex.EncodeToString(digest[:8])
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

func runtimeEnvironmentMap(run domain.RunnableRun, eventPath, resultPath string, values map[string]string) map[string]string {
	values["WERKT_AUTOMATION_ID"] = run.AutomationID
	values["WERKT_REVISION_ID"] = run.RevisionID
	values["WERKT_RUN_ID"] = run.ID
	values["WERKT_EVENT_PATH"] = eventPath
	values["WERKT_RESULT_PATH"] = resultPath
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
}

func (e *huskerAPIError) Error() string {
	if e.Kind != "" {
		return fmt.Sprintf("husker API returned %d (%s): %s", e.StatusCode, e.Kind, e.Message)
	}
	return fmt.Sprintf("husker API returned %d: %s", e.StatusCode, e.Message)
}
