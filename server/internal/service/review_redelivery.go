package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"go.uber.org/zap"
)

// 投递失败对账（INT-146）：GitHub 发出但没送达本服务的投递（公网隧道超时/502）
// GitHub App 不会自动重试，webhook 侧也没有别的对账入口——不做这一步，一次
// 投递失败就意味着这次推送永远不会被审查。独立低频循环（redeliveryLoop，不
// 占 10s 调度 tick）拉投递记录、按 guid 分组并重投；复杂度收在 pkg/githubapp
// 适配器里，本文件只做分组与决策。
//
// 已重投次数持久化在 github_webhook_redeliveries（重投次数账）：投递列表只
// 保留约 3 天，原始行滑出 24h 回看窗口后从列表推导的次数会变少——可能重复
// 重投或漏发超限通知；账丢了（被清/恢复丢失）顶多退化回列表推导的近期次数。

const (
	// reviewRedeliverySweepEvery 对账频率：远低于 10s 调度 tick——投递失败是
	// 少数事件，且列表端点走 App JWT、没必要每轮都调。
	reviewRedeliverySweepEvery = 5 * time.Minute
	// reviewRedeliveryLookback 回看窗口：GitHub 只保留约 3 天投递记录，24h
	// 足够覆盖「上一轮对账之间漏掉的失败」，也不会翻太多页。
	reviewRedeliveryLookback = 24 * time.Hour
	// reviewRedeliveryMaxAttempts 同一 guid 的重投上限：超出即认定入口持续
	// 不可达（服务停机/隧道下线），继续重投只会空转。
	reviewRedeliveryMaxAttempts = 3
	// reviewRedeliveryTimeout 单轮对账的兜底超时：列表 + 若干次重投的总额。
	// 独立循环不阻塞调度，但仍要有界，避免 GitHub 半挂时无限拖。
	reviewRedeliveryTimeout = 90 * time.Second
	// reviewRedeliveryMaxPerSweep 一轮对账内最多发出的重投请求数（按「尝试」
	// 计，失败也占名额）：到顶就停，剩余留到下一轮（5 分钟后）。不按成功计
	// 是因为接口持续 5xx / 404 时请求同样在打 GitHub 与本服务的 webhook 入口，
	// 只按成功计数会让上限形同虚设。
	reviewRedeliveryMaxPerSweep = 20
)

// hookDeliveryState 同一 guid 的投递汇总：原始投递与每次重投各占一行（GitHub
// 追加记录），去重和次数兜底计数都从这里推。
type hookDeliveryState struct {
	GUID      string
	Event     string
	Action    string
	Delivered bool // 任一行 2xx
	Attempts  int  // redelivery=true 的行数（列表可见的近期重投次数）
	TargetID  int64
	TargetAt  time.Time
}

// reconcileWebhookDeliveries 一轮投递失败对账：GitHub 发出但没送达本服务的
// pull_request 投递（隧道超时/502）会被重投，避免一次投递失败就让这次推送
// 永远不会被审查。节流由 redeliveryLoop 的 ticker 负责，本函数不做节流。
// 超上限后不再重试并发站内通知。
func (s *ReviewService) reconcileWebhookDeliveries(now time.Time) {
	if s.gh == nil {
		return
	}
	// 投递记录是 App 维度、映射不到具体项目：通知发给所有启用审查的用户；
	// 没有启用审查的项目时整个对账都没有意义。
	var userIDs []string
	if err := s.db.Model(&model.ProjectReviewSettings{}).
		Where("enabled = ?", true).Distinct().Pluck("user_id", &userIDs).Error; err != nil {
		s.logger.Error("查询启用审查的用户失败", zap.Error(err))
		return
	}
	if len(userIDs) == 0 {
		return
	}
	// 网络面（ClientFor + 列表 + 重投）共用同一个兜底超时，先建 ctx 再取客户端。
	ctx, cancel := context.WithTimeout(context.Background(), reviewRedeliveryTimeout)
	defer cancel()
	client, err := s.gh.ClientFor(ctx)
	if err != nil {
		// App 未配置等前置缺失是常态，不按错误处理。
		s.logger.Info("投递对账跳过：无法取得 GitHub 客户端", zap.Error(err))
		return
	}
	hooks, ok := client.(githubapp.HookDeliveries)
	if !ok {
		return // 静态令牌等实现没有投递记录访问面
	}
	items, err := hooks.ListHookDeliveries(ctx, now.Add(-reviewRedeliveryLookback))
	if err != nil {
		s.logger.Warn("拉取 GitHub 投递记录失败", zap.Error(err))
		return
	}
	states := groupHookDeliveries(items)
	pending := make([]hookDeliveryState, 0, len(states))
	for _, st := range states {
		if st.Delivered {
			continue
		}
		pending = append(pending, st)
	}
	if len(pending) == 0 {
		return
	}
	guids := make([]string, len(pending))
	for i, st := range pending {
		guids[i] = st.GUID
	}
	// 本地幂等账一次查全（含超限组）：否则「GitHub 侧三次都超时、但某次重投
	// 实际已在本地落库」的 guid 会被误报故障通知。
	existing, err := s.deliveries.ExistingIDs(s.db, guids)
	if err != nil {
		s.logger.Error("查询本地投递幂等账失败", zap.Error(err))
		return
	}
	// 重投次数账一次查全：有效次数取 max(账, 列表)——列表里 redelivery=true
	// 的行数只是兜底（DB 行被清/恢复丢失时列表还能反映近期尝试），账是跨
	// 24h 窗口的真相（原始行滑出窗口后列表推不出总次数）。
	dbAttempts, err := s.redeliveries.AttemptsFor(s.db, guids)
	if err != nil {
		s.logger.Error("查询 webhook 重投次数账失败", zap.Error(err))
		return
	}
	tried := 0
	for _, st := range pending {
		if ctx.Err() != nil {
			break // 总超时到了，别再发请求
		}
		if existing[st.GUID] {
			continue // 已送达（GitHub 侧记录滞后于本地登记），不重投
		}
		attempts := dbAttempts[st.GUID]
		if st.Attempts > attempts {
			attempts = st.Attempts
		}
		if attempts >= reviewRedeliveryMaxAttempts {
			s.notifyRedeliveryExhausted(userIDs, st, attempts)
			continue
		}
		if tried >= reviewRedeliveryMaxPerSweep {
			s.logger.Info("本轮重投请求数已达上限，剩余留到下一轮",
				zap.Int("limit", reviewRedeliveryMaxPerSweep))
			break
		}
		// 先占名额再请求：被拒的尝试同样消耗一轮的预算（见常量注释）。
		tried++
		err := hooks.RedeliverHookDelivery(ctx, st.TargetID)
		if err != nil {
			s.logger.Warn("投递重投请求失败",
				zap.String("guid", st.GUID), zap.Int64("delivery", st.TargetID), zap.Error(err))
			var statusErr *githubapi.StatusError
			if errors.As(err, &statusErr) &&
				(statusErr.StatusCode == http.StatusForbidden || statusErr.StatusCode == http.StatusTooManyRequests) {
				// 被限流：GitHub 明确要求停止请求并退避；本轮停止，5 分钟后
				// ticker 自然再来。
				s.logger.Warn("投递重投被限流，本轮对账停止",
					zap.String("guid", st.GUID), zap.Int("status", statusErr.StatusCode))
				break
			}
			continue
		}
		// 只记成功的重投：请求被拒时 GitHub 没产生投递记录，下一轮会再试；
		// 记账失败只留日志，不影响后续组（顶多少计一次，靠列表兜底）。
		if err := s.redeliveries.RecordAttempt(s.db, st.GUID, time.Now().UTC()); err != nil {
			s.logger.Error("记录 webhook 重投次数失败",
				zap.String("guid", st.GUID), zap.Error(err))
		}
		s.logger.Info("已请求 GitHub 重投 webhook 投递",
			zap.String("guid", st.GUID), zap.Int64("delivery", st.TargetID))
	}
}

