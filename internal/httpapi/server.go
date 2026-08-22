package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/domain"
	"github.com/rvben/werkt/internal/packageio"
	"github.com/rvben/werkt/internal/secretvault"
	"github.com/rvben/werkt/internal/service"
)

const maxWebhookBody = 2 << 20
const maxEmailBody = 12 << 20
const minimumIngressSecretBytes = 32
const defaultWebhookSignatureHeader = "X-Werkt-Signature"
const webhookTimestampHeader = "X-Werkt-Timestamp"
const webhookSignatureTolerance = 5 * time.Minute

//go:embed openapi.yaml
var openAPIFS embed.FS

type Server struct {
	store            Store
	deploymentIntake DeploymentIntake
	retention        RetentionManager
	secrets          SecretManager
	managementToken  string
	server           *http.Server
}

type Store interface {
	Ping(context.Context) error
	IngestEvent(context.Context, string, string, string, string, time.Time, json.RawMessage, map[string]any) (string, bool, error)
	GetTriggerIngressPolicy(context.Context, string, string, string) (database.TriggerIngressPolicy, error)
	ListAutomations(context.Context, database.AutomationFilter) ([]database.AutomationSummary, error)
	GetAutomation(context.Context, string) (database.AutomationDetail, error)
	SetAutomationEnabled(context.Context, string, bool, string) (bool, error)
	RollbackAutomation(context.Context, string, string, string) (bool, error)
	EnqueueManualRun(context.Context, string, string, json.RawMessage, string) (string, bool, error)
	ListRunsFiltered(context.Context, string, string, int) ([]domain.Run, error)
	GetRun(context.Context, string) (domain.Run, error)
	ListAuditEvents(context.Context, string, int) ([]database.AuditEvent, error)
	ListDeploymentsFiltered(context.Context, string, string, int) ([]domain.Deployment, error)
	GetDeployment(context.Context, string) (domain.Deployment, error)
	RequestDeploymentCancellation(context.Context, string, string) (domain.Deployment, error)
	RetryDeployment(context.Context, string, string, string, string) (domain.Deployment, bool, error)
}

type DeploymentIntake interface {
	Accept(context.Context, io.Reader, string, string, string) (domain.Deployment, bool, error)
}

type RetentionManager interface {
	Plan(context.Context, domain.RetentionPolicy, string) (domain.RetentionPlan, error)
	Get(context.Context, string) (domain.RetentionPlan, error)
	Apply(context.Context, string, string) (domain.RetentionPlan, error)
}

type SecretManager interface {
	List(context.Context) ([]domain.SecretMetadata, error)
	Get(context.Context, string) (domain.SecretMetadata, error)
	Put(context.Context, string, string, string, string) (domain.SecretMetadata, bool, error)
	Delete(context.Context, string, string) error
	Resolve(context.Context, []string) (map[string]string, error)
}

type Option func(*Server)

func WithDeploymentIntake(intake DeploymentIntake) Option {
	return func(server *Server) { server.deploymentIntake = intake }
}

func WithRetentionManager(manager RetentionManager) Option {
	return func(server *Server) { server.retention = manager }
}

func WithSecretManager(manager SecretManager) Option {
	return func(server *Server) { server.secrets = manager }
}

