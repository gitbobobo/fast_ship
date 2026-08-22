package repository

import (
	"errors"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrStaleIssueShipHook = errors.New("issue ship hook execution is stale")

type IssueShipHookRepository struct {
	db *gorm.DB
}

func NewIssueShipHookRepository(db *gorm.DB) *IssueShipHookRepository {
	return &IssueShipHookRepository{db: db}
}

func (r *IssueShipHookRepository) Upsert(hook *model.IssueShipHook) error {
	// This is a new appointment, not an update to an in-flight execution.
	// Keep execution result columns owned by the worker so a stale worker can
	// never write its old appointment back over this one.
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "issue_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"project_id",
			"status",
			"comment_enabled",
			"comment_body",
			"close_enabled",
			"workflow_enabled",
			"workflow_status",
			"fired_version_id",
			"fired_version_number",
			"fired_release_url",
			"fired_at",
			"comment_ok",
			"comment_skipped",
			"comment_error",
			"comment_rendered_body",
			"close_ok",
			"close_skipped",
			"close_error",
			"workflow_ok",
			"workflow_skipped",
			"workflow_error",
			"retry_pending",
			"updated_by_user_id",
			"updated_at",
			"execution_token",
			"lease_token",
			"lease_expires_at",
		}),
	}).Create(hook).Error
}

// ClaimPendingByProjectID atomically moves pending hooks to running. Stale
// running hooks are reclaimed as well, allowing a new ship request to retry a
// worker that died after claiming the row.
func (r *IssueShipHookRepository) ClaimPendingByProjectID(
	projectID string,
	versionID string,
	versionNumber string,
	releaseURL string,
	claimedAt time.Time,
	staleBefore time.Time,
) ([]model.IssueShipHook, error) {
	const leaseDuration = 30 * time.Minute
	var claimed []model.IssueShipHook
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var candidates []model.IssueShipHook
		if err := tx.Where(
			"project_id = ? AND (status = ? OR (status = ? AND ((lease_expires_at IS NULL AND updated_at < ?) OR lease_expires_at < ?)) OR (status = ? AND retry_pending = ?))",
			projectID, model.IssueShipHookStatusPending, model.IssueShipHookStatusRunning, staleBefore, claimedAt, model.IssueShipHookStatusFired, true,
		).Order("created_at ASC, issue_id ASC").Find(&candidates).Error; err != nil {
			return err
		}
		for i := range candidates {
			wasPending := candidates[i].Status == model.IssueShipHookStatusPending
			executionID := candidates[i].ExecutionToken
			if executionID == "" {
				executionID = uuid.NewString()
			}
			leaseToken := uuid.NewString()
			leaseExpiresAt := claimedAt.Add(leaseDuration)
			updates := map[string]any{
				"status":           model.IssueShipHookStatusRunning,
				"execution_token":  executionID,
				"lease_token":      leaseToken,
				"lease_expires_at": leaseExpiresAt,
				"updated_at":       claimedAt,
			}
			// Only the first claim establishes execution ownership. Recovery
			// must retain the original release identity and idempotency token.
			if wasPending {
				updates["fired_version_id"] = versionID
				updates["fired_version_number"] = versionNumber
				updates["fired_release_url"] = releaseURL
				updates["fired_at"] = claimedAt
			}
			result := tx.Model(&model.IssueShipHook{}).Where(
				"issue_id = ? AND (status = ? OR (status = ? AND ((lease_expires_at IS NULL AND updated_at < ?) OR lease_expires_at < ?)) OR (status = ? AND retry_pending = ?))",
				candidates[i].IssueID, model.IssueShipHookStatusPending, model.IssueShipHookStatusRunning, staleBefore, claimedAt, model.IssueShipHookStatusFired, true,
			).Updates(updates)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				continue
			}
			candidates[i].Status = model.IssueShipHookStatusRunning
			candidates[i].ExecutionToken = executionID
			candidates[i].LeaseToken = leaseToken
			candidates[i].LeaseExpiresAt = &leaseExpiresAt
			if wasPending {
				candidates[i].FiredVersionID = versionID
				candidates[i].FiredVersionNumber = versionNumber
				candidates[i].FiredReleaseURL = releaseURL
				candidates[i].FiredAt = &claimedAt
			}
			candidates[i].UpdatedAt = claimedAt
			claimed = append(claimed, candidates[i])
		}
		return nil
	})
	return claimed, err
}

