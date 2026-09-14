package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rvben/werkt/internal/buildinfo"
	"github.com/rvben/werkt/internal/config"
	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/domain"
	"github.com/rvben/werkt/internal/httpapi"
	"github.com/rvben/werkt/internal/managementclient"
	"github.com/rvben/werkt/internal/manifest"
	"github.com/rvben/werkt/internal/packageio"
	"github.com/rvben/werkt/internal/provenance"
	"github.com/rvben/werkt/internal/runner"
	"github.com/rvben/werkt/internal/secretvault"
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
	case "deployment":
		return deploymentCommand(arguments[1:])
	case "rollback":
		return rollback(arguments[1:])
	case "retention":
		return retentionCommand(arguments[1:])
	case "secret":
		return secretCommand(arguments[1:])
	case "recovery":
		return recoveryCommand(arguments[1:])
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
		if result.Status == "failed" || result.Status == "cancelled" {
			if err := printJSON(result); err != nil {
				return err
			}
			return fmt.Errorf("deployment %s failed: %s", result.ID, result.Error)
		}
	}
	return printJSON(result)
}

func deploymentCommand(arguments []string) error {
	if len(arguments) < 2 {
		return errors.New("usage: werkt deployment get|cancel|retry DEPLOYMENT_ID")
	}
	configuration := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), configuration.DeployTimeout)
	defer cancel()
	client, err := managementclient.New(configuration.APIURL, configuration.ManagementToken, nil)
	if err != nil {
		return err
	}
	deploymentID := arguments[1]
	switch arguments[0] {
	case "get":
		value, err := client.GetDeployment(ctx, deploymentID)
		if err != nil {
			return err
		}
		return printJSON(value)
	case "cancel":
		value, err := client.CancelDeployment(ctx, deploymentID, "cli")
		if err != nil {
			return err
		}
		return printJSON(value)
	case "retry":
		flags := flag.NewFlagSet("deployment retry", flag.ContinueOnError)
		idempotencyKey := flags.String("idempotency-key", "retry-"+deploymentID, "retry replay key")
		wait := flags.Bool("wait", true, "wait for validation, checks, and activation")
		if err := flags.Parse(arguments[2:]); err != nil {
			return err
		}
		value, _, err := client.RetryDeployment(ctx, deploymentID, *idempotencyKey, "cli")
		if err != nil {
			return err
		}
		if *wait {
			value, err = client.WaitDeployment(ctx, value, configuration.DeploymentPoll)
			if err != nil {
				return err
			}
		}
		if err := printJSON(value); err != nil {
			return err
		}
		if value.Status == "failed" || value.Status == "cancelled" {
			return fmt.Errorf("deployment %s %s: %s", value.ID, value.Status, value.Error)
		}
		return nil
	default:
		return fmt.Errorf("unknown deployment command %q", arguments[0])
	}
}

func rollback(arguments []string) error {
	flags := flag.NewFlagSet("rollback", flag.ContinueOnError)
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 2 {
		return errors.New("usage: werkt rollback AUTOMATION_ID REVISION_ID")
	}
	configuration := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), configuration.DeployTimeout)
	defer cancel()
	client, err := managementclient.New(configuration.APIURL, configuration.ManagementToken, nil)
	if err != nil {
		return err
	}
	value, err := client.RollbackAutomation(ctx, flags.Arg(0), flags.Arg(1), "cli")
	if err != nil {
		return err
	}
	return printJSON(value)
}

