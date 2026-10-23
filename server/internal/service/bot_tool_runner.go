package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 工具执行：assistant 回合提交、同步执行、异步 worker、取消与终态落定。
// 调用记录先落库再执行；CAS 认领与条件终态保证同一 call 只有一次执行、
// 一个终态；回执与 SSE 事件提交后才允许 worker 被唤醒。

const botWorkerCount = 4
const botAsyncExecTimeout = 15 * time.Minute

// ---------- assistant 回合提交 ----------

// botBatchAcceptance 是同一回复内逐调用受理时的同批账本：限额与配额评估只
// 查已提交行的话，同一回复里的 N 个同类调用会共享旧计数全部通过。同批已
// 受理的成员在这里补充计数，插入事务内再对已提交行二次复核兜底。
type botBatchAcceptance struct {
	// waitThreads 记录同批已受理（非重复挂）wait 的 (env,thread) → 调用 ID：
	// 同批同线程的后到者按 already_waiting 处理。
	waitThreads map[string]string
	// waitCount 是同批已受理的 wait 数（计入每 bot 并发上限）。
	waitCount int
	// autoSends 按 (env,thread) 记录同批已受理的自动「继续」数（共享每小时配额）。
	autoSends map[string]int
}

func newBotBatchAcceptance() botBatchAcceptance {
	return botBatchAcceptance{
		waitThreads: map[string]string{},
		autoSends:   map[string]int{},
	}
}

func (b *botBatchAcceptance) threadKey(envID, threadID string) string {
	return envID + "\x00" + threadID
}

// botPreparedEntry 是 commitAssistantTurn 逐调用受理的中间形态：校验、范围
// 解析、策略评估与 wait 受理的结论都挂在这里，插入事务按它落账本行。提升
// 到包级别是让受理事务内的二次复核辅助函数能就地改判。
type botPreparedEntry struct {
	call       *model.BotToolCall
	providerID string
	toolName   string
	prepared   *botPreparedCall
	execCtx    botCallExecContext
	authzID    *string
	invalidErr error
	// policy 是分级放行评估结论（INT-160）：凡需确认的写调用都评估，
	// 结论（含降级/拒绝事实）写进 policy_json 审计。
	policy *botWritePolicyEval
	// wait 专用（INT-160）：受理即 running、由评估器收尾；waitDup 非空
	// 表示线程已有 wait，本调用在提交后立即以 already_waiting 落终态。
	waitAccepted bool
	waitDup      string
	waitConds    []string
	waitTimeout  time.Duration
}

// failAcceptedEntry 把已受理的调用改判为受理端同步失败（与既有 invalidErr
// 路径同一落库形态：failed + 40001 + 同步失败回复，账本记回 sync——从未进
// 异步路径，避免排查时误以为是异步执行失败）。
func failAcceptedEntry(entry *botPreparedEntry, now time.Time, err error) {
	entry.invalidErr = err
	entry.call.Status = model.BotCallStatusFailed
	// wait 受理段可能已置 started_at；终态行带着未消费的 started_at 会让
	// 账本误读为「曾真正开始执行」。
	entry.call.StartedAt = nil
	entry.call.FinishedAt = &now
	entry.call.ErrorCode = botStrPtr(botErrCodeOf(err))
	entry.call.ErrorMessage = botStrPtr(botErrMessage(err))
	entry.call.ExecutionMode = string(model.BotExecutionSync)
	entry.prepared = nil
	entry.waitAccepted = false
	entry.waitDup = ""
}

// botWaitRecheckVerdict 是插入事务内 wait 受理二次复核的结论。
type botWaitRecheckVerdict struct {
	// dupCallID 非空：同线程已有 running wait（并发请求抢先挂上），本调用
	// 改判 already_waiting。
	dupCallID string
	// limErr 非空：超出并发上限或 1 小时限额，本调用改判受理失败。
	limErr error
}

// recheckWaitAcceptance 在插入事务内对 wait 受理二次复核：以已提交行为准
// 重新裁决重复挂与两级限额。同批成员由本事务早前的插入天然可见；查询失败
// 不改判（限额评估已在外层做过，这里只兜受理与插入之间的并发窗口）。
func (s *BotService) recheckWaitAcceptance(tx *gorm.DB, botID string, args botThreadArgs) botWaitRecheckVerdict {
	repo := repository.NewBotToolCallRepository(tx)
	// 与受理端同序：1 小时额度先于重复判定——重复挂返回 already_waiting 也
	// 会写账本行并唤醒模型，额度必须先拦住（否则重试循环绕过防循环上限）。
	if n, cerr := repo.CountWaitCallsByThreadSinceTx(tx, botID, args.EnvID, args.ThreadID,
		time.Now().UTC().Add(-time.Hour)); cerr == nil && n >= botWaitMaxPerThreadHour {
		return botWaitRecheckVerdict{limErr: errs.New(errs.ErrInvalidParams.Code,
			fmt.Sprintf("同一线程 1 小时内最多挂 %d 次 wait，已达上限，请向用户汇报", botWaitMaxPerThreadHour))}
	}
	existing, err := repo.FindRunningWaitByThreadTx(tx, botID, args.EnvID, args.ThreadID)
	switch {
	case err == nil:
		return botWaitRecheckVerdict{dupCallID: existing.ID}
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return botWaitRecheckVerdict{}
	}
	if n, cerr := repo.CountRunningWaitCallsTx(tx, botID); cerr == nil && n >= botWaitMaxPerBot {
		return botWaitRecheckVerdict{limErr: errs.New(errs.ErrInvalidParams.Code,
			fmt.Sprintf("同时挂起的 wait 已达上限（%d 个），请等待现有等待结束或用 fs_cancel 取消后再挂；已达上限，请向用户汇报", botWaitMaxPerBot))}
	}
	return botWaitRecheckVerdict{}
}

// recheckSendQuota 在插入事务内对 tier1 自动「继续」的配额二次复核：并发
// 请求（主线与 schedule 会话并行）可能已用尽额度。自动放行是受理时刻的
// 裁决，这里不再降级成确认卡——超限按受理端同步失败落 failed+40001。
func (s *BotService) recheckSendQuota(tx *gorm.DB, botID string, args botThreadArgs, now time.Time) error {
	used, _, ok := s.policySendQuotaUsedTx(tx, botID, args.EnvID, args.ThreadID, now)
	if !ok {
		// 计数查询失败按超限同口径 fail closed——受理评估时同类失败会
		// 降级为确认卡，这里不能无声放行重新打开并发窗口。
		return errs.New(errs.ErrInternal.Code,
			"自动放行配额复核失败，请重新发起该操作")
	}
	if used < botPolicySendQuotaPerThreadHour {
		return nil
	}
	return errs.New(errs.ErrInvalidParams.Code,
		"自动「继续」的每小时额度已用尽（并发请求已消耗），请向用户汇报")
}

