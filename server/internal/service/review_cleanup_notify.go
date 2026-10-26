package service

import (
	"fmt"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
)

// 回收问题的聚合通知（INT-170）：按（环境, 仓库, 原因）分键——同一仓库的
// 批量回收问题（分支残留、登记待确认）收敛成一条清单，而不是一次事件刷一屏。
// 与状态机主体（review_cleanup.go）分开成文件，只放通知的发布与文案。

// publishCleanupAggregate 发布一次回收聚合通知：每次发布都重算全量受影响
// 运行，dedup 键相同、指纹（标题+正文）变化才更新正文并清已读，清单没变只续
// last_seen_at，重复发布天然收敛。
//
// 发布前先查 dedup 键是否已存在（INT-170）：Publish 的语义是「查无此键就新
// 建」，恢复刷新（finishWorktreeCleanup 对旧原因键的收缩重发）针对的可能是
// 从未发布过的键——直接发会凭空造出一条正文「受影响运行均已回收完成」的通
// 知，向用户宣告一个从没被告之过的问题已解决，纯噪音。键不存在且本次清单为
// 空 → 跳过不建；键不存在但清单非空 → 正常建；键存在 → 照常发布更新。
func (s *ReviewService) publishCleanupAggregate(run *model.ReviewRun, reason string, now time.Time) {
	if s.sink == nil || run.ID == "" {
		return
	}
	var affected []model.ReviewRun
	if err := s.db.
		Where("environment_id = ? AND repo_full_name = ? AND worktree_reason = ? AND worktree_state IN ?",
			run.EnvironmentID, run.RepoFullName, reason,
			[]model.ReviewWorktreeState{model.ReviewWorktreeNone,
				model.ReviewWorktreeBranchPending, model.ReviewWorktreeFailed}).
		Order("finished_at ASC").Limit(reviewStageBatch + 1).Find(&affected).Error; err != nil {
		s.logger.Warn("查询回收受影响运行失败", zap.String("run", run.ID), zap.Error(err))
	}
	// 清单截断按 limit+1 探测：恰好 reviewStageBatch 条不算截断，多探的那条
	// 只用来判断「还有更多」。
	truncated := len(affected) > reviewStageBatch
	if truncated {
		affected = affected[:reviewStageBatch]
	}
	body := cleanupReasonSummary(reason)
	if len(affected) == 0 {
		problemKey := run.EnvironmentID + "|" + run.RepoFullName + "|" + reason
		dedupKey := notificationHash(run.EnvironmentID, "",
			string(model.NotificationReviewCleanupFailed), problemKey)
		var exists int64
		if err := s.db.Model(&model.Notification{}).
			Where("user_id = ? AND dedup_key = ?", run.UserID, dedupKey).
			Count(&exists).Error; err != nil || exists == 0 {
			if err != nil {
				s.logger.Warn("查询回收聚合通知存在性失败", zap.String("run", run.ID), zap.Error(err))
			}
			return
		}
		body += "\n受影响运行均已回收完成"
	} else {
		for i := range affected {
			body += "\n" + cleanupAffectedLine(&affected[i])
		}
		if truncated {
			body += "\n…（清单截断）"
		}
	}
	if err := s.sink.Publish(NotificationInput{
		UserID: run.UserID,
		Kind:   model.NotificationReviewCleanupFailed,
		Title:  cleanupAggregateTitle(reason) + "：" + run.RepoFullName,
		Body:   body,
		// 聚合通知不绑单个线程；dedup 键自带线程位，留空即可。
		EnvironmentID: run.EnvironmentID,
		ProblemKey:    run.EnvironmentID + "|" + run.RepoFullName + "|" + reason,
	}); err != nil {
		s.logger.Warn("写回收聚合通知失败", zap.String("run", run.ID), zap.String("reason", reason), zap.Error(err))
	}
}

// cleanupAggregateTitle 聚合通知标题按原因给可读名：分支残留与待确认不是
// 「失败」，用中性的说法避免把可自动恢复的问题渲染成故障。
func cleanupAggregateTitle(reason string) string {
	switch reason {
	case worktreeReasonBranchPending:
		return "PR 审查分支待清理"
	case worktreeReasonRegistryIncomplete, worktreeReasonRemoveUncertain:
		return "PR 审查回收待确认"
	}
	return "PR 审查工作区回收失败"
}

// cleanupReasonSummary 聚合通知正文首句：一句话说清原因，运行清单跟在后面。
func cleanupReasonSummary(reason string) string {
	switch reason {
	case worktreeReasonArchiveFailed:
		return "线程归档在有限重试后仍失败，工作区回收已放弃（先归档后删除是硬前提）。"
	case worktreeReasonThreadRead:
		return "线程工作区真值读取连续失败，无法证明归属。"
	case worktreeReasonRegistryError:
		return "工作区登记读取连续失败，无法核验归属。"
	case worktreeReasonRegistryIncomplete:
		return "工作区登记未能在时间预算内读取完整，保持待确认并按退避复查。"
	case worktreeReasonOwnershipMismatch:
		return "工作区归属核验未通过，未执行删除。"
	case worktreeReasonWorktreeRemove:
		return "工作区删除未完成：被远端明确拒绝，或删除请求未能送达远端（环境/连接不可用）。"
	case worktreeReasonRemoveUncertain:
		return "工作区删除结果不可见（超时或连接中断），保持待确认并按退避复查远端状态。"
	case worktreeReasonBranchPending:
		return "工作树已删除，本轮专属分支仍在；当前远程 T3 无删除分支接口，待审查代理收尾或人工清理。"
	case worktreeReasonEnvBinding:
		return "环境绑定缺失，无法定位工作区根目录。"
	}
	return "工作区回收未完成。"
}

// cleanupAffectedLine 受影响运行的一行摘要。状态标签按原因给（INT-170）：
// 「待确认」只用于真正等复查的原因——恒 none 的视觉运行（archive_failed，
// 只归档线程、无工作区可清）挂在「待确认」下永不消退，只会让用户反复点开
// 一个不会变化的状态；其余 none 行给中性的「未完成」。
func cleanupAffectedLine(run *model.ReviewRun) string {
	state := "失败"
	switch {
	case run.WorktreeState == model.ReviewWorktreeBranchPending:
		state = "分支待清理"
	case run.WorktreeState == model.ReviewWorktreeNone:
		switch run.WorktreeReason {
		case worktreeReasonRegistryIncomplete, worktreeReasonRemoveUncertain:
			state = "待确认复查"
		case worktreeReasonArchiveFailed:
			state = "已放弃"
		default:
			state = "未完成"
		}
	}
	summary := run.WorktreeError
	if runes := []rune(summary); len(runes) > 80 {
		summary = string(runes[:80]) + "…"
	}
	shortID := run.ID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	line := fmt.Sprintf("PR #%d · 运行 %s · %s", run.PRNumber, shortID, state)
	// 错误为空不渲染孤立的「· 」裸尾巴。
	if summary != "" {
		line += " · " + summary
	}
	return line
}
