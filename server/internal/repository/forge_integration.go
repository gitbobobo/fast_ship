package repository

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// ForgeIntegrationRepository 读写部署级代码仓集成（INT-166）。被项目引用检查
// 不走数据库外键（projects→forge_integrations 无外键），由 ReferencingProjects
// 在应用层完成，删除前必须调用。
type ForgeIntegrationRepository struct {
	db *gorm.DB
}

func NewForgeIntegrationRepository(db *gorm.DB) *ForgeIntegrationRepository {
	return &ForgeIntegrationRepository{db: db}
}

func (r *ForgeIntegrationRepository) Create(row *model.ForgeIntegration) error {
	return r.db.Create(row).Error
}

// List 全量集成按创建时间升序：列表投影首条恒为内置 github-default，forgejo
// 行按创建顺序跟在后面（service 层拼装）。
func (r *ForgeIntegrationRepository) List() ([]model.ForgeIntegration, error) {
	var rows []model.ForgeIntegration
	err := r.db.Order("created_at ASC, id ASC").Find(&rows).Error
	return rows, err
}

// ListByIDs 按 id 批量取集成行：项目响应的 code_repo_url 等投影一次取回，
// 避免列表路径 N+1。空入参直接返回空。
func (r *ForgeIntegrationRepository) ListByIDs(ids []string) ([]model.ForgeIntegration, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []model.ForgeIntegration
	err := r.db.Where("id IN ?", ids).Find(&rows).Error
	return rows, err
}

func (r *ForgeIntegrationRepository) FindByID(id string) (*model.ForgeIntegration, error) {
	var row model.ForgeIntegration
	if err := r.db.Where("id = ?", id).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// UpdateColumns 只写给定列并同步 updated_at：部分更新的字段合并都在 service
// 层算好，这里不整行 Save，避免把未参与本次请求的列盖回旧值。
func (r *ForgeIntegrationRepository) UpdateColumns(id string, fields map[string]any) error {
	updates := make(map[string]any, len(fields)+1)
	for k, v := range fields {
		updates[k] = v
	}
	updates["updated_at"] = time.Now().UTC()
	res := r.db.Model(&model.ForgeIntegration{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// DeleteIfUnreferenced 原子删除未被项目引用的集成行：引用判定写进同一条
// DELETE（NOT EXISTS 子查询），「先查后删」之间项目并发绑定成功也不会留下
// 悬空引用。返回删除行数——0 表示行不存在或仍被引用，由调用方区分两种情况。
func (r *ForgeIntegrationRepository) DeleteIfUnreferenced(id string) (int64, error) {
	res := r.db.Exec(
		`DELETE FROM forge_integrations
		 WHERE id = ?
		   AND NOT EXISTS (
			SELECT 1 FROM projects
			WHERE code_integration_id = ? OR public_integration_id = ?
		   )`, id, id, id)
	return res.RowsAffected, res.Error
}

// ReferencingProjects 返回 code_integration_id / public_integration_id 任一引用了
// 该集成的项目（id+name，按创建时间倒序）。删除集成前调用：非空即被引用，
// 返回 ErrForgeIntegrationInUse 并把项目名拼进消息。
func (r *ForgeIntegrationRepository) ReferencingProjects(id string) ([]ReferencingProject, error) {
	var projects []ReferencingProject
	err := r.db.Model(&model.Project{}).
		Select("id, name").
		Where("code_integration_id = ? OR public_integration_id = ?", id, id).
		Order("created_at DESC").
		Scan(&projects).Error
	return projects, err
}