// commitAssistantTurn 在一个短事务内提交：assistant 消息、每调用的账本行、
// 异步回执（dedup receipt:<call_id>）、校验失败的即时回复与全部 SSE 事件。
// scope/授权解析与调用 ID 分配都在事务外完成（消息账本有防 UPDATE 守护，
// assistant 行必须一次写对）；提交成功后才唤醒 worker。msgID 由调用方在
// 模型请求前预分配，流式增量信封已携带同一 ID。
func (s *BotService) commitAssistantTurn(bot *model.Bot, run *model.BotRun, requestRow *model.BotModelRequest, reply *botModelReply, msgID string) ([]*model.BotToolCall, error) {
	// 受理评估与提交互斥：wait 判重与 tier1 配额都以已提交行为准，并发事务
	// 互相读不到对方未提交行，评估到提交完成的窗口必须闭合（见 commitMu）。
	s.commitMu.Lock()
	defer s.commitMu.Unlock()

	now := time.Now().UTC()
	requestID := requestRow.ID
	batch := newBotBatchAcceptance()

	// 事务外：逐调用分配 ID、解析/校验/解析范围/匹配授权。
	entries := make([]botPreparedEntry, 0, len(reply.ToolCalls))
	content := botAssistantContent{Text: reply.Text}
	// 校验用本请求构建时的冻结工具集（压缩边界后新旧版本可能不同）。
	ts := botToolsetVersionOf(requestRow.ToolsetVersion)
	for _, tc := range reply.ToolCalls {
		entry := botPreparedEntry{
			providerID: tc.ID,
			toolName:   tc.Name,
			call: &model.BotToolCall{
				ID:                 uuid.NewString(),
				BotID:              bot.ID,
				RequestID:          requestID,
				ProviderToolCallID: tc.ID,
				Ordinal:            int64(len(entries)),
				CreatedAt:          now,
			},
		}
		prepared, err := botPrepareToolCall(ts, tc.Name, tc.Arguments)
		if err != nil {
			entry.invalidErr = err
			entry.call.ToolName = tc.Name
			// 受理即拒的调用也要留下动作名：失败卡的标题与排查都靠
			// tool.action 说清「被拒的是哪个动作」。
			var rawArgs struct {
				Action string `json:"action"`
			}
			if jsonErr := json.Unmarshal([]byte(tc.Arguments), &rawArgs); jsonErr == nil {
				entry.call.Action = strings.TrimSpace(rawArgs.Action)
			}
			entry.call.Status = model.BotCallStatusFailed
			entry.call.FinishedAt = &now
			entry.call.ErrorCode = botStrPtr(botErrCodeOf(err))
			entry.call.ErrorMessage = botStrPtr(botErrMessage(err))
		} else {
			entry.prepared = prepared
			// 已通过校验的元数据先落进调用账本：后面任何一步（schedule 会话
			// 拦截、范围解析）拒绝这次调用时，账本仍能说清「被拒的是哪个动作」，
			// 而不是一条 tool_name/action 为空的泛化失败。
			entry.call.ToolName = prepared.ToolName
			entry.call.Action = prepared.Action
			entry.call.ArgumentsJSON = prepared.ArgumentsJSON
			entry.call.ArgumentsHash = prepared.ArgumentsHash
			entry.call.ExecutionMode = string(prepared.Mode)
			entry.call.RequiresConfirmation = prepared.RequiresConfirmation
			// 定时任务会话禁止管理任务（INT-136）：schedule 会话默认放行写
			// 操作，受理端直接拦截 create/update/run，防止造出递归绕过确认的
			// 会话。命中按校验失败同路径处理（failed + 40001 + 同步失败回复）。
			if run.SessionKind == model.BotSessionKindSchedule {
				if err := botScheduleManageDenied(prepared); err != nil {
					entry.invalidErr = err
					entry.call.ToolName = tc.Name
					entry.call.Action = prepared.Action
					entry.call.Status = model.BotCallStatusFailed
					entry.call.FinishedAt = &now
					entry.call.ErrorCode = botStrPtr(botErrCodeOf(err))
					entry.call.ErrorMessage = botStrPtr(botErrMessage(err))
					// 这是受理端同步拒绝（从未进异步队列），账本记回 sync，
					// 避免排查时误以为是异步执行失败。
					entry.call.ExecutionMode = string(model.BotExecutionSync)
					entry.prepared = nil
				}
			}
		}
		if entry.prepared != nil {
			if err := s.resolveCallScope(bot, prepared, tc.Arguments, &entry.execCtx); err != nil {
				entry.invalidErr = err
				entry.call.Status = model.BotCallStatusFailed
				entry.call.FinishedAt = &now
				entry.call.ErrorCode = botStrPtr(botErrCodeOf(err))
				entry.call.ErrorMessage = botStrPtr(botErrMessage(err))
				entry.prepared = nil
			}
		}
		if entry.prepared != nil && prepared.ToolName == "fs_thread" && prepared.Action == "wait" {
			// wait 受理端专门校验（INT-160）：定时任务会话内等待上限 15 分钟
			//（schedule run 要等全部异步调用落终态，超长等待会吃掉整个结算
			// 预算），防循环限额（每机器人 3 个、同线程 1 个、每小时 6 次）。
			var waitArgs botThreadArgs
			_ = json.Unmarshal([]byte(prepared.ArgumentsJSON), &waitArgs)
			conds, perr := botWaitParseOn(waitArgs.On)
			timeout, terr := botWaitParseTimeout(waitArgs.TimeoutMinutes)
			if perr == nil {
				perr = terr
			}
			scheduleOver := run.SessionKind == model.BotSessionKindSchedule &&
				waitArgs.TimeoutMinutes > botWaitScheduleMaxMinutes
			switch {
			case perr != nil:
				entry.invalidErr = perr
			case scheduleOver:
				entry.invalidErr = errs.New(errs.ErrInvalidParams.Code,
					fmt.Sprintf("定时任务会话内 timeout_minutes 不得超过 %d（定时任务要等待全部异步调用落终态），请缩短等待或改在主线跟踪",
						botWaitScheduleMaxMinutes))
			default:
				dup, limErr := s.threadWaitAcceptance(bot.ID, waitArgs.EnvID, waitArgs.ThreadID, batch.waitCount)
				if limErr != nil {
					entry.invalidErr = limErr
				} else {
					if dup == "" {
						// 同批判重：同一线程在两个 wait 之间只可能有一个挂上，
						// 后到者立即走 already_waiting。
						if mate, ok := batch.waitThreads[batch.threadKey(waitArgs.EnvID, waitArgs.ThreadID)]; ok {
							dup = mate
						}
					}
					if dup == "" {
						batch.waitThreads[batch.threadKey(waitArgs.EnvID, waitArgs.ThreadID)] = entry.call.ID
						batch.waitCount++
					}
					entry.waitAccepted = true
					entry.waitConds = conds
					entry.waitTimeout = timeout
					entry.waitDup = dup
				}
			}
			if entry.invalidErr != nil {
				entry.call.Status = model.BotCallStatusFailed
				entry.call.FinishedAt = &now
				entry.call.ErrorCode = botStrPtr(botErrCodeOf(entry.invalidErr))
				entry.call.ErrorMessage = botStrPtr(botErrMessage(entry.invalidErr))
				// 受理端同步拒绝（从未进异步路径），账本记回 sync，避免排查时
				// 误以为是异步执行失败。
				entry.call.ExecutionMode = string(model.BotExecutionSync)
				entry.prepared = nil
			}
		}
		if entry.prepared != nil {
			if prepared.RequiresConfirmation {
				// 范围仍要求可解析（目标不明的调用按失败回复模型），两种会话
				// 口径一致。
				if err := botRequireScope(prepared); err != nil {
					entry.invalidErr = err
					entry.call.Status = model.BotCallStatusFailed
					entry.call.FinishedAt = &now
					entry.call.ErrorCode = botStrPtr(botErrCodeOf(err))
					entry.call.ErrorMessage = botStrPtr(botErrMessage(err))
					entry.prepared = nil
				} else {
					// 分级放行（INT-160）：档位由服务端按「动作 + 归一化参数 +
					// 目标线程内存状态」计算，模型不能自证安全。评估结论无论
					// 落在哪条路径（自动执行 / 确认卡 / 定时任务同步拒绝）都
					// 写进 policy_json 审计。同批已受理的「继续」计入无人值守配
					// 额仅限 source=tier1：always-allow 接管的降级候选行同样带
					// auto+rule_id，但那是用户明示授权，不占自动放行额度。
					// 键取 botPolicyThreadTargetOf 口径（send 的 ThreadID 缺省，
					// 回退 ScopeID），直接读 execCtx.ThreadID 会把不同线程的
					// 发送串进同一配额桶。
					batchEnv, batchThread := botPolicyThreadTargetOf(entry.execCtx)
					batchKey := batch.threadKey(batchEnv, batchThread)
					pol := s.evaluateWritePolicy(bot, run, prepared, batch.autoSends[batchKey])
					entry.policy = pol
					entry.call.PolicyJSON = botPolicyRecordJSON(&pol.record)
					if pol.record.Auto && pol.record.Source == "tier1" &&
						pol.record.RuleID == botPolicyRuleSendContinue {
						batch.autoSends[batchKey]++
					}
					switch {
					case pol.denied:
						// 定时任务会话 tier3 同步拒绝（不建卡）：主线重新发起
						// 同一动作才会生成正常确认卡；本轮强制汇报由 run 收尾
						// 按账本里的拒绝计数触发。按受理端同步失败处理（从未
						// 进异步队列，账本记回 sync）。
						entry.invalidErr = errs.New(errs.ErrInvalidParams.Code,
							"需用户确认的动作在定时任务会话中不可执行，请向用户汇报")
						entry.call.Status = model.BotCallStatusFailed
						entry.call.FinishedAt = &now
						entry.call.ErrorCode = botStrPtr(botErrCodeOf(entry.invalidErr))
						entry.call.ErrorMessage = botStrPtr(botErrMessage(entry.invalidErr))
						entry.call.ExecutionMode = string(model.BotExecutionSync)
						entry.prepared = nil
					case pol.authz != nil:
						id := pol.authz.ID
						entry.authzID = &id
					}
				}
			}
		}
		if entry.prepared != nil {
			call := entry.call
			call.ExecutionContextJSON = marshalExecContext(entry.execCtx)
			switch {
			case entry.waitAccepted:
				// wait（INT-160）：受理即 running（不进 queued——ListQueued 会
				// 把 queued 调用推给 worker，而 runThreadWrite 没有 wait 实现，
				// 会被错误认领）。started_at 即挂上时刻，由评估器收尾。
				call.Status = model.BotCallStatusRunning
				call.StartedAt = &now
			case entry.prepared.Mode == model.BotExecutionSync:
				call.Status = model.BotCallStatusQueued
			case entry.authzID != nil:
				call.Status = model.BotCallStatusQueued
				call.AuthorizationID = entry.authzID
			case entry.policy != nil && entry.policy.record.Auto:
				// tier1 策略自动放行（主线与 schedule 会话一致）与 schedule
				// 会话 tier2 默认放行：直接排队。tier1 不建授权记录——策略
				// 放行每次现算，与持久授权的审计语义分开。
				call.Status = model.BotCallStatusQueued
			case run.SessionKind == model.BotSessionKindSchedule:
				// schedule 会话兜底：需确认的写操作默认放行（INT-103），理论
				// 不可达（策略评估已覆盖），保留防回归。
				call.Status = model.BotCallStatusQueued
			default:
				call.Status = model.BotCallStatusPendingConfirmation
			}
		}
		// 账本里的 assistant 调用要原样回放给模型：校验失败的调用保留
		// 原始参数（含坏 JSON/未知参数）——空参数会被校验历史参数的
		// 供应商拒绝，用户消息将永远到不了模型。
		arguments := entry.call.ArgumentsJSON
		if entry.prepared == nil {
			arguments = tc.Arguments
		}
		content.ToolCalls = append(content.ToolCalls, botAssistantToolCall{
			CallID:             entry.call.ID,
			ProviderToolCallID: tc.ID,
			ToolName:           tc.Name,
			Action:             entry.call.Action,
			Arguments:          arguments,
		})
		entries = append(entries, entry)
	}

	calls := make([]*model.BotToolCall, 0, len(entries))
	assistantRaw, _ := json.Marshal(content)

	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		txMsgs := repository.NewBotMessageRepository(tx)
		txCalls := repository.NewBotToolCallRepository(tx)

		// 1. assistant 消息（与回执连续落库，协议块完整；一次写对）。
		seq, err := s.bots.NextMessageSeqTx(tx, bot.ID)
		if err != nil {
			return err
		}
		assistantMsg := &model.BotMessage{
			ID: msgID, BotID: bot.ID, Seq: seq,
			RunID: &run.ID, RequestID: &requestID,
			Kind: model.BotMessageKindAssistant, ContentJSON: string(assistantRaw),
			SessionKind: run.SessionKind,
			CreatedAt:   now,
		}
		if err := txMsgs.AppendTx(tx, assistantMsg); err != nil {
			return err
		}

		// 2. 调用账本行 + 回执 / 失败回复 / 事件。
		for i := range entries {
			entry := &entries[i]
			call := entry.call
			var dupOutcome botToolOutcome
			// 受理端限额的二次复核（INT-160）：受理评估与插入之间有并发窗口
			//（其他请求在同一间隙提交的行，受理评估看不见），同批判重也代替
			// 不了已提交行。在插入事务内以已提交行再裁决一次（同事务早前插入
			// 的同批成员天然可见）：超限的调用按受理端同步失败落 failed+40001，
			// 与既有 invalidErr 路径一致。
			if entry.waitAccepted {
				var waitArgs botThreadArgs
				_ = json.Unmarshal([]byte(entry.call.ArgumentsJSON), &waitArgs)
				verdict := s.recheckWaitAcceptance(tx, bot.ID, waitArgs)
				switch {
				case verdict.dupCallID != "":
					// 同批判重已把后到者链到本批最初的受理者；这里只在批内
					// 没判出重复（并发请求抢先挂上）时改判。
					if entry.waitDup == "" {
						entry.waitDup = verdict.dupCallID
					}
				case verdict.limErr != nil:
					failAcceptedEntry(entry, now, verdict.limErr)
				}
				if entry.waitAccepted && entry.waitDup != "" {
					// 撞重行直落 already_waiting 终态：running wait 的
					// （bot,env,thread）唯一索引不允许第二条 running 行。
					// 终态在事务内写全，提交后只做唤醒（见下方 post-commit）。
					dupOutcome = waitStampDuplicateTerminal(call, entry.waitDup, entry.waitConds, now)
				}
			} else if entry.policy != nil && entry.policy.record.Auto &&
				entry.policy.record.Source == "tier1" &&
				call.Status == model.BotCallStatusQueued {
				// 只复核策略自动放行（source=tier1）：always-allow 是用户明示
				// 的持久授权，不因无人在场额度用尽而翻脸。
				// 规则开关对所有 tier1 自动放行生效：评估与提交之间被关闭时
				// 不再放行这一次——开关是即时生效的用户意图。
				rule, ruleErr := repository.NewBotWritePolicyRuleRepository(tx).GetTx(
					tx, bot.ID, entry.policy.record.RuleID)
				switch {
				case ruleErr != nil && !errors.Is(ruleErr, gorm.ErrRecordNotFound):
					failAcceptedEntry(entry, now, errs.New(errs.ErrInternal.Code,
						"规则开关复核失败，请重新发起该操作"))
				case ruleErr == nil && !rule.Enabled:
					// 语义是策略状态不是参数错误：调用直落终态，模型须
					// 重新发起才会走确认流程——文案如实说明，别让它干等。
					failAcceptedEntry(entry, now, errs.New(errs.ErrPermissionDenied.Code,
						"分级放行规则已关闭，请重新发起该操作（将走确认流程）"))
				case entry.policy.record.RuleID == botPolicyRuleSendContinue:
					// 发送配额只约束「继续」类 send。
					var args botThreadArgs
					_ = json.Unmarshal([]byte(entry.call.ArgumentsJSON), &args)
					if quotaErr := s.recheckSendQuota(tx, bot.ID, args, now); quotaErr != nil {
						failAcceptedEntry(entry, now, quotaErr)
					}
				}
			}
			if err := txCalls.CreateTx(tx, call); err != nil {
				// DB 层兜底判重：running wait 的（bot,env,thread）部分唯一
				// 索引兜住跨进程/漏锁路径写入的第二条 running wait。命中即
				// 改判受理失败（already_waiting 需要胜者 call_id，尽力查出
				// 放进错误消息），改判后以 failed 落行不再撞索引。
				if !entry.waitAccepted || !strings.Contains(err.Error(), "idx_bot_tool_calls_running_wait") {
					return err
				}
				msg := "同一线程已有运行中的 wait（并发受理由唯一索引兜底），请向用户汇报"
				var waitArgs botThreadArgs
				_ = json.Unmarshal([]byte(call.ArgumentsJSON), &waitArgs)
				if existing, ferr := repository.NewBotToolCallRepository(tx).FindRunningWaitByThreadTx(
					tx, bot.ID, waitArgs.EnvID, waitArgs.ThreadID); ferr == nil {
					msg = fmt.Sprintf("同一线程已有运行中的 wait（call_id=%s），请等待其结束或用 fs_cancel 取消后再挂", existing.ID)
				}
				failAcceptedEntry(entry, now, errs.New(errs.ErrInvalidParams.Code, msg))
				if err := txCalls.CreateTx(tx, call); err != nil {
					return err
				}
			}
			calls = append(calls, call)

			// 事件：tool_call 提案，载荷带完整调用投影（参数、执行模式、
			// 确认要求）——确认卡不靠前端猜默认值。
			phase := "proposed"
			if call.Status == model.BotCallStatusPendingConfirmation {
				phase = "awaiting_confirmation"
			} else if call.Status == model.BotCallStatusQueued && call.ExecutionMode == string(model.BotExecutionAsync) {
				phase = "queued"
			} else if call.Status == model.BotCallStatusRunning && call.ExecutionMode == string(model.BotExecutionAsync) {
				// wait（INT-160）：受理即 running。
				phase = "running"
			}
			if err := s.appendEventTx(tx, bot.ID, &run.ID, call.ID, model.BotEventToolCall, botEventEnvelope{
				Kind: "tool_call", Phase: phase,
				Payload: botToolCallResponseOf(call),
			}); err != nil {
				return err
			}

			switch {
			case entry.invalidErr != nil:
				// 协议要求每个 provider_tool_call_id 恰好一条回复：校验失败
				// 也即时补齐失败回复。
				replyContent := botToolReplyContent{
					CallID: call.ID, ProviderToolCallID: entry.providerID,
					ToolName: entry.toolName, Status: string(model.BotCallStatusFailed),
					Content: botToolActionErrorReply(entry.invalidErr),
				}
				if err := s.appendToolReplyTx(tx, bot.ID, run.ID, requestID, call, replyContent, now); err != nil {
					return err
				}
			case call.ExecutionMode == string(model.BotExecutionAsync):
				// 异步回执：accepted=true 表示受理，不代表已执行。
				status := string(model.BotCallStatusPendingConfirmation)
				if call.Status == model.BotCallStatusQueued {
					status = string(model.BotCallStatusQueued)
				} else if call.Status == model.BotCallStatusRunning {
					// wait（INT-160）：受理即 running，回执带触发条件。
					status = string(model.BotCallStatusRunning)
				} else if call.Status.IsTerminal() {
					// 撞重 wait 已在事务内直落终态，回执如实回报终态
					//（already_waiting 在下方字段补语义），不能误报成
					// 待确认。
					status = string(call.Status)
				}
				receipt := map[string]interface{}{
					"accepted": true, "call_id": call.ID, "status": status,
					"tool_name": call.ToolName, "action": call.Action,
					"requires_confirmation": call.RequiresConfirmation,
				}
				if len(entry.waitConds) > 0 {
					receipt["waiting_for"] = entry.waitConds
				}
				if entry.waitDup != "" {
					// 重复挂：线程已有 wait，终态随后立即送达。
					receipt["already_waiting"] = true
					receipt["waiting_call_id"] = entry.waitDup
				}
				receiptRaw, _ := json.Marshal(receipt)
				replyContent := botToolReplyContent{
					CallID: call.ID, ProviderToolCallID: entry.providerID,
					ToolName: call.ToolName, Status: status,
					Content: string(receiptRaw),
				}
				if err := s.appendToolReplyTx(tx, bot.ID, run.ID, requestID, call, replyContent, now); err != nil {
					return err
				}
				if entry.waitDup != "" {
					// 撞重行的 already_waiting 终态消息同事务落库（异步调用
					// 的终态不走 provider 回复位，走 background_result）。
					if err := s.appendBackgroundResultTx(tx, run.ID, call, call.Status, dupOutcome, nil, false, now); err != nil {
						return err
					}
				}
			}
		}

		// 3. message_commit 事件（供其他标签页同步完整消息）。
		if err := s.appendEventTx(tx, bot.ID, &run.ID, "", model.BotEventDelta, botEventEnvelope{
			Kind:      "message_commit",
			MessageID: assistantMsg.ID,
			Payload:   botMessageResponseOf(assistantMsg),
		}); err != nil {
			return err
		}

		// 4. run usage 列（内存累计值落库）。
		usageCols := map[string]interface{}{}
		if run.PromptTokens != nil {
			usageCols["prompt_tokens"] = *run.PromptTokens
		}
		if run.CompletionTokens != nil {
			usageCols["completion_tokens"] = *run.CompletionTokens
		}
		if run.TotalTokens != nil {
			usageCols["total_tokens"] = *run.TotalTokens
		}
		if run.CachedTokens != nil {
			usageCols["cached_tokens"] = *run.CachedTokens
		}
		if run.UsageComplete {
			usageCols["usage_complete"] = true
		}
		if run.CostEstimate != nil {
			usageCols["cost_estimate"] = *run.CostEstimate
			if run.CostSource != nil {
				usageCols["cost_source"] = *run.CostSource
			}
		}
		if len(usageCols) > 0 {
			if _, err := repository.NewBotRunRepository(tx).UpdateColumnsWhereTx(tx, run.ID, nil, usageCols); err != nil {
				return err
			}
		}
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}

	s.publishEvent(bot.ID)

	// 事务提交后才唤醒 worker（回执已可见）；wait 不进 worker 队列——提交后
	// 注册评估现场，重复挂的 already_waiting 终态与终态消息已同事务落库，
	// 这里只补提交后的唤醒。
	//
	// 两趟而非一趟：同响应里 send+wait 指向同一线程时，若先派发 send 再挂
	// wait，快速完成的写会让 wait 基线采到「发送后的 idle」现场，宽限过后
	// 误报 not_started。先把本批全部 wait 挂上（基线在 send 起效前采定），
	// 再派发 queued 写。
	for i := range entries {
		entry := &entries[i]
		call := entry.call
		if entry.waitAccepted && call.Status == model.BotCallStatusRunning {
			s.threadWaitAttach(bot, call, entry.waitConds, entry.waitTimeout)
		}
	}
	for i := range entries {
		entry := &entries[i]
		call := entry.call
		if call.Status == model.BotCallStatusQueued && call.ExecutionMode == string(model.BotExecutionAsync) {
			s.enqueueCallWork(call.ID)
		}
		if entry.waitDup != "" && call.Status.IsTerminal() {
			// 撞重行的终态与终态消息已在事务内落库；补 finalizeCall 本来
			// 在提交后做的唤醒（模型读到 already_waiting 后继续）。
			s.wakeForCallResult(bot.ID, call, string(call.Status))
		}
		// 误判只记「用户」反向操作：机器人自己受理的 settle/unsettle 往返
		//（自动放行、定时任务默认放行、甚至自己发的确认卡待批准前的受理）
		// 都不在这里记——用户批准/拒批/撤销的检测在 DecideCall 路径上
		//（detectPolicyMisjudgeOnDecision）。受理端计数会把正常巡检的
		// settle→unsettle 往返也记成误判（复审 P2 修正）。
	}
	return calls, nil
}

