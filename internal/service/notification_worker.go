package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/notification"
)

type NotificationWorker struct {
	store          *database.Store
	config         notification.Config
	sender         *notification.Sender
	publicURL      string
	id             string
	pollInterval   time.Duration
	expiringWindow time.Duration
	lastExpiryScan time.Time
}

func NewNotificationWorker(store *database.Store, config notification.Config, sender *notification.Sender, publicURL, id string, pollInterval, expiringWindow time.Duration) *NotificationWorker {
	return &NotificationWorker{
		store: store, config: config, sender: sender, publicURL: publicURL,
		id: id, pollInterval: pollInterval, expiringWindow: expiringWindow,
	}
}

func (w *NotificationWorker) Run(ctx context.Context) {
	if len(w.config.Routes) == 0 {
		slog.Info("notification delivery is disabled; no routes configured")
	}
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		if err := w.runOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Error("notification worker iteration failed", "worker", w.id, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *NotificationWorker) runOnce(ctx context.Context) error {
	if w.expiringWindow > 0 && (w.lastExpiryScan.IsZero() || time.Since(w.lastExpiryScan) >= time.Minute) {
		if _, err := w.store.EnqueueExpiringApprovalNotifications(ctx, time.Now().UTC().Add(w.expiringWindow), 100); err != nil {
			return fmt.Errorf("scan expiring approvals: %w", err)
		}
		w.lastExpiryScan = time.Now()
	}
	if event, err := w.store.AcquireNotificationEvent(ctx); err != nil {
		return err
	} else if event != nil {
		targets, err := w.targetsFor(*event)
		if err != nil {
			return err
		}
		if err := w.store.ExpandNotificationEvent(ctx, event.ID, targets); err != nil {
			return err
		}
	}
	delivery, err := w.store.AcquireNotificationDelivery(ctx, w.id)
	if err != nil || delivery == nil {
		return err
	}
	message, err := notification.Render(delivery.EventType, delivery.AutomationID, delivery.SubjectID, delivery.Payload, w.publicURL, delivery.EventCreatedAt)
	if err != nil {
		return w.store.FailNotificationDelivery(ctx, *delivery, w.id, "render notification message", false)
	}
	var destination notification.Destination
	if err := json.Unmarshal(delivery.Config, &destination); err != nil {
		return w.store.FailNotificationDelivery(ctx, *delivery, w.id, "decode notification destination", false)
	}
	responseCode, sendErr := w.sender.Send(ctx, destination, message, delivery.ID)
	if sendErr != nil {
		var deliveryErr *notification.DeliveryError
		retryable := errors.As(sendErr, &deliveryErr) && deliveryErr.Retryable
		if err := w.store.FailNotificationDelivery(ctx, *delivery, w.id, sendErr.Error(), retryable); err != nil {
			return err
		}
		slog.Warn("notification delivery failed", "delivery", delivery.ID, "destination", delivery.DestinationID, "provider", delivery.Provider, "attempt", delivery.Attempt, "retryable", retryable)
		return nil
	}
	if err := w.store.CompleteNotificationDelivery(ctx, *delivery, w.id, responseCode); err != nil {
		return err
	}
	slog.Info("notification delivered", "delivery", delivery.ID, "destination", delivery.DestinationID, "provider", delivery.Provider)
	return nil
}

func (w *NotificationWorker) targetsFor(event database.NotificationEvent) ([]database.NotificationTarget, error) {
	destinations := w.config.DestinationsFor(event.Type, event.AutomationID)
	targets := make([]database.NotificationTarget, 0, len(destinations))
	for _, destination := range destinations {
		encoded, err := json.Marshal(destination)
		if err != nil {
			return nil, err
		}
		targets = append(targets, database.NotificationTarget{
			DestinationID: destination.ID, Provider: destination.Provider, Config: encoded,
		})
	}
	return targets, nil
}
