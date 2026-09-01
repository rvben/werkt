package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
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

	"github.com/rvben/werkt/internal/buildinfo"
	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/domain"
	"github.com/rvben/werkt/internal/packageio"
	"github.com/rvben/werkt/internal/secretvault"
	"github.com/rvben/werkt/internal/service"
)

const maxWebhookBody = 2 << 20
const maxEmailBody = 12 << 20
const (
	minimumIngressSecretBytes   = 32
	minimumZoomSecretTokenBytes = 16
)
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
	artifacts        ArtifactVerifier
	managementToken  string
	scopedTokens     []scopedManagementToken
	environment      string
	instance         string
	browserAuth      *BrowserAuth
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
	EnqueueManualRun(context.Context, string, string, string, json.RawMessage, string) (string, bool, error)
	ListRunSummariesPage(context.Context, string, string, database.ListCursor, int) ([]domain.RunSummary, error)
	GetRun(context.Context, string) (domain.Run, error)
	ListApprovalsPage(context.Context, string, string, database.ListCursor, int) ([]domain.Approval, error)
	GetApproval(context.Context, string) (domain.Approval, error)
	ResolveApproval(context.Context, string, string, string, string, map[string]any, string) (domain.Approval, bool, error)
	ListAuditEventsPage(context.Context, database.AuditFilter, database.ListCursor, int) ([]database.AuditEvent, error)
	ListDeploymentsPage(context.Context, string, string, database.ListCursor, int) ([]domain.Deployment, error)
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

type ArtifactVerifier interface {
	VerifyRevision(context.Context, string, string) error
}

type Option func(*Server)

type ManagementScope string

const (
	ScopeRead      ManagementScope = "read"
	ScopeOperate   ManagementScope = "operate"
	ScopeDeploy    ManagementScope = "deploy"
	ScopeSecrets   ManagementScope = "secrets"
	ScopeRetention ManagementScope = "retention"
)

type scopedManagementToken struct {
	digest [32]byte
	scopes map[ManagementScope]struct{}
}

func WithDeploymentIntake(intake DeploymentIntake) Option {
	return func(server *Server) { server.deploymentIntake = intake }
}

func WithRetentionManager(manager RetentionManager) Option {
	return func(server *Server) { server.retention = manager }
}

func WithSecretManager(manager SecretManager) Option {
	return func(server *Server) { server.secrets = manager }
}

func WithArtifactVerifier(verifier ArtifactVerifier) Option {
	return func(server *Server) { server.artifacts = verifier }
}

func WithBrowserAuth(auth *BrowserAuth) Option {
	return func(server *Server) { server.browserAuth = auth }
}

func WithOperatorScope(environment, instance string) Option {
	return func(server *Server) {
		server.environment = strings.TrimSpace(environment)
		server.instance = strings.TrimSpace(instance)
	}
}

func WithScopedManagementToken(token string, scopes ...ManagementScope) Option {
	return func(server *Server) {
		token = strings.TrimSpace(token)
		if token == "" {
			return
		}
		allowed := make(map[ManagementScope]struct{}, len(scopes))
		for _, scope := range scopes {
			allowed[scope] = struct{}{}
		}
		server.scopedTokens = append(server.scopedTokens, scopedManagementToken{digest: sha256.Sum256([]byte(token)), scopes: allowed})
	}
}