func New(store Store, address, managementToken string, options ...Option) *Server {
	value := &Server{store: store, managementToken: managementToken}
	for _, option := range options {
		option(value)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", value.workspaceRoot)
	mux.HandleFunc("GET /app", value.workspaceRoot)
	mux.HandleFunc("GET /app/", value.workspace)
	mux.HandleFunc("GET /app/{asset}", value.workspace)
	mux.HandleFunc("GET /healthz", value.health)
	mux.HandleFunc("GET /api/openapi.yaml", value.openAPI)
	mux.HandleFunc("POST /api/v1/hooks/{automation}/{trigger}", value.webhook)
	mux.HandleFunc("POST /api/v1/email/{automation}/{trigger}", value.email)
	mux.Handle("GET /api/v1/automations", value.requireManagementAuth(http.HandlerFunc(value.automations)))
	mux.Handle("GET /api/v1/automations/{automation}", value.requireManagementAuth(http.HandlerFunc(value.automation)))
	mux.Handle("PATCH /api/v1/automations/{automation}", value.requireManagementAuth(http.HandlerFunc(value.updateAutomation)))
	mux.Handle("POST /api/v1/automations/{automation}/runs", value.requireManagementAuth(http.HandlerFunc(value.manualRun)))
	mux.Handle("POST /api/v1/automations/{automation}/rollback", value.requireManagementAuth(http.HandlerFunc(value.rollbackAutomation)))
	mux.Handle("GET /api/v1/runs", value.requireManagementAuth(http.HandlerFunc(value.runs)))
	mux.Handle("GET /api/v1/runs/{run}", value.requireManagementAuth(http.HandlerFunc(value.run)))
	mux.Handle("GET /api/v1/audit", value.requireManagementAuth(http.HandlerFunc(value.audit)))
	mux.Handle("POST /api/v1/deployments", value.requireManagementAuth(http.HandlerFunc(value.createDeployment)))
	mux.Handle("GET /api/v1/deployments", value.requireManagementAuth(http.HandlerFunc(value.deployments)))
	mux.Handle("GET /api/v1/deployments/{deployment}", value.requireManagementAuth(http.HandlerFunc(value.deployment)))
	mux.Handle("POST /api/v1/deployments/{deployment}/cancel", value.requireManagementAuth(http.HandlerFunc(value.cancelDeployment)))
	mux.Handle("POST /api/v1/deployments/{deployment}/retry", value.requireManagementAuth(http.HandlerFunc(value.retryDeployment)))
	mux.Handle("POST /api/v1/retention/plans", value.requireManagementAuth(http.HandlerFunc(value.createRetentionPlan)))
	mux.Handle("GET /api/v1/retention/plans/{plan}", value.requireManagementAuth(http.HandlerFunc(value.retentionPlan)))
	mux.Handle("POST /api/v1/retention/plans/{plan}/apply", value.requireManagementAuth(http.HandlerFunc(value.applyRetentionPlan)))
	mux.Handle("GET /api/v1/secrets", value.requireManagementAuth(http.HandlerFunc(value.secretsList)))
	mux.Handle("GET /api/v1/secrets/{secret}", value.requireManagementAuth(http.HandlerFunc(value.secret)))
	mux.Handle("PUT /api/v1/secrets/{secret}", value.requireManagementAuth(http.HandlerFunc(value.putSecret)))
	mux.Handle("DELETE /api/v1/secrets/{secret}", value.requireManagementAuth(http.HandlerFunc(value.deleteSecret)))
	value.server = &http.Server{
		Addr:              address,
		Handler:           requestLogger(mux),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return value
}

func (s *Server) createDeployment(response http.ResponseWriter, request *http.Request) {
	if s.deploymentIntake == nil {
		writeError(response, http.StatusServiceUnavailable, "deployment intake is not configured")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/gzip" && mediaType != "application/x-gzip") {
		writeError(response, http.StatusUnsupportedMediaType, "Content-Type must be application/gzip")
		return
	}
	value, created, err := s.deploymentIntake.Accept(
		request.Context(), request.Body, request.Header.Get("X-Werkt-Content-SHA256"),
		request.Header.Get("Idempotency-Key"), requestActor(request),
	)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidIdempotencyKey), errors.Is(err, service.ErrInvalidDeploymentDigest):
			writeError(response, http.StatusBadRequest, err.Error())
		case errors.Is(err, service.ErrDeploymentDigestMismatch):
			writeError(response, http.StatusUnprocessableEntity, err.Error())
		case errors.Is(err, database.ErrDeploymentIdempotencyConflict):
			writeError(response, http.StatusConflict, err.Error())
		case errors.Is(err, packageio.ErrCompressedLimit), errors.Is(err, packageio.ErrExpandedLimit), errors.Is(err, packageio.ErrEntryLimit):
			writeError(response, http.StatusRequestEntityTooLarge, err.Error())
		case errors.Is(err, packageio.ErrUnsafeArchive), errors.Is(err, packageio.ErrInvalidArchive):
			writeError(response, http.StatusBadRequest, err.Error())
		default:
			slog.Error("create deployment", "error", err)
			writeError(response, http.StatusInternalServerError, "create deployment")
		}
		return
	}
	status := http.StatusAccepted
	if !created {
		status = http.StatusOK
	}
	response.Header().Set("Location", "/api/v1/deployments/"+value.ID)
	if !terminalDeploymentStatus(value.Status) {
		response.Header().Set("Retry-After", "1")
	}
	writeJSON(response, status, map[string]any{"deployment": value, "created": created})
}

