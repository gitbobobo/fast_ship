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

type DashboardService struct {
	dashboardRepo *repository.DashboardRepository
	unread        dashboardUnreadCounter
	t3Waiting     dashboardT3WaitingSource
}

func NewDashboardService(
	dashboardRepo *repository.DashboardRepository,
	unread dashboardUnreadCounter,
	t3Waiting dashboardT3WaitingSource,
) *DashboardService {
	return &DashboardService{dashboardRepo: dashboardRepo, unread: unread, t3Waiting: t3Waiting}
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

	// 「待我处理」聚合区：固定顺序输出。单组失败只降级该组
	// （complete=false），绝不拖垮整页，也绝不静默清零伪装成「一切正常」。
	resp.ActionItemGroups = append(resp.ActionItemGroups, s.actionGroups(ctx, userID)...)

	return resp, nil
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
// unread_notifications。
func (s *DashboardService) actionGroups(ctx context.Context, userID string) []DashboardActionItemGroup {
	groups := make([]DashboardActionItemGroup, 0, 6)

	if s.t3Waiting != nil {
		// T3 聚合只允许降级成「组不完整」，绝不拖垮整页：出错按全不可读
		// 处理（count=0 complete=false），result=nil 表示无环境、整组缺席。
		result, err := s.t3Waiting.WaitingThreads(ctx, userID)
		switch {
		case err != nil:
			groups = append(groups, DashboardActionItemGroup{
				Kind:     api.T3Waiting,
				Count:    0,
				Complete: api.Ptr(false),
				Items:    []DashboardActionItem{},
			})
		case result != nil:
			items := make([]DashboardActionItem, 0, dashboardActionItemsMax)
			for _, w := range result.Items {
				if len(items) >= dashboardActionItemsMax {
					break
				}
				items = append(items, dashboardItem(
					w.Title, w.EnvironmentName,
					dashboardActionItemHref(api.T3Waiting, dashboardActionRef{envID: w.EnvironmentID, threadID: w.ThreadID}),
				))
			}
			groups = append(groups, DashboardActionItemGroup{
				Kind:     api.T3Waiting,
				Count:    len(result.Items),
				Complete: api.Ptr(result.Complete),
				Items:    items,
			})
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
