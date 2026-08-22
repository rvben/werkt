package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
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
	renewalDone := make(chan struct{})
	go w.renewLease(executionContext, cancelExecution, deployment.ID, renewalDone)

	deploymentErr := w.process(executionContext, *deployment)
	cancelExecution(nil)
	<-renewalDone
	if cause := context.Cause(executionContext); cause != nil && !errors.Is(cause, context.Canceled) {
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
		_ = os.RemoveAll(deployment.SourcePath)
		slog.Warn("deployment failed", "deployment", deployment.ID, "error", deploymentErr)
		return nil
	}
	_ = os.RemoveAll(deployment.SourcePath)
	slog.Info("deployment succeeded", "deployment", deployment.ID)
	return nil
}

func (w *DeploymentWorker) process(ctx context.Context, deployment domain.RunnableDeployment) error {
	prepared, err := w.deployer.Prepare(deployment.SourcePath)
	if err != nil {
		return fmt.Errorf("validate package: %w", err)
	}
	if len(prepared.Manifest.Runtime.Build) > 0 {
		if err := w.store.SetDeploymentStage(
			ctx, deployment.ID, w.id, domain.DeploymentBuilding,
			prepared.Manifest.Metadata.Name, prepared.ContentHash,
		); err != nil {
			return err
		}
	}
	built, err := w.deployer.BuildArtifact(ctx, prepared)
	if err != nil {
		return fmt.Errorf("build package: %w", err)
	}
	if err := w.store.SetDeploymentStage(
		ctx, deployment.ID, w.id, domain.DeploymentActivating,
		prepared.Manifest.Metadata.Name, prepared.ContentHash,
	); err != nil {
		return err
	}
	_, err = w.deployer.ActivateDeployment(ctx, built, deployment.ID, w.id, deployment.Actor)
	return err
}

func (w *DeploymentWorker) renewLease(ctx context.Context, cancel context.CancelCauseFunc, deploymentID string, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(leaseRenewal)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
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