func New(store Store, address, managementToken string, options ...Option) *Server {
	value := &Server{store: store, managementToken: managementToken, environment: "development", instance: address}
	for _, option := range options {
		option(value)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", value.workspaceRoot)
	mux.HandleFunc("GET /app", value.workspaceRoot)
	mux.HandleFunc("GET /app/", value.workspace)
	mux.HandleFunc("GET /app/{asset}", value.workspace)
	mux.HandleFunc("GET /healthz", value.health)
	mux.HandleFunc("GET /readyz", value.health)
	mux.HandleFunc("GET /api/openapi.yaml", value.openAPI)
	mux.HandleFunc("GET /api/{$}", value.apiGuide)
	mux.HandleFunc("GET /api/v1/auth/session", value.browserSession)
	mux.HandleFunc("GET /api/v1/auth/login", value.browserLogin)
	mux.HandleFunc("GET /api/v1/auth/callback", value.browserCallback)
	mux.HandleFunc("POST /api/v1/auth/logout", value.browserLogout)
	mux.HandleFunc("POST /api/v1/hooks/{automation}/{trigger}", value.webhook)
	mux.HandleFunc("POST /api/v1/email/{automation}/{trigger}", value.email)
	mux.Handle("GET /api/v1/automations", value.requireManagementAuth(ScopeRead, http.HandlerFunc(value.automations)))
	mux.Handle("GET /api/v1/automations/{automation}", value.requireManagementAuth(ScopeRead, http.HandlerFunc(value.automation)))
	mux.Handle("PATCH /api/v1/automations/{automation}", value.requireManagementAuth(ScopeOperate, http.HandlerFunc(value.updateAutomation)))
	mux.Handle("POST /api/v1/automations/{automation}/runs", value.requireManagementAuth(ScopeOperate, http.HandlerFunc(value.manualRun)))
	mux.Handle("POST /api/v1/automations/{automation}/rollback", value.requireManagementAuth(ScopeOperate, http.HandlerFunc(value.rollbackAutomation)))
	mux.Handle("GET /api/v1/runs", value.requireManagementAuth(ScopeRead, http.HandlerFunc(value.runs)))
	mux.Handle("GET /api/v1/runs/{run}", value.requireManagementAuth(ScopeRead, http.HandlerFunc(value.run)))
	mux.Handle("GET /api/v1/approvals", value.requireManagementAuth(ScopeRead, http.HandlerFunc(value.approvals)))
	mux.Handle("GET /api/v1/approvals/{approval}", value.requireManagementAuth(ScopeRead, http.HandlerFunc(value.approval)))
	mux.Handle("POST /api/v1/approvals/{approval}/actions", value.requireManagementAuth(ScopeOperate, http.HandlerFunc(value.resolveApproval)))
	mux.Handle("GET /api/v1/audit", value.requireManagementAuth(ScopeRead, http.HandlerFunc(value.audit)))
	mux.Handle("POST /api/v1/deployments", value.requireManagementAuth(ScopeDeploy, http.HandlerFunc(value.createDeployment)))
	mux.Handle("GET /api/v1/deployments", value.requireManagementAuth(ScopeRead, http.HandlerFunc(value.deployments)))
	mux.Handle("GET /api/v1/deployments/{deployment}", value.requireManagementAuth(ScopeRead, http.HandlerFunc(value.deployment)))
	mux.Handle("POST /api/v1/deployments/{deployment}/cancel", value.requireManagementAuth(ScopeDeploy, http.HandlerFunc(value.cancelDeployment)))
	mux.Handle("POST /api/v1/deployments/{deployment}/retry", value.requireManagementAuth(ScopeDeploy, http.HandlerFunc(value.retryDeployment)))
	mux.Handle("POST /api/v1/retention/plans", value.requireManagementAuth(ScopeRetention, http.HandlerFunc(value.createRetentionPlan)))
	mux.Handle("GET /api/v1/retention/plans/{plan}", value.requireManagementAuth(ScopeRetention, http.HandlerFunc(value.retentionPlan)))
	mux.Handle("POST /api/v1/retention/plans/{plan}/apply", value.requireManagementAuth(ScopeRetention, http.HandlerFunc(value.applyRetentionPlan)))
	mux.Handle("GET /api/v1/secrets", value.requireManagementAuth(ScopeSecrets, http.HandlerFunc(value.secretsList)))
	mux.Handle("GET /api/v1/secrets/{secret}", value.requireManagementAuth(ScopeSecrets, http.HandlerFunc(value.secret)))
	mux.Handle("PUT /api/v1/secrets/{secret}", value.requireManagementAuth(ScopeSecrets, http.HandlerFunc(value.putSecret)))
	mux.Handle("DELETE /api/v1/secrets/{secret}", value.requireManagementAuth(ScopeSecrets, http.HandlerFunc(value.deleteSecret)))
	value.server = &http.Server{
		Addr:              address,
		Handler:           requestMetadata(requestLogger(mux)),
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
			writeProblem(response, http.StatusConflict, "idempotency_conflict", err.Error(), false, map[string]any{"header": "Idempotency-Key"})
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
	cursor, err := requestCursor(request)
	if err != nil {
		writeProblem(response, http.StatusBadRequest, "invalid_cursor", err.Error(), false, nil)
		return
	}
	values, err := s.store.ListDeploymentsPage(request.Context(), request.URL.Query().Get("automation"), status, cursor, limit+1)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list deployments")
		return
	}
	if len(values) > limit {
		values = values[:limit]
		last := values[len(values)-1]
		writeNextPageHeaders(response, request, database.ListCursor{CreatedAt: last.CreatedAt, ID: last.ID})
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
		writeProblem(response, http.StatusConflict, "deployment_not_cancellable", err.Error(), false, map[string]any{"deploymentId": request.PathValue("deployment")})
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
	case errors.Is(err, database.ErrDeploymentNotRetryable):
		writeProblem(response, http.StatusConflict, "deployment_not_retryable", err.Error(), false, map[string]any{"deploymentId": request.PathValue("deployment")})
	case errors.Is(err, database.ErrDeploymentSourceUnavailable):
		writeProblem(response, http.StatusConflict, "deployment_source_unavailable", err.Error(), false, map[string]any{"deploymentId": request.PathValue("deployment")})
	case errors.Is(err, database.ErrDeploymentIdempotencyConflict):
		writeProblem(response, http.StatusConflict, "idempotency_conflict", err.Error(), false, map[string]any{"header": "Idempotency-Key"})
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
	automationID := request.PathValue("automation")
	revisionID := strings.TrimSpace(body.RevisionID)
	if s.artifacts != nil {
		if err := s.artifacts.VerifyRevision(request.Context(), automationID, revisionID); err != nil {
			switch {
			case errors.Is(err, database.ErrRevisionNotFound):
				writeError(response, http.StatusNotFound, "revision not found for automation")
			case errors.Is(err, database.ErrRevisionArtifactUnavailable):
				writeProblem(response, http.StatusConflict, "revision_artifact_unavailable", err.Error(), false, map[string]any{"automationId": automationID, "revisionId": revisionID})
			default:
				writeProblem(response, http.StatusConflict, "revision_provenance_invalid", "revision artifact provenance verification failed", false, map[string]any{"automationId": automationID, "revisionId": revisionID})
			}
			return
		}
	}
	changed, err := s.store.RollbackAutomation(
		request.Context(), automationID, revisionID, requestActor(request),
	)
	switch {
	case errors.Is(err, database.ErrAutomationNotFound):
		writeError(response, http.StatusNotFound, "automation not found")
	case errors.Is(err, database.ErrRevisionNotFound):
		writeError(response, http.StatusNotFound, "revision not found for automation")
	case errors.Is(err, database.ErrRevisionArtifactUnavailable):
		writeProblem(response, http.StatusConflict, "revision_artifact_unavailable", err.Error(), false, map[string]any{"automationId": automationID, "revisionId": revisionID})
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
		writeProblem(response, http.StatusGone, "retention_plan_expired", err.Error(), false, map[string]any{"planId": request.PathValue("plan")})
	case errors.Is(err, database.ErrRetentionPlanBusy):
		writeProblem(response, http.StatusConflict, "retention_plan_busy", err.Error(), true, map[string]any{"planId": request.PathValue("plan")})
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
		writeProblem(response, http.StatusConflict, "secret_in_use", err.Error(), false, map[string]any{"secret": request.PathValue("secret")})
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
	token, ok := s.ingressSecret(response, request, config.TokenSecret, minimumIngressSecretBytes)
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
	identity := buildinfo.Current()
	writeJSON(response, http.StatusOK, struct {
		Status string `json:"status"`
		buildinfo.Info
	}{Status: "ok", Info: identity})
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
		Provider        string `json:"provider"`
	}
	if err := json.Unmarshal(policy.Config, &config); err != nil || config.Secret == "" || (config.Provider != "" && config.Provider != "werkt" && config.Provider != "github" && config.Provider != "zoom") {
		slog.Error("webhook trigger has invalid credential configuration", "automation", request.PathValue("automation"), "trigger", request.PathValue("trigger"))
		writeError(response, http.StatusServiceUnavailable, "trigger credential unavailable")
		return
	}
	minimumSecretBytes := minimumIngressSecretBytes
	if config.Provider == "zoom" {
		// Zoom issues and owns the webhook Secret Token. Its provider-managed
		// values can be shorter than Werkt's minimum for operator-generated
		// HMAC secrets, so validate them against Zoom's supported contract.
		minimumSecretBytes = minimumZoomSecretTokenBytes
	}
	secret, ok := s.ingressSecret(response, request, config.Secret, minimumSecretBytes)
	if !ok {
		return
	}
	header := config.SignatureHeader
	if header == "" {
		header = defaultWebhookSignatureHeader
	}
	if config.Provider == "github" {
		s.githubWebhook(response, request, body, secret)
		return
	}
	if config.Provider == "zoom" {
		s.zoomWebhook(response, request, body, secret)
		return
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

func (s *Server) githubWebhook(response http.ResponseWriter, request *http.Request, body, secret []byte) {
	deliveryID := request.Header.Get("X-GitHub-Delivery")
	if deliveryID == "" {
		writeError(response, http.StatusBadRequest, "X-GitHub-Delivery is required")
		return
	}
	if len(deliveryID) > 255 {
		writeError(response, http.StatusBadRequest, "X-GitHub-Delivery cannot exceed 255 bytes")
		return
	}
	provided, found := strings.CutPrefix(request.Header.Get("X-Hub-Signature-256"), "sha256=")
	providedDigest, decodeErr := hex.DecodeString(provided)
	expectedDigest := hmac.New(sha256.New, secret)
	_, _ = expectedDigest.Write(body)
	if !found || decodeErr != nil || len(providedDigest) != sha256.Size || !hmac.Equal(expectedDigest.Sum(nil), providedDigest) {
		writeError(response, http.StatusUnauthorized, "invalid GitHub webhook signature")
		return
	}
	if len(body) == 0 || !json.Valid(body) {
		writeError(response, http.StatusBadRequest, "GitHub webhook body must be JSON")
		return
	}
	bodyDigest := sha256.Sum256(body)
	externalID := "github:" + hex.EncodeToString(bodyDigest[:])
	metadata := map[string]any{
		"source":         "webhook",
		"provider":       "github",
		"deliveryId":     deliveryID,
		"githubEvent":    request.Header.Get("X-GitHub-Event"),
		"githubHookId":   request.Header.Get("X-GitHub-Hook-ID"),
		"githubTargetId": request.Header.Get("X-GitHub-Hook-Installation-Target-ID"),
		"contentType":    request.Header.Get("Content-Type"),
		"userAgent":      request.UserAgent(),
	}
	runID, created, err := s.store.IngestEvent(
		request.Context(), request.PathValue("automation"), request.PathValue("trigger"),
		"webhook", externalID, time.Now().UTC(), json.RawMessage(body), metadata,
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

func (s *Server) zoomWebhook(response http.ResponseWriter, request *http.Request, body, secret []byte) {
	timestampValue := request.Header.Get("X-Zm-Request-Timestamp")
	timestampSeconds, timestampErr := strconv.ParseInt(timestampValue, 10, 64)
	timestamp := time.Unix(timestampSeconds, 0)
	age := time.Since(timestamp)
	if timestampErr != nil || age < -webhookSignatureTolerance || age > webhookSignatureTolerance {
		writeError(response, http.StatusUnauthorized, "invalid or stale Zoom webhook timestamp")
		return
	}
	provided, found := strings.CutPrefix(request.Header.Get("X-Zm-Signature"), "v0=")
	providedDigest, decodeErr := hex.DecodeString(provided)
	expectedDigest := hmac.New(sha256.New, secret)
	_, _ = io.WriteString(expectedDigest, "v0:"+timestampValue+":")
	_, _ = expectedDigest.Write(body)
	if !found || decodeErr != nil || len(providedDigest) != sha256.Size || !hmac.Equal(expectedDigest.Sum(nil), providedDigest) {
		writeError(response, http.StatusUnauthorized, "invalid Zoom webhook signature")
		return
	}
	if len(body) == 0 || !json.Valid(body) {
		writeError(response, http.StatusBadRequest, "Zoom webhook body must be JSON")
		return
	}
	var event struct {
		Event   string `json:"event"`
		Payload struct {
			PlainToken string `json:"plainToken"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &event); err != nil || event.Event == "" {
		writeError(response, http.StatusBadRequest, "Zoom webhook event is required")
		return
	}
	if event.Event == "endpoint.url_validation" {
		if event.Payload.PlainToken == "" {
			writeError(response, http.StatusBadRequest, "Zoom validation plainToken is required")
			return
		}
		encryptedToken := hmac.New(sha256.New, secret)
		_, _ = io.WriteString(encryptedToken, event.Payload.PlainToken)
		writeJSON(response, http.StatusOK, map[string]string{
			"plainToken":     event.Payload.PlainToken,
			"encryptedToken": hex.EncodeToString(encryptedToken.Sum(nil)),
		})
		return
	}
	bodyDigest := sha256.Sum256(body)
	externalID := "zoom:" + hex.EncodeToString(bodyDigest[:])
	metadata := map[string]any{
		"source":      "webhook",
		"provider":    "zoom",
		"zoomEvent":   event.Event,
		"contentType": request.Header.Get("Content-Type"),
		"userAgent":   request.UserAgent(),
	}
	runID, created, err := s.store.IngestEvent(
		request.Context(), request.PathValue("automation"), request.PathValue("trigger"),
		"webhook", externalID, time.Now().UTC(), json.RawMessage(body), metadata,
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

func (s *Server) ingressSecret(response http.ResponseWriter, request *http.Request, name string, minimumBytes int) ([]byte, bool) {
	if s.secrets == nil {
		writeError(response, http.StatusServiceUnavailable, "trigger credential unavailable")
		return nil, false
	}
	values, err := s.secrets.Resolve(request.Context(), []string{name})
	secret := values[name]
	if err != nil || len(secret) < minimumBytes {
		slog.Error("trigger credential is missing or too short", "secret", name, "minimumBytes", minimumBytes, "error", err)
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

func (s *Server) approvals(response http.ResponseWriter, request *http.Request) {
	limit, err := requestLimit(request, 100)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	status := request.URL.Query().Get("status")
	if status != "" && status != "pending" && status != "approved" && status != "rejected" && status != "expired" {
		writeError(response, http.StatusBadRequest, "status must be pending, approved, rejected, or expired")
		return
	}
	cursor, err := requestCursor(request)
	if err != nil {
		writeProblem(response, http.StatusBadRequest, "invalid_cursor", err.Error(), false, nil)
		return
	}
	values, err := s.store.ListApprovalsPage(request.Context(), request.URL.Query().Get("automation"), status, cursor, limit+1)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list approvals")
		return
	}
	if len(values) > limit {
		values = values[:limit]
		last := values[len(values)-1]
		writeNextPageHeaders(response, request, database.ListCursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	writeJSON(response, http.StatusOK, values)
}

func (s *Server) approval(response http.ResponseWriter, request *http.Request) {
	value, err := s.store.GetApproval(request.Context(), request.PathValue("approval"))
	if errors.Is(err, database.ErrApprovalNotFound) {
		writeError(response, http.StatusNotFound, "approval not found")
		return
	}
	if err != nil {
		writeError(response, http.StatusInternalServerError, "get approval")
		return
	}
	writeJSON(response, http.StatusOK, value)
}

func (s *Server) resolveApproval(response http.ResponseWriter, request *http.Request) {
	idempotencyKey := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if err := service.ValidateIdempotencyKey(idempotencyKey); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Action string         `json:"action"`
		Fields map[string]any `json:"fields"`
	}
	if err := decodeJSON(response, request, maxWebhookBody, &body); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	value, created, err := s.store.ResolveApproval(
		request.Context(), request.PathValue("approval"), idempotencyKey,
		request.Header.Get("X-Werkt-Expected-Revision"), body.Action, body.Fields, requestActor(request),
	)
	switch {
	case errors.Is(err, database.ErrApprovalNotFound):
		writeError(response, http.StatusNotFound, "approval not found")
	case errors.Is(err, database.ErrApprovalResolved):
		writeProblem(response, http.StatusConflict, "approval_resolved", "approval has already been resolved", false, map[string]any{"approvalId": request.PathValue("approval")})
	case errors.Is(err, database.ErrApprovalExpired):
		writeProblem(response, http.StatusConflict, "approval_expired", "approval has expired", false, map[string]any{"approvalId": request.PathValue("approval")})
	case errors.Is(err, database.ErrAutomationRevisionChanged):
		writeProblem(response, http.StatusConflict, "active_revision_changed", "active revision changed; refresh the approval and review its execution target", false, map[string]any{"approvalId": request.PathValue("approval")})
	case errors.Is(err, database.ErrApprovalInvalidResponse):
		writeError(response, http.StatusBadRequest, err.Error())
	case err != nil:
		slog.Error("resolve approval", "error", err)
		writeError(response, http.StatusInternalServerError, "resolve approval")
	default:
		status := http.StatusAccepted
		if !created {
			status = http.StatusOK
		}
		if value.ActionRunID != "" {
			response.Header().Set("Location", "/api/v1/runs/"+value.ActionRunID)
		}
		writeJSON(response, status, map[string]any{"approval": value, "created": created})
	}
}

func (s *Server) manualRun(response http.ResponseWriter, request *http.Request) {
	idempotencyKey := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if err := service.ValidateIdempotencyKey(idempotencyKey); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
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
		request.Context(), request.PathValue("automation"), idempotencyKey,
		request.Header.Get("X-Werkt-Expected-Revision"), data, requestActor(request),
	)
	if errors.Is(err, database.ErrAutomationNotFound) {
		writeError(response, http.StatusNotFound, "automation not found")
		return
	}
	if errors.Is(err, database.ErrAutomationRevisionChanged) {
		writeProblem(response, http.StatusConflict, "active_revision_changed", "active revision changed; refresh the automation and review the new run target", false, map[string]any{"automationId": request.PathValue("automation")})
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
	cursor, err := requestCursor(request)
	if err != nil {
		writeProblem(response, http.StatusBadRequest, "invalid_cursor", err.Error(), false, nil)
		return
	}
	values, err := s.store.ListRunSummariesPage(request.Context(), request.URL.Query().Get("automation"), status, cursor, limit+1)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list runs")
		return
	}
	if len(values) > limit {
		values = values[:limit]
		last := values[len(values)-1]
		writeNextPageHeaders(response, request, database.ListCursor{CreatedAt: last.CreatedAt, ID: last.ID})
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
	cursor, err := requestCursor(request)
	if err != nil {
		writeProblem(response, http.StatusBadRequest, "invalid_cursor", err.Error(), false, nil)
		return
	}
	since, err := optionalTimeParameter(request, "since")
	if err != nil {
		writeProblem(response, http.StatusBadRequest, "invalid_since", err.Error(), false, nil)
		return
	}
	until, err := optionalTimeParameter(request, "until")
	if err != nil {
		writeProblem(response, http.StatusBadRequest, "invalid_until", err.Error(), false, nil)
		return
	}
	values, err := s.store.ListAuditEventsPage(request.Context(), database.AuditFilter{
		AutomationID: request.URL.Query().Get("automation"),
		Action:       request.URL.Query().Get("action"),
		Actor:        request.URL.Query().Get("actor"),
		Since:        since,
		Until:        until,
	}, cursor, limit+1)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "list audit events")
		return
	}
	if len(values) > limit {
		values = values[:limit]
		last := values[len(values)-1]
		writeNextPageHeaders(response, request, database.ListCursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	writeJSON(response, http.StatusOK, values)
}

func (s *Server) requireManagementAuth(scope ManagementScope, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		provided, found := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer ")
		if found {
			expectedHash := sha256.Sum256([]byte(s.managementToken))
			providedHash := sha256.Sum256([]byte(provided))
			if s.managementToken != "" && subtle.ConstantTimeCompare(expectedHash[:], providedHash[:]) == 1 {
				next.ServeHTTP(response, request)
				return
			}
			for _, token := range s.scopedTokens {
				if subtle.ConstantTimeCompare(token.digest[:], providedHash[:]) != 1 {
					continue
				}
				if _, allowed := token.scopes[scope]; !allowed {
					writeProblem(response, http.StatusForbidden, "insufficient_scope", "This token is not permitted to perform this operation.", false, map[string]any{"requiredScope": scope})
					return
				}
				next.ServeHTTP(response, request)
				return
			}
			response.Header().Set("WWW-Authenticate", `Bearer realm="werkt-management"`)
			writeProblem(response, http.StatusUnauthorized, "authentication_required", "Management authentication is required.", false, nil)
			return
		}
		if s.browserAuth != nil {
			session, err := s.browserAuth.readSession(request)
			if err == nil {
				if !safeRequestMethod(request.Method) && !constantTimeEqual(request.Header.Get("X-Werkt-CSRF"), session.CSRF) {
					writeProblem(response, http.StatusForbidden, "csrf_validation_failed", "CSRF validation failed", false, nil)
					return
				}
				request = request.WithContext(context.WithValue(request.Context(), browserIdentityContextKey{}, session.Identity))
				next.ServeHTTP(response, request)
				return
			}
		}
		if s.managementToken == "" && len(s.scopedTokens) == 0 {
			next.ServeHTTP(response, request)
			return
		}
		response.Header().Set("WWW-Authenticate", `Bearer realm="werkt-management"`)
		writeProblem(response, http.StatusUnauthorized, "authentication_required", "Management authentication is required.", false, nil)
	})
}

type browserIdentityContextKey struct{}

func safeRequestMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func constantTimeEqual(left, right string) bool {
	leftHash := sha256.Sum256([]byte(left))
	rightHash := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftHash[:], rightHash[:]) == 1
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

func requestCursor(request *http.Request) (database.ListCursor, error) {
	raw := strings.TrimSpace(request.URL.Query().Get("cursor"))
	if raw == "" {
		return database.ListCursor{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return database.ListCursor{}, errors.New("cursor is not valid base64url")
	}
	var cursor struct {
		CreatedAt time.Time `json:"createdAt"`
		ID        string    `json:"id"`
	}
	if err := json.Unmarshal(decoded, &cursor); err != nil || cursor.ID == "" || cursor.CreatedAt.IsZero() {
		return database.ListCursor{}, errors.New("cursor is invalid or incomplete")
	}
	return database.ListCursor{CreatedAt: cursor.CreatedAt, ID: cursor.ID}, nil
}

func writeNextPageHeaders(response http.ResponseWriter, request *http.Request, cursor database.ListCursor) {
	encoded, err := json.Marshal(map[string]any{"createdAt": cursor.CreatedAt, "id": cursor.ID})
	if err != nil {
		return
	}
	next := base64.RawURLEncoding.EncodeToString(encoded)
	url := *request.URL
	query := url.Query()
	query.Set("cursor", next)
	url.RawQuery = query.Encode()
	response.Header().Set("X-Werkt-Next-Cursor", next)
	response.Header().Set("Link", fmt.Sprintf("<%s>; rel=\"next\"", url.RequestURI()))
}

func optionalTimeParameter(request *http.Request, name string) (*time.Time, error) {
	raw := strings.TrimSpace(request.URL.Query().Get(name))
	if raw == "" {
		return nil, nil
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("%s must be an RFC 3339 timestamp", name)
	}
	return &value, nil
}

func requestActor(request *http.Request) string {
	if identity, ok := request.Context().Value(browserIdentityContextKey{}).(browserIdentity); ok {
		return browserActor(identity)
	}
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

func browserActor(identity browserIdentity) string {
	digest := sha256.Sum256([]byte(identity.Subject))
	return "workspace:oidc:" + hex.EncodeToString(digest[:8])
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	if err := json.NewEncoder(response).Encode(value); err != nil {
		slog.Error("encode HTTP response", "error", err)
	}
}

func writeError(response http.ResponseWriter, status int, message string) {
	writeProblem(response, status, defaultProblemCode(status), message, status >= 500, nil)
}

func writeProblem(response http.ResponseWriter, status int, code, message string, retryable bool, details map[string]any) {
	problem := map[string]any{
		"code":      code,
		"message":   message,
		"error":     message,
		"retryable": retryable,
	}
	if requestID := response.Header().Get("X-Request-ID"); requestID != "" {
		problem["requestId"] = requestID
	}
	if len(details) > 0 {
		problem["context"] = details
	}
	writeJSON(response, status, problem)
}

func defaultProblemCode(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusUnsupportedMediaType, http.StatusRequestEntityTooLarge:
		return "invalid_request"
	case http.StatusUnauthorized:
		return "authentication_required"
	case http.StatusForbidden:
		return "permission_denied"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusServiceUnavailable:
		return "service_unavailable"
	default:
		return "internal_error"
	}
}

type requestIDContextKey struct{}

func requestMetadata(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		buffer := make([]byte, 12)
		var requestID string
		if _, err := rand.Read(buffer); err == nil {
			requestID = hex.EncodeToString(buffer)
		} else {
			requestID = strconv.FormatInt(time.Now().UnixNano(), 36)
		}
		response.Header().Set("X-Request-ID", requestID)
		request = request.WithContext(context.WithValue(request.Context(), requestIDContextKey{}, requestID))
		next.ServeHTTP(response, request)
	})
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		started := time.Now()
		next.ServeHTTP(response, request)
		slog.Info("HTTP request", "requestId", request.Context().Value(requestIDContextKey{}), "method", request.Method, "path", request.URL.Path, "duration", time.Since(started).Round(time.Millisecond).String())
	})
}
