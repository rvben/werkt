package service

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/domain"
)

const retentionPlanLifetime = 15 * time.Minute
const retentionApplyLease = 5 * time.Minute

var ErrInvalidRetentionPolicy = errors.New("invalid retention policy")

var DefaultRetentionPolicy = domain.RetentionPolicy{
	SourceMaxAge:          "720h",
	ArtifactMaxAge:        "2160h",
	KeepRetryableSources:  3,
	KeepInactiveRevisions: 5,
}

type RetentionManager struct {
	store   *database.Store
	dataDir string
	now     func() time.Time
}

func NewRetentionManager(store *database.Store, dataDir string) *RetentionManager {
	return &RetentionManager{store: store, dataDir: dataDir, now: time.Now}
}

func (m *RetentionManager) Plan(ctx context.Context, policy domain.RetentionPolicy, actor string) (domain.RetentionPlan, error) {
	normalized, sourceAge, artifactAge, err := validateRetentionPolicy(policy)
	if err != nil {
		return domain.RetentionPlan{}, err
	}
	items, err := m.candidates(ctx, normalized, sourceAge, artifactAge)
	if err != nil {
		return domain.RetentionPlan{}, err
	}
	planID, err := newRetentionPlanID()
	if err != nil {
		return domain.RetentionPlan{}, err
	}
	now := m.now().UTC()
	value := domain.RetentionPlan{
		ID: planID, Status: domain.RetentionPlanPlanned, Policy: normalized,
		Items: items, Actor: actor, CreatedAt: now, ExpiresAt: now.Add(retentionPlanLifetime),
	}
	for _, item := range items {
		value.Summary.Items++
		value.Summary.EstimatedBytes += item.EstimatedBytes
	}
	if err := m.store.CreateRetentionPlan(ctx, value); err != nil {
		return domain.RetentionPlan{}, err
	}
	return m.store.GetRetentionPlan(ctx, planID)
}

func (m *RetentionManager) Get(ctx context.Context, planID string) (domain.RetentionPlan, error) {
	return m.store.GetRetentionPlan(ctx, planID)
}

func (m *RetentionManager) Apply(ctx context.Context, planID, actor string) (domain.RetentionPlan, error) {
	claimed, err := m.store.ClaimRetentionPlan(ctx, planID, retentionApplyLease)
	if err != nil {
		return domain.RetentionPlan{}, err
	}
	if !claimed {
		return m.store.GetRetentionPlan(ctx, planID)
	}
	plan, err := m.store.GetRetentionPlan(ctx, planID)
	if err != nil {
		return domain.RetentionPlan{}, err
	}
	_, sourceAge, artifactAge, err := validateRetentionPolicy(plan.Policy)
	if err != nil {
		return domain.RetentionPlan{}, err
	}
	current, err := m.selectCandidates(ctx, plan.Policy, sourceAge, artifactAge)
	if err != nil {
		return domain.RetentionPlan{}, err
	}
	eligible := make(map[string]struct{}, len(current))
	for _, item := range current {
		eligible[retentionItemKey(item.Kind, item.StoragePath)] = struct{}{}
	}
	for _, item := range plan.Items {
		if item.Status != domain.RetentionItemPlanned {
			continue
		}
		if _, ok := eligible[retentionItemKey(item.Kind, item.StoragePath)]; !ok {
			if err := m.store.RecordRetentionItem(ctx, plan.ID, item.Position, domain.RetentionItemSkipped, "storage became protected or no longer exists"); err != nil {
				return domain.RetentionPlan{}, err
			}
			continue
		}
		if _, _, err := m.inspectStorage(item.Kind, item.StoragePath); err != nil {
			if recordErr := m.store.RecordRetentionItem(ctx, plan.ID, item.Position, domain.RetentionItemFailed, err.Error()); recordErr != nil {
				return domain.RetentionPlan{}, errors.Join(err, recordErr)
			}
			continue
		}
		detached, err := m.store.DetachRetentionItem(ctx, item.Kind, item.StoragePath)
		if err != nil {
			if recordErr := m.store.RecordRetentionItem(ctx, plan.ID, item.Position, domain.RetentionItemFailed, err.Error()); recordErr != nil {
				return domain.RetentionPlan{}, errors.Join(err, recordErr)
			}
			continue
		}
		if !detached {
			if err := m.store.RecordRetentionItem(ctx, plan.ID, item.Position, domain.RetentionItemSkipped, "database references became protected or were already detached"); err != nil {
				return domain.RetentionPlan{}, err
			}
			continue
		}
		if err := os.RemoveAll(item.StoragePath); err != nil {
			if recordErr := m.store.RecordRetentionItem(ctx, plan.ID, item.Position, domain.RetentionItemFailed, "database references detached; remove storage: "+err.Error()); recordErr != nil {
				return domain.RetentionPlan{}, errors.Join(err, recordErr)
			}
			continue
		}
		if err := m.store.RecordRetentionItem(ctx, plan.ID, item.Position, domain.RetentionItemDeleted, ""); err != nil {
			return domain.RetentionPlan{}, err
		}
	}
	if err := m.store.CompleteRetentionPlan(ctx, plan.ID, actor); err != nil {
		return domain.RetentionPlan{}, err
	}
	return m.store.GetRetentionPlan(ctx, plan.ID)
}

