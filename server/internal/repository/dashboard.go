package repository

import (
	"fmt"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

type DashboardRepository struct {
	db *gorm.DB
}

type DashboardProjectOpenIssueCount struct {
	ProjectID      string `gorm:"column:project_id"`
	ProjectName    string `gorm:"column:project_name"`
	OpenIssueCount int    `gorm:"column:open_issue_count"`
}

type DashboardDailyResolvedCount struct {
	Date          string `gorm:"column:date"`
	ResolvedCount int    `gorm:"column:resolved_count"`
}

func NewDashboardRepository(db *gorm.DB) *DashboardRepository {
	return &DashboardRepository{db: db}
}

func (r *DashboardRepository) GetOpenIssueCounts(userID string) ([]DashboardProjectOpenIssueCount, error) {
	var rows []DashboardProjectOpenIssueCount
	err := r.db.
		Table("projects AS p").
		Select(`
			p.id AS project_id,
			p.name AS project_name,
			COALESCE(SUM(CASE
				WHEN i.id IS NOT NULL
				 AND i.state = ?
				 AND (m.workflow_status IS NULL OR m.workflow_status != ?)
				THEN 1
				ELSE 0
			END), 0) AS open_issue_count
		`, model.IssueStateOpen, model.IssueWorkflowStatusDone).
		Joins("LEFT JOIN issues i ON i.project_id = p.id").
		Joins("LEFT JOIN issue_internal_meta m ON m.issue_id = i.id").
		Where("p.user_id = ?", userID).
		// 搁置中的项目不进概览（INT-165）：整行消失，而不是计数归零。
		Where(projectNotShelvedSQL("p"), time.Now().UTC()).
		Group("p.id, p.name, p.created_at").
		Order("open_issue_count DESC, p.created_at DESC").
		Scan(&rows).Error
	return rows, err
}

func (r *DashboardRepository) GetDailyResolvedCounts(
	userID string,
	startInclusive time.Time,
	endExclusive time.Time,
) ([]DashboardDailyResolvedCount, error) {
	var rows []DashboardDailyResolvedCount
	err := r.db.
		Table("issue_internal_meta AS m").
		Select("DATE(m.completed_at) AS date, COUNT(*) AS resolved_count").
		Joins("JOIN issues i ON i.id = m.issue_id").
		Joins("JOIN projects p ON p.id = i.project_id").
		Where("p.user_id = ?", userID).
		// 搁置中的项目不进每日完成趋势（INT-165）；看历史仍以项目列表页为准。
		Where(projectNotShelvedSQL("p"), time.Now().UTC()).
		Where("m.completed_at IS NOT NULL").
		Where("m.completed_at >= ? AND m.completed_at < ?", startInclusive, endExclusive).
		Group("DATE(m.completed_at)").
		Order("DATE(m.completed_at) ASC").
		Scan(&rows).Error
	return rows, err
}

// countAndScan 对同一过滤链先取总数再取明细，是各「待我处理」组的共用骨架：
// count 是全量匹配数（卡片上展示的角标），items 只截前 limit 条。
func (r *DashboardRepository) countAndScan(base *gorm.DB, limit int, dest any) (int64, error) {
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return 0, err
	}
	if limit > 0 {
		base = base.Limit(limit)
	}
	if err := base.Scan(dest).Error; err != nil {
		return 0, err
	}
	return total, nil
}

// DashboardFailedReview 一条失败审查行的聚合视图（按 PR 去重后一行一 PR）。
type DashboardFailedReview struct {
	ProjectID    string `gorm:"column:project_id"`
	RepoFullName string `gorm:"column:repo_full_name"`
	ProjectName  string `gorm:"column:project_name"`
	PRTitle      string `gorm:"column:pr_title"`
	PRNumber     int    `gorm:"column:pr_number"`
}

