package runner

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/werkt/internal/domain"
)

func TestResolveToolEnvironmentIsCanonicalAndRejectsCatalogEscape(t *testing.T) {
	config := toolEnvironmentConfig{
		BaseImage: "minimal-base", BaseDigest: testDigest("base"), Platform: "linux-arm64",
		MisePath: "/opt/werkt/mise", MiseVersion: "2026.8.1", MiseDigest: testDigest("mise"),
	}
	first, err := resolveToolEnvironment(map[string]string{"python": "3.13.7"}, config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolveToolEnvironment(map[string]string{"python": "3.13.7"}, config)
	if err != nil {
		t.Fatal(err)
	}
	if first.IdentityDigest != second.IdentityDigest || first.Image != second.Image {
		t.Fatalf("resolution is not canonical: %#v != %#v", first, second)
	}
	if first.ImageDigest != "" || first.CatalogRevision == "" || first.PreparerRevision == "" {
		t.Fatalf("incomplete logical resolution: %#v", first)
	}
	if _, err := resolveToolEnvironment(map[string]string{"terraform": "1.13.1"}, config); err == nil || !strings.Contains(err.Error(), "not in catalog") {
		t.Fatalf("unknown tool error = %v", err)
	}
	if _, err := resolveToolEnvironment(map[string]string{"python": "latest"}, config); err == nil || !strings.Contains(err.Error(), "exact version") {
		t.Fatalf("mutable version error = %v", err)
	}
	withFFmpeg, err := resolveToolEnvironment(map[string]string{"ffmpeg": "8.1.2-50-g1a748fe2cd", "python": "3.13.7"}, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(withFFmpeg.Tools) != 2 || withFFmpeg.Tools[0].Name != "ffmpeg" || withFFmpeg.Tools[0].Artifact == nil ||
		withFFmpeg.Tools[0].Artifact.Digest != "sha256:ae5da4f51b9052390f414005f8ab26c1eed1268f327cce7cb79aa076b29bd66e" ||
		len(withFFmpeg.Tools[0].Executables) != 2 || withFFmpeg.Tools[0].Executables[0].Name != "ffmpeg" ||
		withFFmpeg.Tools[0].Executables[0].RelativePath != "bin/ffmpeg" {
		t.Fatalf("resolved FFmpeg artifact = %#v", withFFmpeg.Tools)
	}
	if len(withFFmpeg.PreparationEgress) != 6 {
		t.Fatalf("deduplicated preparation egress = %#v", withFFmpeg.PreparationEgress)
	}
	if _, err := resolveToolEnvironment(map[string]string{"ffmpeg": "8.1.2"}, config); err == nil || !strings.Contains(err.Error(), "not in catalog") {
		t.Fatalf("uncataloged FFmpeg version error = %v", err)
	}
}

func TestRenderMiseConfigPinsCatalogArtifactsAndKeepsCoreToolsSimple(t *testing.T) {
	artifact := &domain.ToolArtifact{
		URL: "https://example.com/tool.tar.xz", Digest: testDigest("tool archive"),
		SizeBytes: 1234, Format: "tar.xz", StripComponents: 1,
	}
	config, err := renderMiseConfig([]domain.ResolvedTool{
		{Name: "ffmpeg", Backend: "http:ffmpeg", Version: "1.2.3", Artifact: artifact},
		{Name: "python", Backend: "python", Version: "3.13.7"},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(config)
	for _, expected := range []string{
		`"http:ffmpeg" = { version = "1.2.3"`, `url = "https://example.com/tool.tar.xz"`,
		`checksum = "` + artifact.Digest + `"`, `size = "1234"`, `format = "tar.xz"`,
		`strip_components = 1`, `"python" = "3.13.7"`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("mise config %q does not contain %q", text, expected)
		}
	}
}

func TestPrepareManifestBuildsVerifiedPythonEnvironmentWithoutOCIImport(t *testing.T) {
	misePath := t.TempDir() + "/mise"
	if err := os.WriteFile(misePath, []byte("verified-mise-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	miseDigest := testDigest("verified-mise-binary")
	baseDigest := testDigest("minimal-base-image")
	derivedDigest := testDigest("prepared-python-image")

	var mu sync.Mutex
	var created createVMRequest
	var committed string
	deleted := false
	linked := make(map[string]string)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/images/minimal-base":
			writeJSON(t, response, imageResponse{Name: "minimal-base", Kind: "rootfs", ContentDigest: baseDigest})
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/images/werkt-tools-"):
			if committed == "" {
				writeJSONStatus(t, response, http.StatusNotFound, map[string]string{"kind": "image_not_found"})
				return
			}
			writeJSON(t, response, imageResponse{Name: committed, Kind: "rootfs", ParentImage: "minimal-base", ContentDigest: derivedDigest})
		case request.Method == http.MethodPost && request.URL.Path == "/v1/vms":
			if err := json.NewDecoder(request.Body).Decode(&created); err != nil {
				t.Errorf("decode create VM: %v", err)
			}
			response.WriteHeader(http.StatusCreated)
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/ready"):
			writeJSON(t, response, readyResponse{Ready: true})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/files/write"):
			response.WriteHeader(http.StatusOK)
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/exec"):
			var command execRequest
			if err := json.NewDecoder(request.Body).Decode(&command); err != nil {
				t.Errorf("decode exec: %v", err)
			}
			result := execResponse{ExitCode: 0}
			if command.Command == "/bin/uname" {
				result.Stdout = "aarch64\n"
			}
			if command.Command == guestMisePath && len(command.Args) == 1 && command.Args[0] == "--version" {
				result.Stdout = "2026.8.1 linux-arm64\n"
			}
			if command.Command == guestMisePath && len(command.Args) == 2 && command.Args[0] == "where" {
				result.Stdout = guestMiseDataDir + "/installs/python/3.13.7\n"
			}
			if command.Command == "/bin/ln" && len(command.Args) == 3 {
				linked[path.Base(command.Args[2])] = command.Args[1]
			}
			writeJSON(t, response, result)
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/stop"):
			response.WriteHeader(http.StatusNoContent)
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/commit-image"):
			var body commitImageRequest
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode commit: %v", err)
			}
			committed = body.Name
			writeJSONStatus(t, response, http.StatusCreated, imageResponse{Name: committed, Kind: "rootfs", ParentImage: "minimal-base", ContentDigest: derivedDigest})
		case request.Method == http.MethodDelete && strings.HasPrefix(request.URL.Path, "/v1/vms/werkt-prepare-"):
			deleted = true
			response.WriteHeader(http.StatusNoContent)
		case request.URL.Path == "/v1/images/import-oci":
			t.Error("runtime.tools must not import an OCI image")
			http.Error(response, "unexpected OCI import", http.StatusInternalServerError)
		default:
			http.Error(response, "unexpected request "+request.Method+" "+request.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

	runner, err := NewHuskerRunner(HuskerConfig{
		URL: server.URL, HTTPClient: server.Client(), Kernel: "/boot/vmlinux", VCPUs: 1, MemoryMiB: 256,
		ProvisionTimeout: time.Second, CleanupTimeout: time.Second,
		ToolBaseImage: "minimal-base", ToolBaseDigest: baseDigest, ToolPlatform: "linux-arm64",
		MisePath: misePath, MiseVersion: "2026.8.1", MiseDigest: miseDigest, ToolPrepareTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := domain.Manifest{Runtime: domain.Runtime{Language: "python", Tools: map[string]string{"python": "3.13.7"}, Command: []string{"python3", "main.py"}}}
	prepared, err := runner.PrepareManifest(context.Background(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Runtime.ResolvedTools == nil || prepared.Runtime.ResolvedTools.ImageDigest != derivedDigest {
		t.Fatalf("resolved environment = %#v", prepared.Runtime.ResolvedTools)
	}
	if created.RootFSPath != "minimal-base" || created.Network != "filtered" || len(created.Egress) != 6 {
		t.Fatalf("preparation VM = %#v", created)
	}
	attestationAPI := false
	attestationTrustRoot := false
	for _, rule := range created.Egress {
		if rule.Host == "api.github.com" && rule.Port == 443 && rule.Protocol == "tcp" {
			attestationAPI = true
		}
		if rule.Host == "tuf-repo-cdn.sigstore.dev" && rule.Port == 443 && rule.Protocol == "tcp" {
			attestationTrustRoot = true
		}
	}
	if !attestationAPI || !attestationTrustRoot {
		t.Fatalf("preparation VM does not permit GitHub attestation verification: %#v", created.Egress)
	}
	if committed != prepared.Runtime.ResolvedTools.Image || len(linked) != 3 || !deleted {
		t.Fatalf("committed=%q linked=%#v deleted=%v resolved=%#v", committed, linked, deleted, prepared.Runtime.ResolvedTools)
	}
}

func TestExecuteUsesPreparedPythonImageAndPublishedToolBin(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(directory+"/main.py", []byte("print('hello')\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	baseDigest := testDigest("base")
	imageDigest := testDigest("prepared")
	environment, err := resolveToolEnvironment(map[string]string{"python": "3.13.7"}, toolEnvironmentConfig{
		BaseImage: "minimal-base", BaseDigest: baseDigest, Platform: "linux-arm64",
		MisePath: "/opt/werkt/mise", MiseVersion: "2026.8.1", MiseDigest: testDigest("mise"),
	})
	if err != nil {
		t.Fatal(err)
	}
	environment.ImageDigest = imageDigest
	createdRootFS := ""
	runtimeCommandSeen := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/images/"+environment.Image:
			writeJSON(t, response, imageResponse{Name: environment.Image, Kind: "rootfs", ParentImage: "minimal-base", ContentDigest: imageDigest})
		case request.Method == http.MethodPost && request.URL.Path == "/v1/vms":
			var body createVMRequest
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode create VM: %v", err)
			}
			createdRootFS = body.RootFSPath
			response.WriteHeader(http.StatusCreated)
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/ready"):
			writeJSON(t, response, readyResponse{Ready: true})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/files/write"):
			response.WriteHeader(http.StatusOK)
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/exec"):
			var command execRequest
			if err := json.NewDecoder(request.Body).Decode(&command); err != nil {
				t.Errorf("decode exec: %v", err)
			}
			if command.Command == "python3" {
				runtimeCommandSeen = true
				if !strings.HasPrefix(command.Environment["PATH"], guestRuntimeBinDir+":") {
					t.Errorf("runtime PATH = %q", command.Environment["PATH"])
				}
			}
			writeJSON(t, response, execResponse{ExitCode: 0})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/files/read"):
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode read: %v", err)
			}
			writeJSON(t, response, map[string]any{"data": base64.StdEncoding.EncodeToString([]byte(`{}`)), "size": 2})
		case request.Method == http.MethodDelete && strings.HasPrefix(request.URL.Path, "/v1/vms/werkt-"):
			response.WriteHeader(http.StatusNoContent)
		case request.URL.Path == "/v1/images/import-oci":
			t.Error("prepared runtime.tools must not import OCI at execution time")
			http.Error(response, "unexpected", http.StatusInternalServerError)
		default:
			http.Error(response, "unexpected request "+request.Method+" "+request.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

	runner, err := NewHuskerRunner(HuskerConfig{URL: server.URL, HTTPClient: server.Client(), ProvisionTimeout: time.Second, CleanupTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Execute(context.Background(), domain.RunnableRun{
		Run:          domain.Run{ID: "run_tools", AutomationID: "python-tools", RevisionID: "rev_tools", Attempt: 1},
		ArtifactPath: directory,
		Manifest: domain.Manifest{
			Runtime:   domain.Runtime{Language: "python", Tools: map[string]string{"python": "3.13.7"}, ResolvedTools: &environment, Command: []string{"python3", "main.py"}},
			Execution: domain.Execution{Timeout: "5s"},
		},
		Event: domain.EventEnvelope{ID: "evt_tools", Data: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if createdRootFS != environment.Image || !runtimeCommandSeen {
		t.Fatalf("rootfs=%q runtimeCommandSeen=%v", createdRootFS, runtimeCommandSeen)
	}
}

func TestToolRuntimeEnvironmentPreservesLegacyMiseShimPath(t *testing.T) {
	legacy := toolRuntimeEnvironment(&domain.ResolvedToolEnvironment{Version: 1})
	if !strings.HasPrefix(legacy["PATH"], guestMiseDataDir+"/shims:") {
		t.Fatalf("legacy PATH = %q", legacy["PATH"])
	}
	current := toolRuntimeEnvironment(&domain.ResolvedToolEnvironment{Version: 2})
	if !strings.HasPrefix(current["PATH"], guestRuntimeBinDir+":") {
		t.Fatalf("current PATH = %q", current["PATH"])
	}
}

func testDigest(contents string) string {
	digest := sha256.Sum256([]byte(contents))
	return "sha256:" + hex.EncodeToString(digest[:])
}