func (s *Server) deployments(response http.ResponseWriter, request *http.Request) {
	limit, err := requestLimit(request, 100)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	status := request.URL.Query().Get("status")
	if status != "" && !validDeploymentStatus(status) {
		writeError(response, http.StatusBadRequest, "status must be queued, validating, building, checking, activating, succeeded, failed, or cancelled")
		return
	}
	values, err := s.store.ListDeploymentsFiltered(request.Context(), request.URL.Query().Get("automation"), status, limit)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list deployments")
		return
	}
	writeJSON(response, http.StatusOK, values)
}

func (s *Server) deployment(response http.ResponseWriter, request *http.Request) {
	value, err := s.store.GetDeployment(request.Context(), request.PathValue("deployment"))
	if errors.Is(err, database.ErrDeploymentNotFound) {
		writeError(response, http.StatusNotFound, "deployment not found")
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "get deployment")
		return
	}
	if !terminalDeploymentStatus(value.Status) {
		response.Header().Set("Retry-After", "1")
	}
	writeJSON(response, http.StatusOK, value)
}

func validDeploymentStatus(status string) bool {
	switch status {
	case domain.DeploymentQueued, domain.DeploymentValidating, domain.DeploymentBuilding,
		domain.DeploymentChecking, domain.DeploymentActivating, domain.DeploymentSucceeded,
		domain.DeploymentFailed, domain.DeploymentCancelled:
		return true
	default:
		return false
	}
}

func terminalDeploymentStatus(status string) bool {
	return status == domain.DeploymentSucceeded || status == domain.DeploymentFailed || status == domain.DeploymentCancelled
}

func (s *Server) cancelDeployment(response http.ResponseWriter, request *http.Request) {
	value, err := s.store.RequestDeploymentCancellation(request.Context(), request.PathValue("deployment"), requestActor(request))
	switch {
	case errors.Is(err, database.ErrDeploymentNotFound):
		writeError(response, http.StatusNotFound, "deployment not found")
	case errors.Is(err, database.ErrDeploymentNotCancellable):
		writeError(response, http.StatusConflict, err.Error())
	case err != nil:
		writeError(response, http.StatusInternalServerError, "cancel deployment")
	default:
		if !terminalDeploymentStatus(value.Status) {
			response.Header().Set("Retry-After", "1")
		}
		writeJSON(response, http.StatusOK, value)
	}
}

func (s *Server) retryDeployment(response http.ResponseWriter, request *http.Request) {
	idempotencyKey := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if err := service.ValidateIdempotencyKey(idempotencyKey); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	deploymentID, err := service.NewDeploymentID()
	if err != nil {
		writeError(response, http.StatusInternalServerError, "create deployment identity")
		return
	}
	value, created, err := s.store.RetryDeployment(
		request.Context(), request.PathValue("deployment"), deploymentID, idempotencyKey, requestActor(request),
	)
	switch {
	case errors.Is(err, database.ErrDeploymentNotFound):
		writeError(response, http.StatusNotFound, "deployment not found")
	case errors.Is(err, database.ErrDeploymentNotRetryable), errors.Is(err, database.ErrDeploymentSourceUnavailable), errors.Is(err, database.ErrDeploymentIdempotencyConflict):
		writeError(response, http.StatusConflict, err.Error())
	case err != nil:
		writeError(response, http.StatusInternalServerError, "retry deployment")
	default:
		status := http.StatusAccepted
		if !created {
			status = http.StatusOK
		}
		response.Header().Set("Location", "/api/v1/deployments/"+value.ID)
		response.Header().Set("Retry-After", "1")
		writeJSON(response, status, map[string]any{"deployment": value, "created": created})
	}
}