// CompleteExecution is the only worker write path. The rotated lease token
// makes completion compare-and-set: a reclaimed worker cannot overwrite the
// newer worker's result, even though both claims share one execution token.
func (r *IssueShipHookRepository) CompleteExecution(hook *model.IssueShipHook, completedAt time.Time) error {
	result := r.db.Model(&model.IssueShipHook{}).Where(
		"issue_id = ? AND status = ? AND lease_token = ? AND lease_expires_at > ?",
		hook.IssueID, model.IssueShipHookStatusRunning, hook.LeaseToken, completedAt,
	).Updates(map[string]any{
		"status":                model.IssueShipHookStatusFired,
		"fired_version_id":      hook.FiredVersionID,
		"fired_version_number":  hook.FiredVersionNumber,
		"fired_release_url":     hook.FiredReleaseURL,
		"fired_at":              hook.FiredAt,
		"comment_ok":            hook.CommentOK,
		"comment_skipped":       hook.CommentSkipped,
		"comment_error":         hook.CommentError,
		"comment_rendered_body": hook.CommentRenderedBody,
		"close_ok":              hook.CloseOK,
		"close_skipped":         hook.CloseSkipped,
		"close_error":           hook.CloseError,
		"workflow_ok":           hook.WorkflowOK,
		"workflow_skipped":      hook.WorkflowSkipped,
		"workflow_error":        hook.WorkflowError,
		"retry_pending":         hook.RetryPending,
		"lease_expires_at":      nil,
		"updated_at":            completedAt,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrStaleIssueShipHook
	}
	return nil
}

func (r *IssueShipHookRepository) RenewLease(hook *model.IssueShipHook, now time.Time) error {
	expiresAt := now.Add(30 * time.Minute)
	result := r.db.Model(&model.IssueShipHook{}).Where(
		"issue_id = ? AND status = ? AND lease_token = ? AND lease_expires_at > ?",
		hook.IssueID, model.IssueShipHookStatusRunning, hook.LeaseToken, now,
	).Updates(map[string]any{"lease_expires_at": expiresAt, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrStaleIssueShipHook
	}
	hook.LeaseExpiresAt = &expiresAt
	return nil
}

func (r *IssueShipHookRepository) GetByIssueID(issueID string) (*model.IssueShipHook, error) {
	var hook model.IssueShipHook
	if err := r.db.Where("issue_id = ?", issueID).First(&hook).Error; err != nil {
		return nil, err
	}
	return &hook, nil
}

func (r *IssueShipHookRepository) ListByIssueIDs(issueIDs []string) (map[string]model.IssueShipHook, error) {
	result := make(map[string]model.IssueShipHook, len(issueIDs))
	if len(issueIDs) == 0 {
		return result, nil
	}

	var hooks []model.IssueShipHook
	if err := r.db.Where("issue_id IN ?", issueIDs).Find(&hooks).Error; err != nil {
		return nil, err
	}
	for _, hook := range hooks {
		result[hook.IssueID] = hook
	}
	return result, nil
}

// ListByFiredVersionIDs returns every execution belonging to the original
// release. A release is only recovered after this complete set is resolved.
func (r *IssueShipHookRepository) ListByFiredVersionIDs(versionIDs []string) ([]model.IssueShipHook, error) {
	if len(versionIDs) == 0 {
		return []model.IssueShipHook{}, nil
	}
	var hooks []model.IssueShipHook
	if err := r.db.Where("fired_version_id IN ?", versionIDs).Order("fired_version_id ASC, issue_id ASC").Find(&hooks).Error; err != nil {
		return nil, err
	}
	return hooks, nil
}

func (r *IssueShipHookRepository) DeleteByIssueID(issueID string) error {
	return r.db.Where("issue_id = ?", issueID).Delete(&model.IssueShipHook{}).Error
}

func (r *IssueShipHookRepository) ListPendingByProjectID(projectID string) ([]model.IssueShipHook, error) {
	var hooks []model.IssueShipHook
	err := r.db.
		Where("project_id = ? AND (status = ? OR (status = ? AND retry_pending = ?))", projectID, model.IssueShipHookStatusPending, model.IssueShipHookStatusFired, true).
		Order("created_at ASC, issue_id ASC").
		Find(&hooks).Error
	return hooks, err
}