// resolveCallScope 在受理时解析写操作范围并填充执行上下文；读操作不设范围。
func (s *BotService) resolveCallScope(bot *model.Bot, prepared *botPreparedCall, rawArguments string, execCtx *botCallExecContext) error {
	var raw map[string]json.RawMessage
	_ = json.Unmarshal([]byte(rawArguments), &raw)
	execCtx.ScopeKind = prepared.ScopeKind
	execCtx.ScopeID = prepared.ScopeID
	execCtx.Fields = prepared.FieldsConstraint

	switch prepared.ToolName {
	case "fs_project":
		s.resolveProjectScope(bot, prepared, raw)
	case "fs_issue":
		s.resolveIssueScope(bot, prepared, raw)
	case "fs_thread":
		s.resolveThreadScope(prepared, raw)
		threadID, commandID, messageID := buildThreadExecIDs(prepared.Action)
		execCtx.ThreadID, execCtx.CommandID, execCtx.MessageID = threadID, commandID, messageID
		if prepared.EnvironmentID != nil {
			execCtx.EnvID = *prepared.EnvironmentID
		}
	case "fs_schedule":
		if err := s.resolveScheduleScope(bot, prepared, raw); err != nil {
			return err
		}
	}
	// scope 解析可能回填（issue→project），同步回执行上下文。
	execCtx.ScopeKind = prepared.ScopeKind
	execCtx.ScopeID = prepared.ScopeID
	execCtx.Fields = prepared.FieldsConstraint
	execCtx.Param = prepared.ParamConstraint
	execCtx.ExecConfig = prepared.ExecConfigJSON
	execCtx.Schedule = prepared.SchedulePreview
	return nil
}