// ListFailedReviews 近 14 天失败/超时的审查（仅当前轮次+当前 head，与
// review_pull_requests 全键 JOIN），排除搁置项目；按 (project, repo, pr)
// 去重——同一 PR 同轮次的 code/visual/多模型 run 是不同行，直接按 run
// 展示会产生不可分辨的重复条目。返回去重后全量计数与前
// limit 条（finished_at 倒序）。
func (r *DashboardRepository) ListFailedReviews(userID string, since time.Time, limit int) (int64, []DashboardFailedReview, error) {
	base := r.db.
		Table("review_runs AS r").
		Joins("JOIN review_pull_requests rp ON r.project_id = rp.project_id AND r.repo_full_name = rp.repo_full_name AND r.pr_number = rp.pr_number AND r.integration_id = rp.integration_id AND r.round_number = rp.round_number AND r.head_sha = rp.head_sha").
		Joins("JOIN projects p ON p.id = r.project_id").
		Where("r.user_id = ?", userID).
		Where("r.status IN ?", []string{string(model.ReviewRunFailed), string(model.ReviewRunTimedOut)}).
		Where("r.finished_at >= ?", since).
		Where(projectNotShelvedSQL("p"), time.Now().UTC())

	var total int64
	if err := base.Session(&gorm.Session{}).
		Select("COUNT(DISTINCT r.project_id || '~' || r.repo_full_name || '~' || r.pr_number)").
		Scan(&total).Error; err != nil {
		return 0, nil, err
	}

	// 先在 SQL 侧按 PR 分组取最新 run，保证 items 取满 limit 个不同 PR
	//（一个 PR 同轮次可能有多条 run：code/visual × 多模型组合）。
	type prKey struct {
		ProjectID    string `gorm:"column:project_id"`
		RepoFullName string `gorm:"column:repo_full_name"`
		PRNumber     int    `gorm:"column:pr_number"`
	}
	var keys []prKey
	if err := base.Session(&gorm.Session{}).
		Select("r.project_id, r.repo_full_name, r.pr_number").
		Group("r.project_id, r.repo_full_name, r.pr_number").
		Order("MAX(r.finished_at) DESC").
		Limit(limit).
		Scan(&keys).Error; err != nil {
		return 0, nil, err
	}
	items := make([]DashboardFailedReview, 0, len(keys))
	if len(keys) == 0 {
		return total, items, nil
	}

	conds := make([]string, len(keys))
	args := make([]any, 0, 3*len(keys))
	for i, k := range keys {
		conds[i] = "(r.project_id = ? AND r.repo_full_name = ? AND r.pr_number = ?)"
		args = append(args, k.ProjectID, k.RepoFullName, k.PRNumber)
	}
	var rows []DashboardFailedReview
	if err := base.Session(&gorm.Session{}).
		Select("r.project_id, r.repo_full_name, p.name AS project_name, r.pr_title, r.pr_number").
		Where(strings.Join(conds, " OR "), args...).
		Order("r.finished_at DESC").
		Scan(&rows).Error; err != nil {
		return 0, nil, err
	}

	// 行按 finished_at 倒序，每个 PR 取第一条即该 PR 最新失败 run；
	// 输出顺序回填 keys 的分组排序（MAX finished_at 倒序）。
	rank := make(map[string]int, len(keys))
	for i, k := range keys {
		rank[k.ProjectID+"|"+k.RepoFullName+"|"+fmt.Sprint(k.PRNumber)] = i
	}
	byPR := make([]*DashboardFailedReview, len(keys))
	for i := range rows {
		row := &rows[i]
		key := row.ProjectID + "|" + row.RepoFullName + "|" + fmt.Sprint(row.PRNumber)
		idx, ok := rank[key]
		if !ok || byPR[idx] != nil {
			continue
		}
		byPR[idx] = row
	}
	for _, row := range byPR {
		if row != nil {
			items = append(items, *row)
		}
	}
	return total, items, nil
}