func (m *RetentionManager) candidates(ctx context.Context, policy domain.RetentionPolicy, sourceAge, artifactAge time.Duration) ([]domain.RetentionItem, error) {
	items, err := m.selectCandidates(ctx, policy, sourceAge, artifactAge)
	if err != nil {
		return nil, err
	}
	result := make([]domain.RetentionItem, 0, len(items))
	for _, item := range items {
		storageKey, size, err := m.inspectStorage(item.Kind, item.StoragePath)
		if err != nil {
			return nil, err
		}
		item.StorageKey = storageKey
		item.EstimatedBytes = size
		item.Position = len(result)
		item.Status = domain.RetentionItemPlanned
		result = append(result, item)
	}
	return result, nil
}

func (m *RetentionManager) selectCandidates(ctx context.Context, policy domain.RetentionPolicy, sourceAge, artifactAge time.Duration) ([]domain.RetentionItem, error) {
	sources, err := m.store.ListDeploymentSourceRecords(ctx)
	if err != nil {
		return nil, err
	}
	artifacts, err := m.store.ListRevisionArtifactRecords(ctx)
	if err != nil {
		return nil, err
	}
	return selectRetentionCandidates(m.now(), policy, sourceAge, artifactAge, sources, artifacts), nil
}

func validateRetentionPolicy(value domain.RetentionPolicy) (domain.RetentionPolicy, time.Duration, time.Duration, error) {
	if value.SourceMaxAge == "" {
		value.SourceMaxAge = DefaultRetentionPolicy.SourceMaxAge
	}
	if value.ArtifactMaxAge == "" {
		value.ArtifactMaxAge = DefaultRetentionPolicy.ArtifactMaxAge
	}
	sourceAge, err := time.ParseDuration(value.SourceMaxAge)
	if err != nil || sourceAge <= 0 {
		return domain.RetentionPolicy{}, 0, 0, fmt.Errorf("%w: sourceMaxAge must be a positive Go duration", ErrInvalidRetentionPolicy)
	}
	artifactAge, err := time.ParseDuration(value.ArtifactMaxAge)
	if err != nil || artifactAge <= 0 {
		return domain.RetentionPolicy{}, 0, 0, fmt.Errorf("%w: artifactMaxAge must be a positive Go duration", ErrInvalidRetentionPolicy)
	}
	if value.KeepRetryableSources < 0 || value.KeepRetryableSources > 10_000 {
		return domain.RetentionPolicy{}, 0, 0, fmt.Errorf("%w: keepRetryableSources must be between 0 and 10000", ErrInvalidRetentionPolicy)
	}
	if value.KeepInactiveRevisions < 0 || value.KeepInactiveRevisions > 10_000 {
		return domain.RetentionPolicy{}, 0, 0, fmt.Errorf("%w: keepInactiveRevisions must be between 0 and 10000", ErrInvalidRetentionPolicy)
	}
	value.SourceMaxAge = sourceAge.String()
	value.ArtifactMaxAge = artifactAge.String()
	return value, sourceAge, artifactAge, nil
}

type sourceGroup struct {
	path          string
	resourceIDs   []string
	automationIDs []string
	newest        time.Time
	terminal      bool
	retryable     bool
	keepByCount   bool
}

type artifactGroup struct {
	path          string
	resourceIDs   []string
	automationIDs []string
	newest        time.Time
	protected     bool
	keepByCount   bool
}