// botRequireScope 写操作必须有可展示的具体范围，否则建卡失败。
func botRequireScope(prepared *botPreparedCall) error {
	if prepared.ScopeKind == "" || prepared.ScopeID == "" {
		return errs.New(errs.ErrInvalidParams.Code, "无法确定该操作的目标（参数缺失或目标不存在）")
	}
	return nil
}

func marshalExecContext(ctx botCallExecContext) string {
	raw, _ := json.Marshal(ctx)
	return string(raw)
}

// botFailureReplyText 面向模型的失败回复正文。
func botFailureReplyText(err error) string {
	payload, _ := json.Marshal(map[string]interface{}{
		"accepted": false,
		"error":    botErrMessage(err),
	})
	return string(payload)
}

// botErrCodeOf 把 AppError 的业务码折算成稳定错误码字符串。
func botErrCodeOf(err error) string {
	var appErr *errs.AppError
	if errors.As(err, &appErr) {
		return fmt.Sprintf("e%d", appErr.Code)
	}
	return "invalid_arguments"
}

// appendToolReplyTx 追加唯一的 tool 角色回复（dedup terminal/receipt）。
func (s *BotService) appendToolReplyTx(tx *gorm.DB, botID, runID, requestID string, call *model.BotToolCall, content botToolReplyContent, now time.Time) error {
	// 出口围栏按 tool/action 判豁免，reply 正文必须带上动作（构造点分散在
	// 受理、终态、恢复三处，统一在这里补齐）。
	content.Action = call.Action
	txMsgs := repository.NewBotMessageRepository(tx)
	dedup := "receipt:" + call.ID
	if call.Status.IsTerminal() {
		dedup = "terminal:" + call.ID
	}
	if existing, err := txMsgs.FindByDedupKey(botID, dedup); err == nil {
		_ = existing
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return err
	}
	seq, err := s.bots.NextMessageSeqTx(tx, botID)
	if err != nil {
		return err
	}
	msg := &model.BotMessage{
		ID: uuid.NewString(), BotID: botID, Seq: seq,
		RunID: &runID, RequestID: &requestID, CallID: &call.ID,
		Kind: model.BotMessageKindToolReply, ContentJSON: string(raw),
		SessionKind: botRunSessionKindTx(tx, runID),
		DedupKey:    &dedup, CreatedAt: now,
	}
	return txMsgs.AppendTx(tx, msg)
}

