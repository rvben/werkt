package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/rvben/werkt/internal/config"
	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/httpapi"
	"github.com/rvben/werkt/internal/managementclient"
	"github.com/rvben/werkt/internal/manifest"
	"github.com/rvben/werkt/internal/packageio"
	"github.com/rvben/werkt/internal/runner"
	"github.com/rvben/werkt/internal/service"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("command failed", "error", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		usage()
		return errors.New("a command is required")
	}
	switch arguments[0] {
	case "validate":
		return validate(arguments[1:])
	case "deploy":
		return deploy(arguments[1:])
	case "serve":
		return serve(arguments[1:])
	case "automations":
		return listAutomations(arguments[1:])
	case "runs":
		return listRuns(arguments[1:])
	case "version":
		fmt.Println(version())
		return nil
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", arguments[0])
	}
}

func validate(arguments []string) error {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	directory := "."
	if flags.NArg() > 0 {
		directory = flags.Arg(0)
	}
	value, err := manifest.Load(directory)
	if err != nil {
		return err
	}
	hash, err := manifest.HashDirectory(directory)
	if err != nil {
		return err
	}
	return printJSON(map[string]any{"valid": true, "automation": value.Metadata.Name, "contentHash": hash})
}

func deploy(arguments []string) error {
	configuration := config.Load()
	flags := flag.NewFlagSet("deploy", flag.ContinueOnError)
	apiURL := flags.String("api", configuration.APIURL, "Werkt management API URL")
	idempotencyKey := flags.String("idempotency-key", "", "deployment replay key (defaults to the package digest)")
	wait := flags.Bool("wait", true, "wait for validation, build, and activation")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	directory := "."
	if flags.NArg() > 0 {
		directory = flags.Arg(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), configuration.DeployTimeout)
	defer cancel()
	archive, err := os.CreateTemp("", "werkt-deploy-*.tar.gz")
	if err != nil {
		return err
	}
	archivePath := archive.Name()
	defer os.Remove(archivePath) //nolint:errcheck
	digest, err := packageio.WriteArchive(directory, archive)
	if err != nil {
		_ = archive.Close()
		return err
	}
	if err := archive.Close(); err != nil {
		return err
	}
	if *idempotencyKey == "" {
		*idempotencyKey = "deploy-" + digest
	}
	client, err := managementclient.New(*apiURL, configuration.ManagementToken, nil)
	if err != nil {
		return err
	}
	archive, err = os.Open(archivePath)
	if err != nil {
		return err
	}
	result, _, uploadErr := client.CreateDeployment(ctx, archive, digest, *idempotencyKey, "cli")
	closeErr := archive.Close()
	if err := errors.Join(uploadErr, closeErr); err != nil {
		return err
	}
	if *wait {
		result, err = client.WaitDeployment(ctx, result, configuration.DeploymentPoll)
		if err != nil {
			return err
		}
		if result.Status == "failed" {
			if err := printJSON(result); err != nil {
				return err
			}
			return fmt.Errorf("deployment %s failed: %s", result.ID, result.Error)
		}
	}
	return printJSON(result)
}

