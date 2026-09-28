package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rvben/werkt/internal/authoring"
	"github.com/rvben/werkt/internal/config"
	"github.com/rvben/werkt/internal/service"
)

func TestDraftCommandIsDisabledBeforeReadingIntentWithoutBYOKKey(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	if err := draftCommand(nil); !errors.Is(err, authoring.ErrDisabled) {
		t.Fatalf("error = %v, want ErrDisabled", err)
	}
}

func TestDefaultShutdownPeriodCoversADrainingHuskerAttempt(t *testing.T) {
	t.Setenv("WERKT_EXECUTOR", "husker")
	t.Setenv("WERKT_SHUTDOWN_PERIOD", "")
	t.Setenv("WERKT_HUSKER_CLEANUP_TIMEOUT", "")
	configuration := config.Load()
	if needed := drainAllowance(configuration); configuration.ShutdownPeriod <= needed {
		t.Fatalf("default shutdown period %s does not outlast the %s a draining attempt may take to destroy its VM and record its outcome",
			configuration.ShutdownPeriod, needed)
	}
}

func TestDrainAllowanceCoversVMCleanupOnlyForHusker(t *testing.T) {
	configuration := config.Config{Executor: "husker", HuskerCleanupTimeout: 30 * time.Second}
	if got, want := drainAllowance(configuration), 30*time.Second+service.OutcomeTimeout; got != want {
		t.Fatalf("husker drain allowance = %s, want %s", got, want)
	}
	configuration.Executor = "process"
	if got := drainAllowance(configuration); got != service.OutcomeTimeout {
		t.Fatalf("process drain allowance = %s, want %s", got, service.OutcomeTimeout)
	}
}

// The staging files are what an operator copies; their shutdown settings have
// to satisfy the same drain rule the binary warns about.
func TestStagingShutdownSettingsOutlastADrainingAttempt(t *testing.T) {
	// Only the file's settings count: an unset key must read as its default,
	// not as whatever the test process inherited.
	for _, entry := range os.Environ() {
		if key, _, _ := strings.Cut(entry, "="); strings.HasPrefix(key, "WERKT_") {
			t.Setenv(key, "")
		}
	}
	env := readKeyValues(t, "../../deploy/staging/werkt.env.example")
	for key, value := range env {
		t.Setenv(key, value)
	}
	configuration := config.Load()
	if needed := drainAllowance(configuration); configuration.ShutdownPeriod <= needed {
		t.Fatalf("staging WERKT_SHUTDOWN_PERIOD %s does not outlast the %s a draining attempt may take",
			configuration.ShutdownPeriod, needed)
	}
	unit := readKeyValues(t, "../../deploy/staging/werkt.service")
	// systemd reads a bare number as seconds.
	raw := unit["TimeoutStopSec"]
	if strings.Trim(raw, "0123456789") == "" {
		raw += "s"
	}
	stop, err := time.ParseDuration(raw)
	if err != nil {
		t.Fatalf("staging TimeoutStopSec %q: %v", unit["TimeoutStopSec"], err)
	}
	if stop <= configuration.ShutdownPeriod {
		t.Fatalf("staging TimeoutStopSec %s lets systemd kill a drain allowed %s", stop, configuration.ShutdownPeriod)
	}
}

func readKeyValues(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		if key, value, ok := strings.Cut(line, "="); ok {
			values[key] = value
		}
	}
	return values
}