// ---------- 同步执行（主线循环内） ----------

// invokeOrAccept 单个调用的执行入口：同步认领执行，异步交给 worker。
func (s *BotService) invokeOrAccept(bot *model.Bot, run *model.BotRun, call *model.BotToolCall, ctx context.Context) {
	switch {
	case call.Status == model.BotCallStatusQueued && call.ExecutionMode == string(model.BotExecutionSync):
		s.executeSyncCall(bot, run.ID, call, ctx)
	case call.Status == model.BotCallStatusQueued && call.ExecutionMode == string(model.BotExecutionAsync):
		s.enqueueCallWork(call.ID)
	}
}

// executeSyncCall 认领并执行同步工具，落终态与协议回复。
func (s *BotService) executeSyncCall(bot *model.Bot, runID string, call *model.BotToolCall, ctx context.Context) {
	now := time.Now().UTC()
	var claimed bool
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		claimed, err = repository.NewBotToolCallRepository(tx).ClaimQueuedTx(tx, call.ID, now)
		return err
	})
	if err != nil || !claimed {
		return // 已被取消或被 worker 认领
	}
	call.Status = model.BotCallStatusRunning
	call.StartedAt = &now

	// 能力查询（fs_*/status）的 available 按本 run 冻结的工具集水位判定——
	// 与会话工具面同一事实源，在这里把 bot 副本的水位透传给执行器；其余动作
	// 走通用分派。
	var content string
	var execErr error
	if call.Action == "status" && (call.ToolName == "fs_schedule" || call.ToolName == "fs_memory") {
		content, execErr = s.execCapabilityStatus(call, bot.ToolsetVersion)
	} else {
		content, execErr = s.botExecuteSyncTool(ctx, call)
	}
	outcome := botBuildOutcomeRaw(call.ID, content, execErr)
	status := model.BotCallStatusSucceeded
	if execErr != nil {
		status = model.BotCallStatusFailed
	}
	s.finalizeCall(bot, runID, call, status, outcome, execErr, false)
}

// closeUnstartedSyncCalls 给尚未认领的同步调用补 not_executed 回复，让模型
// 重新决定；已受理的后台调用保持原状态。
func (s *BotService) closeUnstartedSyncCalls(calls []*model.BotToolCall, reason string) {
	for _, call := range calls {
		if call.Status != model.BotCallStatusQueued || call.ExecutionMode != string(model.BotExecutionSync) {
			continue
		}
		runID := s.runIDOfCall(call)
		now := time.Now().UTC()
		// 状态转移、协议回复与事件同一事务：崩溃不会留下无回复的终态行
		//（恢复只修 queued 同步调用，缺口将永久阻塞上下文构建）。
		txErr := s.db.Transaction(func(tx *gorm.DB) error {
			ok, err := repository.NewBotToolCallRepository(tx).UpdateWhereTx(tx, call.ID,
				string(model.BotCallStatusQueued), map[string]interface{}{
					"status":        model.BotCallStatusNotExecuted,
					"finished_at":   now,
					"error_code":    "not_executed",
					"error_message": reason,
				})
			if err != nil || !ok {
				return err
			}
			call.Status = model.BotCallStatusNotExecuted
			reply := botToolReplyContent{
				CallID: call.ID, ProviderToolCallID: call.ProviderToolCallID,
				ToolName: call.ToolName, Status: string(model.BotCallStatusNotExecuted),
				Content: botNotExecutedReplyText(reason),
			}
			if err := s.appendToolReplyTx(tx, call.BotID, runID, call.RequestID, call, reply, now); err != nil {
				return err
			}
			return s.appendEventTx(tx, call.BotID, runIDPtr(runID), call.ID, model.BotEventToolResult, botEventEnvelope{
				Kind: "tool_result", Phase: "terminal",
				Payload: botToolCallResponseOf(call),
			})
		})
		if txErr != nil {
			s.logger.Warn("补 not_executed 终态失败", zap.String("call_id", call.ID), zap.Error(txErr))
			continue
		}
	}
}

// closeUnstartedWriteCalls 给尚未认领的写操作补 not_executed：读类调用
// （botCallIsReadOnly）保留给调用方继续执行，不因新输入被丢弃。同步写走
// 协议回复，已受理但尚未被 worker 认领的异步写走后台结果——两者都要闭合，
// 否则排队中的写会在用户改了主意之后仍然落地。
func (s *BotService) closeUnstartedWriteCalls(calls []*model.BotToolCall, reason string) {
	syncWrites := make([]*model.BotToolCall, 0, len(calls))
	for _, call := range calls {
		if botCallIsReadOnly(call) {
			continue
		}
		if call.ExecutionMode == string(model.BotExecutionAsync) {
			s.closeUnstartedAsyncCall(call, reason)
			continue
		}
		syncWrites = append(syncWrites, call)
	}
	s.closeUnstartedSyncCalls(syncWrites, reason)
}

// closeUnstartedAsyncCall 把 queued 的异步写原子地置为 not_executed 并补后台
// 结果；已被 worker 认领（running）的调用 CAS 失败即跳过——副作用可能已经
// 发生，不能在这里逆转，交给模型按后台结果如实汇报。
func (s *BotService) closeUnstartedAsyncCall(call *model.BotToolCall, reason string) {
	if call.Status != model.BotCallStatusQueued {
		return
	}
	runID := s.runIDOfCall(call)
	now := time.Now().UTC()
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		ok, err := repository.NewBotToolCallRepository(tx).UpdateWhereTx(tx, call.ID,
			string(model.BotCallStatusQueued), map[string]interface{}{
				"status":        model.BotCallStatusNotExecuted,
				"finished_at":   now,
				"error_code":    "not_executed",
				"error_message": reason,
			})
		if err != nil || !ok {
			return err
		}
		call.Status = model.BotCallStatusNotExecuted
		return s.appendBackgroundResultMsgTx(tx, runID, call, model.BotCallStatusNotExecuted,
			botToolOutcome{}, "not_executed", reason, false, now)
	})
	if txErr != nil {
		s.logger.Warn("补异步 not_executed 终态失败", zap.String("call_id", call.ID), zap.Error(txErr))
		return
	}
	s.publishEvent(call.BotID)
	s.wakeForCallResult(call.BotID, call, string(model.BotCallStatusNotExecuted))
}

