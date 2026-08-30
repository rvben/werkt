package runner

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

type testSecretResolver map[string]string

func (r testSecretResolver) Resolve(_ context.Context, names []string) (map[string]string, error) {
	values := make(map[string]string, len(names))
	for _, name := range names {
		value, ok := r[name]
		if !ok {
			return nil, fmt.Errorf("secret %s not found", name)
		}
		values[name] = value
	}
	return values, nil
}

func serveTestHuskerImageImport(t *testing.T, response http.ResponseWriter, request *http.Request, imported *importOCIImageRequest) bool {
	t.Helper()
	switch {
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/images/werkt-oci-"):
		writeJSONStatus(t, response, http.StatusNotFound, map[string]string{
			"kind":    "image_not_found",
			"message": "image not found",
		})
		return true
	case request.Method == http.MethodPost && request.URL.Path == "/v1/images/import-oci":
		var body importOCIImageRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode image import: %v", err)
		}
		if imported != nil {
			*imported = body
		}
		writeJSONStatus(t, response, http.StatusCreated, imageResponse{
			Name:       body.Name,
			SourcePath: "oci://" + strings.TrimPrefix(body.Reference, "oci://"),
		})
		return true
	default:
		return false
	}
}

func TestHuskerRunnerExecutesLanguageNeutralContractAndCleansUp(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "main.py"), []byte("print('hello')\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	files := map[string][]byte{}
	deleted := false
	createNetwork := ""
	var createEgress []egressRuleRequest
	createRootFS := ""
	var importedImage importOCIImageRequest
	createOwner := ""
	createLifetime := uint64(0)
	runtimeSeen := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(response, "missing token", http.StatusUnauthorized)
			return
		}
		if serveTestHuskerImageImport(t, response, request, &importedImage) {
			return
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/vms":
			var body createVMRequest
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode create: %v", err)
			}
			createNetwork = body.Network
			createEgress = body.Egress
			createRootFS = body.RootFSPath
			createOwner = body.Owner
			createLifetime = body.ExpiresAfterSecs
			response.WriteHeader(http.StatusCreated)
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/ready"):
			writeJSON(t, response, map[string]any{"ready": true})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/files/write"):
			var body writeFileRequest
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode write: %v", err)
			}
			data, err := base64.StdEncoding.DecodeString(body.Data)
			if err != nil {
				t.Errorf("decode file data: %v", err)
			}
			mu.Lock()
			if body.Append {
				files[body.Path] = append(files[body.Path], data...)
			} else {
				files[body.Path] = append([]byte(nil), data...)
			}
			mu.Unlock()
			writeJSON(t, response, map[string]any{"bytes_written": len(data)})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/exec"):
			var body execRequest
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode exec: %v", err)
			}
			if body.Command == "python3" {
				runtimeSeen = true
				if body.WorkingDir == "" || !strings.HasSuffix(body.WorkingDir, "/work") {
					t.Errorf("working dir = %q", body.WorkingDir)
				}
				if body.Environment["WERKT_RUN_ID"] != "run_test" {
					t.Errorf("WERKT_RUN_ID = %q", body.Environment["WERKT_RUN_ID"])
				}
				if body.Environment["CUSTOM"] != "value" {
					t.Errorf("CUSTOM = %q", body.Environment["CUSTOM"])
				}
				if body.Environment["SERVICE_TOKEN"] != "guest-secret-value" {
					t.Errorf("SERVICE_TOKEN was not resolved")
				}
				if body.Environment["WERKT_STATE_VERSION"] != "7" || body.Environment["WERKT_STATE_PATH"] == "" {
					t.Errorf("transactional state environment = %#v", body.Environment)
				}
			}
			writeJSON(t, response, execResponse{ExitCode: 0, Stdout: "running guest-secret-value\n"})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/files/read"):
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode read: %v", err)
			}
			contents := []byte(`{"ok":true}`)
			if strings.HasSuffix(fmt.Sprint(body["path"]), "/state.json") {
				contents = []byte(`{"count":2}`)
			} else if strings.HasSuffix(fmt.Sprint(body["path"]), "/control.json") {
				contents = []byte(`{}`)
			}
			writeJSON(t, response, map[string]any{
				"data": base64.StdEncoding.EncodeToString(contents),
				"size": len(contents),
			})
		case request.Method == http.MethodDelete && strings.HasPrefix(request.URL.Path, "/v1/vms/werkt-"):
			deleted = true
			response.WriteHeader(http.StatusNoContent)
		default:
			http.Error(response, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	executor, err := NewHuskerRunner(HuskerConfig{
		URL:              server.URL,
		Token:            "test-token",
		ProvisionTimeout: time.Second,
		CleanupTimeout:   time.Second,
		UploadChunkSize:  16,
		HTTPClient:       server.Client(),
		Secrets:          testSecretResolver{"ops/guest-token": "guest-secret-value"},
	})
	if err != nil {
		t.Fatal(err)
	}
	run := domain.RunnableRun{
		Run:          domain.Run{ID: "run_test", AutomationID: "example", RevisionID: "rev_test", Attempt: 2},
		ArtifactPath: directory,
		Manifest: domain.Manifest{
			Runtime: domain.Runtime{
				Language:    "python",
				Image:       "python@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Command:     []string{"python3", "main.py"},
				Environment: map[string]string{"CUSTOM": "value"},
				Secrets:     map[string]string{"SERVICE_TOKEN": "ops/guest-token"},
				Egress: []domain.EgressRule{
					{Host: "api.example.com", Port: 443},
					{Host: "metrics.example.com", Port: 9090, Protocol: "udp"},
				},
			},
			Execution: domain.Execution{Timeout: "5s", Concurrency: "forbid", State: domain.StatePolicy{Enabled: true}},
		},
		Event:        domain.EventEnvelope{ID: "evt_test", Data: json.RawMessage(`{"message":"hello"}`)},
		State:        json.RawMessage(`{"count":1}`),
		StateVersion: 7,
	}

	result, err := executor.Execute(context.Background(), run)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if string(result.Output) != `{"ok":true}` {
		t.Fatalf("output = %s", result.Output)
	}
	if result.Logs != "[stdout]\nrunning [REDACTED]\n" {
		t.Fatalf("logs = %q", result.Logs)
	}
	if string(result.State) != `{"count":2}` {
		t.Fatalf("state = %s", result.State)
	}
	if createNetwork != "filtered" {
		t.Fatalf("network = %q", createNetwork)
	}
	if len(createEgress) != 2 || createEgress[0].Protocol != "tcp" || createEgress[1].Protocol != "udp" {
		t.Fatalf("egress = %#v", createEgress)
	}
	if createRootFS != huskerImageName("python@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Fatalf("rootfs = %q", createRootFS)
	}
	if importedImage.Reference != "python@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || importedImage.Name != createRootFS {
		t.Fatalf("image import = %#v", importedImage)
	}
	if createOwner != "werkt/run_test" {
		t.Fatalf("owner = %q", createOwner)
	}
	if createLifetime < 35 {
		t.Fatalf("expiration lifetime = %d", createLifetime)
	}
	if !runtimeSeen {
		t.Fatal("runtime command was not executed")
	}
	if !deleted {
		t.Fatal("VM was not cleaned up")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(files) < 4 {
		t.Fatalf("uploaded files = %d, want at least 4", len(files))
	}
}

