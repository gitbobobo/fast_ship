package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

type DocumentRepository struct {
	db *gorm.DB
}

func NewDocumentRepository(db *gorm.DB) *DocumentRepository {
	return &DocumentRepository{db: db}
}

func (r *DocumentRepository) Transaction(fn func(txRepo *DocumentRepository) error) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		return fn(&DocumentRepository{db: tx})
	})
}

func (r *DocumentRepository) Create(doc *model.Document) error {
	return r.db.Create(doc).Error
}

func (r *DocumentRepository) FindByID(id string) (*model.Document, error) {
	var doc model.Document
	if err := r.db.Where("id = ?", id).First(&doc).Error; err != nil {
		return nil, err
	}
	return &doc, nil
}

func (r *DocumentRepository) ListByProject(projectID string) ([]model.Document, error) {
	var docs []model.Document
	err := r.db.Select("id", "project_id", "parent_id", "title", "created_at", "updated_at").
		Where("project_id = ?", projectID).
		Order("created_at ASC, id ASC").
		Find(&docs).Error
	return docs, err
}

func (r *DocumentRepository) CountByProject(projectID string) (int64, error) {
	var count int64
	err := r.db.Model(&model.Document{}).Where("project_id = ?", projectID).Count(&count).Error
	return count, err
}

func (r *DocumentRepository) UpdateByMap(id string, fields map[string]interface{}) error {
	return r.db.Model(&model.Document{}).Where("id = ?", id).Updates(fields).Error
}

func (r *DocumentRepository) Delete(id string) error {
	result := r.db.Where("id = ?", id).Delete(&model.Document{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
