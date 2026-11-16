package repository

import (
	"fmt"
	"sort"
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

// DashboardPendingVersion 一个待发版版本。
type DashboardPendingVersion struct {
	VersionID     string    `gorm:"column:version_id"`
	VersionNumber string    `gorm:"column:version_number"`
	ProjectID     string    `gorm:"column:project_id"`
	ProjectName   string    `gorm:"column:project_name"`
	CreatedAt     time.Time `gorm:"column:created_at"`
}

// ListPendingVersions status=pending（未发货）的版本，排除搁置项目；按
// created_at 升序——创建越早等得越久，越该先处理。count 是全量 pending 数，
// items 截前 limit 条。
func (r *DashboardRepository) ListPendingVersions(userID string, limit int) (int64, []DashboardPendingVersion, error) {
	items := make([]DashboardPendingVersion, 0, limit)
	base := r.db.
		Table("versions AS v").
		Select("v.id AS version_id, v.version_number, v.project_id, p.name AS project_name, v.created_at").
		Joins("JOIN projects p ON p.id = v.project_id").
		Where("p.user_id = ?", userID).
		Where("v.status = ?", string(model.VersionStatusPending)).
		Where(projectNotShelvedSQL("p"), time.Now().UTC()).
		Order("v.created_at ASC")
	total, err := r.countAndScan(base, limit, &items)
	return total, items, err
}

// DashboardRecentRelease 一个已发版版本。ShippedAt 是 Go 侧解析后的真实时间；
// 文本解析失败的行保持 nil（展示层只见版本号不见时间，排序垫底）。
type DashboardRecentRelease struct {
	VersionID     string     `gorm:"column:version_id"`
	VersionNumber string     `gorm:"column:version_number"`
	ProjectID     string     `gorm:"column:project_id"`
	ProjectName   string     `gorm:"column:project_name"`
	CreatedAt     time.Time  `gorm:"column:created_at"`
	ShippedAt     *time.Time `gorm:"column:shipped_at"`
}

// dashboardShippedAtRow 是 ListRecentReleases 的扫描行：shipped_at 有意扫成
// 字符串——驱动解析不了的写法不应让整个查询报错降级。
type dashboardShippedAtRow struct {
	VersionID     string    `gorm:"column:version_id"`
	VersionNumber string    `gorm:"column:version_number"`
	ProjectID     string    `gorm:"column:project_id"`
	ProjectName   string    `gorm:"column:project_name"`
	CreatedAt     time.Time `gorm:"column:created_at"`
	ShippedAtRaw  string    `gorm:"column:shipped_at"`
}

// dashboardShippedAtLayouts 是 shipped_at 文本的历史写法：ship.go 现统一写
// UTC（RFC3339），存量行有本地时区偏移（如 +08:00）与空格分隔的写法；无
// 偏移的历史行按 UTC 读。
var dashboardShippedAtLayouts = []string{
	time.RFC3339,
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
}

// ListRecentReleases 最近 limit 次已发版。有意**不在 SQL 侧排序/截断**：
// shipped_at 是文本存储，历史行（ship.go 已改为写 UTC）存在本地时区偏移的
// 写法，字典序与真实时间序错位会把最新发版排丢。候选全取（表按版本量级极
// 小，仍不加时间窗口），Go 侧解析后按真实时间倒序取前 limit；解析失败的行
// 排最后不丢弃（按 created_at 垄底保持稳定序）。
func (r *DashboardRepository) ListRecentReleases(userID string, limit int) ([]DashboardRecentRelease, error) {
	var rows []dashboardShippedAtRow
	err := r.db.
		Table("versions AS v").
		Select("v.id AS version_id, v.version_number, v.project_id, p.name AS project_name, v.created_at, v.shipped_at").
		Joins("JOIN projects p ON p.id = v.project_id").
		Where("p.user_id = ?", userID).
		Where("v.status = ?", string(model.VersionStatusShipped)).
		Where("v.shipped_at IS NOT NULL").
		Where(projectNotShelvedSQL("p"), time.Now().UTC()).
		Order("v.created_at DESC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	type candidate struct {
		item  DashboardRecentRelease
		at    time.Time
		valid bool
	}
	list := make([]candidate, 0, len(rows))
	for i := range rows {
		raw := &rows[i]
		item := DashboardRecentRelease{
			VersionID:     raw.VersionID,
			VersionNumber: raw.VersionNumber,
			ProjectID:     raw.ProjectID,
			ProjectName:   raw.ProjectName,
			CreatedAt:     raw.CreatedAt,
		}
		at, ok := parseShippedAt(raw.ShippedAtRaw)
		if ok {
			t := at
			item.ShippedAt = &t
		}
		list = append(list, candidate{item: item, at: at, valid: ok})
	}
	sort.SliceStable(list, func(i, j int) bool {
		switch {
		case list[i].valid != list[j].valid:
			return list[i].valid // 可解析的排前；解析失败的垫底不丢弃
		case list[i].valid:
			return list[i].at.After(list[j].at)
		default:
			return list[i].item.CreatedAt.After(list[j].item.CreatedAt)
		}
	})
	if len(list) > limit {
		list = list[:limit]
	}
	items := make([]DashboardRecentRelease, 0, len(list))
	for _, c := range list {
		items = append(items, c.item)
	}
	return items, nil
}

// parseShippedAt 按历史写法集合逐个尝试解析 shipped_at 文本；全部失配返回 false。
func parseShippedAt(raw string) (time.Time, bool) {
	for _, layout := range dashboardShippedAtLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// CountUnmergedPRIssues 开启且至少关联一个未合并 PR（含 closed 未合并）的
// 问题数，按 issue 去重、排除搁置项目。连接方向 projects(user_id 索引) →
// issues(project_id) → issue_pull_requests(issue_id/project_id 各有索引)，
// 与其它仪表盘查询同向，不从关联表出发全表扫。
func (r *DashboardRepository) CountUnmergedPRIssues(userID string) (int64, error) {
	var total int64
	err := r.db.
		Table("projects AS p").
		Select("COUNT(DISTINCT i.id)").
		Joins("JOIN issues i ON i.project_id = p.id").
		Joins("JOIN issue_pull_requests l ON l.issue_id = i.id").
		Where("p.user_id = ?", userID).
		Where("i.state = ?", model.IssueStateOpen).
		Where("l.state != ?", model.IssuePullRequestStateMerged).
		Where(projectNotShelvedSQL("p"), time.Now().UTC()).
		Scan(&total).Error
	return total, err
}

// DashboardWorkflowBuckets 开启 issue 按 workflow_status 的两档分桶。
type DashboardWorkflowBuckets struct {
	TodoCount       int `gorm:"column:todo_count"`
	InProgressCount int `gorm:"column:in_progress_count"`
}

// GetWorkflowBuckets 开启 issue 按 workflow_status 分桶：meta 缺失（NULL）、
// 空串（列默认值）与显式 'todo' 都归 todo，其余（外层谓词已排除 done，合法
// 取值只剩 'in_progress'）归 in_progress。分母判定与 GetOpenIssueCounts 完全
// 一致（state=open AND (m.workflow_status IS NULL OR m.workflow_status !=
// 'done')），恒等式 todo+in_progress == Σopen_issue_count。刻意独立成查询而
// 不塞进 GetOpenIssueCounts：概览那两个查询失败即 500，新指标必须能单独降级
// （complete=false）。
func (r *DashboardRepository) GetWorkflowBuckets(userID string) (DashboardWorkflowBuckets, error) {
	var buckets DashboardWorkflowBuckets
	err := r.db.
		Table("projects AS p").
		Select(`
			COALESCE(SUM(CASE WHEN m.workflow_status IS NULL OR m.workflow_status IN (?, ?) THEN 1 ELSE 0 END), 0) AS todo_count,
			COALESCE(SUM(CASE WHEN m.workflow_status IS NOT NULL AND m.workflow_status NOT IN (?, ?) THEN 1 ELSE 0 END), 0) AS in_progress_count
		`, "", model.IssueWorkflowStatusTodo, "", model.IssueWorkflowStatusTodo).
		Joins("LEFT JOIN issues i ON i.project_id = p.id").
		Joins("LEFT JOIN issue_internal_meta m ON m.issue_id = i.id").
		Where("p.user_id = ?", userID).
		// 搁置中的项目不进概览（INT-165），与 GetOpenIssueCounts 同排除。
		Where(projectNotShelvedSQL("p"), time.Now().UTC()).
		Where("i.state = ? AND (m.workflow_status IS NULL OR m.workflow_status != ?)", model.IssueStateOpen, model.IssueWorkflowStatusDone).
		Scan(&buckets).Error
	return buckets, err
}

// DashboardDailyCreatedCount 一天的新建 issue 数。
type DashboardDailyCreatedCount struct {
	Date         string `gorm:"column:date"`
	CreatedCount int    `gorm:"column:created_count"`
}

// GetDailyCreatedCounts 与 GetDailyResolvedCounts 同一 UTC 窗口（同一
// startInclusive/endExclusive 参数）。只统计 source='internal'：GitHub 同步
// issue 关闭时不写 completed_at，混算会把积压趋势系统性画成增长（INT-235
// 裁决）。连接从 projects(user_id 索引) 出发，不从关联表出发。
func (r *DashboardRepository) GetDailyCreatedCounts(
	userID string,
	startInclusive time.Time,
	endExclusive time.Time,
) ([]DashboardDailyCreatedCount, error) {
	var rows []DashboardDailyCreatedCount
	err := r.db.
		Table("projects AS p").
		Select("DATE(i.created_at) AS date, COUNT(*) AS created_count").
		Joins("JOIN issues i ON i.project_id = p.id").
		Where("p.user_id = ?", userID).
		Where(projectNotShelvedSQL("p"), time.Now().UTC()).
		Where("i.source = ?", model.IssueSourceInternal).
		Where("i.created_at >= ? AND i.created_at < ?", startInclusive, endExclusive).
		Group("DATE(i.created_at)").
		Order("DATE(i.created_at) ASC").
		Scan(&rows).Error
	return rows, err
}

// DashboardCycleTimeSample 一条周期样本（started→completed）。
type DashboardCycleTimeSample struct {
	ProjectID   string    `gorm:"column:project_id"`
	ProjectName string    `gorm:"column:project_name"`
	StartedAt   time.Time `gorm:"column:started_at"`
	CompletedAt time.Time `gorm:"column:completed_at"`
}

// GetCycleTimeSamples 近 30 天 resolved 窗口内 completed 且有 started_at 的
// issue 样本（零时长行由服务层剔除，中位数也在服务层 Go 内算）。连接方向
// projects → issues(project_id 索引) → meta 主键，不从 meta 出发全表扫。
func (r *DashboardRepository) GetCycleTimeSamples(
	userID string,
	startInclusive time.Time,
	endExclusive time.Time,
) ([]DashboardCycleTimeSample, error) {
	var rows []DashboardCycleTimeSample
	err := r.db.
		Table("projects AS p").
		Select("p.id AS project_id, p.name AS project_name, m.started_at, m.completed_at").
		Joins("JOIN issues i ON i.project_id = p.id").
		Joins("JOIN issue_internal_meta m ON m.issue_id = i.id").
		Where("p.user_id = ?", userID).
		Where(projectNotShelvedSQL("p"), time.Now().UTC()).
		Where("m.completed_at >= ? AND m.completed_at < ?", startInclusive, endExclusive).
		Where("m.started_at IS NOT NULL").
		Scan(&rows).Error
	return rows, err
}

// DashboardBacklogAge 当前开启问题按创建时间的三档分布。
type DashboardBacklogAge struct {
	Under7d int `gorm:"column:under_7d"`
	D7To30d int `gorm:"column:d7_to_30d"`
	Over30d int `gorm:"column:over_30d"`
}

// GetBacklogAge 范围与待办分桶同口径（state=open 且 workflow 未 done、排除
// 搁置项目），恒等式三档之和 == todo+in_progress == Σopen_issue_count。分档
// 边界由服务层 Go 算好 time 参数传入：时间是文本存储，比较走字符串序，
// 不用 julianday。
func (r *DashboardRepository) GetBacklogAge(userID string, under7, under30 time.Time) (DashboardBacklogAge, error) {
	var age DashboardBacklogAge
	err := r.db.
		Table("projects AS p").
		Select(`
			COALESCE(SUM(CASE WHEN i.created_at >= ? THEN 1 ELSE 0 END), 0) AS under_7d,
			COALESCE(SUM(CASE WHEN i.created_at >= ? AND i.created_at < ? THEN 1 ELSE 0 END), 0) AS d7_to_30d,
			COALESCE(SUM(CASE WHEN i.created_at < ? THEN 1 ELSE 0 END), 0) AS over_30d
		`, under7, under30, under7, under30).
		Joins("LEFT JOIN issues i ON i.project_id = p.id").
		Joins("LEFT JOIN issue_internal_meta m ON m.issue_id = i.id").
		Where("p.user_id = ?", userID).
		Where(projectNotShelvedSQL("p"), time.Now().UTC()).
		Where("i.state = ? AND (m.workflow_status IS NULL OR m.workflow_status != ?)", model.IssueStateOpen, model.IssueWorkflowStatusDone).
		Scan(&age).Error
	return age, err
}

// DashboardReviewPRRow PR 级审查聚合行：窗口内（或仍有在途 run）的当前轮次
// +当前 head 连接结论，分桶在服务层归一。
type DashboardReviewPRRow struct {
	ProjectID     string `gorm:"column:project_id"`
	RepoFullName  string `gorm:"column:repo_full_name"`
	PRNumber      int    `gorm:"column:pr_number"`
	IntegrationID string `gorm:"column:integration_id"`
	CodeStatus    string `gorm:"column:code_status"`
	ActiveRuns    int64  `gorm:"column:active_runs"`
}

// GetReviewOutcomes PR 级审查结论：复用阶段一 review_runs⨝review_pull_requests
// 全键连接（当前轮次+当前 head，与 ListFailedReviews 同一连接），按 PR 分组。
// 入窗条件（finished_at≥since 或仍在途）下推 SQL：MAX(finished_at) 聚合在
// SQLite 会以字符串回传（文本存储的时间不走驱动转换），不下推就得在 Go 里
// 自行解析多种历史写法；在途 run 的 finished_at 为 NULL，由 OR 的第二支保住。
func (r *DashboardRepository) GetReviewOutcomes(userID string, since time.Time) ([]DashboardReviewPRRow, error) {
	var rows []DashboardReviewPRRow
	err := r.db.
		Table("review_runs AS r").
		Select(`
			r.project_id, r.repo_full_name, r.pr_number, r.integration_id,
			rp.code_status,
			SUM(CASE WHEN r.status IN ? THEN 1 ELSE 0 END) AS active_runs
		`, model.ReviewRunActiveStatuses).
		Joins("JOIN review_pull_requests rp ON r.project_id = rp.project_id AND r.repo_full_name = rp.repo_full_name AND r.pr_number = rp.pr_number AND r.integration_id = rp.integration_id AND r.round_number = rp.round_number AND r.head_sha = rp.head_sha").
		Joins("JOIN projects p ON p.id = r.project_id").
		Where("r.user_id = ?", userID).
		Where(projectNotShelvedSQL("p"), time.Now().UTC()).
		Where("r.finished_at >= ? OR r.status IN ?", since, model.ReviewRunActiveStatuses).
		Group("r.project_id, r.repo_full_name, r.pr_number, r.integration_id, rp.code_status").
		Scan(&rows).Error
	return rows, err
}

// DashboardNextScheduleTask 机器人下一次计划触发的任务。
type DashboardNextScheduleTask struct {
	BotID     string    `gorm:"column:bot_id"`
	BotName   string    `gorm:"column:bot_name"`
	TaskName  string    `gorm:"column:task_name"`
	NextRunAt time.Time `gorm:"column:next_run_at"`
}

// GetNextScheduleTask 最近一次将要触发的已启用定时任务；没有则返回 nil。
// bot_schedule_tasks 与 bots 都是小表且按主键连接，无额外索引可接受
// （INT-235 文档注记）。
func (r *DashboardRepository) GetNextScheduleTask(userID string) (*DashboardNextScheduleTask, error) {
	var row DashboardNextScheduleTask
	err := r.db.
		Table("bot_schedule_tasks AS t").
		Select("t.bot_id, b.name AS bot_name, t.name AS task_name, t.next_run_at").
		Joins("JOIN bots b ON b.id = t.bot_id").
		Where("b.user_id = ?", userID).
		Where("t.enabled AND t.next_run_at IS NOT NULL").
		Order("t.next_run_at ASC").
		Limit(1).
		Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.BotID == "" {
		return nil, nil // Scan 空结果不报错：按无任务处理
	}
	return &row, nil
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
