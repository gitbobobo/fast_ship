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

// GetLatestByProjectIDs 用一条窗口函数查询取每个项目的最新版本（created_at
// 最大，并列按 id 升序取小者——与原 Order("created_at DESC").First 隐式追加
// 主键升序的口径一致），供项目列表按页填充 latest_version；无版本的项目
// 不出现在结果中。外层列清单与 model.Version 的持久化字段一一对应，版本模型
// 增删列（含引入软删除）时需同步维护。
func (r *VersionRepository) GetLatestByProjectIDs(projectIDs []string) (map[string]*model.Version, error) {
	versions := make(map[string]*model.Version, len(projectIDs))
	if len(projectIDs) == 0 {
		return versions, nil
	}
	var rows []model.Version
	err := r.db.Raw(`
		SELECT id, project_id, version_number, status, release_notes, target_commitish,
			github_release_url, error_log, ship_status, ship_stage, ship_message,
			ship_hooks_status, created_at, shipped_at
		FROM (
			SELECT *, ROW_NUMBER() OVER (
				PARTITION BY project_id ORDER BY created_at DESC, id ASC
			) AS rn
			FROM versions
			WHERE project_id IN ?
		)
		WHERE rn = 1`, projectIDs).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for i := range rows {
		versions[rows[i].ProjectID] = &rows[i]
	}
	return versions, nil
}