func (s *Server) rollbackAutomation(response http.ResponseWriter, request *http.Request) {
	var body struct {
		RevisionID string `json:"revisionId"`
	}
	if err := decodeJSON(response, request, 1024, &body); err != nil {
		return
	}
	if strings.TrimSpace(body.RevisionID) == "" {
		writeError(response, http.StatusBadRequest, "revisionId is required")
		return
	}
	changed, err := s.store.RollbackAutomation(
		request.Context(), request.PathValue("automation"), strings.TrimSpace(body.RevisionID), requestActor(request),
	)
	switch {
	case errors.Is(err, database.ErrAutomationNotFound):
		writeError(response, http.StatusNotFound, "automation not found")
	case errors.Is(err, database.ErrRevisionNotFound):
		writeError(response, http.StatusNotFound, "revision not found for automation")
	case errors.Is(err, database.ErrRevisionArtifactUnavailable):
		writeError(response, http.StatusConflict, err.Error())
	case err != nil:
		writeError(response, http.StatusInternalServerError, "rollback automation")
	default:
		value, getErr := s.store.GetAutomation(request.Context(), request.PathValue("automation"))
		if getErr != nil {
			writeError(response, http.StatusInternalServerError, "get rolled back automation")
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"automation": value, "changed": changed})
	}
}

func (s *Server) createRetentionPlan(response http.ResponseWriter, request *http.Request) {
	if s.retention == nil {
		writeError(response, http.StatusServiceUnavailable, "retention is not configured")
		return
	}
	policy := service.DefaultRetentionPolicy
	if err := decodeJSON(response, request, 4096, &policy); err != nil {
		return
	}
	value, err := s.retention.Plan(request.Context(), policy, requestActor(request))
	if errors.Is(err, service.ErrInvalidRetentionPolicy) {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		slog.Error("create retention plan", "error", err)
		writeError(response, http.StatusInternalServerError, "create retention plan")
		return
	}
	response.Header().Set("Location", "/api/v1/retention/plans/"+value.ID)
	writeJSON(response, http.StatusCreated, value)
}

func (s *Server) retentionPlan(response http.ResponseWriter, request *http.Request) {
	if s.retention == nil {
		writeError(response, http.StatusServiceUnavailable, "retention is not configured")
		return
	}
	value, err := s.retention.Get(request.Context(), request.PathValue("plan"))
	if errors.Is(err, database.ErrRetentionPlanNotFound) {
		writeError(response, http.StatusNotFound, "retention plan not found")
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "get retention plan")
		return
	}
	writeJSON(response, http.StatusOK, value)
}

func (s *Server) applyRetentionPlan(response http.ResponseWriter, request *http.Request) {
	if s.retention == nil {
		writeError(response, http.StatusServiceUnavailable, "retention is not configured")
		return
	}
	value, err := s.retention.Apply(request.Context(), request.PathValue("plan"), requestActor(request))
	switch {
	case errors.Is(err, database.ErrRetentionPlanNotFound):
		writeError(response, http.StatusNotFound, "retention plan not found")
	case errors.Is(err, database.ErrRetentionPlanExpired):
		writeError(response, http.StatusGone, err.Error())
	case errors.Is(err, database.ErrRetentionPlanBusy):
		writeError(response, http.StatusConflict, err.Error())
	case err != nil:
		slog.Error("apply retention plan", "plan", request.PathValue("plan"), "error", err)
		writeError(response, http.StatusInternalServerError, "apply retention plan")
	default:
		writeJSON(response, http.StatusOK, value)
	}
}

func (s *Server) secretsList(response http.ResponseWriter, request *http.Request) {
	if s.secrets == nil {
		writeError(response, http.StatusServiceUnavailable, "secret vault is not configured")
		return
	}
	values, err := s.secrets.List(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list secrets")
		return
	}
	writeJSON(response, http.StatusOK, values)
}

func (s *Server) secret(response http.ResponseWriter, request *http.Request) {
	if s.secrets == nil {
		writeError(response, http.StatusServiceUnavailable, "secret vault is not configured")
		return
	}
	value, err := s.secrets.Get(request.Context(), request.PathValue("secret"))
	switch {
	case errors.Is(err, secretvault.ErrInvalidName):
		writeError(response, http.StatusBadRequest, err.Error())
	case errors.Is(err, database.ErrSecretNotFound):
		writeError(response, http.StatusNotFound, "secret not found")
	case err != nil:
		writeError(response, http.StatusInternalServerError, "get secret")
	default:
		writeJSON(response, http.StatusOK, value)
	}
}