// botCallIsReadOnly 判断一个调用是否是纯读取动作：只有同步执行且命中读类
// 动作表才算。同步执行的写动作（fs_memory 写、fs_schedule 管理）不是读。
func botCallIsReadOnly(call *model.BotToolCall) bool {
	if call.ExecutionMode != string(model.BotExecutionSync) {
		return false
	}
	if botReadOnlyActions[call.ToolName+"/"+call.Action] {
		return true
	}
	// fs_schedule/run 的 dry_run=true 是只读预览（不建运行、不执行），参数决定
	// 语义：新输入到达时它应当执行完，而不是被标成 not_executed。
	if call.ToolName == "fs_schedule" && call.Action == "run" {
		var args botScheduleTaskArgs
		if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err == nil && args.DryRunBool() {
			return true
		}
	}
	return false
}

// botNotExecutedReplyText 是「调用未执行」的模型回复：与参数校验失败（40001）
// 区分开，用独立的 error_code 标明原因（新输入 / 停止 / 服务重启）。
func botNotExecutedReplyText(reason string) string {
	payload, _ := json.Marshal(map[string]interface{}{
		"accepted":   false,
		"error_code": "not_executed",
		"reason":     reason,
		"error":      "调用未执行（" + reason + "）：请基于新输入重新决定是否再次调用",
	})
	return string(payload)
}

// runIDOfCall 从请求记录反查 run（事务外调用）。
func (s *BotService) runIDOfCall(call *model.BotToolCall) string {
	req, err := s.requests.FindByID(call.RequestID)
	if err != nil {
		return ""
	}
	return req.RunID
}

// ---------- 后台 worker ----------

func (s *BotService) startWorkers() {
	s.workCh = make(chan string, 256)
	s.callCancels = map[string]context.CancelFunc{}
	for i := 0; i < botWorkerCount; i++ {
		go s.workerLoop()
	}
}

func (s *BotService) workerLoop() {
	for callID := range s.workCh {
		s.executeQueuedCall(callID)
	}
}

func (s *BotService) enqueueCallWork(callID string) {
	s.ensureWorkers()
	select {
	case s.workCh <- callID:
	default:
		// 队列满：扫描循环会补推（丢失推送不丢失任务，数据库是事实来源）。
	}
}

func (s *BotService) ensureWorkers() {
	s.workOnce.Do(func() { s.startWorkers() })
}

// scanQueuedCalls 推送排队的异步调用；重复推送无害（CAS 认领挡并发）。
func (s *BotService) scanQueuedCalls() {
	calls, err := s.calls.ListQueued(64)
	if err != nil {
		return
	}
	for _, call := range calls {
		if call.ExecutionMode == string(model.BotExecutionAsync) {
			s.enqueueCallWork(call.ID)
		}
	}
}

// executeQueuedCall 认领前重新核对归属与授权；执行、落终态、追加结果消息
// 并唤醒主线。
func (s *BotService) executeQueuedCall(callID string) {
	call, err := s.calls.FindByID(callID)
	if err != nil || call.Status != model.BotCallStatusQueued {
		return
	}
	bot, err := s.bots.Get(call.BotID)
	if err != nil {
		return
	}

	// 撤销/过期对尚未认领的授权调用生效：授权失效则回到待确认（快速路径）。
	if call.AuthorizationID != nil {
		authz, authzErr := s.authz.FindByID(*call.AuthorizationID)
		if authzErr != nil || authz.RevokedAt != nil || authz.BotID != bot.ID ||
			botAuthorizationExpired(authz, time.Now().UTC()) {
			s.revertQueuedCall(call)
			return
		}
	}

	// 认领与授权复核在同一事务内：撤销与认领的竞速由事务串行化裁决，
	// 已撤销的授权不能再把调用推进到 running。
	now := time.Now().UTC()
	var claimed, authzRevoked bool
	claimErr := s.db.Transaction(func(tx *gorm.DB) error {
		claimed, authzRevoked = false, false
		if call.AuthorizationID != nil {
			authz, err := repository.NewBotAuthorizationRepository(tx).FindByID(*call.AuthorizationID)
			if err != nil {
				return err
			}
			if authz.RevokedAt != nil || authz.BotID != bot.ID || botAuthorizationExpired(authz, now) {
				authzRevoked = true
				return nil
			}
		}
		var err error
		claimed, err = repository.NewBotToolCallRepository(tx).ClaimQueuedTx(tx, callID, now)
		return err
	})
	if claimErr != nil {
		return
	}
	if authzRevoked || !claimed {
		if authzRevoked {
			s.revertQueuedCall(call)
		}
		return
	}
	call.Status = model.BotCallStatusRunning
	call.StartedAt = &now
	// 事件带 run：会话判别由此推导，schedule 会话的执行事件不进聊天页。
	s.emitEvent(bot.ID, runIDPtr(s.runIDOfCall(call)), call.ID, model.BotEventToolCall, botEventEnvelope{
		Kind: "tool_call", Phase: "running",
		Payload: botToolCallResponseOf(call),
	})

	ctx, cancel := context.WithTimeout(context.Background(), botAsyncExecTimeout)
	s.registerCallCancel(callID, cancel)
	defer func() {
		cancel()
		s.unregisterCallCancel(callID)
	}()
	// 注册后再核对一次取消标记：claim 与注册之间的窗口里 cancelBotCall
	// 可能已记录标记（此时 signalCallCancel 还没有可取消的 handler），
	// 直接以取消语义执行，不再发起外部写入。
	if s.abortIfCancelRequested(bot, call) {
		return
	}

	// 分级放行执行前复检（INT-160）：降级确认卡带过期时间，且「判断时满足、
	// 执行时已变」的窗口由这里重评前置条件收口。复检失败落 failed +
	// policy_recheck_failed，不发起外部写入（无副作用）。
	if recheckErr := s.policyRecheckCall(bot, call); recheckErr != nil {
		s.finalizeCall(bot, s.runIDOfCall(call), call, model.BotCallStatusFailed,
			botBuildOutcomeRaw(call.ID, "", recheckErr), recheckErr, false)
		return
	}

	output, execErr := s.runAsyncTool(ctx, call)

	status := model.BotCallStatusSucceeded
	if execErr != nil {
		status = model.BotCallStatusFailed
	}
	// 取消标记的终态裁决统一在 finalizeCall 内最后一次核对。
	outcome := botBuildOutcome(call.ID, output, execErr)
	s.finalizeCall(bot, s.runIDOfCall(call), call, status, outcome, execErr, false)
}

// runAsyncTool 分派异步写操作到对应适配器。
func (s *BotService) runAsyncTool(ctx context.Context, call *model.BotToolCall) (interface{}, error) {
	switch call.ToolName {
	case "fs_project":
		return s.runProjectWrite(ctx, call)
	case "fs_issue":
		return s.runIssueWrite(ctx, call)
	case "fs_thread":
		return s.runThreadWrite(ctx, call)
	case "fs_schedule":
		// INT-136：create/update 敏感字段/run 在确认后由 worker 执行。
		return s.runScheduleWrite(ctx, call)
	}
	return nil, errs.New(errs.ErrInvalidParams.Code, "工具没有异步实现: "+call.ToolName)
}

// abortIfCancelRequested 核对持久化的取消标记；已受理的取消直接落
// cancelled 终态（副作用标记为 true：注册前外部写入可能已发生），返回
// 是否应中止执行。
func (s *BotService) abortIfCancelRequested(bot *model.Bot, call *model.BotToolCall) bool {
	fresh, err := s.calls.FindByID(call.ID)
	if err == nil && fresh.CancelRequestedAt != nil {
		s.finalizeCall(bot, s.runIDOfCall(call), call, model.BotCallStatusCancelled,
			botToolOutcome{ResultJSON: "{}", PreviewJSON: "{}", ResultChars: 2}, nil, true)
		return true
	}
	return false
}

