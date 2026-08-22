package repository

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type IssueShipHookRepository struct {
	db *gorm.DB
}

func NewIssueShipHookRepository(db *gorm.DB) *IssueShipHookRepository {
	return &IssueShipHookRepository{db: db}
}

func (r *IssueShipHookRepository) Upsert(hook *model.IssueShipHook) error {
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
			"updated_by_user_id",
			"updated_at",
		}),
	}).Create(hook).Error
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

func (r *IssueShipHookRepository) DeleteByIssueID(issueID string) error {
	return r.db.Where("issue_id = ?", issueID).Delete(&model.IssueShipHook{}).Error
}

func (r *IssueShipHookRepository) ListPendingByProjectID(projectID string) ([]model.IssueShipHook, error) {
	var hooks []model.IssueShipHook
	err := r.db.
		Where("project_id = ? AND status = ?", projectID, model.IssueShipHookStatusPending).
		Order("created_at ASC, issue_id ASC").
		Find(&hooks).Error
	return hooks, err
}

func (r *IssueShipHookRepository) ConsumePendingByProjectID(
	projectID, versionID, versionNumber, releaseURL string,
	firedAt time.Time,
) ([]model.IssueShipHook, error) {
	var consumed []model.IssueShipHook
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var pending []model.IssueShipHook
		if err := tx.
			Where("project_id = ? AND status = ?", projectID, model.IssueShipHookStatusPending).
			Order("created_at ASC, issue_id ASC").
			Find(&pending).Error; err != nil {
			return err
		}
		if len(pending) == 0 {
			return nil
		}

		issueIDs := make([]string, 0, len(pending))
		for _, hook := range pending {
			issueIDs = append(issueIDs, hook.IssueID)
		}

		if err := tx.Model(&model.IssueShipHook{}).
			Where("issue_id IN ? AND status = ?", issueIDs, model.IssueShipHookStatusPending).
			Updates(map[string]any{
				"status":               model.IssueShipHookStatusFired,
				"fired_version_id":     versionID,
				"fired_version_number": versionNumber,
				"fired_release_url":    releaseURL,
				"fired_at":             firedAt,
				"updated_at":           firedAt,
			}).Error; err != nil {
			return err
		}

		// issue_id 是主键，SELECT 出的 pending 即本次 UPDATE 命中的全集，直接在内存标记为 fired 返回。
		consumed = pending
		for i := range consumed {
			consumed[i].Status = model.IssueShipHookStatusFired
			consumed[i].FiredVersionID = versionID
			consumed[i].FiredVersionNumber = versionNumber
			consumed[i].FiredReleaseURL = releaseURL
			consumed[i].FiredAt = &firedAt
			consumed[i].UpdatedAt = firedAt
		}
		return nil
	})
	return consumed, err
}
