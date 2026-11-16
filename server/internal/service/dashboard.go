package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
)

const (
	dashboardResolvedWindowDays   = 30
	dashboardFailedReviewLookback = 14 * 24 * time.Hour
	dashboardActionItemsMax       = 5
	dashboardPendingVersionsMax   = 3
	dashboardRecentReleasesMax    = 5
	dashboardCycleTimeMax         = 5
	dashboardBacklogUnder7Days    = 7 * 24 * time.Hour
	dashboardBacklogUnder30Days   = 30 * 24 * time.Hour
	dashboardReviewsWindowDays    = 30
)

// dashboardT3WaitingSource 是 DashboardService 对 T3 侧的唯一依赖：窄接口，
// 只读活跃 shell 快照，绝不阻塞（实现见 T3ThreadsService.WaitingThreads）。
// result 为 nil 表示用户没有任何 T3 环境，该组不出现在响应里。
type dashboardT3WaitingSource interface {
	WaitingThreads(ctx context.Context, userID string) (*T3WaitingResult, error)
}

// dashboardUnreadCounter 未读通知计数的窄接口（NotificationService 实现）。
type dashboardUnreadCounter interface {
	UnreadCount(ctx context.Context, userID string) (int64, error)
}

// dashboardQuotaLister 额度账号快照的窄接口（QuotaService.List 实现）：只读
// 本地快照不访问网络。nil 时 quota 子块整体缺席（阶段一 s.unread != nil 模式）。
type dashboardQuotaLister interface {
	List(userID string) ([]api.QuotaAccount, error)
}

// dashboardBotUsageSource 机器人用量总览的窄接口（BotService 实现）：复用
// GetBotUsageOverview 的今天/脏日回退实时扫描，绝不直读 bot_usage_daily。
// nil 时 bots 子块整体缺席。
type dashboardBotUsageSource interface {
	GetBotUsageOverview(userID, rangeParam, botID string) (*api.BotUsageOverview, error)
}

type DashboardService struct {
	dashboardRepo *repository.DashboardRepository
	unread        dashboardUnreadCounter
	t3Waiting     dashboardT3WaitingSource
	quota         dashboardQuotaLister
	bots          dashboardBotUsageSource
}

func NewDashboardService(
	dashboardRepo *repository.DashboardRepository,
	unread dashboardUnreadCounter,
	t3Waiting dashboardT3WaitingSource,
	quota dashboardQuotaLister,
	bots dashboardBotUsageSource,
) *DashboardService {
	return &DashboardService{dashboardRepo: dashboardRepo, unread: unread, t3Waiting: t3Waiting, quota: quota, bots: bots}
}

// dashboardActionRef 一条聚合条目的路由参数集合（按组取用其中字段）。
type dashboardActionRef struct {
	projectID string
	envID     string
	threadID  string
	issueID   string
	versionID string
}

// dashboardActionItemHref 生成各组条目的站内跳转路径，集中一处便于测试与
// 路由变更时对齐前端。
func dashboardActionItemHref(kind api.DashboardActionItemKind, ref dashboardActionRef) string {
	switch kind {
	case api.T3Waiting:
		return "/threads/" + ref.envID + "/" + ref.threadID
	case api.ReviewFailed:
		return "/reviews?project=" + ref.projectID
	case api.Recommendations:
		return "/projects/" + ref.projectID + "/issues/" + ref.issueID
	case api.ShipFailed:
		return "/projects/" + ref.projectID + "/versions/" + ref.versionID
	case api.QuotaAttention:
		return "/quota"
	case api.UnreadNotifications:
		return "/notifications"
	default:
		return "/"
	}
}

func dashboardItem(title, detail, href string) DashboardActionItem {
	item := DashboardActionItem{Title: title, Href: href}
	if detail != "" {
		item.Detail = api.Ptr(detail)
	}
	return item
}

// dashboardVersionHref 版本详情的站内路径；发版节奏卡与 ship_failed 组共用
// 同一路由形态，路由变更时随 dashboardActionItemHref 一处对齐。
func dashboardVersionHref(projectID, versionID string) string {
	return dashboardActionItemHref(api.ShipFailed, dashboardActionRef{projectID: projectID, versionID: versionID})
}

