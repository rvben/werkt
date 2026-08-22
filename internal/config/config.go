package config

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type Config struct {
	APIURL                  string
	DatabaseURL             string
	DataDir                 string
	ListenAddress           string
	ManagementToken         string
	SecretKey               string
	WorkerPoll              time.Duration
	SchedulerPoll           time.Duration
	ShutdownPeriod          time.Duration
	DeployTimeout           time.Duration
	DeploymentPoll          time.Duration
	MaxPackageBytes         int64
	MaxExpandedPackageBytes int64
	MaxPackageEntries       int
	Executor                string
	HuskerURL               string
	HuskerToken             string
	HuskerRootFS            string
	HuskerKernel            string
	HuskerVCPUs             uint32
	HuskerMemory            uint32
	HuskerNetwork           string
	HuskerBuildNetwork      string
	HuskerBuildTimeout      time.Duration
	HuskerProvisionTimeout  time.Duration
	HuskerCleanupTimeout    time.Duration
}

func Load() Config {
	return Config{
		APIURL:                  env("WERKT_API_URL", "http://127.0.0.1:8080"),
		DatabaseURL:             env("WERKT_DATABASE_URL", "postgres://automations:automations@localhost:54329/automations?sslmode=disable"),
		DataDir:                 env("WERKT_DATA_DIR", filepath.Join(".", "data")),
		ListenAddress:           env("WERKT_LISTEN_ADDR", "127.0.0.1:8080"),
		ManagementToken:         os.Getenv("WERKT_MANAGEMENT_TOKEN"),
		SecretKey:               os.Getenv("WERKT_SECRET_KEY"),
		WorkerPoll:              durationEnv("WERKT_WORKER_POLL", 500*time.Millisecond),
		SchedulerPoll:           durationEnv("WERKT_SCHEDULER_POLL", time.Second),
		ShutdownPeriod:          durationEnv("WERKT_SHUTDOWN_PERIOD", 10*time.Second),
		DeployTimeout:           durationEnv("WERKT_DEPLOY_TIMEOUT", 30*time.Minute),
		DeploymentPoll:          durationEnv("WERKT_DEPLOYMENT_POLL", 500*time.Millisecond),
		MaxPackageBytes:         int64Env("WERKT_MAX_PACKAGE_BYTES", 64<<20),
		MaxExpandedPackageBytes: int64Env("WERKT_MAX_EXPANDED_PACKAGE_BYTES", 256<<20),
		MaxPackageEntries:       intEnv("WERKT_MAX_PACKAGE_ENTRIES", 10_000),
		Executor:                env("WERKT_EXECUTOR", "process"),
		HuskerURL:               env("WERKT_HUSKER_URL", "http://127.0.0.1:8081"),
		HuskerToken:             os.Getenv("WERKT_HUSKER_TOKEN"),
		HuskerRootFS:            os.Getenv("WERKT_HUSKER_ROOTFS"),
		HuskerKernel:            os.Getenv("WERKT_HUSKER_KERNEL"),
		HuskerVCPUs:             uint32Env("WERKT_HUSKER_VCPUS", 1),
		HuskerMemory:            uint32Env("WERKT_HUSKER_MEMORY_MIB", 256),
		HuskerNetwork:           env("WERKT_HUSKER_NETWORK", "none"),
		HuskerBuildNetwork:      env("WERKT_HUSKER_BUILD_NETWORK", "nat"),
		HuskerBuildTimeout:      durationEnv("WERKT_HUSKER_BUILD_TIMEOUT", 15*time.Minute),
		HuskerProvisionTimeout:  durationEnv("WERKT_HUSKER_PROVISION_TIMEOUT", 2*time.Minute),
		HuskerCleanupTimeout:    durationEnv("WERKT_HUSKER_CLEANUP_TIMEOUT", 30*time.Second),
	}
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
