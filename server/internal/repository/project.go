package repository

import (
	"errors"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// ErrGitHubTokenMissing 表示写入项目引用时，对应的用户 Token 行已经不在。
// 与 CheckTokenReference 之后、项目行落库之前被删掉的竞态对应。
var ErrGitHubTokenMissing = errors.New("referenced github token missing")

type ProjectRepository struct {
	db *gorm.DB
}

func NewProjectRepository(db *gorm.DB) *ProjectRepository {
	return &ProjectRepository{db: db}
}

func (r *ProjectRepository) Create(project *model.Project) error {
	now := time.Now().UTC()
	if project.CreatedAt.IsZero() {
		project.CreatedAt = now
	}
	if project.UpdatedAt.IsZero() {
		project.UpdatedAt = now
	}
	pred, args := ownedTokenWritePredicate(project.UserID, project.PublicGitHubTokenID, project.CodeGitHubTokenID)
	args = append([]interface{}{
		project.ID,
		project.UserID,
		project.Name,
		project.Description,
		project.GithubOwner,
		project.GithubRepo,
		project.CodeGithubOwner,
		project.CodeGithubRepo,
		project.PublicGitHubTokenID,
		project.CodeGitHubTokenID,
		project.CreatedAt,
		project.UpdatedAt,
	}, args...)
	res := r.db.Exec(`
		INSERT INTO projects (
			id, user_id, name, description, github_owner, github_repo,
			code_github_owner, code_github_repo,
			public_github_token_id, code_github_token_id, created_at, updated_at
		)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE `+pred, args...)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrGitHubTokenMissing
	}
	return nil
}

// ownedTokenWritePredicate 把「空引用合法，非空必须仍属于该用户」写进同一条
// INSERT/UPDATE，避免 CheckTokenReference 与项目行写入之间 Token 被删掉。
func ownedTokenWritePredicate(userID, publicID, codeID string) (string, []interface{}) {
	return `(? = '' OR EXISTS (SELECT 1 FROM user_github_tokens WHERE id = ? AND user_id = ?))
		AND (? = '' OR EXISTS (SELECT 1 FROM user_github_tokens WHERE id = ? AND user_id = ?))`,
		[]interface{}{publicID, publicID, userID, codeID, codeID, userID}
}

func (r *ProjectRepository) FindByID(id, userID string) (*model.Project, error) {
	return r.FindByIDTx(r.db, id, userID)
}

