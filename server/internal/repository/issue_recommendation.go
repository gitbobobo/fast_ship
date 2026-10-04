package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type IssueRecommendationRepository struct {
	db *gorm.DB
}

func NewIssueRecommendationRepository(db *gorm.DB) *IssueRecommendationRepository {
	return &IssueRecommendationRepository{db: db}
}

func (r *IssueRecommendationRepository) Transaction(fc func(tx *gorm.DB) error) error {
	return r.db.Transaction(fc)
}

func (r *IssueRecommendationRepository) Get(issueID string) (*model.IssueRecommendation, error) {
	var rec model.IssueRecommendation
	if err := r.db.Where("issue_id = ?", issueID).First(&rec).Error; err != nil {
		return nil, err
	}
	return &rec, nil
}

// UpsertTx 覆盖写：冲突时整体替换业务列，仅保留原 created_at。
func (r *IssueRecommendationRepository) UpsertTx(tx *gorm.DB, rec *model.IssueRecommendation) error {
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "issue_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"project_id",
			"reason",
			"priority",
			"created_by",
			"updated_at",
		}),
	}).Create(rec).Error
}

// ReplaceDependenciesTx 整组替换前置依赖，position 按传入顺序 0..n 重排。
func (r *IssueRecommendationRepository) ReplaceDependenciesTx(tx *gorm.DB, issueID string, deps []model.RecommendationDependency) error {
	if err := tx.Where("issue_id = ?", issueID).Delete(&model.RecommendationDependency{}).Error; err != nil {
		return err
	}
	if len(deps) == 0 {
		return nil
	}
	return tx.Create(&deps).Error
}

func (r *IssueRecommendationRepository) Delete(issueID string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		return r.DeleteTx(tx, issueID)
	})
}

// DeleteTx 删除推荐行及其依赖行（依赖行外键指向 issues，不随推荐行级联，需显式删）。
func (r *IssueRecommendationRepository) DeleteTx(tx *gorm.DB, issueID string) error {
	if err := tx.Where("issue_id = ?", issueID).Delete(&model.RecommendationDependency{}).Error; err != nil {
		return err
	}
	return tx.Where("issue_id = ?", issueID).Delete(&model.IssueRecommendation{}).Error
}

// ListByProjectIDs 返回指定项目的推荐，按 priority 降序（high>medium>low）、updated_at 降序排序。
func (r *IssueRecommendationRepository) ListByProjectIDs(projectIDs []string) ([]model.IssueRecommendation, error) {
	recs := make([]model.IssueRecommendation, 0)
	if len(projectIDs) == 0 {
		return recs, nil
	}
	err := r.db.
		Where("project_id IN ?", projectIDs).
		Order("CASE priority WHEN 'high' THEN 0 WHEN 'medium' THEN 1 ELSE 2 END, updated_at DESC, issue_id ASC").
		Find(&recs).Error
	return recs, err
}

// ListDependencies 返回多个推荐的前置依赖行，按 position 升序保持写入顺序。
func (r *IssueRecommendationRepository) ListDependencies(issueIDs []string) ([]model.RecommendationDependency, error) {
	deps := make([]model.RecommendationDependency, 0)
	if len(issueIDs) == 0 {
		return deps, nil
	}
	err := r.db.
		Where("issue_id IN ?", issueIDs).
		Order("position ASC").
		Find(&deps).Error
	return deps, err
}