func TestRuntimeNetworkIsOfflineUnlessTheManifestDeclaresEgress(t *testing.T) {
	if got := runtimeNetwork(domain.Runtime{}); got != "none" {
		t.Fatalf("empty runtime network = %q", got)
	}
	if got := runtimeNetwork(domain.Runtime{Egress: []domain.EgressRule{{Host: "api.example.com", Port: 443}}}); got != "filtered" {
		t.Fatalf("policy runtime network = %q", got)
	}
}

func TestHuskerRunnerReusesExistingBoundedImageAlias(t *testing.T) {
	reference := "registry.example.com/team/very-long-runtime-name@sha256:" + strings.Repeat("a", 64)
	wantName := huskerImageName(reference)
	if len(wantName) > 64 {
		t.Fatalf("image name length = %d", len(wantName))
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		if request.Method != http.MethodGet || request.URL.Path != "/v1/images/"+wantName {
			http.Error(response, "unexpected request", http.StatusBadRequest)
			return
		}
		writeJSON(t, response, imageResponse{Name: wantName, SourcePath: "oci://" + reference})
	}))
	defer server.Close()

	runner, err := NewHuskerRunner(HuskerConfig{URL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		name, err := runner.ensureOCIImage(context.Background(), reference)
		if err != nil {
			t.Fatal(err)
		}
		if name != wantName {
			t.Fatalf("image name = %q, want %q", name, wantName)
		}
	}
	if requests != 1 {
		t.Fatalf("catalog requests = %d, want 1", requests)
	}
}