func serve(arguments []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	workers := flags.Int("workers", 2, "number of local automation workers")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *workers < 1 {
		return errors.New("workers must be at least 1")
	}
	configuration := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := openStore(ctx, configuration)
	if err != nil {
		return err
	}
	defer store.Close()

	executor, err := newExecutor(configuration)
	if err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	for index := range *workers {
		workerID := fmt.Sprintf("%s-%d-%d", hostname, os.Getpid(), index)
		go service.NewWorker(store, executor, workerID, configuration.WorkerPoll).Run(ctx)
	}
	builder, err := newBuilder(configuration)
	if err != nil {
		return err
	}
	deployer := service.NewDeployer(store, configuration.DataDir, builder)
	deploymentWorkerID := fmt.Sprintf("%s-%d-deployments", hostname, os.Getpid())
	go service.NewDeploymentWorker(store, deployer, deploymentWorkerID, configuration.DeploymentPoll).Run(ctx)
	go service.NewScheduler(store, configuration.SchedulerPoll).Run(ctx)
	go service.NewNtfyReconciler(store).Run(ctx)

	if configuration.ManagementToken == "" {
		slog.Warn("management API authentication is disabled; set WERKT_MANAGEMENT_TOKEN outside local development")
	}
	if configuration.Executor == "process" {
		slog.Warn("process executor runs deployed build and runtime commands on this host; use husker before accepting untrusted packages")
	}
	intake := service.NewDeploymentIntake(store, configuration.DataDir, packageio.Limits{
		CompressedBytes: configuration.MaxPackageBytes,
		ExpandedBytes:   configuration.MaxExpandedPackageBytes,
		Entries:         configuration.MaxPackageEntries,
	})
	api := httpapi.New(store, configuration.ListenAddress, configuration.ManagementToken, httpapi.WithDeploymentIntake(intake))
	serverErrors := make(chan error, 1)
	go func() {
		slog.Info("control plane listening", "address", configuration.ListenAddress, "workers", *workers)
		serverErrors <- api.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		return err
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), configuration.ShutdownPeriod)
		defer cancel()
		return api.Shutdown(shutdownContext)
	}
}

func newExecutor(configuration config.Config) (runner.Executor, error) {
	switch configuration.Executor {
	case "process":
		return runner.NewProcessRunner(), nil
	case "husker":
		return newHuskerRunner(configuration)
	default:
		return nil, fmt.Errorf("unsupported executor %q (must be process or husker)", configuration.Executor)
	}
}

func newBuilder(configuration config.Config) (service.Builder, error) {
	switch configuration.Executor {
	case "process":
		return runner.NewProcessRunner(), nil
	case "husker":
		return newHuskerRunner(configuration)
	default:
		return nil, fmt.Errorf("unsupported executor %q (must be process or husker)", configuration.Executor)
	}
}

func newHuskerRunner(configuration config.Config) (*runner.HuskerRunner, error) {
	return runner.NewHuskerRunner(runner.HuskerConfig{
		URL:              configuration.HuskerURL,
		Token:            configuration.HuskerToken,
		RootFS:           configuration.HuskerRootFS,
		Kernel:           configuration.HuskerKernel,
		VCPUs:            configuration.HuskerVCPUs,
		MemoryMiB:        configuration.HuskerMemory,
		Network:          configuration.HuskerNetwork,
		BuildNetwork:     configuration.HuskerBuildNetwork,
		BuildTimeout:     configuration.HuskerBuildTimeout,
		ProvisionTimeout: configuration.HuskerProvisionTimeout,
		CleanupTimeout:   configuration.HuskerCleanupTimeout,
	})
}

func listAutomations(arguments []string) error {
	flags := flag.NewFlagSet("automations", flag.ContinueOnError)
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := openStore(ctx, config.Load())
	if err != nil {
		return err
	}
	defer store.Close()
	values, err := store.ListAutomations(ctx, database.AutomationFilter{})
	if err != nil {
		return err
	}
	return printJSON(values)
}

func listRuns(arguments []string) error {
	flags := flag.NewFlagSet("runs", flag.ContinueOnError)
	limit := flags.Int("limit", 100, "maximum runs to return")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := openStore(ctx, config.Load())
	if err != nil {
		return err
	}
	defer store.Close()
	values, err := store.ListRuns(ctx, *limit)
	if err != nil {
		return err
	}
	return printJSON(values)
}

func openStore(ctx context.Context, configuration config.Config) (*database.Store, error) {
	if err := os.MkdirAll(configuration.DataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	store, err := database.Open(ctx, configuration.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := store.Migrate(ctx); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

func usage() {
	executable := filepath.Base(os.Args[0])
	fmt.Fprintf(os.Stderr, `Usage:
  %s validate [directory]
  %s deploy [-api URL] [-idempotency-key KEY] [-wait=true] [directory]
  %s serve [-workers N]
  %s automations
  %s runs [-limit N]
  %s version
`, executable, executable, executable, executable, executable, executable)
}
