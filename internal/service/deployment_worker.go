package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/domain"
)

type DeploymentWorker struct {
	store        *database.Store
	deployer     *Deployer
	id           string
	pollInterval time.Duration
}

func NewDeploymentWorker(store *database.Store, deployer *Deployer, id string, pollInterval time.Duration) *DeploymentWorker {
	return &DeploymentWorker{store: store, deployer: deployer, id: id, pollInterval: pollInterval}
}

func (w *DeploymentWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		if err := w.runOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Error("deployment worker iteration failed", "worker", w.id, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *DeploymentWorker) runOnce(ctx context.Context) error {
	deployment, err := w.store.AcquireDeployment(ctx, w.id, runLeaseDuration)
	if err != nil || deployment == nil {
		return err
	}
	slog.Info("deployment started", "deployment", deployment.ID, "actor", deployment.Actor)
	executionContext, cancelExecution := context.WithCancelCause(ctx)
	monitorDone := make(chan struct{})
	go w.monitor(executionContext, cancelExecution, deployment.ID, monitorDone)

	deploymentErr := w.process(executionContext, *deployment)
	cause := context.Cause(executionContext)
	checkContext, cancelCheck := context.WithTimeout(context.Background(), 2*time.Second)
	requested, checkErr := w.store.DeploymentCancellationRequested(checkContext, deployment.ID, w.id)
	cancelCheck()
	if checkErr == nil && requested {
		cause = database.ErrDeploymentCancelled
	} else if checkErr != nil && !errors.Is(checkErr, database.ErrDeploymentLeaseLost) {
		deploymentErr = errors.Join(deploymentErr, checkErr)
	}
	cancelExecution(nil)
	<-monitorDone
	if errors.Is(cause, database.ErrDeploymentCancelled) || errors.Is(deploymentErr, database.ErrDeploymentCancelled) {
		completionContext, cancelCompletion := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelCompletion()
		if err := w.store.CompleteDeploymentCancellation(completionContext, deployment.ID, w.id); err != nil {
			return err
		}
		slog.Info("deployment cancelled", "deployment", deployment.ID)
		return nil
	}
	if cause != nil && !errors.Is(cause, context.Canceled) {
		deploymentErr = errors.Join(deploymentErr, cause)
	}
	if deploymentErr != nil {
		if errors.Is(deploymentErr, database.ErrDeploymentLeaseLost) {
			return deploymentErr
		}
		if ctx.Err() != nil {
			return nil
		}
		failureContext, cancelFailure := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelFailure()
		if err := w.store.FailDeployment(failureContext, deployment.ID, w.id, deploymentErr); err != nil {
			return errors.Join(deploymentErr, err)
		}
		slog.Warn("deployment failed", "deployment", deployment.ID, "error", deploymentErr)
		return nil
	}
	slog.Info("deployment succeeded", "deployment", deployment.ID)
	return nil
}

func (w *DeploymentWorker) process(ctx context.Context, deployment domain.RunnableDeployment) error {
	report := func(update domain.DeploymentStepUpdate) error {
		return w.store.RecordDeploymentStep(ctx, deployment.ID, w.id, update)
	}
	if err := report(domain.DeploymentStepUpdate{ID: "validate", Kind: "validate", Status: domain.DeploymentStepRunning}); err != nil {
		return err
	}
	prepared, err := w.deployer.Prepare(deployment.SourcePath)
	if err != nil {
		_ = report(domain.DeploymentStepUpdate{ID: "validate", Kind: "validate", Status: domain.DeploymentStepFailed, Error: err.Error()})
		return fmt.Errorf("validate package: %w", err)
	}
	if err := report(domain.DeploymentStepUpdate{ID: "validate", Kind: "validate", Status: domain.DeploymentStepSucceeded}); err != nil {
		return err
	}
	if len(prepared.Manifest.Runtime.Build) > 0 || len(prepared.Manifest.Deployment.Checks) > 0 {
		stage := domain.DeploymentBuilding
		if len(prepared.Manifest.Runtime.Build) == 0 {
			stage = domain.DeploymentChecking
		}
		if err := w.store.SetDeploymentStage(
			ctx, deployment.ID, w.id, stage,
			prepared.Manifest.Metadata.Name, prepared.ContentHash,
		); err != nil {
			return err
		}
	}
	built, err := w.deployer.BuildArtifact(ctx, prepared, report)
	if err != nil {
		return fmt.Errorf("build package: %w", err)
	}
	if err := w.store.SetDeploymentStage(
		ctx, deployment.ID, w.id, domain.DeploymentActivating,
		prepared.Manifest.Metadata.Name, prepared.ContentHash,
	); err != nil {
		return err
	}
	if err := report(domain.DeploymentStepUpdate{ID: "activate", Kind: "activate", Status: domain.DeploymentStepRunning}); err != nil {
		return err
	}
	_, err = w.deployer.ActivateDeployment(ctx, built, deployment.ID, w.id, deployment.Actor)
	return err
}

func (w *DeploymentWorker) monitor(ctx context.Context, cancel context.CancelCauseFunc, deploymentID string, done chan<- struct{}) {
	defer close(done)
	checkEvery := w.pollInterval
	if checkEvery <= 0 || checkEvery > time.Second {
		checkEvery = time.Second
	}
	cancellationTicker := time.NewTicker(checkEvery)
	defer cancellationTicker.Stop()
	renewalTicker := time.NewTicker(leaseRenewal)
	defer renewalTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-cancellationTicker.C:
			requested, err := w.store.DeploymentCancellationRequested(ctx, deploymentID, w.id)
			if errors.Is(err, database.ErrDeploymentLeaseLost) {
				cancel(err)
				return
			}
			if err != nil {
				slog.Error("deployment cancellation check failed", "worker", w.id, "deployment", deploymentID, "error", err)
				continue
			}
			if requested {
				cancel(database.ErrDeploymentCancelled)
				return
			}
		case <-renewalTicker.C:
			renewed, err := w.store.RenewDeploymentLease(ctx, deploymentID, w.id, runLeaseDuration)
			if err != nil {
				slog.Error("deployment lease renewal failed", "worker", w.id, "deployment", deploymentID, "error", err)
				continue
			}
			if !renewed {
				cancel(database.ErrDeploymentLeaseLost)
				return
			}
		}
	}
}
