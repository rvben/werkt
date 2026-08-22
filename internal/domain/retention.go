package domain

import "time"

const (
	RetentionPlanPlanned  = "planned"
	RetentionPlanApplying = "applying"
	RetentionPlanApplied  = "applied"

	RetentionItemPlanned = "planned"
	RetentionItemDeleted = "deleted"
	RetentionItemSkipped = "skipped"
	RetentionItemFailed  = "failed"

	RetentionKindSource   = "source"
	RetentionKindArtifact = "artifact"
)

// RetentionPolicy retains material when it is newer than the corresponding
// age OR falls within the corresponding per-automation count. An item becomes
// eligible only after both protections no longer apply.
type RetentionPolicy struct {
	SourceMaxAge          string `json:"sourceMaxAge"`
	ArtifactMaxAge        string `json:"artifactMaxAge"`
	KeepRetryableSources  int    `json:"keepRetryableSources"`
	KeepInactiveRevisions int    `json:"keepInactiveRevisions"`
}

type RetentionSummary struct {
	Items          int   `json:"items"`
	EstimatedBytes int64 `json:"estimatedBytes"`
	Deleted        int   `json:"deleted"`
	Skipped        int   `json:"skipped"`
	Failed         int   `json:"failed"`
}

type RetentionItem struct {
	Position       int        `json:"position"`
	Kind           string     `json:"kind"`
	StorageKey     string     `json:"storageKey"`
	ResourceIDs    []string   `json:"resourceIds"`
	AutomationIDs  []string   `json:"automationIds,omitempty"`
	Reason         string     `json:"reason"`
	EstimatedBytes int64      `json:"estimatedBytes"`
	Status         string     `json:"status"`
	Error          string     `json:"error,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	DeletedAt      *time.Time `json:"deletedAt,omitempty"`

	// StoragePath never leaves the management boundary.
	StoragePath string `json:"-"`
}

type RetentionPlan struct {
	ID        string           `json:"id"`
	Status    string           `json:"status"`
	Policy    RetentionPolicy  `json:"policy"`
	Summary   RetentionSummary `json:"summary"`
	Items     []RetentionItem  `json:"items"`
	Actor     string           `json:"actor"`
	CreatedAt time.Time        `json:"createdAt"`
	ExpiresAt time.Time        `json:"expiresAt"`
	AppliedAt *time.Time       `json:"appliedAt,omitempty"`
}
