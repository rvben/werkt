package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// TargetEnvironment and TargetInstance are optional deployment-target values
// supplied through linker flags. Ordinary builds remain development-oriented;
// target-bound builds use these values as defaults and reject conflicting
// runtime overrides before serving traffic.
var (
	TargetEnvironment string
	TargetInstance    string
)

type Config struct {
	APIURL                   string
	DatabaseURL              string
	DataDir                  string
	ListenAddress            string
	Environment              string
	Instance                 string
	ManagementToken          string
	ManagementReadToken      string
	ManagementOperateToken   string
	ManagementDeployToken    string
	ManagementSecretsToken   string
	ManagementRetentionToken string
	SecretKey                string
	OIDCIssuer               string
	OIDCClientID             string
	OIDCClientSecret         string
	OIDCRedirectURL          string
	OIDCAllowedEmails        []string
	OIDCSessionSecret        string
	OIDCSessionTTL           time.Duration
	WorkerPoll               time.Duration
	SchedulerPoll            time.Duration
	ShutdownPeriod           time.Duration
	DeployTimeout            time.Duration
	DeploymentPoll           time.Duration
	MaxPackageBytes          int64
	MaxExpandedPackageBytes  int64
	MaxPackageEntries        int
	Executor                 string
	HuskerURL                string
	HuskerToken              string
	HuskerRootFS             string
	HuskerKernel             string
	HuskerVCPUs              uint32
	HuskerMemory             uint32
	HuskerBuildNetwork       string
	HuskerBuildTimeout       time.Duration
	HuskerProvisionTimeout   time.Duration
	HuskerCleanupTimeout     time.Duration
	HuskerToolBaseImage      string
	HuskerToolBaseDigest     string
	HuskerToolPlatform       string
	HuskerMisePath           string
	HuskerMiseVersion        string
	HuskerMiseDigest         string
	HuskerToolPrepareTimeout time.Duration
}