func (s *DashboardService) GetOverview(ctx context.Context, userID string) (*DashboardOverviewResponse, error) {
	openCounts, err := s.dashboardRepo.GetOpenIssueCounts(userID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)
	startInclusive := today.AddDate(0, 0, -(dashboardResolvedWindowDays - 1))
	endExclusive := today.AddDate(0, 0, 1)

	resolvedCounts, err := s.dashboardRepo.GetDailyResolvedCounts(userID, startInclusive, endExclusive)
	if err != nil {
		return nil, errs.ErrInternal
	}

	resolvedByDate := make(map[string]int, len(resolvedCounts))
	for _, row := range resolvedCounts {
		resolvedByDate[row.Date] = row.ResolvedCount
	}

	resp := &DashboardOverviewResponse{
		OpenIssuesByProject: make([]DashboardProjectOpenIssuePoint, 0, len(openCounts)),
		DailyResolved:       make([]DashboardDailyResolvedPoint, 0, dashboardResolvedWindowDays),
		ActionItemGroups:    make([]DashboardActionItemGroup, 0, 6),
	}

	for _, row := range openCounts {
		resp.OpenIssuesByProject = append(resp.OpenIssuesByProject, DashboardProjectOpenIssuePoint{
			ProjectId:      row.ProjectID,
			ProjectName:    row.ProjectName,
			OpenIssueCount: row.OpenIssueCount,
		})
	}

	for offset := 0; offset < dashboardResolvedWindowDays; offset++ {
		current := startInclusive.AddDate(0, 0, offset)
		date := current.Format("2006-01-02")
		resp.DailyResolved = append(resp.DailyResolved, DashboardDailyResolvedPoint{
			Date:          date,
			ResolvedCount: resolvedByDate[date],
		})
	}

	// T3 只读快照只取一次：「待我处理」组与 AI 面板共用同一结果，绝不
	// 调第二次 PeekActive（INT-235 单遍历约束）。窄接口缺席时不取数。
	var t3Snap dashboardT3Snapshot
	if s.t3Waiting != nil {
		t3Snap.queried = true
		t3Snap.result, t3Snap.err = s.t3Waiting.WaitingThreads(ctx, userID)
	}

	// 「待我处理」聚合区：固定顺序输出。单组失败只降级该组
	// （complete=false），绝不拖垮整页，也绝不静默清零伪装成「一切正常」。
	resp.ActionItemGroups = append(resp.ActionItemGroups, s.actionGroups(ctx, userID, t3Snap)...)

	// 发版节奏（阶段三）：纯 SQL 聚合，单块失败降级 complete=false。
	resp.Release = s.buildRelease(userID)

	// 流转效率（阶段二）：与 daily_resolved 共用同一 UTC 窗口。
	resp.Flow = s.buildFlow(userID, startInclusive, endExclusive)

	// AI 运行面板（阶段三）：各子块独立降级；T3 与「待我处理」共用同一
	// 遍历结果（见上），quota/bots 走窄接口。
	resp.Ai = s.buildAi(userID, t3Snap)

	return resp, nil
}

// dashboardT3Snapshot 是 WaitingThreads 的一次取数结果，供「待我处理」组与
// AI 面板两个消费方共用：queried=窄接口在场且已取数；result=nil=无环境。
type dashboardT3Snapshot struct {
	queried bool
	result  *T3WaitingResult
	err     error
}

func (s *DashboardService) buildAi(userID string, t3Snap dashboardT3Snapshot) *api.DashboardAi {
	return &api.DashboardAi{
		T3:      s.buildAiT3(t3Snap),
		Reviews: s.buildAiReviews(userID, time.Now().UTC()),
		Bots:    s.buildAiBots(userID),
		Quota:   s.buildAiQuota(userID),
	}
}