// DashboardActiveRecommendation 一条未延后推荐的聚合视图。
type DashboardActiveRecommendation struct {
	IssueID     string `gorm:"column:issue_id"`
	IssueTitle  string `gorm:"column:issue_title"`
	ProjectID   string `gorm:"column:project_id"`
	ProjectName string `gorm:"column:project_name"`
}

// ListActiveRecommendations 未延后的 issue 推荐，排除搁置项目；排序与现有
// 推荐列表同口径（priority 降序再 updated_at 降序，issue_id 兜底稳定序）。
func (r *DashboardRepository) ListActiveRecommendations(userID string, limit int) (int64, []DashboardActiveRecommendation, error) {
	items := make([]DashboardActiveRecommendation, 0, limit)
	base := r.db.
		Table("issue_recommendations AS rec").
		Select("rec.issue_id, i.title AS issue_title, rec.project_id, p.name AS project_name").
		Joins("JOIN issues i ON i.id = rec.issue_id").
		Joins("JOIN projects p ON p.id = rec.project_id").
		Where("p.user_id = ?", userID).
		Where("rec.deferred_at IS NULL").
		Where(projectNotShelvedSQL("p"), time.Now().UTC()).
		Order("CASE rec.priority WHEN 'high' THEN 0 WHEN 'medium' THEN 1 ELSE 2 END ASC").
		Order("rec.updated_at DESC").
		Order("rec.issue_id ASC")
	total, err := r.countAndScan(base, limit, &items)
	return total, items, err
}

// DashboardFailedShip 一条发版失败版本的聚合视图。
type DashboardFailedShip struct {
	VersionID     string `gorm:"column:version_id"`
	VersionNumber string `gorm:"column:version_number"`
	ProjectID     string `gorm:"column:project_id"`
	ProjectName   string `gorm:"column:project_name"`
}

// ListFailedShips 需要处理的发版事项，排除搁置项目。两类口径：
// pending 且 ship_status=failed（发货本身失败，重试前一直挂着）；
// 已发货但 ship_hooks_status=failed/incomplete（钩子失败/未齐，会在下次
// 发货重试并回写状态）。注意 hooks 状态只在发货成功后写入，把两个条件
// 用 AND 串在 pending 上是不可达的死分支。
func (r *DashboardRepository) ListFailedShips(userID string, limit int) (int64, []DashboardFailedShip, error) {
	items := make([]DashboardFailedShip, 0, limit)
	base := r.db.
		Table("versions AS v").
		Select("v.id AS version_id, v.version_number, v.project_id, p.name AS project_name").
		Joins("JOIN projects p ON p.id = v.project_id").
		Where("p.user_id = ?", userID).
		Where(
			"(v.status = ? AND v.ship_status = ?) OR v.ship_hooks_status IN ?",
			string(model.VersionStatusPending), string(model.ShipStatusFailed), []string{"failed", "incomplete"},
		).
		Where(projectNotShelvedSQL("p"), time.Now().UTC()).
		Order("v.created_at DESC")
	total, err := r.countAndScan(base, limit, &items)
	return total, items, err
}

// DashboardQuotaAttention 一条需要处理的配额账号。
type DashboardQuotaAttention struct {
	Label    string `gorm:"column:label"`
	Provider string `gorm:"column:provider"`
}

// ListAttentionQuotaAccounts 凭据失效或服务商报错的配额账号（user_id 直连，
// 无项目归属，不适用搁置排除）。
func (r *DashboardRepository) ListAttentionQuotaAccounts(userID string, limit int) (int64, []DashboardQuotaAttention, error) {
	items := make([]DashboardQuotaAttention, 0, limit)
	base := r.db.
		Table("quota_accounts AS q").
		Select("q.label, q.provider").
		Where("q.user_id = ?", userID).
		Where("q.status IN ?", []string{model.QuotaStatusCredentialInvalid, model.QuotaStatusProviderError}).
		Order("q.updated_at DESC")
	total, err := r.countAndScan(base, limit, &items)
	return total, items, err
}