func Load() Config {
	return Config{
		APIURL:                   env("WERKT_API_URL", "http://127.0.0.1:8080"),
		DatabaseURL:              env("WERKT_DATABASE_URL", "postgres://automations:automations@localhost:54329/automations?sslmode=disable"),
		DataDir:                  env("WERKT_DATA_DIR", filepath.Join(".", "data")),
		ListenAddress:            env("WERKT_LISTEN_ADDR", "127.0.0.1:8080"),
		Environment:              env("WERKT_ENVIRONMENT", envValue(TargetEnvironment, "development")),
		Instance:                 env("WERKT_INSTANCE", TargetInstance),
		ManagementToken:          os.Getenv("WERKT_MANAGEMENT_TOKEN"),
		ManagementReadToken:      os.Getenv("WERKT_MANAGEMENT_READ_TOKEN"),
		ManagementOperateToken:   os.Getenv("WERKT_MANAGEMENT_OPERATE_TOKEN"),
		ManagementDeployToken:    os.Getenv("WERKT_MANAGEMENT_DEPLOY_TOKEN"),
		ManagementSecretsToken:   os.Getenv("WERKT_MANAGEMENT_SECRETS_TOKEN"),
		ManagementRetentionToken: os.Getenv("WERKT_MANAGEMENT_RETENTION_TOKEN"),
		SecretKey:                os.Getenv("WERKT_SECRET_KEY"),
		OIDCIssuer:               os.Getenv("WERKT_OIDC_ISSUER"),
		OIDCClientID:             os.Getenv("WERKT_OIDC_CLIENT_ID"),
		OIDCClientSecret:         os.Getenv("WERKT_OIDC_CLIENT_SECRET"),
		OIDCRedirectURL:          os.Getenv("WERKT_OIDC_REDIRECT_URL"),
		OIDCAllowedEmails:        csvEnv("WERKT_OIDC_ALLOWED_EMAILS"),
		OIDCSessionSecret:        os.Getenv("WERKT_OIDC_SESSION_SECRET"),
		OIDCSessionTTL:           durationEnv("WERKT_OIDC_SESSION_TTL", 12*time.Hour),
		WorkerPoll:               durationEnv("WERKT_WORKER_POLL", 500*time.Millisecond),
		SchedulerPoll:            durationEnv("WERKT_SCHEDULER_POLL", time.Second),
		ShutdownPeriod:           durationEnv("WERKT_SHUTDOWN_PERIOD", 10*time.Second),
		DeployTimeout:            durationEnv("WERKT_DEPLOY_TIMEOUT", 30*time.Minute),
		DeploymentPoll:           durationEnv("WERKT_DEPLOYMENT_POLL", 500*time.Millisecond),
		MaxPackageBytes:          int64Env("WERKT_MAX_PACKAGE_BYTES", 64<<20),
		MaxExpandedPackageBytes:  int64Env("WERKT_MAX_EXPANDED_PACKAGE_BYTES", 256<<20),
		MaxPackageEntries:        intEnv("WERKT_MAX_PACKAGE_ENTRIES", 10_000),
		Executor:                 env("WERKT_EXECUTOR", "process"),
		HuskerURL:                env("WERKT_HUSKER_URL", "http://127.0.0.1:8081"),
		HuskerToken:              os.Getenv("WERKT_HUSKER_TOKEN"),
		HuskerRootFS:             os.Getenv("WERKT_HUSKER_ROOTFS"),
		HuskerKernel:             os.Getenv("WERKT_HUSKER_KERNEL"),
		HuskerVCPUs:              uint32Env("WERKT_HUSKER_VCPUS", 1),
		HuskerMemory:             uint32Env("WERKT_HUSKER_MEMORY_MIB", 256),
		HuskerBuildNetwork:       env("WERKT_HUSKER_BUILD_NETWORK", "nat"),
		HuskerBuildTimeout:       durationEnv("WERKT_HUSKER_BUILD_TIMEOUT", 15*time.Minute),
		HuskerProvisionTimeout:   durationEnv("WERKT_HUSKER_PROVISION_TIMEOUT", 2*time.Minute),
		HuskerCleanupTimeout:     durationEnv("WERKT_HUSKER_CLEANUP_TIMEOUT", 30*time.Second),
		HuskerToolBaseImage:      os.Getenv("WERKT_HUSKER_TOOL_BASE_IMAGE"),
		HuskerToolBaseDigest:     os.Getenv("WERKT_HUSKER_TOOL_BASE_DIGEST"),
		HuskerToolPlatform:       os.Getenv("WERKT_HUSKER_TOOL_PLATFORM"),
		HuskerMisePath:           os.Getenv("WERKT_HUSKER_MISE_PATH"),
		HuskerMiseVersion:        os.Getenv("WERKT_HUSKER_MISE_VERSION"),
		HuskerMiseDigest:         os.Getenv("WERKT_HUSKER_MISE_DIGEST"),
		HuskerToolPrepareTimeout: durationEnv("WERKT_HUSKER_TOOL_PREPARE_TIMEOUT", 20*time.Minute),
	}
}

// ValidateDeploymentTarget prevents a target-bound binary from presenting
// itself as a different environment or instance because of configuration
// drift. Untargeted development and release builds remain unrestricted.
func ValidateDeploymentTarget(value Config) error {
	if TargetEnvironment != "" && value.Environment != TargetEnvironment {
		return fmt.Errorf("WERKT_ENVIRONMENT %q conflicts with build target %q", value.Environment, TargetEnvironment)
	}
	if TargetInstance != "" && value.Instance != TargetInstance {
		return fmt.Errorf("WERKT_INSTANCE %q conflicts with build target %q", value.Instance, TargetInstance)
	}
	return nil
}

func envValue(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func csvEnv(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	values := strings.Split(raw, ",")
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func int64Env(key string, fallback int64) int64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func intEnv(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func uint32Env(key string, fallback uint32) uint32 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return fallback
	}
	return uint32(parsed)
}
