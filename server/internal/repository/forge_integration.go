package repository

import (
	"errors"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// ErrForgeHostTaken 同归一主机的数据库唯一索引（idx_forge_integrations_
// host_key）兜底触发：应用层查重（ensureHostAvailable）通过之后、写入之前，
// 并发请求抢占了同一主机。service 层折成 40001（尽力点名占用集成）。
var ErrForgeHostTaken = errors.New("forge integration host key already taken")

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
	if err := r.db.Create(row).Error; err != nil {
		if isUniqueConstraintError(err) {
			return ErrForgeHostTaken
		}
		return err
	}
	return nil
}

// List 全量集成按创建时间升序：列表投影首条恒为内置 github-default，forgejo
// 行按创建顺序跟在后面（service 层拼装）。
func (r *ForgeIntegrationRepository) List() ([]model.ForgeIntegration, error) {
	var rows []model.ForgeIntegration
	err := r.db.Order("created_at ASC, id ASC").Find(&rows).Error
	return rows, err
}

// FindByHostKey 按归一主机键点查 forgejo 集成的 id+name：项目绑定的主机
// 匹配、保存侧同主机查重与兜底报错都走这条——只取两列、走
// idx_forge_integrations_host_key 索引，不全表 List 也不把凭证密文读进内存。
// excludeID 传被更新行的 id（自身不与自己冲突），空串不排除。未命中
// found=false。
func (r *ForgeIntegrationRepository) FindByHostKey(key, excludeID string) (id, name string, found bool, err error) {
	var row model.ForgeIntegration
	q := r.db.Select("id", "name").
		Where("host_key = ? AND kind = ?", key, model.ForgeIntegrationKindForgejo)
	if excludeID != "" {
		q = q.Where("id <> ?", excludeID)
	}
	err = q.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return row.ID, row.Name, true, nil
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
		if isUniqueConstraintError(res.Error) {
			return ErrForgeHostTaken
		}
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// UpdateColumnsIfUnreferenced 同 UpdateColumns，但把「无项目引用」写进同一条
// UPDATE（NOT EXISTS 子查询）——归一主机变更路径用它防「先查后改」窗口：
// 项目并发绑定成功时本次更新 0 行，调用方按仍被引用拒绝，已绑定项目不会被
// 静默改指新主机。返回 0 表示行不存在或仍被引用，由调用方区分两种情况。
func (r *ForgeIntegrationRepository) UpdateColumnsIfUnreferenced(id string, fields map[string]any) (int64, error) {
	updates := make(map[string]any, len(fields)+1)
	for k, v := range fields {
		updates[k] = v
	}
	updates["updated_at"] = time.Now().UTC()
	res := r.db.Model(&model.ForgeIntegration{}).
		Where("id = ?", id).
		Where(`NOT EXISTS (
			SELECT 1 FROM projects
			WHERE code_integration_id = ? OR public_integration_id = ?
		)`, id, id).
		Updates(updates)
	if res.Error != nil {
		if isUniqueConstraintError(res.Error) {
			return 0, ErrForgeHostTaken
		}
		return 0, res.Error
	}
	return res.RowsAffected, nil
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
// 该集成的项目（id+name，按创建时间倒序，limit 条预览）与引用总数。删除集成
// 前调用：非空即被引用，返回 ErrForgeIntegrationInUse 并把项目名拼进消息——
// limit 截断防报错随引用数膨胀（部署级集成的引用项目不分属主，名串是全量
// 用户项目名，只给前几条够指明阻塞对象）。
func (r *ForgeIntegrationRepository) ReferencingProjects(id string, limit int) ([]ReferencingProject, int64, error) {
	var total int64
	if err := r.db.Model(&model.Project{}).
		Where("code_integration_id = ? OR public_integration_id = ?", id, id).
		Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var projects []ReferencingProject
	err := r.db.Model(&model.Project{}).
		Select("id, name").
		Where("code_integration_id = ? OR public_integration_id = ?", id, id).
		Order("created_at DESC").
		Limit(limit).
		Scan(&projects).Error
	return projects, total, err
}
