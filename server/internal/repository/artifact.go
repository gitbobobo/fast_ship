package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

type ArtifactRepository struct {
	db *gorm.DB
}

func NewArtifactRepository(db *gorm.DB) *ArtifactRepository {
	return &ArtifactRepository{db: db}
}

func (r *ArtifactRepository) Create(artifact *model.Artifact) error {
	return r.db.Create(artifact).Error
}

func (r *ArtifactRepository) FindByID(id string) (*model.Artifact, error) {
	var artifact model.Artifact
	err := r.db.Where("id = ?", id).First(&artifact).Error
	if err != nil {
		return nil, err
	}
	return &artifact, nil
}

func (r *ArtifactRepository) ListByVersionID(versionID string) ([]model.Artifact, error) {
	var artifacts []model.Artifact
	err := r.db.Where("version_id = ?", versionID).Order("uploaded_at DESC").Find(&artifacts).Error
	return artifacts, err
}

func (r *ArtifactRepository) Delete(id string) error {
	return r.db.Where("id = ?", id).Delete(&model.Artifact{}).Error
}

func (r *ArtifactRepository) DeleteByVersionID(versionID string) error {
	return r.db.Where("version_id = ?", versionID).Delete(&model.Artifact{}).Error
}