// cancelRaceCols 构造取消竞速下的终态列：取消标记在执行与落定之间落上时，
// 成功/失败结论不得以原状态送达。复检失败发生在任何外部写入之前，effect
// false；wait（INT-160）无任何副作用，取消也不例外，结果体覆写为
// outcome=cancelled；其余调用 effect 恒 true（保守记「可能已写出」），
// 结果保持原样。
func cancelRaceCols(call *model.BotToolCall, outcome botToolOutcome, execErr error, now time.Time) (model.BotToolCallStatus, botToolOutcome, bool, map[string]interface{}) {
	// 缺省按「可能已写出」处理：取消与已派发写入竞速时，适配器报错
	//（context.Canceled/超时/丢响应）不代表远端没应用——保守留痕让模型
	// 先核实再决定是否重试，避免重复写。只有确定发生在写入之前的失败
	//（执行前复检）与无副作用的 wait 才清掉该标记。
	effect := true
	if isBotPolicyRecheckError(execErr) {
		effect = false
	}
	status := model.BotCallStatusCancelled
	if call.ToolName == "fs_thread" && call.Action == "wait" {
		effect = false
		// 取消竞速落到本分支时 waitResolveCancelled 的富结果 CAS 不上，
		// 这里用账本行重建同构 payload（waiting_for 从 args.on 归一化，
		// attached_at 取受理时刻），不留最小体丢上下文。
		payload := map[string]interface{}{
			"outcome":     "cancelled",
			"resolved_at": rfc3339(&now),
		}
		var wargs botThreadArgs
		_ = json.Unmarshal([]byte(call.ArgumentsJSON), &wargs)
		if conds, cerr := botWaitParseOn(wargs.On); cerr == nil {
			payload["waiting_for"] = conds
		}
		attachedAt := call.StartedAt
		if attachedAt == nil {
			attachedAt = &call.CreatedAt
		}
		payload["attached_at"] = rfc3339(attachedAt)
		cancelRaw := marshalJSONString(payload)
		outcome = botToolOutcome{
			ResultJSON:  cancelRaw,
			PreviewJSON: cancelRaw,
			ResultChars: int64(len([]rune(cancelRaw))),
			WaitResult:  json.RawMessage(cancelRaw),
		}
	}
	cols := map[string]interface{}{
		"status":                   status,
		"finished_at":              now,
		"result_json":              outcome.ResultJSON,
		"result_preview_json":      outcome.PreviewJSON,
		"result_chars":             outcome.ResultChars,
		"result_items":             outcome.ResultItems,
		"truncated":                outcome.Truncated,
		"effect_may_have_occurred": effect,
	}
	if execErr != nil {
		cols["error_code"] = botCallErrorCodeOf(execErr)
		cols["error_message"] = botErrMessage(execErr)
	}
	return status, outcome, effect, cols
}

// finalizeCall 原子落终态、结果、background_result 消息与事件；CAS 失败说明
// 终态已被别人落定（取消竞速），迟到返回不覆盖。
func (s *BotService) finalizeCall(bot *model.Bot, runID string, call *model.BotToolCall, status model.BotToolCallStatus, outcome botToolOutcome, execErr error, effect bool) {
	now := time.Now().UTC()
	cols := map[string]interface{}{
		"status":                   status,
		"finished_at":              now,
		"result_json":              outcome.ResultJSON,
		"result_preview_json":      outcome.PreviewJSON,
		"result_chars":             outcome.ResultChars,
		"result_items":             outcome.ResultItems,
		"truncated":                outcome.Truncated,
		"effect_may_have_occurred": effect,
	}
	if execErr != nil {
		// 分级放行执行前复检失败用专用错误码而不是折算业务码，账本检索与
		// 后台结果消息按同一口径识别。
		cols["error_code"] = botCallErrorCodeOf(execErr)
		cols["error_message"] = botErrMessage(execErr)
	}
	var ok bool
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		repo := repository.NewBotToolCallRepository(tx)
		// 取消竞态（INT-160 复审）：CAS 条件带 cancel_requested_at IS NULL。
		// 取消标记在读取与写入之间落上的窗口里，成功终态不再覆盖取消——
		// 0 行时重读该行裁决：有取消标记改走 cancelled 收尾，否则终态已被
		// 取消/恢复路径落定，迟到返回不覆盖。
		var err error
		ok, err = repo.UpdateWhereRunningFreshTx(tx, call.ID, cols)
		if err != nil {
			return err
		}
		if !ok {
			fresh, ferr := repo.FindByID(call.ID)
			if ferr != nil {
				return ferr
			}
			if fresh.Status != model.BotCallStatusRunning || fresh.CancelRequestedAt == nil {
				return nil
			}
			status, outcome, effect, cols = cancelRaceCols(call, outcome, execErr, now)
			ok, err = repo.UpdateWhereTx(tx, call.ID,
				string(model.BotCallStatusRunning), cols)
			if err != nil || !ok {
				return err
			}
		}
		if call.ExecutionMode != string(model.BotExecutionAsync) {
			// 同步工具：协议回复就在本事务内补齐（每个 provider_tool_call_id
			// 恰好一条 tool 消息）；不追加第二条结果消息。tool_result 事件
			// 同事务落库：其他标签页的卡片不靠重读也能翻到终态。
			call.Status = status
			content := outcome.PreviewJSON
			if execErr != nil {
				content = botFailureReplyText(execErr)
			}
			reply := botToolReplyContent{
				CallID: call.ID, ProviderToolCallID: call.ProviderToolCallID,
				ToolName: call.ToolName, Status: string(status),
				Content: content,
			}
			if err := s.appendToolReplyTx(tx, call.BotID, runID, call.RequestID, call, reply, now); err != nil {
				return err
			}
			return s.appendEventTx(tx, call.BotID, runIDPtr(runID), call.ID, model.BotEventToolResult, botEventEnvelope{
				Kind: "tool_result", Phase: "terminal",
				Payload: botToolCallResponseOf(call),
			})
		}
		// 平台后台结果只属于异步调用（完成事件不复用 provider_tool_call_id）。
		return s.appendBackgroundResultTx(tx, runID, call, status, outcome, execErr, effect, now)
	})
	if txErr != nil {
		s.logger.Warn("落调用终态失败", zap.String("call_id", call.ID), zap.Error(txErr))
		return
	}
	if !ok {
		return // 终态已被取消路径落定
	}
	s.publishEvent(bot.ID)
	// 新结果唤醒现有 run，或触发新的 tool_result run；schedule 会话的调用
	// 唤醒对应执行槽（不进主线）。
	s.wakeForCallResult(bot.ID, call, string(status))
}

// wakeForCallResult 后台结果唤醒所属会话：主线调用唤醒主线（现有 run 继续
// 或新建 tool_result run），schedule 会话的调用唤醒对应执行槽。
func (s *BotService) wakeForCallResult(botID string, call *model.BotToolCall, status string) {
	runID := s.runIDOfCall(call)
	if runID != "" && s.runs.SessionKindOf(runID) == model.BotSessionKindSchedule {
		s.wakeScheduleRun(botID, runID)
		return
	}
	s.wakeRuntime(botID, botWake{
		Trigger: model.BotTriggerToolResult,
		Key:     "tool_result:" + call.ID,
		Context: marshalJSONString(map[string]interface{}{"call_id": call.ID, "status": status}),
	})
}

// botResultSummary 从完整结果中截取一行摘要供后台结果消息展示。
func botResultSummary(resultJSON string) string {
	runes := []rune(resultJSON)
	if len(runes) > 200 {
		runes = runes[:200]
	}
	return string(runes)
}

func runIDPtr(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}

func marshalJSONString(v interface{}) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// ---------- 取消 ----------

func (s *BotService) registerCallCancel(callID string, cancel context.CancelFunc) {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	if s.callCancels == nil {
		s.callCancels = map[string]context.CancelFunc{}
	}
	s.callCancels[callID] = cancel
}

func (s *BotService) unregisterCallCancel(callID string) {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	delete(s.callCancels, callID)
}

func (s *BotService) signalCallCancel(callID string) {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	if cancel, ok := s.callCancels[callID]; ok {
		cancel()
	}
}

