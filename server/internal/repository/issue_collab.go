package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type IssueCollabRepository struct {
	db *gorm.DB
}

func NewIssueCollabRepository(db *gorm.DB) *IssueCollabRepository {
	return &IssueCollabRepository{db: db}
}

func (r *IssueCollabRepository) Transaction(fc func(tx *gorm.DB) error) error {
	return r.db.Transaction(fc)
}

func (r *IssueCollabRepository) ListSuggestionsByIssueID(issueID string) ([]model.IssueCollabSuggestion, error) {
	var suggestions []model.IssueCollabSuggestion
	err := r.db.
		Where("issue_id = ?", issueID).
		Order("sort_order ASC, id ASC").
		Find(&suggestions).Error
	return suggestions, err
}

// ReplaceSuggestionsTx 全量替换：事务内删旧建新；空切片仅删除（清空）。
func (r *IssueCollabRepository) ReplaceSuggestionsTx(tx *gorm.DB, issueID string, suggestions []model.IssueCollabSuggestion) error {
	if err := tx.Where("issue_id = ?", issueID).Delete(&model.IssueCollabSuggestion{}).Error; err != nil {
		return err
	}
	if len(suggestions) == 0 {
		return nil
	}
	return tx.Create(&suggestions).Error
}

func (r *IssueCollabRepository) GetPlan(issueID string) (*model.IssueCollabPlan, error) {
	var plan model.IssueCollabPlan
	if err := r.db.Where("issue_id = ?", issueID).First(&plan).Error; err != nil {
		return nil, err
	}
	return &plan, nil
}

func (r *IssueCollabRepository) UpsertPlan(plan *model.IssueCollabPlan) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "issue_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"body",
			"author_user_id",
			"author_kind",
			"updated_at",
		}),
	}).Create(plan).Error
}

func (r *IssueCollabRepository) GetReview(issueID string) (*model.IssueCollabReview, error) {
	var review model.IssueCollabReview
	if err := r.db.Where("issue_id = ?", issueID).First(&review).Error; err != nil {
		return nil, err
	}
	return &review, nil
}

func (r *IssueCollabRepository) UpsertReview(review *model.IssueCollabReview) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "issue_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"body",
			"author_user_id",
			"author_kind",
			"updated_at",
		}),
	}).Create(review).Error
}

func (r *IssueCollabRepository) GetSummary(issueID string) (*model.IssueCollabSummary, error) {
	var summary model.IssueCollabSummary
	if err := r.db.Where("issue_id = ?", issueID).First(&summary).Error; err != nil {
		return nil, err
	}
	return &summary, nil
}

func (r *IssueCollabRepository) UpsertSummary(summary *model.IssueCollabSummary) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "issue_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"body",
			"commit_ids_json",
			"author_user_id",
			"author_kind",
			"updated_at",
		}),
	}).Create(summary).Error
}

func (r *IssueCollabRepository) DeleteSuggestionsByIssueID(issueID string) error {
	return r.db.Where("issue_id = ?", issueID).Delete(&model.IssueCollabSuggestion{}).Error
}

func (r *IssueCollabRepository) DeletePlanByIssueID(issueID string) error {
	return r.db.Where("issue_id = ?", issueID).Delete(&model.IssueCollabPlan{}).Error
}

func (r *IssueCollabRepository) DeleteReviewByIssueID(issueID string) error {
	return r.db.Where("issue_id = ?", issueID).Delete(&model.IssueCollabReview{}).Error
}

func (r *IssueCollabRepository) DeleteSummaryByIssueID(issueID string) error {
	return r.db.Where("issue_id = ?", issueID).Delete(&model.IssueCollabSummary{}).Error
}

func (r *IssueCollabRepository) DeleteAllByIssueID(issueID string) error {
	return r.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("issue_id = ?", issueID).Delete(&model.IssueCollabSuggestion{}).Error; err != nil {
			return err
		}
		if err := tx.Where("issue_id = ?", issueID).Delete(&model.IssueCollabPlan{}).Error; err != nil {
			return err
		}
		if err := tx.Where("issue_id = ?", issueID).Delete(&model.IssueCollabReview{}).Error; err != nil {
			return err
		}
		return tx.Where("issue_id = ?", issueID).Delete(&model.IssueCollabSummary{}).Error
	})
}