func selectRetentionCandidates(now time.Time, policy domain.RetentionPolicy, sourceAge, artifactAge time.Duration, sources []database.DeploymentSourceRecord, artifacts []database.RevisionArtifactRecord) []domain.RetentionItem {
	sourceGroups := make(map[string]*sourceGroup)
	for _, record := range sources {
		group := sourceGroups[record.Path]
		if group == nil {
			group = &sourceGroup{path: record.Path, terminal: true}
			sourceGroups[record.Path] = group
		}
		group.resourceIDs = append(group.resourceIDs, record.ID)
		group.automationIDs = appendUnique(group.automationIDs, record.AutomationID)
		if record.CreatedAt.After(group.newest) {
			group.newest = record.CreatedAt
		}
		if record.Status != domain.DeploymentSucceeded && record.Status != domain.DeploymentFailed && record.Status != domain.DeploymentCancelled {
			group.terminal = false
		}
		if record.Status == domain.DeploymentFailed || record.Status == domain.DeploymentCancelled {
			group.retryable = true
		}
	}
	markSourceCountProtection(sourceGroups, policy.KeepRetryableSources)

	artifactGroups := make(map[string]*artifactGroup)
	for _, record := range artifacts {
		group := artifactGroups[record.Path]
		if group == nil {
			group = &artifactGroup{path: record.Path}
			artifactGroups[record.Path] = group
		}
		group.resourceIDs = append(group.resourceIDs, record.ID)
		group.automationIDs = appendUnique(group.automationIDs, record.AutomationID)
		if record.CreatedAt.After(group.newest) {
			group.newest = record.CreatedAt
		}
		group.protected = group.protected || record.Active || record.ReferencedByRun || record.ReferencedByDeployment
	}
	markArtifactCountProtection(artifactGroups, policy.KeepInactiveRevisions)

	var result []domain.RetentionItem
	for _, group := range sourceGroups {
		if !group.terminal || !group.newest.Before(now.Add(-sourceAge)) || (group.retryable && group.keepByCount) {
			continue
		}
		sort.Strings(group.resourceIDs)
		sort.Strings(group.automationIDs)
		reason := "terminal deployment source exceeded sourceMaxAge"
		if group.retryable {
			reason += " and keepRetryableSources"
		}
		result = append(result, domain.RetentionItem{Kind: domain.RetentionKindSource, StoragePath: group.path,
			ResourceIDs: group.resourceIDs, AutomationIDs: nonEmpty(group.automationIDs), Reason: reason})
	}
	for _, group := range artifactGroups {
		if group.protected || group.keepByCount || !group.newest.Before(now.Add(-artifactAge)) {
			continue
		}
		sort.Strings(group.resourceIDs)
		sort.Strings(group.automationIDs)
		result = append(result, domain.RetentionItem{Kind: domain.RetentionKindArtifact, StoragePath: group.path,
			ResourceIDs: group.resourceIDs, AutomationIDs: nonEmpty(group.automationIDs),
			Reason: "inactive revision artifact exceeded artifactMaxAge and keepInactiveRevisions"})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind == result[j].Kind {
			return result[i].StoragePath < result[j].StoragePath
		}
		return result[i].Kind < result[j].Kind
	})
	return result
}

func markSourceCountProtection(groups map[string]*sourceGroup, keep int) {
	byAutomation := make(map[string][]*sourceGroup)
	for _, group := range groups {
		if !group.retryable {
			continue
		}
		keys := nonEmpty(group.automationIDs)
		if len(keys) == 0 {
			keys = []string{"_unresolved"}
		}
		for _, automationID := range keys {
			byAutomation[automationID] = append(byAutomation[automationID], group)
		}
	}
	markNewestSources(byAutomation, keep)
}

func markNewestSources(groups map[string][]*sourceGroup, keep int) {
	for _, values := range groups {
		sort.Slice(values, func(i, j int) bool { return values[i].newest.After(values[j].newest) })
		for index := 0; index < len(values) && index < keep; index++ {
			values[index].keepByCount = true
		}
	}
}

func markArtifactCountProtection(groups map[string]*artifactGroup, keep int) {
	byAutomation := make(map[string][]*artifactGroup)
	for _, group := range groups {
		if group.protected {
			continue
		}
		for _, automationID := range nonEmpty(group.automationIDs) {
			byAutomation[automationID] = append(byAutomation[automationID], group)
		}
	}
	for _, values := range byAutomation {
		sort.Slice(values, func(i, j int) bool { return values[i].newest.After(values[j].newest) })
		for index := 0; index < len(values) && index < keep; index++ {
			values[index].keepByCount = true
		}
	}
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func nonEmpty(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func (m *RetentionManager) inspectStorage(kind, path string) (string, int64, error) {
	directory := ""
	switch kind {
	case domain.RetentionKindSource:
		directory = "deployment-sources"
	case domain.RetentionKindArtifact:
		directory = "artifacts"
	default:
		return "", 0, fmt.Errorf("unsupported retention kind %q", kind)
	}
	root, err := filepath.Abs(filepath.Join(m.dataDir, directory))
	if err != nil {
		return "", 0, err
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return "", 0, err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || strings.Contains(relative, string(filepath.Separator)) {
		return "", 0, fmt.Errorf("retention path is not a direct child of %s", root)
	}
	size, err := storageSize(target)
	if err != nil {
		return "", 0, err
	}
	return filepath.ToSlash(filepath.Join(directory, relative)), size, nil
}

func storageSize(path string) (int64, error) {
	var size int64
	err := filepath.WalkDir(path, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			size += info.Size()
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return size, err
}

func retentionItemKey(kind, path string) string { return kind + "\x00" + path }

func newRetentionPlanID() (string, error) {
	var value [12]byte
	if _, err := cryptorand.Read(value[:]); err != nil {
		return "", err
	}
	return "ret_" + hex.EncodeToString(value[:]), nil
}
