package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

type VersionRepository struct {
	db *gorm.DB
}

func NewVersionRepository(db *gorm.DB) *VersionRepository {
	return &VersionRepository{db: db}
}

func (r *VersionRepository) Create(version *model.Version) error {
	return r.db.Create(version).Error
}

func (r *VersionRepository) FindByID(id string) (*model.Version, error) {
	var version model.Version
	err := r.db.Preload("Artifacts").Where("id = ?", id).First(&version).Error
	if err != nil {
		return nil, err
	}
	return &version, nil
}

func (r *VersionRepository) List(projectID string, status string, page, pageSize int) ([]model.Version, int64, error) {
	var versions []model.Version
	var total int64

	query := r.db.Where("project_id = ?", projectID)
	if status != "" {
		query = query.Where("status = ?", status)
	}

	query.Model(&model.Version{}).Count(&total)

	err := query.Preload("Artifacts").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Order("created_at DESC").Find(&versions).Error
	return versions, total, err
}

func (r *VersionRepository) Update(version *model.Version) error {
	return r.db.Save(version).Error
}

func (r *VersionRepository) Delete(id string) error {
	return r.db.Where("id = ?", id).Delete(&model.Version{}).Error
}

func (r *VersionRepository) ExistsByVersionNumber(projectID, versionNumber string) (bool, error) {
	var count int64
	err := r.db.Model(&model.Version{}).
		Where("project_id = ? AND version_number = ?", projectID, versionNumber).
		Count(&count).Error
	return count > 0, err
}

func (r *VersionRepository) ExistsByVersionNumberExcludeID(projectID, versionNumber, excludeID string) (bool, error) {
	var count int64
	err := r.db.Model(&model.Version{}).
		Where("project_id = ? AND version_number = ? AND id != ?", projectID, versionNumber, excludeID).
		Count(&count).Error
	return count > 0, err
}

func (r *VersionRepository) GetLatestByProjectID(projectID string) (*model.Version, error) {
	var version model.Version
	err := r.db.Where("project_id = ?", projectID).
		Order("created_at DESC").First(&version).Error
	if err != nil {
		return nil, err
	}
	return &version, nil
}