func retentionCommand(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("usage: werkt retention plan|get|apply [PLAN_ID]")
	}
	configuration := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), configuration.DeployTimeout)
	defer cancel()
	client, err := managementclient.New(configuration.APIURL, configuration.ManagementToken, nil)
	if err != nil {
		return err
	}
	switch arguments[0] {
	case "plan":
		flags := flag.NewFlagSet("retention plan", flag.ContinueOnError)
		sourceMaxAge := flags.String("source-max-age", service.DefaultRetentionPolicy.SourceMaxAge, "minimum age for terminal deployment sources")
		artifactMaxAge := flags.String("artifact-max-age", service.DefaultRetentionPolicy.ArtifactMaxAge, "minimum age for inactive revision artifacts")
		keepRetryable := flags.Int("keep-retryable-sources", service.DefaultRetentionPolicy.KeepRetryableSources, "newest retryable sources retained per automation")
		keepRevisions := flags.Int("keep-inactive-revisions", service.DefaultRetentionPolicy.KeepInactiveRevisions, "newest inactive revisions retained per automation")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("usage: werkt retention plan [flags]")
		}
		value, err := client.CreateRetentionPlan(ctx, domain.RetentionPolicy{
			SourceMaxAge: *sourceMaxAge, ArtifactMaxAge: *artifactMaxAge,
			KeepRetryableSources: *keepRetryable, KeepInactiveRevisions: *keepRevisions,
		}, "cli")
		if err != nil {
			return err
		}
		return printJSON(value)
	case "get", "apply":
		if len(arguments) != 2 {
			return fmt.Errorf("usage: werkt retention %s PLAN_ID", arguments[0])
		}
		var value domain.RetentionPlan
		var err error
		if arguments[0] == "get" {
			value, err = client.GetRetentionPlan(ctx, arguments[1])
		} else {
			value, err = client.ApplyRetentionPlan(ctx, arguments[1], "cli")
		}
		if err != nil {
			return err
		}
		return printJSON(value)
	default:
		return fmt.Errorf("unknown retention command %q", arguments[0])
	}
}

func secretCommand(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("usage: werkt secret list|get|set|delete [NAME]")
	}
	configuration := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), configuration.DeployTimeout)
	defer cancel()
	client, err := managementclient.New(configuration.APIURL, configuration.ManagementToken, nil)
	if err != nil {
		return err
	}
	switch arguments[0] {
	case "list":
		if len(arguments) != 1 {
			return errors.New("usage: werkt secret list")
		}
		values, err := client.ListSecrets(ctx)
		if err != nil {
			return err
		}
		return printJSON(values)
	case "get":
		if len(arguments) != 2 {
			return errors.New("usage: werkt secret get NAME")
		}
		value, err := client.GetSecret(ctx, arguments[1])
		if err != nil {
			return err
		}
		return printJSON(value)
	case "set":
		flags := flag.NewFlagSet("secret set", flag.ContinueOnError)
		description := flags.String("description", "", "operator-facing purpose (never the value)")
		fromEnv := flags.String("from-env", "", "read the value from this local environment variable")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 1 {
			return errors.New("usage: werkt secret set [--description TEXT] [--from-env ENV] NAME")
		}
		var secretValue string
		if *fromEnv != "" {
			var exists bool
			secretValue, exists = os.LookupEnv(*fromEnv)
			if !exists {
				return fmt.Errorf("environment variable %s is not set", *fromEnv)
			}
		} else {
			info, statErr := os.Stdin.Stat()
			if statErr != nil {
				return statErr
			}
			if info.Mode()&os.ModeCharDevice != 0 {
				return errors.New("refusing to read a visible terminal; pipe the secret on stdin or use --from-env")
			}
			contents, readErr := io.ReadAll(io.LimitReader(os.Stdin, secretvault.MaximumValueBytes+1))
			if readErr != nil {
				return readErr
			}
			if len(contents) > secretvault.MaximumValueBytes {
				return fmt.Errorf("secret value exceeds %d bytes", secretvault.MaximumValueBytes)
			}
			secretValue = string(contents)
		}
		value, err := client.PutSecret(ctx, flags.Arg(0), secretValue, *description, "cli")
		if err != nil {
			return err
		}
		return printJSON(value)
	case "delete":
		if len(arguments) != 2 {
			return errors.New("usage: werkt secret delete NAME")
		}
		if err := client.DeleteSecret(ctx, arguments[1], "cli"); err != nil {
			return err
		}
		return printJSON(map[string]any{"deleted": true, "name": arguments[1]})
	default:
		return fmt.Errorf("unknown secret command %q", arguments[0])
	}
}