// buildAiT3 每环境活跃线程计数：直接取自与「待我处理」组同一次遍历的
// 结果。查询错误 → 子块降级（结构完整）；无 T3 环境（result=nil）→ 缺席。
// complete 仅在降级时返回 false（契约「成功时不返回」，与 reviews/bots/
// quota 子块同形），成功时不携带该字段。
func (s *DashboardService) buildAiT3(t3Snap dashboardT3Snapshot) *api.DashboardAiT3 {
	if !t3Snap.queried {
		return nil
	}
	if t3Snap.err != nil {
		return &api.DashboardAiT3{
			Complete:     api.Ptr(false),
			Environments: []api.DashboardAiT3Environment{},
		}
	}
	if t3Snap.result == nil {
		return nil
	}
	environments := make([]api.DashboardAiT3Environment, 0, len(t3Snap.result.Environments))
	for _, env := range t3Snap.result.Environments {
		environments = append(environments, api.DashboardAiT3Environment{
			EnvironmentId:   env.EnvironmentID,
			EnvironmentName: env.EnvironmentName,
			Running:         env.Running,
			Waiting:         env.Waiting,
			Queued:          env.Queued,
			Readable:        env.Readable,
		})
	}
	t3 := &api.DashboardAiT3{Environments: environments}
	if !t3Snap.result.Complete {
		t3.Complete = api.Ptr(false)
	}
	return t3
}

// buildAiReviews 审查四分桶：按 PR 归一（code_status success→passed /
// failure→blocked / error→errored，pending 或仍有在途 run→in_flight，
// 在途优先——正在审的 PR 不因上轮结论落桶）。completed ≠ 通过：run 终态
// completed 但 code_status=failure 的 PR 归 blocked。pass_rate=passed/(passed+blocked)，
// 分母 0 时为 null（不显示，不代表 100%）。
func (s *DashboardService) buildAiReviews(userID string, now time.Time) *api.DashboardAiReviews {
	since := now.Add(-dashboardReviewsWindowDays * 24 * time.Hour)
	rows, err := s.dashboardRepo.GetReviewOutcomes(userID, since)
	if err != nil {
		return &api.DashboardAiReviews{
			Complete: api.Ptr(false),
			PassRate: nil,
		}
	}
	reviews := &api.DashboardAiReviews{}
	for _, row := range rows {
		// 入窗（finished_at≥since 或仍在途）已下推 SQL，这里只归一分桶。
		switch {
		case row.ActiveRuns > 0:
			reviews.InFlight++
		case row.CodeStatus == "success":
			reviews.Passed++
		case row.CodeStatus == "failure":
			reviews.Blocked++
		case row.CodeStatus == "error":
			reviews.Errored++
		default: // pending/空：本轮结论未出
			reviews.InFlight++
		}
	}
	if reviews.Passed+reviews.Blocked > 0 {
		reviews.PassRate = api.Ptr(float64(reviews.Passed) / float64(reviews.Passed+reviews.Blocked))
	}
	return reviews
}

// buildAiBots 机器人近 7 天用量（按用户时区，与机器人页同源）：经
// GetBotUsageOverview 窄接口取数（今天/脏日回退实时扫描），绝不直读
// bot_usage_daily。服务错误（含 ErrInvalidParams/ErrBotNotFound）一律吞掉降级。
// next_schedule 单独查询，失败也只降级本子块。
func (s *DashboardService) buildAiBots(userID string) *api.DashboardAiBots {
	if s.bots == nil {
		return nil
	}
	overview, err := s.bots.GetBotUsageOverview(userID, "7d", "")
	if err != nil {
		return &api.DashboardAiBots{Complete: api.Ptr(false)}
	}
	bots := &api.DashboardAiBots{
		BotCount:      len(overview.Bots),
		Timezone:      overview.Timezone,
		Cost:          overview.Totals.Cost,
		TotalRequests: overview.Totals.TotalRequests,
		TotalTokens:   overview.Totals.TotalTokens,
	}
	task, err := s.dashboardRepo.GetNextScheduleTask(userID)
	if err != nil {
		bots.Complete = api.Ptr(false)
		return bots
	}
	if task != nil {
		bots.NextSchedule = &api.DashboardAiBotNextSchedule{
			BotId:     task.BotID,
			BotName:   task.BotName,
			TaskName:  task.TaskName,
			NextRunAt: task.NextRunAt.UTC().Format(time.RFC3339Nano),
		}
	}
	return bots
}

// buildAiQuota 额度账号只读快照（QuotaService.List 不访问网络）：剩余百分比
// 不在后端算，前端用 lib/quota.ts accountRemaining 推导。服务错误吞掉降级。
func (s *DashboardService) buildAiQuota(userID string) *api.DashboardAiQuota {
	if s.quota == nil {
		return nil
	}
	accounts, err := s.quota.List(userID)
	if err != nil {
		return &api.DashboardAiQuota{Complete: api.Ptr(false), Accounts: []api.QuotaAccount{}}
	}
	if accounts == nil {
		accounts = []api.QuotaAccount{}
	}
	return &api.DashboardAiQuota{Accounts: accounts}
}