// FindByIDGlobal 不做用户过滤的按 ID 查找：仅供内部维护路径（如 GitHub 镜像
// 解析自动化事件归属用户，INT-90）；对外接口一律用带 user_id 的 FindByID。
func (r *ProjectRepository) FindByIDGlobal(id string) (*model.Project, error) {
	var project model.Project
	if err := r.db.Where("id = ?", id).First(&project).Error; err != nil {
		return nil, err
	}
	return &project, nil
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

// ProjectIssueCounts 是单项目的 Issue 计数：Total 为不区分 open/closed 的总数；
// Unset/Todo/InProgress/Done 仅统计 state=open 的 Issue（切换组件按待办口径展示）。
// Unset 含没有 internal meta 行的 Issue（LEFT JOIN 后为 NULL）。
type ProjectIssueCounts struct {
	Total      int
	Unset      int
	Todo       int
	InProgress int
	Done       int
}

// GetIssueCounts 统计给定项目的 Issue 总数与内部状态分布：总数不区分 open/closed，
// 内部状态分布只计 state=open 的 Issue；供项目列表填充 issue_count 与
// issue_workflow_counts；空切片直接返回空 map
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
			SUM(CASE WHEN issues.state = ? AND (m.workflow_status IS NULL OR m.workflow_status = '') THEN 1 ELSE 0 END) AS unset,
			SUM(CASE WHEN issues.state = ? AND m.workflow_status = ? THEN 1 ELSE 0 END) AS todo,
			SUM(CASE WHEN issues.state = ? AND m.workflow_status = ? THEN 1 ELSE 0 END) AS in_progress,
			SUM(CASE WHEN issues.state = ? AND m.workflow_status = ? THEN 1 ELSE 0 END) AS done`,
			model.IssueStateOpen,
			model.IssueStateOpen, model.IssueWorkflowStatusTodo,
			model.IssueStateOpen, model.IssueWorkflowStatusInProgress,
			model.IssueStateOpen, model.IssueWorkflowStatusDone).
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
	return r.db.Transaction(func(tx *gorm.DB) error {
		// 代码仓库（CodeRepo()，未单独配置即公开仓库）归属变更后，项目审查
		// 设置里缓存的 repo_full_name 必须跟着走：否则旧仓库的设置会继续占着
		// 唯一索引，而新仓库可能和另一个已启用项目重复。审查仓库变了就停用
		// 审查并刷新缓存键，由用户按新仓库重新开启；只改公开仓库（代码仓库
		// 显式配置时）不影响审查。
		var before model.Project
		if err := tx.Select("github_owner", "github_repo", "code_github_owner", "code_github_repo").
			Where("id = ? AND user_id = ?", project.ID, project.UserID).
			First(&before).Error; err != nil {
			return err
		}
		pred, args := ownedTokenWritePredicate(project.UserID, project.PublicGitHubTokenID, project.CodeGitHubTokenID)
		res := tx.Model(&model.Project{}).
			Where("id = ? AND user_id = ?", project.ID, project.UserID).
			Where(pred, args...).
			Updates(map[string]interface{}{
				"name":                   project.Name,
				"description":            project.Description,
				"github_owner":           project.GithubOwner,
				"github_repo":            project.GithubRepo,
				"code_github_owner":      project.CodeGithubOwner,
				"code_github_repo":       project.CodeGithubRepo,
				"public_github_token_id": project.PublicGitHubTokenID,
				"code_github_token_id":   project.CodeGitHubTokenID,
				"updated_at":             time.Now().UTC(),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			if !strings.EqualFold(before.CodeRepoFullName(), project.CodeRepoFullName()) {
				reviewOwner, reviewRepo := project.CodeRepo()
				now := time.Now().UTC()
				if err := tx.Model(&model.ProjectReviewSettings{}).
					Where("project_id = ?", project.ID).
					Updates(map[string]interface{}{
						"enabled":        false,
						"repo_full_name": strings.ToLower(reviewOwner + "/" + reviewRepo),
						"updated_at":     now,
					}).Error; err != nil {
					return err
				}
				// 停用后调度按 status 挑选，非终态运行会继续开线程：取消并
				// 消费令牌、清防抖。
				active := []model.ReviewRunStatus{
					model.ReviewRunQueued, model.ReviewRunDispatched, model.ReviewRunRunning,
				}
				var runIDs []string
				if err := tx.Model(&model.ReviewRun{}).
					Where("project_id = ? AND status IN ?", project.ID, active).
					Pluck("id", &runIDs).Error; err != nil {
					return err
				}
				if len(runIDs) > 0 {
					if err := tx.Model(&model.ReviewRun{}).
						Where("id IN ? AND status IN ?", runIDs, active).
						Updates(map[string]interface{}{
							"status": model.ReviewRunCancelled, "error_code": "repo_changed",
							"finished_at": now, "updated_at": now,
						}).Error; err != nil {
						return err
					}
					if err := tx.Model(&model.ReviewRunToken{}).
						Where("run_id IN ? AND consumed_at IS NULL", runIDs).
						Update("consumed_at", now).Error; err != nil {
						return err
					}
				}
				if err := tx.Where("project_id = ?", project.ID).
					Delete(&model.ReviewDebounce{}).Error; err != nil {
					return err
				}
				// 推进 epoch 并清 pending：正在进行的同步（已持有旧账本快照）
				// 会因 epoch 不符停止，不能再向旧仓库发布状态/评论。
				if err := tx.Model(&model.ReviewPullRequest{}).
					Where("project_id = ?", project.ID).
					Updates(map[string]interface{}{
						"sync_pending": false,
						"sync_epoch":   gorm.Expr("sync_epoch + 1"),
						"updated_at":   now,
					}).Error; err != nil {
					return err
				}
			}
			return nil
		}
		var n int64
		if err := tx.Model(&model.Project{}).Where("id = ? AND user_id = ?", project.ID, project.UserID).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			return gorm.ErrRecordNotFound
		}
		return ErrGitHubTokenMissing
	})
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