func recoveryCommand(arguments []string) error {
	if len(arguments) != 1 || arguments[0] != "verify" {
		return errors.New("usage: werkt recovery verify")
	}
	configuration := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), configuration.DeployTimeout)
	defer cancel()
	store, err := openStore(ctx, configuration)
	if err != nil {
		return fmt.Errorf("verify restored database: %w", err)
	}
	defer store.Close()
	vault, err := secretvault.New(store, configuration.SecretKey)
	if err != nil {
		return fmt.Errorf("verify restored secret key: %w", err)
	}
	verified, err := vault.VerifyAll(ctx)
	if err != nil {
		return fmt.Errorf("verify restored secrets: %w", err)
	}
	attestor, err := provenance.NewAttestor(configuration.SecretKey)
	if err != nil {
		return fmt.Errorf("verify restored provenance key: %w", err)
	}
	artifactsVerified, err := service.NewArtifactCustodian(store, attestor).VerifyAll(ctx)
	if err != nil {
		return fmt.Errorf("verify restored artifacts: %w", err)
	}
	return printJSON(map[string]any{"database": "ok", "secretsVerified": verified, "artifactsVerified": artifactsVerified})
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
	if err := config.ValidateDeploymentTarget(configuration); err != nil {
		return fmt.Errorf("validate deployment target: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := openStore(ctx, configuration)
	if err != nil {
		return err
	}
	defer store.Close()

	attestor, err := provenance.NewAttestor(configuration.SecretKey)
	if err != nil {
		return fmt.Errorf("configure artifact provenance: %w", err)
	}
	vault, err := secretvault.New(store, configuration.SecretKey)
	if err != nil {
		return fmt.Errorf("configure secret vault: %w", err)
	}
	custodian := service.NewArtifactCustodian(store, attestor)
	adopted, err := custodian.AdoptLegacy(ctx)
	if err != nil {
		return fmt.Errorf("adopt legacy artifact provenance: %w", err)
	}
	if adopted > 0 {
		slog.Warn("adopted pre-provenance artifacts during trusted upgrade", "artifacts", adopted)
	}
	var secretResolver runner.SecretResolver = vault
	executor, err := newExecutor(configuration, secretResolver)
	if err != nil {
		return err
	}
	executor = runner.NewVerifyingExecutor(executor, attestor)
	hostname, _ := os.Hostname()
	for index := range *workers {
		workerID := fmt.Sprintf("%s-%d-%d", hostname, os.Getpid(), index)
		go service.NewWorker(store, executor, workerID, configuration.WorkerPoll).Run(ctx)
	}
	builder, err := newBuilder(configuration)
	if err != nil {
		return err
	}
	var pinImages func(domain.Manifest) (domain.Manifest, error)
	if configuration.Executor == "husker" {
		pinImages = provenance.PinHuskerImages
	}
	deployer := service.NewDeployer(store, configuration.DataDir, builder, attestor, pinImages)
	deploymentWorkerID := fmt.Sprintf("%s-%d-deployments", hostname, os.Getpid())
	go service.NewDeploymentWorker(store, deployer, deploymentWorkerID, configuration.DeploymentPoll).Run(ctx)
	go service.NewScheduler(store, configuration.SchedulerPoll).Run(ctx)
	go service.NewNtfyReconciler(store, secretResolver).Run(ctx)

	managementTokens := []string{
		configuration.ManagementToken,
		configuration.ManagementReadToken,
		configuration.ManagementOperateToken,
		configuration.ManagementDeployToken,
		configuration.ManagementSecretsToken,
		configuration.ManagementRetentionToken,
	}
	if strings.TrimSpace(strings.Join(managementTokens, "")) == "" {
		slog.Warn("management API authentication is disabled; configure a management token outside local development")
	}
	if configuration.Executor == "process" {
		slog.Warn("process executor runs deployed build and runtime commands on this host; use husker before accepting untrusted packages")
	}
	intake := service.NewDeploymentIntake(store, configuration.DataDir, packageio.Limits{
		CompressedBytes: configuration.MaxPackageBytes,
		ExpandedBytes:   configuration.MaxExpandedPackageBytes,
		Entries:         configuration.MaxPackageEntries,
	})
	retention := service.NewRetentionManager(store, configuration.DataDir)
	instance := configuration.Instance
	if instance == "" {
		instance = configuration.ListenAddress
	}
	apiOptions := []httpapi.Option{
		httpapi.WithDeploymentIntake(intake),
		httpapi.WithRetentionManager(retention),
		httpapi.WithArtifactVerifier(custodian),
		httpapi.WithOperatorScope(configuration.Environment, instance),
		httpapi.WithScopedManagementToken(configuration.ManagementReadToken, httpapi.ScopeRead),
		httpapi.WithScopedManagementToken(configuration.ManagementOperateToken, httpapi.ScopeRead, httpapi.ScopeOperate),
		httpapi.WithScopedManagementToken(configuration.ManagementDeployToken, httpapi.ScopeRead, httpapi.ScopeDeploy),
		httpapi.WithScopedManagementToken(configuration.ManagementSecretsToken, httpapi.ScopeRead, httpapi.ScopeSecrets),
		httpapi.WithScopedManagementToken(configuration.ManagementRetentionToken, httpapi.ScopeRead, httpapi.ScopeRetention),
	}
	if vault != nil {
		apiOptions = append(apiOptions, httpapi.WithSecretManager(vault))
	}
	oidcValues := []string{configuration.OIDCIssuer, configuration.OIDCClientID, configuration.OIDCClientSecret, configuration.OIDCRedirectURL, configuration.OIDCSessionSecret}
	if strings.TrimSpace(strings.Join(oidcValues, "")) != "" || len(configuration.OIDCAllowedEmails) > 0 {
		browserAuth, err := httpapi.NewBrowserAuth(httpapi.BrowserAuthConfig{
			Issuer: configuration.OIDCIssuer, ClientID: configuration.OIDCClientID, ClientSecret: configuration.OIDCClientSecret,
			RedirectURL: configuration.OIDCRedirectURL, AllowedEmails: configuration.OIDCAllowedEmails,
			SessionSecret: configuration.OIDCSessionSecret, SessionTTL: configuration.OIDCSessionTTL,
		})
		if err != nil {
			return fmt.Errorf("configure browser authentication: %w", err)
		}
		apiOptions = append(apiOptions, httpapi.WithBrowserAuth(browserAuth))
	}
	api := httpapi.New(store, configuration.ListenAddress, configuration.ManagementToken, apiOptions...)
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

func newExecutor(configuration config.Config, secrets runner.SecretResolver) (runner.Executor, error) {
	switch configuration.Executor {
	case "process":
		return runner.NewProcessRunner(secrets), nil
	case "husker":
		return newHuskerRunner(configuration, secrets)
	default:
		return nil, fmt.Errorf("unsupported executor %q (must be process or husker)", configuration.Executor)
	}
}

func newBuilder(configuration config.Config) (service.Builder, error) {
	switch configuration.Executor {
	case "process":
		return runner.NewProcessRunner(), nil
	case "husker":
		return newHuskerRunner(configuration, nil)
	default:
		return nil, fmt.Errorf("unsupported executor %q (must be process or husker)", configuration.Executor)
	}
}

func newHuskerRunner(configuration config.Config, secrets runner.SecretResolver) (*runner.HuskerRunner, error) {
	return runner.NewHuskerRunner(runner.HuskerConfig{
		URL:              configuration.HuskerURL,
		Token:            configuration.HuskerToken,
		RootFS:           configuration.HuskerRootFS,
		Kernel:           configuration.HuskerKernel,
		VCPUs:            configuration.HuskerVCPUs,
		MemoryMiB:        configuration.HuskerMemory,
		BuildNetwork:     configuration.HuskerBuildNetwork,
		BuildTimeout:     configuration.HuskerBuildTimeout,
		ProvisionTimeout: configuration.HuskerProvisionTimeout,
		CleanupTimeout:   configuration.HuskerCleanupTimeout,
		Secrets:          secrets,
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
	store, err := database.Open(ctx, configuration.DatabaseURL, configuration.DataDir)
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
	return buildinfo.Current().Version
}

func usage() {
	executable := filepath.Base(os.Args[0])
	fmt.Fprintf(os.Stderr, `Usage:
  %s validate [directory]
  %s deploy [-api URL] [-idempotency-key KEY] [-wait=true] [directory]
  %s deployment get|cancel|retry DEPLOYMENT_ID
  %s rollback AUTOMATION_ID REVISION_ID
  %s retention plan|get|apply [PLAN_ID]
  %s secret list|get|set|delete [NAME]
  %s recovery verify
  %s serve [-workers N]
  %s automations
  %s runs [-limit N]
  %s version
	`, executable, executable, executable, executable, executable, executable, executable, executable, executable, executable, executable)
}