func TestHuskerRunnerHandlesConcurrentImageImportWinner(t *testing.T) {
	reference := "python@sha256:" + strings.Repeat("b", 64)
	wantName := huskerImageName(reference)
	gets := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/images/"+wantName:
			gets++
			if gets == 1 {
				writeJSONStatus(t, response, http.StatusNotFound, map[string]string{"kind": "image_not_found", "message": "image not found"})
				return
			}
			writeJSON(t, response, imageResponse{Name: wantName, SourcePath: "oci://" + reference})
		case request.Method == http.MethodPost && request.URL.Path == "/v1/images/import-oci":
			writeJSONStatus(t, response, http.StatusConflict, map[string]string{
				"kind":    "image_already_exists",
				"message": "image already exists",
			})
		default:
			http.Error(response, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	runner, err := NewHuskerRunner(HuskerConfig{URL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	name, err := runner.ensureOCIImage(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	if name != wantName || gets != 2 {
		t.Fatalf("image name = %q, gets = %d", name, gets)
	}
}

func TestHuskerRunnerRejectsMissingRuntimeSecretBeforeCreatingVM(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		called = true
		http.Error(response, "unexpected", http.StatusInternalServerError)
	}))
	defer server.Close()
	runner, err := NewHuskerRunner(HuskerConfig{URL: server.URL, HTTPClient: server.Client(), Secrets: testSecretResolver{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Execute(context.Background(), domain.RunnableRun{
		Run:          domain.Run{ID: "missing-secret", Attempt: 1},
		ArtifactPath: t.TempDir(),
		Manifest: domain.Manifest{Runtime: domain.Runtime{
			Image:   "alpine@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Command: []string{"true"},
			Secrets: map[string]string{"SERVICE_TOKEN": "ops/missing"},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "ops/missing") {
		t.Fatalf("Execute() error = %v", err)
	}
	if called {
		t.Fatal("Husker API was called before secret resolution failed")
	}
}

func TestHuskerRunnerCleansUpAfterAutomationFailure(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "run"), []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if serveTestHuskerImageImport(t, response, request, nil) {
			return
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/vms":
			response.WriteHeader(http.StatusCreated)
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/ready"):
			writeJSON(t, response, map[string]any{"ready": true})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/files/write"):
			writeJSON(t, response, map[string]any{"bytes_written": 1})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/exec"):
			var body execRequest
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body.Command == "./run" {
				writeJSON(t, response, execResponse{ExitCode: 7, Stderr: "failed\n"})
				return
			}
			writeJSON(t, response, execResponse{ExitCode: 0})
		case request.Method == http.MethodDelete:
			deleted = true
			response.WriteHeader(http.StatusNoContent)
		default:
			http.Error(response, "unexpected", http.StatusNotFound)
		}
	}))
	defer server.Close()

	executor, err := NewHuskerRunner(HuskerConfig{URL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Execute(context.Background(), domain.RunnableRun{
		Run:          domain.Run{ID: "failed", Attempt: 1},
		ArtifactPath: directory,
		Manifest: domain.Manifest{
			Runtime:   domain.Runtime{Language: "shell", Image: "alpine@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Command: []string{"./run"}},
			Execution: domain.Execution{Timeout: "5s"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "code 7") {
		t.Fatalf("error = %v", err)
	}
	if !deleted {
		t.Fatal("VM was not cleaned up after failure")
	}
}

func TestHuskerRunnerBuildsInVMAndPromotesOutput(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "source.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "obsolete"), []byte("remove me"), 0o644); err != nil {
		t.Fatal(err)
	}
	buildOutput := testTarGz(t, []tar.Header{
		{Name: "source.go", Mode: 0o644, Size: int64(len("package main\n")), Typeflag: tar.TypeReg},
		{Name: "bin/", Mode: 0o755, Typeflag: tar.TypeDir},
		{Name: "bin/automation", Mode: 0o755, Size: int64(len("compiled")), Typeflag: tar.TypeReg},
	}, [][]byte{[]byte("package main\n"), nil, []byte("compiled")})

	deleted := false
	readRequests := 0
	var created createVMRequest
	var importedImage importOCIImageRequest
	buildSeen := false
	checkSeen := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if serveTestHuskerImageImport(t, response, request, &importedImage) {
			return
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/vms":
			if err := json.NewDecoder(request.Body).Decode(&created); err != nil {
				t.Errorf("decode create: %v", err)
			}
			response.WriteHeader(http.StatusCreated)
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/ready"):
			writeJSON(t, response, map[string]any{"ready": true})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/files/write"):
			writeJSON(t, response, map[string]any{"bytes_written": 1})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/exec"):
			var body execRequest
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode exec: %v", err)
			}
			if body.Command == "go" {
				if len(body.Args) > 0 && body.Args[0] == "test" {
					checkSeen = true
				} else {
					buildSeen = true
				}
				if body.WorkingDir == "" || !strings.HasSuffix(body.WorkingDir, "/work") {
					t.Errorf("build working dir = %q", body.WorkingDir)
				}
				if body.Environment["CGO_ENABLED"] != "0" {
					t.Errorf("CGO_ENABLED = %q", body.Environment["CGO_ENABLED"])
				}
			}
			writeJSON(t, response, execResponse{ExitCode: 0})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/files/read"):
			var body struct {
				Offset uint64 `json:"offset"`
				Length uint64 `json:"len"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode read: %v", err)
			}
			readRequests++
			start := min(int(body.Offset), len(buildOutput))
			end := min(start+int(body.Length), len(buildOutput))
			chunk := buildOutput[start:end]
			writeJSON(t, response, map[string]any{
				"data":           base64.StdEncoding.EncodeToString(chunk),
				"size":           len(chunk),
				"total_size":     len(buildOutput),
				"modified_nanos": 42,
			})
		case request.Method == http.MethodDelete:
			deleted = true
			response.WriteHeader(http.StatusNoContent)
		default:
			http.Error(response, "unexpected", http.StatusNotFound)
		}
	}))
	defer server.Close()

	builder, err := NewHuskerRunner(HuskerConfig{
		URL:               server.URL,
		BuildNetwork:      "nat",
		BuildTimeout:      time.Minute,
		ProvisionTimeout:  time.Second,
		CleanupTimeout:    time.Second,
		DownloadChunkSize: 17,
		HTTPClient:        server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var updates []domain.DeploymentStepUpdate
	err = builder.Build(context.Background(), directory, domain.Manifest{
		Metadata: domain.Metadata{Name: "go-example"},
		Runtime: domain.Runtime{
			Image:       "debian@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			BuildImage:  "golang@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
			Build:       []string{"go", "build", "-o", "bin/automation", "."},
			Environment: map[string]string{"CGO_ENABLED": "0"},
		},
		Deployment: domain.DeploymentPolicy{Checks: []domain.DeploymentCheck{{ID: "unit", Command: []string{"go", "test", "./..."}, Timeout: "1m"}}},
	}, func(update domain.DeploymentStepUpdate) error {
		updates = append(updates, update)
		return nil
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if created.RootFSPath != huskerImageName("golang@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee") {
		t.Fatalf("rootfs = %q", created.RootFSPath)
	}
	if importedImage.Reference != "golang@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" || importedImage.Name != created.RootFSPath {
		t.Fatalf("image import = %#v", importedImage)
	}
	if created.Network != "nat" {
		t.Fatalf("network = %q", created.Network)
	}
	if created.Owner != "werkt/build/go-example" {
		t.Fatalf("owner = %q", created.Owner)
	}
	if !buildSeen || !checkSeen || !deleted {
		t.Fatalf("build seen = %v, check seen = %v, deleted = %v", buildSeen, checkSeen, deleted)
	}
	if len(updates) != 4 || updates[2].ID != "check:unit" || updates[3].Status != domain.DeploymentStepSucceeded {
		t.Fatalf("updates = %#v", updates)
	}
	if readRequests < 2 {
		t.Fatalf("read requests = %d, want multiple chunks", readRequests)
	}
	compiled, err := os.ReadFile(filepath.Join(directory, "bin", "automation"))
	if err != nil {
		t.Fatal(err)
	}
	if string(compiled) != "compiled" {
		t.Fatalf("compiled output = %q", compiled)
	}
	info, err := os.Stat(filepath.Join(directory, "bin", "automation"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("compiled mode = %o", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(directory, "obsolete")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("obsolete file still exists: %v", err)
	}
}

func TestHuskerRunnerCleansUpAfterBuildFailure(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "source"), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if serveTestHuskerImageImport(t, response, request, nil) {
			return
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/vms":
			response.WriteHeader(http.StatusCreated)
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/ready"):
			writeJSON(t, response, map[string]any{"ready": true})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/files/write"):
			writeJSON(t, response, map[string]any{"bytes_written": 1})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/exec"):
			var body execRequest
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body.Command == "cargo" {
				writeJSON(t, response, execResponse{ExitCode: 9, Stderr: "compiler failed\n"})
				return
			}
			writeJSON(t, response, execResponse{ExitCode: 0})
		case request.Method == http.MethodDelete:
			deleted = true
			response.WriteHeader(http.StatusNoContent)
		default:
			http.Error(response, "unexpected", http.StatusNotFound)
		}
	}))
	defer server.Close()
	builder, err := NewHuskerRunner(HuskerConfig{URL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	err = builder.Build(context.Background(), directory, domain.Manifest{
		Metadata: domain.Metadata{Name: "rust-example"},
		Runtime:  domain.Runtime{Image: "rust@sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", Build: []string{"cargo", "build"}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "compiler failed") {
		t.Fatalf("Build() error = %v", err)
	}
	if !deleted {
		t.Fatal("build VM was not cleaned up after failure")
	}
}

func TestBuildArtifactPromotionRejectsUnsafeEntries(t *testing.T) {
	tests := []struct {
		name   string
		header tar.Header
	}{
		{name: "traversal", header: tar.Header{Name: "../escaped", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}},
		{name: "symlink", header: tar.Header{Name: "link", Linkname: "/etc/passwd", Mode: 0o777, Typeflag: tar.TypeSymlink}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			directory := filepath.Join(parent, "artifact")
			if err := os.Mkdir(directory, 0o750); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(directory, "original")
			if err := os.WriteFile(marker, []byte("safe"), 0o644); err != nil {
				t.Fatal(err)
			}
			content := []byte(nil)
			if test.header.Size > 0 {
				content = []byte("x")
			}
			archive := testTarGz(t, []tar.Header{test.header}, [][]byte{content})
			if err := replaceDirectoryFromArchive(directory, archive); err == nil {
				t.Fatal("replaceDirectoryFromArchive() error = nil")
			}
			if contents, err := os.ReadFile(marker); err != nil || string(contents) != "safe" {
				t.Fatalf("original artifact changed: contents=%q err=%v", contents, err)
			}
			if _, err := os.Stat(filepath.Join(parent, "escaped")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("traversal target exists: %v", err)
			}
		})
	}
}

func TestArchiveDirectoryPreservesRelativePathsAndExecutableMode(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "bin", "run"), []byte("hello"), 0o755); err != nil {
		t.Fatal(err)
	}

	data, err := archiveDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	gzipReader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			t.Fatal("bin/run not found in archive")
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == "bin/run" {
			if header.Mode&0o111 == 0 {
				t.Fatalf("mode = %o", header.Mode)
			}
			return
		}
	}
}

func writeJSON(t *testing.T, response http.ResponseWriter, value any) {
	t.Helper()
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func writeJSONStatus(t *testing.T, response http.ResponseWriter, status int, value any) {
	t.Helper()
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	if err := json.NewEncoder(response).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func testTarGz(t *testing.T, headers []tar.Header, contents [][]byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	tarWriter := tar.NewWriter(gzipWriter)
	for index := range headers {
		header := headers[index]
		if err := tarWriter.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if len(contents[index]) > 0 {
			if _, err := tarWriter.Write(contents[index]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}
