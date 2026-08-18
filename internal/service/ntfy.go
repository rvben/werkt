package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/domain"
)

type NtfyReconciler struct {
	store  *database.Store
	client *http.Client
	mu     sync.Mutex
	active map[string]context.CancelFunc
}

type ntfyConfig struct {
	Server   string `json:"server"`
	Topic    string `json:"topic"`
	TokenEnv string `json:"tokenEnv,omitempty"`
}

type ntfyMessage struct {
	ID       string   `json:"id"`
	Time     int64    `json:"time"`
	Event    string   `json:"event"`
	Topic    string   `json:"topic"`
	Title    string   `json:"title,omitempty"`
	Message  string   `json:"message,omitempty"`
	Priority int      `json:"priority,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

func NewNtfyReconciler(store *database.Store) *NtfyReconciler {
	return &NtfyReconciler{
		store:  store,
		client: &http.Client{Timeout: 0},
		active: make(map[string]context.CancelFunc),
	}
}

func (r *NtfyReconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	defer r.stopAll()
	for {
		if err := r.reconcile(ctx); err != nil && ctx.Err() == nil {
			slog.Error("ntfy reconciliation failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *NtfyReconciler) reconcile(ctx context.Context) error {
	definitions, err := r.store.ListNtfyTriggers(ctx)
	if err != nil {
		return err
	}
	desired := make(map[string]domain.TriggerDefinition, len(definitions))
	for _, definition := range definitions {
		key := ntfyKey(definition)
		desired[key] = definition
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for key, cancel := range r.active {
		if _, exists := desired[key]; !exists {
			cancel()
			delete(r.active, key)
		}
	}
	for key, definition := range desired {
		if _, exists := r.active[key]; exists {
			continue
		}
		subscriptionContext, cancel := context.WithCancel(ctx)
		r.active[key] = cancel
		go r.subscribe(subscriptionContext, definition)
	}
	return nil
}

func (r *NtfyReconciler) subscribe(ctx context.Context, definition domain.TriggerDefinition) {
	var config ntfyConfig
	if err := json.Unmarshal(definition.Config, &config); err != nil {
		slog.Error("invalid ntfy trigger configuration", "trigger", ntfyKey(definition), "error", err)
		return
	}
	if config.Server == "" || config.Topic == "" {
		slog.Error("ntfy trigger requires server and topic", "trigger", ntfyKey(definition))
		return
	}
	backoff := time.Second
	for ctx.Err() == nil {
		err := r.consume(ctx, definition, config)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("ntfy subscription disconnected", "trigger", ntfyKey(definition), "error", err, "retryIn", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func (r *NtfyReconciler) consume(ctx context.Context, definition domain.TriggerDefinition, config ntfyConfig) error {
	endpoint := strings.TrimRight(config.Server, "/") + "/" + url.PathEscape(config.Topic) + "/json?since=all"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if config.TokenEnv != "" {
		token := os.Getenv(config.TokenEnv)
		if token == "" {
			return fmt.Errorf("environment variable %s is empty", config.TokenEnv)
		}
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := r.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("ntfy returned %s", response.Status)
	}

	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var message ntfyMessage
		if err := json.Unmarshal(line, &message); err != nil {
			slog.Warn("ignored malformed ntfy message", "trigger", ntfyKey(definition), "error", err)
			continue
		}
		if message.Event != "message" {
			continue
		}
		occurredAt := time.Unix(message.Time, 0).UTC()
		_, created, err := r.store.IngestEvent(ctx, definition.AutomationID, definition.TriggerID, "ntfy", message.ID, occurredAt, line, map[string]any{
			"source": "ntfy",
			"topic":  message.Topic,
		})
		if err != nil {
			return err
		}
		if created {
			slog.Info("ntfy run queued", "automation", definition.AutomationID, "trigger", definition.TriggerID, "message", message.ID)
		}
	}
	return scanner.Err()
}

func (r *NtfyReconciler) stopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, cancel := range r.active {
		cancel()
		delete(r.active, key)
	}
}

func ntfyKey(definition domain.TriggerDefinition) string {
	return definition.AutomationID + ":" + definition.TriggerID + ":" + definition.RevisionID
}
