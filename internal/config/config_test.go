package config

import "testing"

func TestLoadUsesDeploymentTargetAsOperatorScopeFallback(t *testing.T) {
	originalEnvironment, originalInstance := TargetEnvironment, TargetInstance
	t.Cleanup(func() {
		TargetEnvironment = originalEnvironment
		TargetInstance = originalInstance
	})
	TargetEnvironment = "staging"
	TargetInstance = "staging-control"
	t.Setenv("WERKT_ENVIRONMENT", "")
	t.Setenv("WERKT_INSTANCE", "")

	value := Load()
	if value.Environment != "staging" || value.Instance != "staging-control" {
		t.Fatalf("operator scope = %q/%q, want staging/staging-control", value.Environment, value.Instance)
	}
	if err := ValidateDeploymentTarget(value); err != nil {
		t.Fatalf("ValidateDeploymentTarget() error = %v", err)
	}
}

func TestValidateDeploymentTargetRejectsConflictingRuntimeScope(t *testing.T) {
	originalEnvironment, originalInstance := TargetEnvironment, TargetInstance
	t.Cleanup(func() {
		TargetEnvironment = originalEnvironment
		TargetInstance = originalInstance
	})
	TargetEnvironment = "staging"
	TargetInstance = "staging-control"

	for name, value := range map[string]Config{
		"environment": {Environment: "development", Instance: "staging-control"},
		"instance":    {Environment: "staging", Instance: "127.0.0.1:8080"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateDeploymentTarget(value); err == nil {
				t.Fatal("ValidateDeploymentTarget() error = nil, want conflict")
			}
		})
	}
}

func TestAutomationStateLimitConfiguration(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int
	}{
		{"", 1 << 20}, {"2097152", 2 << 20}, {"131072", 128 << 10}, {"0", 1 << 20}, {"-1", 1 << 20}, {"invalid", 1 << 20},
	} {
		t.Run(tc.input, func(t *testing.T) {
			t.Setenv("WERKT_MAX_AUTOMATION_STATE_BYTES", tc.input)
			if got := Load().MaxAutomationStateBytes; got != tc.want {
				t.Fatalf("limit = %d, want %d", got, tc.want)
			}
		})
	}
}