// buildFlow 流转效率聚合：待办分桶、internal 新建趋势、周期中位数与积压
// 年龄。任一子查询失败整块降级（complete=false、计数清零、结构仍完整），
// 绝不 500——前端据此显示「暂不可用」而不是误读成零。
func (s *DashboardService) buildFlow(userID string, startInclusive, endExclusive time.Time) *api.DashboardFlow {
	degraded := func() *api.DashboardFlow {
		return &api.DashboardFlow{
			Complete:           api.Ptr(false),
			DailyCreated:       make([]api.DashboardDailyCreatedPoint, 0, dashboardResolvedWindowDays),
			CycleTimeByProject: []api.DashboardCycleTimeProject{},
			BacklogAge:         api.DashboardBacklogAge{},
		}
	}

	buckets, err := s.dashboardRepo.GetWorkflowBuckets(userID)
	if err != nil {
		return degraded()
	}

	createdRows, err := s.dashboardRepo.GetDailyCreatedCounts(userID, startInclusive, endExclusive)
	if err != nil {
		return degraded()
	}
	createdByDate := make(map[string]int, len(createdRows))
	for _, row := range createdRows {
		createdByDate[row.Date] = row.CreatedCount
	}
	dailyCreated := make([]api.DashboardDailyCreatedPoint, 0, dashboardResolvedWindowDays)
	for offset := 0; offset < dashboardResolvedWindowDays; offset++ {
		date := startInclusive.AddDate(0, 0, offset).Format("2006-01-02")
		dailyCreated = append(dailyCreated, api.DashboardDailyCreatedPoint{
			Date: date, CreatedCount: createdByDate[date],
		})
	}

	samples, err := s.dashboardRepo.GetCycleTimeSamples(userID, startInclusive, endExclusive)
	if err != nil {
		return degraded()
	}

	now := time.Now().UTC()
	age, err := s.dashboardRepo.GetBacklogAge(userID, now.Add(-dashboardBacklogUnder7Days), now.Add(-dashboardBacklogUnder30Days))
	if err != nil {
		return degraded()
	}

	return &api.DashboardFlow{
		TodoCount:          buckets.TodoCount,
		InProgressCount:    buckets.InProgressCount,
		DailyCreated:       dailyCreated,
		CycleTimeByProject: dashboardCycleTimeByProject(samples, dashboardCycleTimeMax),
		BacklogAge:         api.DashboardBacklogAge{Under7d: age.Under7d, D7To30d: age.D7To30d, Over30d: age.Over30d},
	}
}