// notifyRedeliveryExhausted 同一 guid 重投超上限仍未送达：给每个启用审查的
// 用户发一条通知。dedup 键含 guid——重复对账只推进 last_seen_at，不刷屏。
func (s *ReviewService) notifyRedeliveryExhausted(userIDs []string, st hookDeliveryState, attempts int) {
	if s.sink == nil {
		return
	}
	title := fmt.Sprintf("PR 审查投递重投失败：%s/%s", st.Event, st.Action)
	body := fmt.Sprintf("GitHub 的 %s/%s 投递在 %d 次重投后仍未送达本服务（guid %s），这次推送可能没有被审查；请检查公网入口与隧道。",
		st.Event, st.Action, attempts, st.GUID)
	for _, userID := range userIDs {
		err := s.sink.Publish(NotificationInput{
			UserID:     userID,
			Kind:       model.NotificationReviewRedeliveryFailed,
			ProblemKey: "webhook-delivery:" + st.GUID,
			Title:      title,
			Body:       body,
		})
		if err != nil {
			s.logger.Warn("投递重投失败通知写入失败", zap.String("user", userID), zap.Error(err))
		}
	}
}

// groupHookDeliveries 把投递行按 guid 汇总：只处理 pull_request 事件里会开轮
// 的 action（opened/synchronize/ready_for_review/reopened）。丢失的 closed
// 投递不会导致「推送没被审查」（本功能唯一目标），但把旧的 closed 事件重投
// 回去会无条件取消该 PR 当前有效的审查运行（webhook 的 closed 分支在任何
// 时间戳判定之前执行），破坏面比收益大，有意不重投。重投目标取最早一条
// 失败行的 delivery id；输出按 TargetAt 升序（GUID 兜同刻排序），保证对账
// 行为确定。Attempts 只数列表可见的重投行——原始行滑出回看窗口后它不再等
// 于总次数，调用方须以重投次数账为准。
func groupHookDeliveries(items []githubapp.HookDelivery) []hookDeliveryState {
	byGUID := map[string]*hookDeliveryState{}
	for _, it := range items {
		if it.Event != "pull_request" || !reviewTriggerableAction(it.Action) {
			continue
		}
		st, ok := byGUID[it.GUID]
		if !ok {
			st = &hookDeliveryState{GUID: it.GUID, Event: it.Event, Action: it.Action}
			byGUID[it.GUID] = st
		}
		if it.Delivered() {
			st.Delivered = true
		}
		if it.Redelivery {
			st.Attempts++
		}
		if !it.Delivered() && (st.TargetID == 0 || it.DeliveredAt.Before(st.TargetAt)) {
			st.TargetID = it.ID
			st.TargetAt = it.DeliveredAt
		}
	}
	out := make([]hookDeliveryState, 0, len(byGUID))
	for _, st := range byGUID {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].TargetAt.Equal(out[j].TargetAt) {
			return out[i].TargetAt.Before(out[j].TargetAt)
		}
		return out[i].GUID < out[j].GUID
	})
	return out
}
