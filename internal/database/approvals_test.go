package database

import (
	"errors"
	"testing"

	"github.com/rvben/werkt/internal/domain"
)

func TestValidateApprovalResponseEnforcesDeclaredTypesAndRequiredFields(t *testing.T) {
	approval := domain.Approval{
		Fields: []domain.ApprovalField{
			{ID: "title", Type: "text", Required: true},
			{ID: "audience", Type: "select", Options: []string{"public", "private"}},
			{ID: "notify", Type: "boolean", Value: true},
		},
		Actions: []domain.ApprovalAction{
			{ID: "approve", RequiresFields: true},
			{ID: "reject"},
		},
	}
	values, err := validateApprovalResponse(approval, "approve", map[string]any{"title": " Sunday service ", "audience": "public"})
	if err != nil {
		t.Fatal(err)
	}
	if values["title"] != "Sunday service" || values["audience"] != "public" || values["notify"] != true {
		t.Fatalf("values=%#v", values)
	}
	if _, err := validateApprovalResponse(approval, "approve", map[string]any{"audience": "public"}); !errors.Is(err, ErrApprovalInvalidResponse) {
		t.Fatalf("missing required field error=%v", err)
	}
	if _, err := validateApprovalResponse(approval, "approve", map[string]any{"title": "Service", "audience": "external"}); !errors.Is(err, ErrApprovalInvalidResponse) {
		t.Fatalf("undeclared option error=%v", err)
	}
	if _, err := validateApprovalResponse(approval, "reject", map[string]any{}); err != nil {
		t.Fatalf("reject should not require approve fields: %v", err)
	}
}