// dashboardCycleTimeByProject 按项目聚合周期样本：零时长（started==completed，
// 常见为同步回填）剔除；中位数在 Go 内算（防空切片与 SQL 方言差异，偶数样本
// 取中间两数均值）；按 completed_count 降序、项目名升序取前 limit。
func dashboardCycleTimeByProject(samples []repository.DashboardCycleTimeSample, limit int) []api.DashboardCycleTimeProject {
	durations := make(map[string][]float64)
	names := make(map[string]string, len(samples))
	for _, sample := range samples {
		if !sample.CompletedAt.After(sample.StartedAt) {
			continue
		}
		durations[sample.ProjectID] = append(durations[sample.ProjectID], sample.CompletedAt.Sub(sample.StartedAt).Hours()/24)
		names[sample.ProjectID] = sample.ProjectName
	}
	items := make([]api.DashboardCycleTimeProject, 0, len(durations))
	for projectID, ds := range durations {
		sort.Float64s(ds)
		median := ds[len(ds)/2]
		if len(ds)%2 == 0 {
			median = (ds[len(ds)/2-1] + ds[len(ds)/2]) / 2
		}
		items = append(items, api.DashboardCycleTimeProject{
			ProjectId:      projectID,
			ProjectName:    names[projectID],
			MedianDays:     median,
			CompletedCount: len(ds),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CompletedCount != items[j].CompletedCount {
			return items[i].CompletedCount > items[j].CompletedCount
		}
		return items[i].ProjectName < items[j].ProjectName
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}

// buildRelease 发版节奏聚合：待发版（pending 升序）+ 近期发版（shipped_at
// 降序，无时间窗口）+ 未合并 PR 关联的开启问题数。三个子查询任一失败整块
// 降级（complete=false、结构仍完整），绝不 500。
func (s *DashboardService) buildRelease(userID string) *api.DashboardRelease {
	release := &api.DashboardRelease{
		PendingVersions: []api.DashboardReleaseVersion{},
		RecentReleases:  []api.DashboardReleaseShipped{},
	}
	pendingTotal, pendingRows, err := s.dashboardRepo.ListPendingVersions(userID, dashboardPendingVersionsMax)
	if err != nil {
		release.Complete = api.Ptr(false)
		return release
	}
	release.PendingCount = int(pendingTotal)
	for _, row := range pendingRows {
		release.PendingVersions = append(release.PendingVersions, api.DashboardReleaseVersion{
			VersionId:     row.VersionID,
			VersionNumber: row.VersionNumber,
			ProjectId:     row.ProjectID,
			ProjectName:   row.ProjectName,
			CreatedAt:     row.CreatedAt.UTC().Format(time.RFC3339Nano),
			Href:          dashboardVersionHref(row.ProjectID, row.VersionID),
		})
	}

	recentRows, err := s.dashboardRepo.ListRecentReleases(userID, dashboardRecentReleasesMax)
	if err != nil {
		release.Complete = api.Ptr(false)
		return release
	}
	for _, row := range recentRows {
		item := api.DashboardReleaseShipped{
			VersionId:     row.VersionID,
			VersionNumber: row.VersionNumber,
			ProjectId:     row.ProjectID,
			ProjectName:   row.ProjectName,
			CreatedAt:     row.CreatedAt.UTC().Format(time.RFC3339Nano),
			Href:          dashboardVersionHref(row.ProjectID, row.VersionID),
		}
		if row.ShippedAt != nil {
			item.ShippedAt = row.ShippedAt.UTC().Format(time.RFC3339Nano)
		}
		release.RecentReleases = append(release.RecentReleases, item)
	}

	unmerged, err := s.dashboardRepo.CountUnmergedPRIssues(userID)
	if err != nil {
		release.Complete = api.Ptr(false)
		return release
	}
	release.UnmergedPrIssueCount = int(unmerged)
	return release
}

// dashboardActionGroup 一个「待我处理」组的统一骨架：查询失败降级为
// count=0 且 complete=false 的组（前端显示「暂不可用」），成功则按
// mapItem 组装条目。
func dashboardActionGroup[Row any](
	kind api.DashboardActionItemKind,
	query func() (int64, []Row, error),
	mapItem func(Row) DashboardActionItem,
) DashboardActionItemGroup {
	total, rows, err := query()
	if err != nil {
		return DashboardActionItemGroup{Kind: kind, Count: 0, Complete: api.Ptr(false), Items: []DashboardActionItem{}}
	}
	items := make([]DashboardActionItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, mapItem(row))
	}
	return DashboardActionItemGroup{Kind: kind, Count: int(total), Items: items}
}

// actionGroups 按固定顺序组装六个组：t3_waiting（无环境时缺席）→
// review_failed → recommendations → ship_failed → quota_attention →
// unread_notifications。t3 组从传入的单遍历快照取数。
func (s *DashboardService) actionGroups(ctx context.Context, userID string, t3Snap dashboardT3Snapshot) []DashboardActionItemGroup {
	groups := make([]DashboardActionItemGroup, 0, 6)

	if t3Snap.queried {
		// T3 聚合只允许降级成「组不完整」，绝不拖垮整页：出错按全不可读
		// 处理（count=0 complete=false），result=nil 表示无环境、整组缺席。
		switch {
		case t3Snap.err != nil:
			groups = append(groups, DashboardActionItemGroup{
				Kind:     api.T3Waiting,
				Count:    0,
				Complete: api.Ptr(false),
				Items:    []DashboardActionItem{},
			})
		case t3Snap.result != nil:
			items := make([]DashboardActionItem, 0, dashboardActionItemsMax)
			for _, w := range t3Snap.result.Items {
				if len(items) >= dashboardActionItemsMax {
					break
				}
				items = append(items, dashboardItem(
					w.Title, w.EnvironmentName,
					dashboardActionItemHref(api.T3Waiting, dashboardActionRef{envID: w.EnvironmentID, threadID: w.ThreadID}),
				))
			}
			group := DashboardActionItemGroup{
				Kind:  api.T3Waiting,
				Count: len(t3Snap.result.Items),
				Items: items,
			}
			// complete 仅在降级时返回 false（契约「成功时不返回」），与
			// buildAiT3 同形。
			if !t3Snap.result.Complete {
				group.Complete = api.Ptr(false)
			}
			groups = append(groups, group)
		}
	}

	groups = append(groups, dashboardActionGroup(api.ReviewFailed,
		func() (int64, []repository.DashboardFailedReview, error) {
			return s.dashboardRepo.ListFailedReviews(userID, time.Now().UTC().Add(-dashboardFailedReviewLookback), dashboardActionItemsMax)
		},
		func(row repository.DashboardFailedReview) DashboardActionItem {
			title := row.PRTitle
			if title == "" {
				title = fmt.Sprintf("PR #%d", row.PRNumber)
			}
			return dashboardItem(title, row.ProjectName,
				dashboardActionItemHref(api.ReviewFailed, dashboardActionRef{projectID: row.ProjectID}))
		}))

	groups = append(groups, dashboardActionGroup(api.Recommendations,
		func() (int64, []repository.DashboardActiveRecommendation, error) {
			return s.dashboardRepo.ListActiveRecommendations(userID, dashboardActionItemsMax)
		},
		func(row repository.DashboardActiveRecommendation) DashboardActionItem {
			return dashboardItem(row.IssueTitle, row.ProjectName,
				dashboardActionItemHref(api.Recommendations, dashboardActionRef{projectID: row.ProjectID, issueID: row.IssueID}))
		}))

	groups = append(groups, dashboardActionGroup(api.ShipFailed,
		func() (int64, []repository.DashboardFailedShip, error) {
			return s.dashboardRepo.ListFailedShips(userID, dashboardActionItemsMax)
		},
		func(row repository.DashboardFailedShip) DashboardActionItem {
			return dashboardItem(row.VersionNumber, row.ProjectName,
				dashboardActionItemHref(api.ShipFailed, dashboardActionRef{projectID: row.ProjectID, versionID: row.VersionID}))
		}))

	groups = append(groups, dashboardActionGroup(api.QuotaAttention,
		func() (int64, []repository.DashboardQuotaAttention, error) {
			return s.dashboardRepo.ListAttentionQuotaAccounts(userID, dashboardActionItemsMax)
		},
		func(row repository.DashboardQuotaAttention) DashboardActionItem {
			return dashboardItem(row.Label, row.Provider,
				dashboardActionItemHref(api.QuotaAttention, dashboardActionRef{}))
		}))

	if s.unread != nil {
		count, err := s.unread.UnreadCount(ctx, userID)
		if err != nil {
			groups = append(groups, DashboardActionItemGroup{
				Kind:     api.UnreadNotifications,
				Count:    0,
				Complete: api.Ptr(false),
				Items:    []DashboardActionItem{},
			})
		} else {
			items := make([]DashboardActionItem, 0, 1)
			if count > 0 {
				items = append(items, dashboardItem(
					fmt.Sprintf("%d 条未读通知", count), "",
					dashboardActionItemHref(api.UnreadNotifications, dashboardActionRef{})))
			}
			groups = append(groups, DashboardActionItemGroup{Kind: api.UnreadNotifications, Count: int(count), Items: items})
		}
	}

	return groups
}

// sortT3WaitingItems 跨环境合流后按更新时间倒序（新近等待的排前）。
// UpdatedAt 是上游透传的字符串，先按 RFC3339 解析比较，解析失败退回
// 字符串字典序，避免不同精度/时区写法把真正最新的等待排到截断之外。
func sortT3WaitingItems(items []T3WaitingItem) {
	sort.SliceStable(items, func(i, j int) bool {
		ti, errI := time.Parse(time.RFC3339, items[i].UpdatedAt)
		tj, errJ := time.Parse(time.RFC3339, items[j].UpdatedAt)
		if errI == nil && errJ == nil {
			return ti.After(tj)
		}
		return items[i].UpdatedAt > items[j].UpdatedAt
	})
}
