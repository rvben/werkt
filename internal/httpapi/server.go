package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"strconv"
	"time"

	"github.com/rvben/werkt/internal/database"
)

const maxWebhookBody = 2 << 20
const maxEmailBody = 12 << 20

type Server struct {
	store  *database.Store
	server *http.Server
}

func New(store *database.Store, address string) *Server {
	value := &Server{store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", value.health)
	mux.HandleFunc("POST /api/v1/hooks/{automation}/{trigger}", value.webhook)
	mux.HandleFunc("POST /api/v1/email/{automation}/{trigger}", value.email)
	mux.HandleFunc("GET /api/v1/automations", value.automations)
	mux.HandleFunc("GET /api/v1/runs", value.runs)
	value.server = &http.Server{
		Addr:              address,
		Handler:           requestLogger(mux),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return value
}

func (s *Server) email(response http.ResponseWriter, request *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxEmailBody))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid or oversized email")
		return
	}
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		writeError(response, http.StatusBadRequest, "body must be an RFC 5322 email message")
		return
	}
	body, err := io.ReadAll(message.Body)
	if err != nil {
		writeError(response, http.StatusBadRequest, "read email body")
		return
	}
	data, _ := json.Marshal(map[string]any{
		"messageId": message.Header.Get("Message-Id"),
		"from":      message.Header.Get("From"),
		"to":        message.Header.Get("To"),
		"subject":   message.Header.Get("Subject"),
		"date":      message.Header.Get("Date"),
		"body":      string(body),
	})
	externalID := message.Header.Get("Message-Id")
	if externalID == "" {
		externalID = request.Header.Get("Idempotency-Key")
	}
	occurredAt := time.Now().UTC()
	if parsed, err := message.Header.Date(); err == nil {
		occurredAt = parsed
	}
	runID, created, err := s.store.IngestEvent(
		request.Context(), request.PathValue("automation"), request.PathValue("trigger"),
		"email", externalID, occurredAt, data, map[string]any{"source": "email"},
	)
	if err != nil {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	status := http.StatusAccepted
	if !created {
		status = http.StatusOK
	}
	writeJSON(response, status, map[string]any{"runId": runID, "created": created})
}

func (s *Server) ListenAndServe() error {
	err := s.server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

func (s *Server) health(response http.ResponseWriter, request *http.Request) {
	if err := s.store.Ping(request.Context()); err != nil {
		writeError(response, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) webhook(response http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxWebhookBody))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid or oversized request body")
		return
	}
	data := json.RawMessage(body)
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	} else if !json.Valid(data) {
		data, _ = json.Marshal(map[string]string{"body": string(body)})
	}
	metadata := map[string]any{
		"source":      "webhook",
		"contentType": request.Header.Get("Content-Type"),
		"userAgent":   request.UserAgent(),
	}
	runID, created, err := s.store.IngestEvent(
		request.Context(), request.PathValue("automation"), request.PathValue("trigger"),
		"webhook", request.Header.Get("Idempotency-Key"), time.Now().UTC(), data, metadata,
	)
	if err != nil {
		writeError(response, http.StatusNotFound, err.Error())
		return
	}
	status := http.StatusAccepted
	if !created {
		status = http.StatusOK
	}
	writeJSON(response, status, map[string]any{"runId": runID, "created": created})
}

func (s *Server) automations(response http.ResponseWriter, request *http.Request) {
	values, err := s.store.ListAutomations(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list automations")
		return
	}
	writeJSON(response, http.StatusOK, values)
}

func (s *Server) runs(response http.ResponseWriter, request *http.Request) {
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	values, err := s.store.ListRuns(request.Context(), limit)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list runs")
		return
	}
	writeJSON(response, http.StatusOK, values)
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	if err := json.NewEncoder(response).Encode(value); err != nil {
		slog.Error("encode HTTP response", "error", err)
	}
}

func writeError(response http.ResponseWriter, status int, message string) {
	writeJSON(response, status, map[string]string{"error": message})
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		started := time.Now()
		next.ServeHTTP(response, request)
		slog.Info("HTTP request", "method", request.Method, "path", request.URL.Path, "duration", fmt.Sprintf("%s", time.Since(started).Round(time.Millisecond)))
	})
}
