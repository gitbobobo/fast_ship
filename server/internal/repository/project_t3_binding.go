package repository

import (
	"context"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ProjectT3BindingRepository struct {
	db *gorm.DB
}

func NewProjectT3BindingRepository(db *gorm.DB) *ProjectT3BindingRepository {
	return &ProjectT3BindingRepository{db: db}
}

// ListByProjectID 列出项目的全部绑定；主键有序即按环境 ID 排序，稳定输出。
func (r *ProjectT3BindingRepository) ListByProjectID(ctx context.Context, projectID string) ([]model.ProjectT3Binding, error) {
	var bindings []model.ProjectT3Binding
	err := r.db.WithContext(ctx).Where("project_id = ?", projectID).Order("environment_id ASC").Find(&bindings).Error
	return bindings, err
}

func (r *ProjectT3BindingRepository) FindByID(ctx context.Context, projectID, environmentID string) (*model.ProjectT3Binding, error) {
	var binding model.ProjectT3Binding
	err := r.db.WithContext(ctx).Where("project_id = ? AND environment_id = ?", projectID, environmentID).First(&binding).Error
	if err != nil {
		return nil, err
	}
	return &binding, nil
}

// Upsert 写入绑定快照；同一 (project_id, environment_id) 重复写入覆盖
// t3_project_id/title/workspace_root 与 updated_at，created_at 保留首次写入值。
func (r *ProjectT3BindingRepository) Upsert(ctx context.Context, binding *model.ProjectT3Binding) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "project_id"}, {Name: "environment_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"t3_project_id", "t3_project_title", "workspace_root", "updated_at"}),
	}).Create(binding).Error
}

func (r *ProjectT3BindingRepository) Delete(ctx context.Context, projectID, environmentID string) error {
	return r.db.WithContext(ctx).Where("project_id = ? AND environment_id = ?", projectID, environmentID).Delete(&model.ProjectT3Binding{}).Error
}