func (s *Server) putSecret(response http.ResponseWriter, request *http.Request) {
	if s.secrets == nil {
		writeError(response, http.StatusServiceUnavailable, "secret vault is not configured")
		return
	}
	var body struct {
		Value       string `json:"value"`
		Description string `json:"description"`
	}
	if err := decodeJSON(response, request, secretvault.MaximumValueBytes*6+4096, &body); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	value, created, err := s.secrets.Put(
		request.Context(), request.PathValue("secret"), body.Value, body.Description, requestActor(request),
	)
	switch {
	case errors.Is(err, secretvault.ErrInvalidName), errors.Is(err, secretvault.ErrInvalidValue), errors.Is(err, secretvault.ErrInvalidDescription):
		writeError(response, http.StatusBadRequest, err.Error())
	case err != nil:
		slog.Error("put secret", "secret", request.PathValue("secret"), "error", err)
		writeError(response, http.StatusInternalServerError, "put secret")
	default:
		status := http.StatusOK
		if created {
			status = http.StatusCreated
			response.Header().Set("Location", "/api/v1/secrets/"+url.PathEscape(value.Name))
		}
		writeJSON(response, status, value)
	}
}

func (s *Server) deleteSecret(response http.ResponseWriter, request *http.Request) {
	if s.secrets == nil {
		writeError(response, http.StatusServiceUnavailable, "secret vault is not configured")
		return
	}
	err := s.secrets.Delete(request.Context(), request.PathValue("secret"), requestActor(request))
	switch {
	case errors.Is(err, secretvault.ErrInvalidName):
		writeError(response, http.StatusBadRequest, err.Error())
	case errors.Is(err, database.ErrSecretNotFound):
		writeError(response, http.StatusNotFound, "secret not found")
	case errors.Is(err, database.ErrSecretInUse):
		writeError(response, http.StatusConflict, err.Error())
	case err != nil:
		writeError(response, http.StatusInternalServerError, "delete secret")
	default:
		response.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) email(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	policy, ok := s.triggerIngressPolicy(response, request, "email")
	if !ok {
		return
	}
	var config struct {
		TokenSecret string `json:"tokenSecret"`
	}
	if err := json.Unmarshal(policy.Config, &config); err != nil || config.TokenSecret == "" {
		slog.Error("email trigger has invalid credential configuration", "automation", request.PathValue("automation"), "trigger", request.PathValue("trigger"))
		writeError(response, http.StatusServiceUnavailable, "trigger credential unavailable")
		return
	}
	token, ok := s.ingressSecret(response, request, config.TokenSecret)
	if !ok {
		return
	}
	provided, found := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer ")
	expectedHash := sha256.Sum256(token)
	providedHash := sha256.Sum256([]byte(provided))
	if !found || subtle.ConstantTimeCompare(expectedHash[:], providedHash[:]) != 1 {
		response.Header().Set("WWW-Authenticate", `Bearer realm="werkt-email-ingress"`)
		writeError(response, http.StatusUnauthorized, "invalid trigger credential")
		return
	}
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
	if externalID == "" {
		writeError(response, http.StatusBadRequest, "Message-Id or Idempotency-Key is required")
		return
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

func (s *Server) openAPI(response http.ResponseWriter, _ *http.Request) {
	contents, err := openAPIFS.ReadFile("openapi.yaml")
	if err != nil {
		writeError(response, http.StatusInternalServerError, "load OpenAPI document")
		return
	}
	response.Header().Set("Content-Type", "application/yaml")
	response.WriteHeader(http.StatusOK)
	if _, err := response.Write(contents); err != nil {
		slog.Error("write OpenAPI document", "error", err)
	}
}

func (s *Server) webhook(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	policy, ok := s.triggerIngressPolicy(response, request, "webhook")
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxWebhookBody))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid or oversized request body")
		return
	}
	var config struct {
		Secret          string `json:"secret"`
		SignatureHeader string `json:"signatureHeader"`
	}
	if err := json.Unmarshal(policy.Config, &config); err != nil || config.Secret == "" {
		slog.Error("webhook trigger has invalid credential configuration", "automation", request.PathValue("automation"), "trigger", request.PathValue("trigger"))
		writeError(response, http.StatusServiceUnavailable, "trigger credential unavailable")
		return
	}
	secret, ok := s.ingressSecret(response, request, config.Secret)
	if !ok {
		return
	}
	header := config.SignatureHeader
	if header == "" {
		header = defaultWebhookSignatureHeader
	}
	idempotencyKey := request.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		writeError(response, http.StatusBadRequest, "Idempotency-Key is required")
		return
	}
	if len(idempotencyKey) > 255 {
		writeError(response, http.StatusBadRequest, "Idempotency-Key cannot exceed 255 bytes")
		return
	}
	timestampValue := request.Header.Get(webhookTimestampHeader)
	timestampSeconds, timestampErr := strconv.ParseInt(timestampValue, 10, 64)
	timestamp := time.Unix(timestampSeconds, 0)
	age := time.Since(timestamp)
	if timestampErr != nil || age < -webhookSignatureTolerance || age > webhookSignatureTolerance {
		writeError(response, http.StatusUnauthorized, "invalid or stale trigger timestamp")
		return
	}
	provided, found := strings.CutPrefix(request.Header.Get(header), "sha256=")
	providedDigest, decodeErr := hex.DecodeString(provided)
	expectedDigest := hmac.New(sha256.New, secret)
	_, _ = io.WriteString(expectedDigest, timestampValue+"."+idempotencyKey+".")
	_, _ = expectedDigest.Write(body)
	if !found || decodeErr != nil || len(providedDigest) != sha256.Size || !hmac.Equal(expectedDigest.Sum(nil), providedDigest) {
		writeError(response, http.StatusUnauthorized, "invalid trigger signature")
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

func (s *Server) triggerIngressPolicy(response http.ResponseWriter, request *http.Request, triggerType string) (database.TriggerIngressPolicy, bool) {
	policy, err := s.store.GetTriggerIngressPolicy(
		request.Context(), request.PathValue("automation"), request.PathValue("trigger"), triggerType,
	)
	if errors.Is(err, database.ErrTriggerNotFound) {
		writeError(response, http.StatusNotFound, "enabled trigger not found")
		return database.TriggerIngressPolicy{}, false
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "read trigger policy")
		return database.TriggerIngressPolicy{}, false
	}
	return policy, true
}

