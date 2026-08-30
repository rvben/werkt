package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/runner"
)

const (
	runLeaseDuration = 10 * time.Minute
	leaseRenewal     = 2 * time.Minute
)

type Worker struct {
	store        *database.Store
	executor     runner.Executor
	id           string
	pollInterval time.Duration
}

func NewWorker(store *database.Store, executor runner.Executor, id string, pollInterval time.Duration) *Worker {
	return &Worker{store: store, executor: executor, id: id, pollInterval: pollInterval}
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		if err := w.runOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Error("worker iteration failed", "worker", w.id, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) runOnce(ctx context.Context) error {
	run, err := w.store.AcquireRun(ctx, w.id, runLeaseDuration)
	if err != nil || run == nil {
		return err
	}
	slog.Info("run started", "run", run.ID, "automation", run.AutomationID, "attempt", run.Attempt)
	executionContext, cancelExecution := context.WithCancelCause(ctx)
	renewalDone := make(chan struct{})
	go w.renewLease(executionContext, cancelExecution, run.ID, renewalDone)
	result, executeErr := w.executor.Execute(executionContext, *run)
	cancelExecution(nil)
	<-renewalDone
	if cause := context.Cause(executionContext); cause != nil && !errors.Is(cause, context.Canceled) {
		executeErr = errors.Join(executeErr, cause)
	}
	if executeErr != nil {
		if err := w.store.FailRun(ctx, run.Run, w.id, result.Logs, executeErr); err != nil {
			return err
		}
		slog.Warn("run failed", "run", run.ID, "automation", run.AutomationID, "error", executeErr)
		return nil
	}
	if err := w.store.CompleteRun(ctx, *run, w.id, result.Logs, result.Output, result.State, result.Control); err != nil {
		return err
	}
	slog.Info("run succeeded", "run", run.ID, "automation", run.AutomationID)
	return nil
}

func (w *Worker) renewLease(ctx context.Context, cancel context.CancelCauseFunc, runID string, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(leaseRenewal)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			renewed, err := w.store.RenewRunLease(ctx, runID, w.id, runLeaseDuration)
			if err != nil {
				slog.Error("run lease renewal failed", "worker", w.id, "run", runID, "error", err)
				continue
			}
			if !renewed {
				cancel(fmt.Errorf("run lease ownership was lost"))
				return
			}
		}
	}
}
