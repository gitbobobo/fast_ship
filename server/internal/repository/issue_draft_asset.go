package repository

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

type IssueDraftAssetRepository struct {
	db *gorm.DB
}

func NewIssueDraftAssetRepository(db *gorm.DB) *IssueDraftAssetRepository {
	return &IssueDraftAssetRepository{db: db}
}

func (r *IssueDraftAssetRepository) Create(asset *model.IssueDraftAsset) error {
	return r.db.Create(asset).Error
}

func (r *IssueDraftAssetRepository) FindByID(id string) (*model.IssueDraftAsset, error) {
	var asset model.IssueDraftAsset
	if err := r.db.Where("id = ?", id).First(&asset).Error; err != nil {
		return nil, err
	}
	return &asset, nil
}

func (r *IssueDraftAssetRepository) ListByProjectIDAndIDs(projectID string, ids []string) ([]model.IssueDraftAsset, error) {
	return r.ListByProjectIDAndIDsTx(r.db, projectID, ids)
}

func (r *IssueDraftAssetRepository) ListByProjectIDAndIDsTx(tx *gorm.DB, projectID string, ids []string) ([]model.IssueDraftAsset, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	var assets []model.IssueDraftAsset
	if err := tx.
		Where("project_id = ? AND id IN ?", projectID, ids).
		Order("created_at ASC").
		Find(&assets).
		Error; err != nil {
		return nil, err
	}
	return assets, nil
}

func (r *IssueDraftAssetRepository) DeleteByProjectIDAndIDs(projectID string, ids []string) error {
	return r.DeleteByProjectIDAndIDsTx(r.db, projectID, ids)
}

func (r *IssueDraftAssetRepository) DeleteByProjectIDAndIDsTx(tx *gorm.DB, projectID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return tx.Where("project_id = ? AND id IN ?", projectID, ids).Delete(&model.IssueDraftAsset{}).Error
}

func (r *IssueDraftAssetRepository) ListCreatedBefore(cutoff time.Time) ([]model.IssueDraftAsset, error) {
	var assets []model.IssueDraftAsset
	if err := r.db.
		Where("created_at < ?", cutoff).
		Order("created_at ASC").
		Find(&assets).
		Error; err != nil {
		return nil, err
	}
	return assets, nil
}