func (s *Server) ingressSecret(response http.ResponseWriter, request *http.Request, name string) ([]byte, bool) {
	if s.secrets == nil {
		writeError(response, http.StatusServiceUnavailable, "trigger credential unavailable")
		return nil, false
	}
	values, err := s.secrets.Resolve(request.Context(), []string{name})
	secret := values[name]
	if err != nil || len(secret) < minimumIngressSecretBytes {
		slog.Error("trigger credential is missing or too short", "secret", name, "minimumBytes", minimumIngressSecretBytes, "error", err)
		writeError(response, http.StatusServiceUnavailable, "trigger credential unavailable")
		return nil, false
	}
	return []byte(secret), true
}

func (s *Server) automations(response http.ResponseWriter, request *http.Request) {
	filter := database.AutomationFilter{
		Project: strings.TrimSpace(request.URL.Query().Get("project")),
		Folder:  strings.TrimSpace(request.URL.Query().Get("folder")),
		Label:   strings.TrimSpace(request.URL.Query().Get("label")),
		Query:   strings.TrimSpace(request.URL.Query().Get("q")),
	}
	if raw := request.URL.Query().Get("enabled"); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(response, http.StatusBadRequest, "enabled must be true or false")
			return
		}
		filter.Enabled = &enabled
	}
	values, err := s.store.ListAutomations(request.Context(), filter)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list automations")
		return
	}
	writeJSON(response, http.StatusOK, values)
}

func (s *Server) automation(response http.ResponseWriter, request *http.Request) {
	value, err := s.store.GetAutomation(request.Context(), request.PathValue("automation"))
	if errors.Is(err, database.ErrAutomationNotFound) {
		writeError(response, http.StatusNotFound, "automation not found")
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "get automation")
		return
	}
	writeJSON(response, http.StatusOK, value)
}

