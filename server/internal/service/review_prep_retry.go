package service

import (
	"context"
	"fmt"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 工作区准备失败自动重试（INT-147 第一部分）：T3 建线程阶段撞 Git 锁竞争等
// 瞬态冲突时 run 落 failed（failure.code=workspace_preparation_failed），用户
// 手点 Retry 即可恢复，但 Fast Ship 不识别会干等 deadline 超时、整轮手动重审。
// 本阶段对在途代码运行按节流查线程投影，确认失败满宽限期后代发
// prepared-run.retry（复用失败前已建成的 worktree），计数达上限则落 failed
// 终态转人工。网络调用全部在事务外。

const (
	// reviewPrepRetryMax 同一运行的自动重试上限：达到后不再发第 4 次。
	reviewPrepRetryMax = 3
	// reviewPrepRetryQueryEvery 同一运行对 T3 投影的最小查询间隔（调度 tick
	// 10s，表内按运行粒度节流）。
	reviewPrepRetryQueryEvery = time.Minute
	// reviewPrepRetryGrace 失败条目出现满此时长才重试：避开与同一次 Git 锁
	// 竞争的直接对抗（T3 自己也可能马上自愈）。
	reviewPrepRetryGrace = 30 * time.Second
)

// retryFailedPreparations 扫描在途代码运行中卡在「工作区准备失败」的运行并
// 代发重试。不设批量上限：在途运行总数受每用户 5 条在途上限约束，DB 查询
// 本身很便宜。s.t3 未配置时整段跳过。
func (s *ReviewService) retryFailedPreparations(now time.Time) {
	s.retryFailedPreparationsBudgeted(now, reviewNetworkStageBudget, time.Now)
}

// retryFailedPreparationsBudgeted 与 retryFailedPreparations 同体，预算与时钟
// 可注入：测试用小预算 + 假时钟确定性地复现「预算耗尽后从断点继续」，不用真等
// 慢投影读。clock 只用于阶段预算计量，运行自身的宽限/deadline 仍按 tick 的 now。
func (s *ReviewService) retryFailedPreparationsBudgeted(now time.Time, budget time.Duration, clock func() time.Time) {
	if s.t3 == nil {
		return
	}
	var runs []model.ReviewRun
	if err := s.db.Where("kind = ? AND status IN ? AND dispatched_at IS NOT NULL",
		model.ReviewKindCode,
		[]model.ReviewRunStatus{model.ReviewRunDispatched, model.ReviewRunRunning}).
		Order("created_at ASC, id ASC").Find(&runs).Error; err != nil {
		s.logger.Error("查询准备失败待检运行失败", zap.Error(err))
		return
	}
	if len(runs) == 0 {
		return
	}
	// 从上一次扫描停下的位置之后开始（环形）：慢投影读会把预算吃光，若每轮
	// 都从 created_at 队首重新扫，队首这几条会反复占满预算、后面的运行永远
	// 轮不到。游标是本进程记住的「上次扫到哪条」，预算 break 后下一轮从它的
	// 下一条继续；运行结束时游标自然失效，从队首重新开始。
	rotated := s.prepRetryFromCursor(runs)
	start := clock()
	for i := range rotated {
		if clock().Sub(start) > budget {
			s.logger.Info("准备失败重试到达阶段网络预算，剩余运行下轮从断点继续",
				zap.Int("remaining", len(rotated)-i))
			break
		}
		run := &rotated[i]
		// 先前进游标再判定：预算在这一条之前耗尽时，游标记住的仍是上一条，
		// 下一轮正好从这一条开始（不会跳过它）。
		s.setPrepRetryCursor(run.ID)
		// 节流账与预算共用同一个时钟（生产是实际时刻，不是 tick 起点）：
		// 慢投影读把 tick 拖长时，按起点登记会让窗口提前到期、同一运行被
		// 重复读；共用时钟也让测试能用假时钟把「读很慢、窗口真的过了」
		// 复现出来，验证轮转确实挡得住饥饿。
		if !s.allowPrepRetryQuery(run.ID, clock()) {
			continue
		}
		s.retryFailedPreparation(run, now)
	}
}

// prepRetryFromCursor 把按 (created_at, id) 升序的候选轮转成「从游标之后开始」
// 的环形序列；游标为空或对应运行已不在候选里时从队首开始。
func (s *ReviewService) prepRetryFromCursor(runs []model.ReviewRun) []model.ReviewRun {
	s.prepRetryMu.Lock()
	cursor := s.prepRetryCursor
	s.prepRetryMu.Unlock()
	start := 0
	if cursor != "" {
		for i := range runs {
			if runs[i].ID == cursor {
				start = (i + 1) % len(runs)
				break
			}
		}
	}
	out := make([]model.ReviewRun, 0, len(runs))
	out = append(out, runs[start:]...)
	out = append(out, runs[:start]...)
	return out
}

// setPrepRetryCursor 记住本轮扫到哪条运行（进程内即可：重启后从头扫一轮，
// 顶多多查几条投影，不会漏终态——重试次数本身持久化在 prep_retry_count）。
func (s *ReviewService) setPrepRetryCursor(runID string) {
	s.prepRetryMu.Lock()
	s.prepRetryCursor = runID
	s.prepRetryMu.Unlock()
}

// allowPrepRetryQuery 节流判定并登记本次查询时刻：距上次查询不足间隔则跳过。
// 登记新项时顺手清掉过期项，表不无界增长。公平性由轮转游标保证（每条运行
// 都有轮到的时候），这里只负责「同一运行不早于 60s 重复读」。
func (s *ReviewService) allowPrepRetryQuery(runID string, now time.Time) bool {
	s.prepRetryMu.Lock()
	defer s.prepRetryMu.Unlock()
	if last, ok := s.prepRetryChecked[runID]; ok && now.Sub(last) < reviewPrepRetryQueryEvery {
		return false
	}
	for id, t := range s.prepRetryChecked {
		if now.Sub(t) >= reviewPrepRetryQueryEvery {
			delete(s.prepRetryChecked, id)
		}
	}
	s.prepRetryChecked[runID] = now
	return true
}

// retryFailedPreparation 单个运行的判定与重试：查投影 → 等宽限期 → 计数先加
// 再发 RPC → 成功后在状态守卫下同时延长 deadline 与未消费令牌的有效期。
// 计数条件写防止并发 tick 重复重试；RPC 失败不回退计数（失败条目仍在，下个
// 节流窗口会再试，commandId 随计数推进变化）。节流与阶段预算由调用方把关，
// 本函数最多一次投影读加一次重试 RPC。
func (s *ReviewService) retryFailedPreparation(run *model.ReviewRun, now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	failed, err := s.t3.FailedPreparation(ctx, run.UserID, run.EnvironmentID, run.ThreadID, run.InitialMessageID)
	if err != nil {
		s.logger.Warn("读取审查线程准备失败状态出错", zap.String("run", run.ID), zap.Error(err))
		return
	}
	if failed == nil {
		return
	}
	failedAt, err := parseT3Timestamp(failed.FailedAt)
	if err != nil {
		s.logger.Warn("解析准备失败时刻失败，暂不重试",
			zap.String("run", run.ID), zap.String("failed_at", failed.FailedAt), zap.Error(err))
		return
	}
	if now.Sub(failedAt) < reviewPrepRetryGrace {
		return
	}

	// 计数已达上限：不再发第 4 次，落 failed 终态（保留 prep_retry_count），
	// error_message 带投影里的 T3 失败原文。failRunDispatch 负责消费令牌、
	// 置 PR sync_pending 与通知。
	if run.PrepRetryCount >= reviewPrepRetryMax {
		s.failRunDispatch(run.ID, "workspace_preparation_failed", failed.Message, now)
		return
	}

	res := s.db.Model(&model.ReviewRun{}).
		Where("id = ? AND prep_retry_count = ? AND status IN ?", run.ID, run.PrepRetryCount,
			[]model.ReviewRunStatus{model.ReviewRunDispatched, model.ReviewRunRunning}).
		Updates(map[string]any{"prep_retry_count": run.PrepRetryCount + 1, "updated_at": now})
	if res.Error != nil {
		s.logger.Error("累加准备重试计数失败", zap.String("run", run.ID), zap.Error(res.Error))
		return
	}
	if res.RowsAffected == 0 {
		return // 已被并发 tick 或其他阶段抢先
	}

	commandID := fmt.Sprintf("fs-review-prep-retry-%s-%d", run.ID, run.PrepRetryCount+1)
	if err := s.t3.RetryPreparation(ctx, run.UserID, run.EnvironmentID, run.ThreadID, failed.RunID, commandID); err != nil {
		s.logger.Warn("审查线程准备重试 RPC 失败", zap.String("run", run.ID), zap.Error(err))
		return
	}

	// 重试已受理：T3 复用同一 run（恢复成 preparing）并重放原用户消息，
	// deadline 从现在重新起算，运行行回到「等 Agent 回报」窗口，避免残留的
	// 旧 deadline 立刻触发超时。deadline 与未消费令牌的 expires_at 必须一起
	// 延——令牌有效期是签发时的 deadline+5min（issueRunToken），只延 deadline
	// 会让 Agent 从旧过期点开始收到 40106，运行挂着直到新 deadline 才
	// timed_out。守卫（prep_retry_count=本次加 1 后的值 + 在途状态）保证
	// RPC 期间运行已被取消/完成/超时终态化时两者都不改写：终态行不得重新
	// 获得活的窗口。先发 RPC 后延长是安全的——重试没被受理时什么都不延，
	// 运行按原 deadline 兜底。
	newDeadline := now.Add(reviewTimeout(run.Kind))
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.ReviewRun{}).
			Where("id = ? AND prep_retry_count = ? AND status IN ?", run.ID, run.PrepRetryCount+1,
				[]model.ReviewRunStatus{model.ReviewRunDispatched, model.ReviewRunRunning}).
			Updates(map[string]any{"deadline_at": newDeadline, "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil // RPC 期间已被终态化：不延 deadline，也不延令牌
		}
		return tx.Model(&model.ReviewRunToken{}).
			Where("run_id = ? AND consumed_at IS NULL", run.ID).
			Update("expires_at", newDeadline.Add(reviewTokenGrace)).Error
	}); err != nil {
		s.logger.Error("重置准备重试 deadline 与令牌有效期失败", zap.String("run", run.ID), zap.Error(err))
	}
}

// parseT3Timestamp 解析 T3 投影里的 ISO 时间戳（RFC3339，容忍纳秒变体）。
func parseT3Timestamp(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		t, err := time.Parse(layout, raw)
		if err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析 T3 时间戳 %q", raw)
}
