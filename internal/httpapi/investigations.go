package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/domain"
)

type InvestigationRegistry interface {
	GetInvestigation(context.Context, string) (domain.Investigation, error)
	ListInvestigations(context.Context, string, int64, database.ListCursor, int) ([]domain.Investigation, error)
	ReserveInvestigation(context.Context, domain.InvestigationSpec, string) (domain.Investigation, bool, error)
	UpdateInvestigation(context.Context, string, int64, domain.InvestigationState, string) (domain.Investigation, error)
}

func WithInvestigationRegistry(registry InvestigationRegistry) Option {
	return func(s *Server) { s.investigations = registry }
}

func (s *Server) investigationAvailable(w http.ResponseWriter) bool {
	w.Header().Set("Cache-Control", "no-store")
	if s.investigations == nil {
		writeProblem(w, http.StatusServiceUnavailable, "lookup_unavailable", "Investigation registry is unavailable.", true, nil)
		return false
	}
	return true
}

func investigationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidInvestigation):
		writeProblem(w, http.StatusBadRequest, "invalid_investigation", err.Error(), false, nil)
	case errors.Is(err, database.ErrInvestigationConflict):
		writeProblem(w, http.StatusConflict, "investigation_conflict", err.Error(), false, nil)
	case errors.Is(err, database.ErrInvestigationNotFound):
		writeProblem(w, http.StatusNotFound, "investigation_not_found", "Investigation not found.", false, nil)
	default:
		writeProblem(w, http.StatusServiceUnavailable, "lookup_unavailable", "Investigation registry could not be read or updated.", true, nil)
	}
}

func (s *Server) listInvestigations(w http.ResponseWriter, r *http.Request) {
	if !s.investigationAvailable(w) {
		return
	}
	repositoryID := r.URL.Query().Get("repositoryId")
	issue, err := strconv.ParseInt(r.URL.Query().Get("issueNumber"), 10, 64)
	if err != nil || domain.ValidateInvestigationSubject(repositoryID, issue) != nil {
		writeProblem(w, 400, "invalid_subject", "A positive repositoryId and issueNumber are required.", false, nil)
		return
	}
	limit, err := requestLimit(r, 100)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	cursor, err := requestCursor(r)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	items, err := s.investigations.ListInvestigations(r.Context(), repositoryID, issue, cursor, limit+1)
	if err != nil {
		investigationError(w, err)
		return
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		writeNextPageHeaders(w, r, database.ListCursor{ID: last.RequestID, CreatedAt: last.CreatedAt})
		next = w.Header().Get("X-Werkt-Next-Cursor")
	}
	if items == nil {
		items = []domain.Investigation{}
	}
	status := "found"
	if len(items) == 0 {
		status = "none"
	}
	writeJSON(w, 200, map[string]any{"status": status, "items": items, "nextCursor": next})
}

func (s *Server) getInvestigation(w http.ResponseWriter, r *http.Request) {
	if !s.investigationAvailable(w) {
		return
	}
	value, err := s.investigations.GetInvestigation(r.Context(), r.PathValue("investigation"))
	if err != nil {
		investigationError(w, err)
		return
	}
	writeJSON(w, 200, value)
}

func (s *Server) reserveInvestigation(w http.ResponseWriter, r *http.Request) {
	if !s.investigationAvailable(w) {
		return
	}
	var spec domain.InvestigationSpec
	if err := decodeJSON(w, r, 8192, &spec); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if err := spec.Validate(); err != nil {
		investigationError(w, err)
		return
	}
	value, created, err := s.investigations.ReserveInvestigation(r.Context(), spec, requestActor(r))
	if err != nil {
		investigationError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"created": created, "investigation": value})
}

func (s *Server) updateInvestigation(w http.ResponseWriter, r *http.Request) {
	if !s.investigationAvailable(w) {
		return
	}
	var body struct {
		ExpectedVersion int64                     `json:"expectedVersion"`
		State           domain.InvestigationState `json:"state"`
	}
	// Allows JSON escaping of the documented 64 KiB UTF-8 handoff plus metadata.
	if err := decodeJSON(w, r, 512<<10, &body); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if body.ExpectedVersion < 1 {
		writeError(w, 400, "expectedVersion is required")
		return
	}
	value, err := s.investigations.UpdateInvestigation(r.Context(), r.PathValue("investigation"), body.ExpectedVersion, body.State, requestActor(r))
	if err != nil {
		investigationError(w, err)
		return
	}
	writeJSON(w, 200, value)
}
