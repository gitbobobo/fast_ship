package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

type ProjectRepository struct {
	db *gorm.DB
}

func NewProjectRepository(db *gorm.DB) *ProjectRepository {
	return &ProjectRepository{db: db}
}

func (r *ProjectRepository) Create(project *model.Project) error {
	return r.db.Create(project).Error
}

func (r *ProjectRepository) FindByID(id, userID string) (*model.Project, error) {
	return r.FindByIDTx(r.db, id, userID)
}

func (r *ProjectRepository) FindByIDTx(tx *gorm.DB, id, userID string) (*model.Project, error) {
	var project model.Project
	err := tx.Where("id = ? AND user_id = ?", id, userID).First(&project).Error
	if err != nil {
		return nil, err
	}
	return &project, nil
}

func (r *ProjectRepository) List(userID string, page, pageSize int) ([]model.Project, int64, error) {
	var projects []model.Project
	var total int64

	query := r.db.Where("user_id = ?", userID)
	query.Model(&model.Project{}).Count(&total)

	err := query.Offset((page - 1) * pageSize).Limit(pageSize).
		Order("created_at DESC").Find(&projects).Error
	return projects, total, err
}

// GetIssueCounts 统计给定项目的 Issue 总数（不区分状态），供项目列表填充
// issue_count；空切片直接返回空 map 不查库，无 Issue 的项目不出现在结果中。
func (r *ProjectRepository) GetIssueCounts(projectIDs []string) (map[string]int, error) {
	counts := make(map[string]int, len(projectIDs))
	if len(projectIDs) == 0 {
		return counts, nil
	}

	var rows []struct {
		ProjectID string `gorm:"column:project_id"`
		Count     int    `gorm:"column:count"`
	}
	err := r.db.Model(&model.Issue{}).
		Select("project_id, COUNT(*) AS count").
		Where("project_id IN ?", projectIDs).
		Group("project_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		counts[row.ProjectID] = row.Count
	}
	return counts, nil
}

func (r *ProjectRepository) Update(project *model.Project) error {
	return r.db.Save(project).Error
}

func (r *ProjectRepository) Delete(id, userID string) error {
	return r.db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.Project{}).Error
}

func (r *ProjectRepository) ExistsByName(userID, name string) (bool, error) {
	var count int64
	err := r.db.Model(&model.Project{}).Where("user_id = ? AND name = ?", userID, name).Count(&count).Error
	return count > 0, err
}

func (r *ProjectRepository) ExistsByNameExcludeID(userID, name, excludeID string) (bool, error) {
	var count int64
	err := r.db.Model(&model.Project{}).
		Where("user_id = ? AND name = ? AND id != ?", userID, name, excludeID).
		Count(&count).Error
	return count > 0, err
}

func (r *ProjectRepository) ListByUser(userID string) ([]model.Project, error) {
	var projects []model.Project
	err := r.db.Where("user_id = ?", userID).Order("created_at DESC").Find(&projects).Error
	return projects, err
}

func (r *ProjectRepository) ListAll() ([]model.Project, error) {
	var projects []model.Project
	err := r.db.Order("created_at DESC").Find(&projects).Error
	return projects, err
}

func (r *ProjectRepository) FindByIDAnyOwner(id string) (*model.Project, error) {
	var project model.Project
	err := r.db.Where("id = ?", id).First(&project).Error
	if err != nil {
		return nil, err
	}
	return &project, nil
}