// cancelBotCall 取消本机器人的后台调用：待确认/queued 直接 CAS 终结；
// running 记录取消并取消其 context（worker 落唯一终态）。返回最新调用与
// 是否发生了取消动作。
func (s *BotService) cancelBotCall(call *model.BotToolCall) (*model.BotToolCall, bool) {
	now := time.Now().UTC()
	switch call.Status {
	case model.BotCallStatusPendingConfirmation, model.BotCallStatusQueued:
		// 终态转移、结果消息与事件同一事务：崩溃不会留下无结果消息的
		// cancelled 终态（恢复只修未终态调用，缺口会让机器人永远不知道
		// 已受理的操作被取消）。
		runID := s.runIDOfCall(call)
		lostRace := false
		var txErr error
		txErr = s.db.Transaction(func(tx *gorm.DB) error {
			ok, err := repository.NewBotToolCallRepository(tx).UpdateWhereTx(tx, call.ID,
				string(call.Status), map[string]interface{}{
					"status":        model.BotCallStatusCancelled,
					"finished_at":   now,
					"error_code":    "cancelled",
					"error_message": "被用户或机器人取消（未执行）",
				})
			if err != nil {
				return err
			}
			if !ok {
				// 初读与 CAS 之间被 worker 认领：取消没有发生，也不该
				// 报成功——重读最新状态交给调用方（对 running 会再走
				// 记标记路径）。
				lostRace = true
				return nil
			}
			call.Status = model.BotCallStatusCancelled
			if call.ExecutionMode == string(model.BotExecutionSync) {
				// 同步调用：协议回复必须闭合 assistant 已声明的 tool 块
				// （每个 provider_tool_call_id 恰好一条 tool 消息），否则
				// 后续 buildRunContext 全部失败。
				reply := botToolReplyContent{
					CallID: call.ID, ProviderToolCallID: call.ProviderToolCallID,
					ToolName: call.ToolName, Status: string(model.BotCallStatusCancelled),
					Content: botFailureReplyText(errs.New(errs.ErrInvalidParams.Code, "调用在排队时被取消，未执行")),
				}
				if err := s.appendToolReplyTx(tx, call.BotID, runID, call.RequestID, call, reply, now); err != nil {
					return err
				}
				return s.appendEventTx(tx, call.BotID, runIDPtr(runID), call.ID, model.BotEventToolResult, botEventEnvelope{
					Kind: "tool_result", Phase: "terminal",
					Payload: botToolCallResponseOf(call),
				})
			}
			// 终态结果消息（唯一 terminal，仅异步调用）。
			return s.appendBackgroundResultTx(tx, runID, call, model.BotCallStatusCancelled, botToolOutcome{}, nil, false, now)
		})
		if txErr != nil || lostRace {
			if txErr != nil {
				s.logger.Warn("落取消终态失败", zap.String("call_id", call.ID), zap.Error(txErr))
			}
			fresh, _ := s.calls.FindByID(call.ID)
			return fresh, false
		}
		s.publishEvent(call.BotID)
		// 取消也是模型输入（后台结果）：立即唤醒所属会话，不等 5 秒扫描兜底。
		s.wakeForCallResult(call.BotID, call, string(model.BotCallStatusCancelled))
		fresh, _ := s.calls.FindByID(call.ID)
		return fresh, true
	case model.BotCallStatusRunning:
		updated, err := s.calls.UpdateWhere(call.ID, string(model.BotCallStatusRunning), map[string]interface{}{
			"cancel_requested_at": now,
		})
		if err != nil {
			fresh, _ := s.calls.FindByID(call.ID)
			return fresh, false
		}
		if !updated {
			// 0 行：调用已在初读与写入之间落终态（或取消标记已被并发请求
			// 记上）。如实报告未能取消，不谎报成功。
			fresh, _ := s.calls.FindByID(call.ID)
			return fresh, false
		}
		s.signalCallCancel(call.ID)
		fresh, _ := s.calls.FindByID(call.ID)
		return fresh, true
	default:
		return call, false
	}
}

// appendBackgroundResultTx 在事务内追加平台后台结果消息与事件（幂等）。
func (s *BotService) appendBackgroundResultTx(tx *gorm.DB, runID string, call *model.BotToolCall, status model.BotToolCallStatus, outcome botToolOutcome, execErr error, effect bool, now time.Time) error {
	code, message := "", ""
	if execErr != nil {
		// 与账本 error_code 同一口径：复检失败的专用码不折算成业务码，
		// 否则后台结果消息与账本行对不上。
		code, message = botCallErrorCodeOf(execErr), botErrMessage(execErr)
	}
	return s.appendBackgroundResultMsgTx(tx, runID, call, status, outcome, code, message, effect, now)
}

// appendBackgroundResultMsgTx 是显式错误码的版本：终态原因不总能用一个
// error 表达（如平台侧未执行 not_executed），此时直接给错误码与说明。
func (s *BotService) appendBackgroundResultMsgTx(tx *gorm.DB, runID string, call *model.BotToolCall, status model.BotToolCallStatus, outcome botToolOutcome, errorCode, errorMessage string, effect bool, now time.Time) error {
	result := botBackgroundResultContent{
		CallID: call.ID, ToolName: call.ToolName, Action: call.Action,
		Status: string(status), EffectMayHaveOccurred: effect,
	}
	if errorCode != "" || errorMessage != "" {
		result.ErrorCode = errorCode
		result.ErrorMessage = errorMessage
	}
	if outcome.ResultJSON != "" {
		result.Summary = botResultSummary(outcome.ResultJSON)
	} else {
		result.Summary = botResultSummary("{}")
	}
	if call.PolicyJSON != "" {
		// 分级放行（INT-160）：评估结论随后台结果进模型上下文与前端渲染
		//（自动执行的调用在聊天流里也能看到「自动执行」标记）。
		result.Policy = json.RawMessage(call.PolicyJSON)
	}
	if len(outcome.WaitResult) > 0 {
		// wait（INT-160）：完整结构化收尾对象随唤醒消息进入模型上下文。
		result.WaitResult = outcome.WaitResult
	}
	raw, _ := json.Marshal(result)
	seq, seqErr := s.bots.NextMessageSeqTx(tx, call.BotID)
	if seqErr != nil {
		return seqErr
	}
	dedup := "terminal:" + call.ID
	msg := &model.BotMessage{
		ID: uuid.NewString(), BotID: call.BotID, Seq: seq,
		RunID: runIDPtr(runID), CallID: &call.ID,
		Kind: model.BotMessageKindBackgroundResult, ContentJSON: string(raw),
		SessionKind: botRunSessionKindTx(tx, runID),
		DedupKey:    &dedup, CreatedAt: now,
	}
	if err := repository.NewBotMessageRepository(tx).AppendTx(tx, msg); err != nil {
		// 幂等：恢复/重放已追加过同样结果时跳过。
		if existing, findErr := repository.NewBotMessageRepository(tx).FindByDedupKey(call.BotID, dedup); findErr == nil {
			_ = existing
			return nil
		}
		return err
	}
	return s.appendEventTx(tx, call.BotID, runIDPtr(runID), call.ID, model.BotEventToolResult, botEventEnvelope{
		Kind: "tool_result", Phase: "terminal",
		Payload: map[string]interface{}{
			"status": string(status), "message_id": msg.ID,
			"tool_name": call.ToolName, "action": call.Action,
			"effect_may_have_occurred": effect,
			// 平台后台结果的完整消息投影：订阅端不靠快照重读也能渲染。
			"message": botMessageResponseOf(msg),
		},
	})
}

// revertQueuedCall 授权失效时把排队调用退回待确认；事件带完整调用投影，
// 订阅端合并后立即恢复审批控件，不靠重读。
func (s *BotService) revertQueuedCall(call *model.BotToolCall) {
	ok, err := s.calls.UpdateWhere(call.ID, string(model.BotCallStatusQueued), map[string]interface{}{
		"status":           model.BotCallStatusPendingConfirmation,
		"authorization_id": nil,
	})
	if err != nil || !ok {
		return
	}
	call.Status = model.BotCallStatusPendingConfirmation
	call.AuthorizationID = nil
	s.emitEvent(call.BotID, nil, call.ID, model.BotEventToolCall, botEventEnvelope{
		Kind: "tool_call", Phase: "awaiting_confirmation",
		Payload: botToolCallResponseOf(call),
	})
}
