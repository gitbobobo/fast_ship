package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
	"time"
)

type IssueAssetRepository struct {
	db *gorm.DB
}

func NewIssueAssetRepository(db *gorm.DB) *IssueAssetRepository {
	return &IssueAssetRepository{db: db}
}

func (r *IssueAssetRepository) Create(asset *model.IssueAsset) error {
	return r.db.Create(asset).Error
}

func (r *IssueAssetRepository) CreateTx(tx *gorm.DB, asset *model.IssueAsset) error {
	return tx.Create(asset).Error
}

func (r *IssueAssetRepository) FindByID(id string) (*model.IssueAsset, error) {
	var asset model.IssueAsset
	if err := r.db.Where("id = ?", id).First(&asset).Error; err != nil {
		return nil, err
	}
	return &asset, nil
}

func (r *IssueAssetRepository) ListByIssueID(issueID string) ([]model.IssueAsset, error) {
	var assets []model.IssueAsset
	if err := r.db.Where("issue_id = ?", issueID).Order("created_at ASC").Find(&assets).Error; err != nil {
		return nil, err
	}
	return assets, nil
}

func (r *IssueAssetRepository) DeleteByIssueIDAndIDs(issueID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return r.db.Where("issue_id = ? AND id IN ?", issueID, ids).Delete(&model.IssueAsset{}).Error
}

func (r *IssueAssetRepository) UpdateStatusByIssueIDAndIDs(issueID string, ids []string, status model.IssueAssetStatus) error {
	if len(ids) == 0 {
		return nil
	}
	return r.db.Model(&model.IssueAsset{}).
		Where("issue_id = ? AND id IN ?", issueID, ids).
		Update("status", status).
		Error
}

func (r *IssueAssetRepository) ListPendingCreatedBefore(cutoff time.Time) ([]model.IssueAsset, error) {
	var assets []model.IssueAsset
	if err := r.db.
		Where("status = ? AND created_at < ?", model.IssueAssetStatusPending, cutoff).
		Order("created_at ASC").
		Find(&assets).
		Error; err != nil {
		return nil, err
	}
	return assets, nil
}
