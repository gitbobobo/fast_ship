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

// ProjectIssueCounts 是单项目的 Issue 计数：总数与按内部状态（workflow_status）
// 分组的数量。Unset 含没有 internal meta 行的 Issue（LEFT JOIN 后为 NULL）。
type ProjectIssueCounts struct {
	Total      int
	Unset      int
	Todo       int
	InProgress int
	Done       int
}

// GetIssueCounts 统计给定项目的 Issue 总数与内部状态分布（不区分 open/closed），
// 供项目列表填充 issue_count 与 issue_workflow_counts；空切片直接返回空 map
// 不查库，无 Issue 的项目不出现在结果中。
func (r *ProjectRepository) GetIssueCounts(projectIDs []string) (map[string]ProjectIssueCounts, error) {
	counts := make(map[string]ProjectIssueCounts, len(projectIDs))
	if len(projectIDs) == 0 {
		return counts, nil
	}

	var rows []struct {
		ProjectID  string `gorm:"column:project_id"`
		Total      int    `gorm:"column:total"`
		Unset      int    `gorm:"column:unset"`
		Todo       int    `gorm:"column:todo"`
		InProgress int    `gorm:"column:in_progress"`
		Done       int    `gorm:"column:done"`
	}
	err := r.db.Model(&model.Issue{}).
		Select(`project_id, COUNT(*) AS total,
			SUM(CASE WHEN m.workflow_status IS NULL OR m.workflow_status = '' THEN 1 ELSE 0 END) AS unset,
			SUM(CASE WHEN m.workflow_status = ? THEN 1 ELSE 0 END) AS todo,
			SUM(CASE WHEN m.workflow_status = ? THEN 1 ELSE 0 END) AS in_progress,
			SUM(CASE WHEN m.workflow_status = ? THEN 1 ELSE 0 END) AS done`,
			model.IssueWorkflowStatusTodo, model.IssueWorkflowStatusInProgress, model.IssueWorkflowStatusDone).
		Joins("LEFT JOIN issue_internal_meta m ON m.issue_id = issues.id").
		Where("project_id IN ?", projectIDs).
		Group("project_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		counts[row.ProjectID] = ProjectIssueCounts{
			Total:      row.Total,
			Unset:      row.Unset,
			Todo:       row.Todo,
			InProgress: row.InProgress,
			Done:       row.Done,
		}
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