func (s *Server) updateAutomation(response http.ResponseWriter, request *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(response, request, 1024, &body); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	if body.Enabled == nil {
		writeError(response, http.StatusBadRequest, "enabled is required")
		return
	}
	automationID := request.PathValue("automation")
	changed, err := s.store.SetAutomationEnabled(request.Context(), automationID, *body.Enabled, requestActor(request))
	if errors.Is(err, database.ErrAutomationNotFound) {
		writeError(response, http.StatusNotFound, "automation not found")
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "update automation")
		return
	}
	value, err := s.store.GetAutomation(request.Context(), automationID)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "read updated automation")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"automation": value, "changed": changed})
}

func (s *Server) manualRun(response http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxWebhookBody))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid or oversized request body")
		return
	}
	data := json.RawMessage(body)
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	} else if !json.Valid(data) {
		writeError(response, http.StatusBadRequest, "body must be valid JSON")
		return
	}
	runID, created, err := s.store.EnqueueManualRun(
		request.Context(), request.PathValue("automation"), request.Header.Get("Idempotency-Key"), data, requestActor(request),
	)
	if errors.Is(err, database.ErrAutomationNotFound) {
		writeError(response, http.StatusNotFound, "automation not found")
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "queue manual run")
		return
	}
	status := http.StatusAccepted
	if !created {
		status = http.StatusOK
	}
	writeJSON(response, status, map[string]any{"runId": runID, "created": created})
}

func (s *Server) runs(response http.ResponseWriter, request *http.Request) {
	limit, err := requestLimit(request, 100)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	status := request.URL.Query().Get("status")
	if status != "" && status != domain.RunQueued && status != domain.RunRunning && status != domain.RunSucceeded && status != domain.RunFailed {
		writeError(response, http.StatusBadRequest, "status must be queued, running, succeeded, or failed")
		return
	}
	values, err := s.store.ListRunsFiltered(request.Context(), request.URL.Query().Get("automation"), status, limit)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list runs")
		return
	}
	writeJSON(response, http.StatusOK, values)
}

func (s *Server) run(response http.ResponseWriter, request *http.Request) {
	value, err := s.store.GetRun(request.Context(), request.PathValue("run"))
	if errors.Is(err, database.ErrRunNotFound) {
		writeError(response, http.StatusNotFound, "run not found")
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "get run")
		return
	}
	writeJSON(response, http.StatusOK, value)
}

func (s *Server) audit(response http.ResponseWriter, request *http.Request) {
	limit, err := requestLimit(request, 100)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	values, err := s.store.ListAuditEvents(request.Context(), request.URL.Query().Get("automation"), limit)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list audit events")
		return
	}
	writeJSON(response, http.StatusOK, values)
}

func (s *Server) requireManagementAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		if s.managementToken == "" {
			next.ServeHTTP(response, request)
			return
		}
		provided, found := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer ")
		expectedHash := sha256.Sum256([]byte(s.managementToken))
		providedHash := sha256.Sum256([]byte(provided))
		if !found || subtle.ConstantTimeCompare(expectedHash[:], providedHash[:]) != 1 {
			response.Header().Set("WWW-Authenticate", `Bearer realm="werkt-management"`)
			writeError(response, http.StatusUnauthorized, "management authentication required")
			return
		}
		next.ServeHTTP(response, request)
	})
}

func decodeJSON(response http.ResponseWriter, request *http.Request, maxBytes int64, destination any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, maxBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("body must contain one JSON value")
	}
	return nil
}

func requestLimit(request *http.Request, fallback int) (int, error) {
	raw := request.URL.Query().Get("limit")
	if raw == "" {
		return fallback, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > 500 {
		return 0, errors.New("limit must be an integer from 1 to 500")
	}
	return limit, nil
}

func requestActor(request *http.Request) string {
	actor := strings.TrimSpace(request.Header.Get("X-Werkt-Actor"))
	actor = strings.Map(func(value rune) rune {
		if unicode.IsControl(value) {
			return -1
		}
		return value
	}, actor)
	if actor == "" {
		return "api"
	}
	runes := []rune(actor)
	if len(runes) > 255 {
		return string(runes[:255])
	}
	return actor
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
