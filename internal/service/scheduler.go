package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/rvben/werkt/internal/database"
)

type Scheduler struct {
	store        *database.Store
	pollInterval time.Duration
}

func NewScheduler(store *database.Store, pollInterval time.Duration) *Scheduler {
	return &Scheduler{store: store, pollInterval: pollInterval}
}

func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	for {
		count, err := s.store.EnqueueDueSchedules(ctx, time.Now(), 100)
		if err != nil && ctx.Err() == nil {
			slog.Error("scheduler iteration failed", "error", err)
		} else if count > 0 {
			slog.Info("scheduled runs queued", "count", count)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
