package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 核心工具循环：只负责顺序（安全点 → 构建请求 → 流式 → 提交 → 执行/受理 →
// 收尾），不负责授权细节、数据库细节与 SSE 编码。循环体不追逻辑大全，
// 目标本文件保持接近 200 行的规模。

// botWake 是一次主线唤醒的触发描述。
type botWake struct {
	Trigger model.BotRunTrigger
	Key     string
	Context string
}

type botSafePoint struct {
	StopRequested bool
	HasNewInput   bool
}

// runBotSlot 之后的入口：确保 run 存在后进入循环。
func (s *BotService) runBot(botID string, wake botWake, ctx context.Context) {
	bot, err := s.bots.Get(botID)
	if err != nil {
		return
	}
	run, ok, err := s.ensureRun(bot, wake)
	if err != nil || !ok {
		return
	}
	s.botLoop(bot, run, ctx)
}

// botRunFreeze 是一次主线 run 期间冻结的配置：人设/模型/地址/密钥/工具集
// 版本对当前 run 生效，只在压缩或进程重启边界接收允许变化的字段。工具集
// 版本一并入冻：主线 run 的工具集在入口激活时钉死，只有压缩边界
// （acceptCompactionBoundary）会换，并发的 schedule 激活不得在 run 中途
// 静默换掉主线工具集。
type botRunFreeze struct {
	Model                 string
	BaseURL               string
	APIKeyEncrypted       []byte
	ProviderID            string
	SystemPromptText      string
	PromptRevision        int64
	PlatformPromptVersion int64
	PersonaPrompt         string
	ToolsetVersion        int64
}

func captureRunFreeze(bot *model.Bot, run *model.BotRun) botRunFreeze {
	// schedule run 用派发时冻结的密钥密文（与快照里的 base_url 成对）；
	// 主线在 run 开始时读一次当前密钥。
	key := append([]byte(nil), bot.APIKeyEncrypted...)
	if run.SessionKind == model.BotSessionKindSchedule && len(run.APIKeyEncryptedSnapshot) > 0 {
		key = append([]byte(nil), run.APIKeyEncryptedSnapshot...)
	}
	return botRunFreeze{
		Model:                 runModelOf(run, bot),
		BaseURL:               runBaseURLOf(run, bot),
		APIKeyEncrypted:       key,
		ProviderID:            runProviderIDOf(run, bot),
		SystemPromptText:      bot.SystemPromptText,
		PromptRevision:        bot.PromptRevision,
		PlatformPromptVersion: bot.PlatformPromptVersion,
		PersonaPrompt:         bot.PersonaPrompt,
		ToolsetVersion:        bot.ToolsetVersion,
	}
}

func applyRunCredentialFreeze(bot *model.Bot, freeze botRunFreeze) {
	bot.Model = freeze.Model
	bot.BaseURL = freeze.BaseURL
	bot.APIKeyEncrypted = freeze.APIKeyEncrypted
	bot.ProviderID = freeze.ProviderID
}

func applyRunPromptFreeze(bot *model.Bot, freeze botRunFreeze) {
	bot.SystemPromptText = freeze.SystemPromptText
	bot.PromptRevision = freeze.PromptRevision
	bot.PlatformPromptVersion = freeze.PlatformPromptVersion
	bot.PersonaPrompt = freeze.PersonaPrompt
	bot.ToolsetVersion = freeze.ToolsetVersion
}

func (s *BotService) acceptCompactionBoundary(bot *model.Bot, freeze *botRunFreeze, snap *botBoundarySnapshot) {
	if snap == nil {
		return
	}
	s.installBoundarySnapshot(bot, snap)
	if fresh, err := s.bots.Get(bot.ID); err == nil {
		bot.LastToldVersion = fresh.LastToldVersion
		bot.OfferedInputSeq = fresh.OfferedInputSeq
	}
	freeze.SystemPromptText = snap.SystemPromptText
	freeze.PromptRevision = snap.PromptRevision
	freeze.PlatformPromptVersion = snap.PlatformPromptVersion
	freeze.PersonaPrompt = snap.PersonaPrompt
	freeze.ToolsetVersion = snap.ToolsetVersion
	applyRunCredentialFreeze(bot, *freeze)
}

// botLoop 单 run 主循环。主线与定时任务（INT-103）共用同一骨架：差异只有
// 上下文构建、压缩（schedule 跳过）、记忆基线与收尾分支；重启边界的版本
// 激活两种会话都在入口执行。
func (s *BotService) botLoop(bot *model.Bot, run *model.BotRun, ctx context.Context) {
	// 槽绑定先于一切收尾路径：硬停止（宽限到期 / 重复点击）取消前核对槽上
	// 绑定的 run，槽已被重入换绑成新 run 时不得取消到别人的 context（INT-137）。
	s.bindSlotRun(bot, run)
	// 工具集/提示词激活先于冻结执行，主线与 schedule 一视同仁：激活在同一个
	// 短事务里读行、判 stale、按该行人设重建提示词并写回，事务结束后副本与
	// 库内行一致，freeze 捕获的才是本次 run 的最终提示词，schedule 会话也基于
	// 激活后的工具集构建。激活失败只降级记日志、不阻塞 run。
	if err := s.activateBotPrompt(bot); err != nil {
		s.logger.Warn("激活工具集失败", zap.String("bot_id", bot.ID), zap.Error(err))
	}
	// 先冻结再取密钥：schedule run 的地址与密钥是派发时成对冻结的，解密必须用
	// 冻结的那一份，否则轮换密钥后会把新密钥发往旧地址。
	freeze := captureRunFreeze(bot, run)
	applyRunCredentialFreeze(bot, freeze)
	applyRunPromptFreeze(bot, freeze)
	key, err := s.decryptAPIKey(bot)
	if err != nil {
		s.finishFailed(bot, run, "key_decrypt_failed", err)
		return
	}
	client := botNewClient(freeze.BaseURL, key)
	streamClient := client

	// schedule 会话冻结物：任务快照、run 开始时的工具集与记忆注入。
	var sched *botScheduleSession
	if run.SessionKind == model.BotSessionKindSchedule {
		sched, err = s.newScheduleSession(bot, run)
		if err != nil {
			s.finishFailed(bot, run, "schedule_task_missing", err)
			return
		}
	}

	var calls []*model.BotToolCall
	contextLimitRetried := false
	// thresholdCompactionFailed 是阈值压缩的 run 内一次性守卫（F8）：阈值
	// 压缩软失败（未发布且估算仍低于窗口）后，本轮不再重试——每轮工具迭代
	// 都会重新越过触发阈值，不设防会把确定性失败放大成每轮一次归档+摘要。
	// 上下文极限后的强制压缩不受此守卫影响（那是唯一合法的重试）。
	thresholdCompactionFailed := false
runLoop:
	for {
		if input := s.safePoint(run); input.StopRequested {
			s.closeUnstartedSyncCalls(calls, "stop_requested")
			s.finishStopped(bot, run)
			return
		}

		// schedule 会话先等自己的异步写结算：调用结果（可能失败）是本次运行
		// 结果的一部分，未结算就结束会谎报成功且丢掉真实状态。等待期间不发
		// 模型请求，结算后重建上下文，模型就能据实汇报。
		if sched != nil {
			switch s.waitScheduleCallsSettled(ctx, run) {
			case scheduleWaitStopped:
				s.closeUnstartedSyncCalls(calls, "stop_requested")
				s.finishStopped(bot, run)
				return
			case scheduleWaitTimeout:
				s.finishFailed(bot, run, "async_calls_unsettled",
					errs.New(errs.ErrInternal.Code, "定时任务运行内仍有未结算的后台调用"))
				return
			}
		}

		if sched == nil {
			// 激活在 captureRunFreeze 之前完成，run 期间只有压缩边界会换前缀。
			// 风格压缩水位随读行刷新：判定以库内值为准，调度会话与外部写入
			// 都不会替主线消费它。
			if fresh, err := s.bots.Get(bot.ID); err == nil {
				bot.LastToldVersion = fresh.LastToldVersion
				bot.OfferedInputSeq = fresh.OfferedInputSeq
				bot.StyleCompactionVersion = fresh.StyleCompactionVersion
			}
			applyRunCredentialFreeze(bot, freeze)
			applyRunPromptFreeze(bot, freeze)
			s.maybeTellToolset(bot, run)
		}

		var built *botBuiltContext
		if sched != nil {
			built, err = s.buildScheduleRunContext(bot, run, sched)
		} else {
			built, err = s.buildRunContext(bot, run)
		}
		if err != nil {
			s.finishFailed(bot, run, "context_build_failed", err)
			return
		}
		// schedule 会话不做 INT-102 压缩：独立会话天然短，且压缩会改写
		// bot_context_states 持久投影（主线专属状态）。
		if sched == nil && !thresholdCompactionFailed {
			// 窗口新鲜度只在真正要按窗口做压缩决策时收敛（INT-115）：输入
			// 估算达到回退触发阈值才做有界同步探测，其余情况非阻塞。探测
			// 成功落缓存后，下面的 inputBudgetFor 立即用探测值——升级后的
			// 存量机器人不会先按默认小窗口误判超限、误消费输入。
			s.ensureModelWindow(bot, run, key, built.InputTokensEstimate)
			window := s.inputBudgetFor(bot, runModelOf(run, bot))
			trigger := botFloorRatio(window, s.cfg.Bots.Context.CompressionRatioV())
			reason := model.BotCompactionThreshold
			needCompaction := built.InputTokensEstimate >= trigger
			if !needCompaction && bot.StyleCompactionVersion < botStylePromptVersion {
				// INT-133：风格压缩水位低于当前说话方式版本表示该会话还欠一次
				// 升级压缩——即使未达阈值也强制一轮，把旧风格的长回复收进
				// 摘要。水位是持久列，schedule run 不读不写，主线压缩尝试后
				// CAS 推进（见下），激活、重启与人设编辑都不会弄丢这个待办。
				needCompaction = true
				reason = model.BotCompactionStyle
			}
			if needCompaction {
				outcome, err := s.runCompactionCycle(ctx, bot, run, reason)
				// 一次性语义：只有风格压缩的尝试才消费风格水位（无论 published /
				// no_gain / 失败）。阈值压缩可能是 fold-only（折叠后不需要摘要），
				// 此时旧风格的长回复仍在投影里，水位必须留给后续 run 的
				// style_upgrade——否则需求要的「收进摘要」就永久丢了。
				if reason == model.BotCompactionStyle {
					s.markStyleCompactionDone(bot)
				}
				if err != nil {
					if reason == model.BotCompactionStyle {
						// 风格压缩只为收进旧长回复：不可达/无净缩减/失败都
						// 不阻塞对话（输入本来低于触发阈值），本 run 内不再
						// 自动重试，与阈值路径同样退让。
						thresholdCompactionFailed = true
					} else if errors.Is(err, errContextUncompressible) || built.InputTokensEstimate >= window {
						s.finishCompactionFailed(bot, run, built, err)
						return
					} else {
						// prompt_changed 等冲突视为「外部正在编辑」：本 run 内不重试是
						// 有意退让，下个 run 或上下文极限的强制路径再压缩。
						thresholdCompactionFailed = true
					}
				}
				if s.maintenanceStopped(ctx, run) {
					s.finishStopped(bot, run)
					return
				}
				if outcome != nil && outcome.Published {
					s.acceptCompactionBoundary(bot, &freeze, outcome.Snapshot)
					s.clearUsageBaseline(bot.ID)
				}
				built, err = s.buildRunContext(bot, run)
				if err != nil {
					s.finishFailed(bot, run, "context_build_failed", err)
					return
				}
			}
		}
		// 尝试循环（INT-137）：每次尝试独立记录模型请求；只在本次尝试尚未
		// 收到任何片段就失败时按分类做有限重试。reply/requestRow/assistantMsgID
		// 留存最后一次尝试的结果。
		var (
			reply          *botModelReply
			requestRow     *model.BotModelRequest
			assistantMsgID string
		)
		for attempt := 0; ; attempt++ {
			requestRow, err = s.beginModelRequest(bot, run, built)
			if err != nil {
				if errors.Is(err, errBotRunStopBeforeRequest) {
					// 停止/结束先落库：不再发请求，也不推进水位（停止之后的新输入
					// 留给下一次 run），直接收尾。
					s.finishStopped(bot, run)
					return
				}
				s.finishFailed(bot, run, "request_record_failed", err)
				return
			}
			s.advanceInputWatermarks(run, built.InputThroughSeq)

			// 预分配 assistant 消息 ID：流式增量信封先带上它，提交时复用同一个。
			assistantMsgID = uuid.NewString()
			deltas := s.newDeltaBuffer(bot.ID, run.ID, assistantMsgID)
			var streamErr error
			reply, streamErr = botStreamChat(ctx, &streamClient, built.Request, s.botStreamTimeoutsOf(), deltas.onDelta)
			deltas.flush()
			if streamErr == nil {
				break
			}
			// 失败（含用户停止打断）：草稿等不来 message_commit，丢弃。
			deltas.discard()
			s.finishModelRequest(requestRow, reply, streamErr)
			if input := s.safePoint(run); input.StopRequested {
				// 被停止打断的流已经丢弃草稿：主线补一条可见的 lifecycle 提示，
				// 否则时间线停在用户的「停止」上，看不出半截回复去哪了（视觉
				// 审查）。定时任务 run 不写主线消息——用户是从运行记录停的，主线
				// 不该多出一条无关提示（也不进入后续主线上下文）。
				if run.SessionKind != model.BotSessionKindSchedule {
					s.appendForcedStopNotice(bot.ID, run.ID)
				}
				s.finishStopped(bot, run)
				return
			}
			// 上游上下文超限：强制压缩一次后恰好重试一次；其他错误
			//（400 参数、429 限流等）不在此列。schedule 会话不压缩，超限
			// 直接失败并记录。压缩后重建上下文，尝试计数一并归零。
			// 已收到过片段的不压缩重试——那不是「没输出被拒」，是这轮对话
			// 本身超窗，重放只会再次计费（INT-137）。
			if sched == nil && botIsContextLimitError(streamErr) && !contextLimitRetried && !botReplyHasOutput(reply) {
				contextLimitRetried = true
				outcome, compErr := s.runCompactionCycle(ctx, bot, run, model.BotCompactionForced)
				if compErr != nil {
					s.finishCompactionFailed(bot, run, built, compErr)
					return
				}
				if outcome == nil || !outcome.Published {
					s.finishCompactionFailed(bot, run, built, errContextUncompressible)
					return
				}
				if s.maintenanceStopped(ctx, run) {
					s.finishStopped(bot, run)
					return
				}
				s.acceptCompactionBoundary(bot, &freeze, outcome.Snapshot)
				s.clearUsageBaseline(bot.ID)
				continue runLoop
			}
			// 有限重试只认「无输出 + 可重试分类」：已收到正文/工具调用片段、
			// 看门狗超时与普通 4xx 都直接落终态。
			retryAfter, canRetry := botShouldRetryModelError(streamErr)
			if botReplyHasOutput(reply) || !canRetry || attempt >= s.cfg.Bots.Runtime.RetryMaxAttemptsV() {
				s.finishModelFailed(bot, run, streamErr)
				return
			}
			wait := max(retryAfter, s.cfg.Bots.Runtime.RetryBaseDelay()<<attempt)
			if !s.waitRetryBackoff(ctx, wait) {
				// 退避被打断：安全点判停止；非停止的 ctx 取消（关停）不再重试。
				if input := s.safePoint(run); input.StopRequested {
					s.finishStopped(bot, run)
					return
				}
				s.finishModelFailed(bot, run, streamErr)
				return
			}
		}
		contextLimitRetried = false
		s.finishModelRequest(requestRow, reply, nil)
		if sched == nil {
			// usage 增量基线是主线压缩估算的状态，schedule 会话不碰。
			s.rememberUsageBaseline(bot, run, built, reply)
		}
		s.accumulateRunUsage(run, reply)

		// 用 = 而不是 :=：外层 calls 保留给循环顶部的 closeUnstartedSyncCalls，
		// 不留一个永远为 nil 的变量（INT-137 复审顺手清理）。
		calls, err = s.commitAssistantTurn(bot, run, requestRow, reply, assistantMsgID)
		if err != nil {
			s.finishFailed(bot, run, "commit_failed", err)
			return
		}

		if input := s.safePoint(run); input.StopRequested {
			s.closeUnstartedSyncCalls(calls, "stop_requested")
			s.finishStopped(bot, run)
			return
		}
		if len(reply.ToolCalls) == 0 {
			if input := s.safePoint(run); input.HasNewInput {
				continue
			}
			if s.tryFinishRun(bot, run) {
				return
			}
			continue
		}
		newInputSeen := false
		for _, call := range calls {
			input := s.safePoint(run)
			if input.StopRequested {
				s.closeUnstartedSyncCalls(calls, "stop_requested")
				break
			}
			if input.HasNewInput && !newInputSeen {
				// 一发现新输入就闭合尚未开始的写操作：排队中的异步写必须
				// 在继续做剩余读之前 CAS 掉，否则读得很久时 worker 仍可能
				// 把它落地。每个调用前重查，长读不会把新输入盖过去。
				s.closeUnstartedWriteCalls(calls, "new_input_before_execution")
				newInputSeen = true
			}
			if newInputSeen && !botCallIsReadOnly(call) {
				continue
			}
			s.invokeOrAccept(bot, run, call, ctx)
		}
	}
}

// markStyleCompactionDone 把风格压缩水位 CAS 推进到当前说话方式版本：
// 本 run 的一次风格压缩尝试（无论成败）即消费掉升级后的一次性额度，已
// 推进过的并发写者落空，副本只在 CAS 命中时对齐。只有 reason=style_upgrade
// 的尝试才推进水位——阈值与 context_limit 压缩不消费它；schedule 会话
// 不读也不写它（INT-133）。
func (s *BotService) markStyleCompactionDone(bot *model.Bot) {
	if bot.StyleCompactionVersion >= botStylePromptVersion {
		return
	}
	ok, err := s.bots.AdvanceStyleCompaction(bot.ID, botStylePromptVersion)
	if err != nil {
		s.logger.Warn("推进风格压缩水位失败", zap.String("bot_id", bot.ID), zap.Error(err))
		return
	}
	if ok {
		bot.StyleCompactionVersion = botStylePromptVersion
	}
}

// runProviderIDOf run 期间的 provider 归属：优先配置快照（熔断按 run 派发时
// 的 provider 打开，用户中途换 provider 不漂移）。INT-137 之前的快照没有
// provider_id 字段——空值回退 bot 当前值，兼容旧数据。
func runProviderIDOf(run *model.BotRun, bot *model.Bot) string {
	var snap botConfigSnapshot
	if err := json.Unmarshal([]byte(run.ConfigSnapshotJSON), &snap); err == nil && snap.ProviderID != "" {
		return snap.ProviderID
	}
	return bot.ProviderID
}

// runBaseURLOf run 期间的地址：优先配置快照。
func runBaseURLOf(run *model.BotRun, bot *model.Bot) string {
	var snap botConfigSnapshot
	if err := json.Unmarshal([]byte(run.ConfigSnapshotJSON), &snap); err == nil && snap.BaseURL != "" {
		return snap.BaseURL
	}
	return bot.BaseURL
}

// runCatalogIDOf run 期间的目录归属：优先配置快照（与快照里的 base_url 成对，
// 机器人换 provider 后旧 run 的窗口仍按派发时的目录解析）。INT-133 之前的
// 快照没有 catalog_id 字段——空值回退 bot 当前派生值，兼容旧数据。
func runCatalogIDOf(run *model.BotRun, bot *model.Bot) string {
	var snap botConfigSnapshot
	if err := json.Unmarshal([]byte(run.ConfigSnapshotJSON), &snap); err == nil && snap.CatalogID != "" {
		return snap.CatalogID
	}
	return bot.CatalogID
}

// botInputRunID 解析 run 的新输入判定范围：schedule 会话按 run 隔离（同一
// 机器人其他 schedule run 的消息不算自己的新输入），主线会话不收窄。
func botInputRunID(run *model.BotRun) string {
	if run.SessionKind == model.BotSessionKindSchedule {
		return run.ID
	}
	return ""
}

// safePoint 读取停止标记与未消费输入；run 的本地水位同步自数据库。输入检查
// 按会话判别：主线只看主线消息，schedule 会话只看自己 run 名下的消息。
func (s *BotService) safePoint(run *model.BotRun) botSafePoint {
	input := botSafePoint{}
	fresh, err := s.runs.FindByID(run.ID)
	if err == nil {
		if fresh.InputThroughSeq > run.InputThroughSeq {
			run.InputThroughSeq = fresh.InputThroughSeq
		}
		input.StopRequested = fresh.StopRequestedAt != nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		s.logger.Warn("读取 run 失败", zap.String("run_id", run.ID), zap.Error(err))
	}
	hasNew, err := s.messages.HasModelInputSince(run.BotID, run.SessionKind, botInputRunID(run), run.InputThroughSeq)
	if err == nil {
		input.HasNewInput = hasNew
	}
	return input
}

// advanceInputWatermarks 推进 run 消费水位与 bot 的 offered 水位（已交给模型
// 请求的输入不再自动重试）。offered 水位只由主线推进，schedule 会话不碰。
// 两处写库都只升不降：硬停止与主线循环并发写时，持有更旧 seq 的一方不得把
// 已推进的水位覆盖回去（INT-137）；run 的内存副本同步取 max。
func (s *BotService) advanceInputWatermarks(run *model.BotRun, seq int64) {
	if seq <= run.InputThroughSeq {
		return
	}
	run.InputThroughSeq = seq
	_, _ = s.runs.AdvanceInputThroughSeq(run.ID, seq)
	if run.SessionKind != model.BotSessionKindSchedule {
		_ = s.bots.SetOfferedInputSeq(run.BotID, seq)
	}
}

// botStreamTimeoutsOf 从部署配置取流式看门狗阈值；主线与辅助请求共用同一份。
func (s *BotService) botStreamTimeoutsOf() botStreamTimeouts {
	return botStreamTimeouts{
		FirstChunk: s.cfg.Bots.Runtime.StreamFirstChunkTimeout(),
		Idle:       s.cfg.Bots.Runtime.StreamIdleTimeout(),
	}
}

// botReplyHasOutput 报告本次尝试已收到正文或工具调用片段：SawOutput 在聚合
// 处置位（半截工具调用也算输出），Text/ToolCalls 兜底覆盖构造出 reply 的
// 非流式路径。有过输出的失败不在重试与强制压缩重试之列（上游可能已计费，
// 重放还可能重复触发副作用）。
func botReplyHasOutput(reply *botModelReply) bool {
	return reply != nil && (reply.SawOutput || reply.Text != "" || len(reply.ToolCalls) > 0)
}

// waitRetryBackoff 等待重试退避；ctx 取消（用户停止 / 关停）立即返回 false，
// 由调用方在安全点判定收尾方式。
func (s *BotService) waitRetryBackoff(ctx context.Context, wait time.Duration) bool {
	if wait <= 0 {
		return true
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// tryFinishRun 最后一次检查与 completed 落定在同一短事务：检查后恰好到达的
// 新消息不会被丢唤醒。
func (s *BotService) tryFinishRun(bot *model.Bot, run *model.BotRun) bool {
	if run.SessionKind == model.BotSessionKindSchedule {
		return s.tryFinishScheduleRun(bot, run)
	}
	finished := false
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		hasNew, err := repository.NewBotMessageRepository(tx).HasModelInputSinceTx(tx, bot.ID, model.BotSessionKindMain, "", run.InputThroughSeq)
		if err != nil || hasNew {
			return err
		}
		now := time.Now().UTC()
		// 完成转移必须带 stop_requested_at IS NULL：停止请求落在本事务前
		// 一刻也不能被 completed 覆盖，返回 false 后由下一轮安全点落
		// stopped。
		ok, err := repository.NewBotRunRepository(tx).UpdateColumnsWhereTx(tx, run.ID,
			map[string]interface{}{"finished_at": nil, "stop_requested_at": nil},
			map[string]interface{}{
				"status":            model.BotRunStatusCompleted,
				"finished_at":       now,
				"input_through_seq": run.InputThroughSeq,
			})
		finished = ok
		if err != nil || !ok {
			return err
		}
		// 终态事件与终态转移同一事务：崩溃不会留下没有 done 事件的已终
		// 态 run（重连客户端按游标回放时 run 栏会永远卡在 running）。
		return s.appendEventTx(tx, bot.ID, runIDPtr(run.ID), "", model.BotEventDone, botEventEnvelope{
			Kind: "run_done", Phase: string(model.BotRunStatusCompleted),
			Payload: map[string]interface{}{"status": string(model.BotRunStatusCompleted)},
		})
	})
	if txErr != nil {
		s.logger.Warn("完成 run 失败", zap.String("run_id", run.ID), zap.Error(txErr))
		return false
	}
	if finished {
		s.publishEvent(bot.ID)
	}
	return finished
}

// finishStopped / finishFailed 落定终态并发 done（失败先 error 再 done）。
// 终态与事件同一事务：崩溃不留无事件的已终态 run；提交后再发布 SSE。
// schedule 会话走专属收尾（摘要/静默判定与运行事件）。
func (s *BotService) finishStopped(bot *model.Bot, run *model.BotRun) {
	if run.SessionKind == model.BotSessionKindSchedule {
		s.finishScheduleStopped(bot, run)
		return
	}
	now := time.Now().UTC()
	ok := false
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		ok, err = repository.NewBotRunRepository(tx).UpdateColumnsWhereTx(tx, run.ID,
			map[string]interface{}{"finished_at": nil},
			map[string]interface{}{"status": model.BotRunStatusStopped, "finished_at": now})
		if err != nil || !ok {
			return err
		}
		return s.appendEventTx(tx, bot.ID, runIDPtr(run.ID), "", model.BotEventDone, botEventEnvelope{
			Kind: "run_done", Phase: string(model.BotRunStatusStopped),
			Payload: map[string]interface{}{"status": string(model.BotRunStatusStopped)},
		})
	})
	if txErr != nil {
		s.logger.Warn("落停止终态失败", zap.String("run_id", run.ID), zap.Error(txErr))
		return
	}
	if ok {
		s.publishEvent(bot.ID)
	}
}

// finishFailed 非模型类失败的收尾（key_decrypt_failed、context_build_failed
// 等）：error_message 取 cause 原文，payload 不带 detail/action，行为不变。
func (s *BotService) finishFailed(bot *model.Bot, run *model.BotRun, code string, cause error) {
	if run.SessionKind == model.BotSessionKindSchedule {
		s.finishScheduleFailed(bot, run, code, cause)
		return
	}
	s.finishFailedDetail(bot, run, code, botErrMessage(cause), "", nil)
}

// finishModelFailed 模型流失败的终态收尾（INT-137）：错误先过分类层得到可
// 行动的 code 与中文说明。schedule 会话沿用专属收尾带出 code/message（熔断
// 任务依赖 error_code）；主线 payload 追加上游 detail 与可选 action。
func (s *BotService) finishModelFailed(bot *model.Bot, run *model.BotRun, cause error) {
	info := botClassifyModelError(cause)
	if run.SessionKind == model.BotSessionKindSchedule {
		s.finishScheduleFailed(bot, run, info.Code, errors.New(info.Message))
		return
	}
	s.finishFailedDetail(bot, run, info.Code, info.Message, info.Detail, info.Action)
}

// finishFailedDetail 主线失败收尾的公共实现：终态转移、error/done 事件与
// 停止抢占回退同 finishFailed；detail/action 只进 run_error payload。
func (s *BotService) finishFailedDetail(bot *model.Bot, run *model.BotRun, code, message, detail string, action *botModelErrorAction) {
	now := time.Now().UTC()
	ok := false
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		// failed 转移带 stop_requested_at IS NULL：错误检出后才受理的停止
		// 请求不能被 failed 覆盖（与 completed 同型保护）。
		ok, err = repository.NewBotRunRepository(tx).UpdateColumnsWhereTx(tx, run.ID,
			map[string]interface{}{"finished_at": nil, "stop_requested_at": nil},
			map[string]interface{}{
				"status":        model.BotRunStatusFailed,
				"finished_at":   now,
				"error_code":    code,
				"error_message": message,
			})
		if err != nil || !ok {
			return err
		}
		payload := map[string]interface{}{
			"code": code, "message": message, "status": string(model.BotRunStatusFailed),
		}
		if detail != "" {
			payload["detail"] = detail
		}
		if action != nil {
			payload["action"] = action
		}
		if err := s.appendEventTx(tx, bot.ID, runIDPtr(run.ID), "", model.BotEventError, botEventEnvelope{
			Kind:    "run_error",
			Payload: payload,
		}); err != nil {
			return err
		}
		return s.appendEventTx(tx, bot.ID, runIDPtr(run.ID), "", model.BotEventDone, botEventEnvelope{
			Kind: "run_done", Phase: string(model.BotRunStatusFailed),
			Payload: map[string]interface{}{"status": string(model.BotRunStatusFailed)},
		})
	})
	if txErr != nil {
		s.logger.Warn("落失败终态", zap.String("run_id", run.ID), zap.Error(txErr))
		return
	}
	if ok {
		s.publishEvent(bot.ID)
		return
	}
	// CAS 失败：可能已被受理的停止抢占（错误检出后停止落库）——落 stopped，
	// 让停止路径赢；已终态时 finishStopped 的 CAS 自然落空。
	s.finishStopped(bot, run)
}

// ---------- 模型请求记录 ----------

// errBotRunStopBeforeRequest 表示登记请求前发现 run 已请求停止或已结束：这时
// 不能再发起模型请求——否则停止 UPDATE 已经命中（没有更新的请求）之后，本条
// 请求仍会登记并推进输入水位，随后被取消，停止之后的新输入就再也没人处理。
// 停止标记与请求登记都走数据库，MaxOpenConns(1) 下两个事务串行：要么停止先
// 落库（这里看见标记直接收尾），要么请求先登记（停止的条件 UPDATE 看见更高
// ordinal 而落空），不存在两者都成功却互相不知道的交错。
var errBotRunStopBeforeRequest = errors.New("bot run stopped before model request")

func (s *BotService) beginModelRequest(bot *model.Bot, run *model.BotRun, built *botBuiltContext) (*model.BotModelRequest, error) {
	var row *model.BotModelRequest
	err := s.db.Transaction(func(tx *gorm.DB) error {
		fresh, err := repository.NewBotRunRepository(tx).FindByID(run.ID)
		if err != nil {
			return err
		}
		if fresh.FinishedAt != nil || fresh.StopRequestedAt != nil {
			return errBotRunStopBeforeRequest
		}
		ordinal, err := repository.NewBotModelRequestRepository(tx).NextOrdinalTx(tx, run.ID)
		if err != nil {
			return err
		}
		row = &model.BotModelRequest{
			ID:                  uuid.NewString(),
			RunID:               run.ID,
			Ordinal:             ordinal,
			Status:              model.BotModelRequestRunning,
			InputThroughSeq:     built.InputThroughSeq,
			PromptRevision:      bot.PromptRevision,
			ToolsetVersion:      built.ToolsetVersion,
			ContextGeneration:   built.ContextGeneration,
			InputTokensEstimate: built.InputTokensEstimate,
			InputEstimator:      built.InputEstimator,
			PrefixHash:          built.PrefixHash,
			ToolsHash:           built.ToolsHash,
			RequestHash:         built.RequestHash,
			ToolsSchemaBytes:    built.ToolsSchemaBytes,
			ToolsTokensEstimate: built.ToolsTokensEstimate,
			TokenEstimator:      built.TokenEstimator,
			StartedAt:           time.Now().UTC(),
		}
		return repository.NewBotModelRequestRepository(tx).CreateTx(tx, row)
	})
	return row, err
}

// finishModelRequest 落请求终态与 usage；usage 缺失与零严格区分。
func (s *BotService) finishModelRequest(row *model.BotModelRequest, reply *botModelReply, streamErr error) {
	cols := map[string]interface{}{"finished_at": time.Now().UTC()}
	if streamErr != nil {
		cols["status"] = model.BotModelRequestFailed
		// 请求行 error_code 直接用分类结果：限流/超时/认证失败可区分。
		cols["error_code"] = botClassifyModelError(streamErr).Code
		cols["error_message"] = botErrMessage(streamErr)
	} else {
		cols["status"] = model.BotModelRequestSucceeded
		if row != nil && reply != nil {
			cols["provider_request_id"] = reply.ProviderRequestID
			if reply.FirstDeltaAt != nil {
				cols["first_delta_at"] = *reply.FirstDeltaAt
			}
			if reply.UsageComplete {
				cols["prompt_tokens"] = reply.PromptTokens
				cols["completion_tokens"] = reply.CompletionTokens
				cols["total_tokens"] = reply.TotalTokens
				cols["cached_tokens"] = reply.CachedTokens
			}
		}
	}
	if row == nil {
		return
	}
	if _, err := s.requests.UpdateColumns(row.ID, nil, cols); err != nil {
		s.logger.Warn("落模型请求终态失败", zap.String("request_id", row.ID), zap.Error(err))
	}
}

// accumulateRunUsage 把本次请求的 usage 累加进 run（内存累计，收尾统一落库）。
func (s *BotService) accumulateRunUsage(run *model.BotRun, reply *botModelReply) {
	if reply == nil || !reply.UsageComplete {
		return
	}
	run.PromptTokens = addToPtr(run.PromptTokens, reply.PromptTokens)
	run.CompletionTokens = addToPtr(run.CompletionTokens, reply.CompletionTokens)
	run.TotalTokens = addToPtr(run.TotalTokens, reply.TotalTokens)
	run.CachedTokens = addToPtr(run.CachedTokens, reply.CachedTokens)
	run.UsageComplete = true
}

// compactionFailDetail 提取压缩失败的精确原因码与中文说明：带类型的
// botCompactionError 原样透出（B1 前置检查的 target_unreachable /
// no_reduction 等不再丢失），其余按哨兵给出固定中文映射。
func compactionFailDetail(err error) (code, message string) {
	var ce *botCompactionError
	if errors.As(err, &ce) {
		return ce.Code, ce.Message
	}
	if errors.Is(err, errContextUncompressible) {
		return "context_uncompressible", "上下文压缩后仍超出模型窗口"
	}
	return "context_compaction_failed", "上下文压缩未成功"
}

// finishCompactionFailed 压缩失败后的 run 收尾（INT-115）。三步都在落终态
// 之前完成，且都不改变终态判定：
//  1. 推进输入水位——这批输入已被「尝试处理过」，扫描器不再自动拉起同一
//     输入重复一轮无效压缩（线上事故：失败 5s 后原样重跑 ≈471s）；用户再发
//     新消息会推进水位并触发新一轮判断。
//  2. 发 failed 维护进度事件——聊天页运行栏立即看到精确的中文失败原因。
//  3. 追加去重的 lifecycle 失败消息——run 结束后 run 栏消失，仅 error 事件
//     看不见，时间线需要一条可见记录。
func (s *BotService) finishCompactionFailed(bot *model.Bot, run *model.BotRun, built *botBuiltContext, cause error) {
	code, message := compactionFailDetail(cause)
	s.advanceInputWatermarks(run, built.InputThroughSeq)
	s.emitMaintenanceProgress(bot.ID, compactionParentRunID(run), "failed", message)
	s.appendCompactionFailedNotice(bot.ID, built.ContextGeneration, built.InputThroughSeq, code, message)
	s.finishFailed(bot, run, code, errors.New(message))
}

// appendCompactionFailedNotice 追加一条压缩失败 lifecycle 消息（幂等）并发布
// 事件；dedup key 按「投影代数:输入水位」，同一条件下不重复追加。detail 的
// note 是现有渲染器直接显示的中文原因，code 保留供排查。
func (s *BotService) appendCompactionFailedNotice(botID string, generation, throughSeq int64, code, message string) {
	detail := botMustJSON(map[string]interface{}{
		"code":        code,
		"message":     message,
		"note":        message,
		"generation":  generation,
		"through_seq": throughSeq,
	})
	content := botLifecycleContent{Event: "compaction_failed", Detail: json.RawMessage(detail)}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		txMsgs := repository.NewBotMessageRepository(tx)
		dedup := fmt.Sprintf("compaction_failed:%d:%d", generation, throughSeq)
		if _, err := txMsgs.FindByDedupKey(botID, dedup); err == nil {
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		seq, err := s.bots.NextMessageSeqTx(tx, botID)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(content)
		if err != nil {
			return err
		}
		msgID := uuid.NewString()
		msg := &model.BotMessage{
			ID: msgID, BotID: botID, Seq: seq,
			Kind: model.BotMessageKindLifecycle, ContentJSON: string(raw),
			DedupKey: &dedup, CreatedAt: time.Now().UTC(),
		}
		if err := txMsgs.AppendTx(tx, msg); err != nil {
			return err
		}
		return s.appendEventTx(tx, botID, nil, "", model.BotEventDelta, botEventEnvelope{
			Kind:    "lifecycle",
			Payload: map[string]interface{}{"event": "compaction_failed", "message_id": msgID},
		})
	})
	if err != nil {
		// 消息追加失败不改变 run 终态，只记日志。
		s.logger.Warn("追加压缩失败消息失败", zap.String("bot_id", botID), zap.Error(err))
		return
	}
	s.publishEvent(botID)
}

// appendForcedStopNotice 在硬取消丢弃草稿后追加一条可见的 lifecycle 提示
// （INT-137 视觉审查）：时间线不能停在用户发的「停止」上什么也不说——用户
// 需要知道半截回复是被主动停止了，而不是机器人没回应。按 run 幂等。
func (s *BotService) appendForcedStopNotice(botID, runID string) {
	content := botLifecycleContent{
		Event:  "run_interrupted",
		Detail: json.RawMessage(`{"note":"已强制停止，未完成的回复已丢弃"}`),
	}
	dedup := "stop_notice:" + runID
	err := s.db.Transaction(func(tx *gorm.DB) error {
		txMsgs := repository.NewBotMessageRepository(tx)
		if _, err := txMsgs.FindByDedupKey(botID, dedup); err == nil {
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		seq, err := s.bots.NextMessageSeqTx(tx, botID)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(content)
		if err != nil {
			return err
		}
		msgID := uuid.NewString()
		msg := &model.BotMessage{
			ID: msgID, BotID: botID, Seq: seq,
			Kind: model.BotMessageKindLifecycle, ContentJSON: string(raw),
			DedupKey: &dedup, CreatedAt: time.Now().UTC(),
		}
		if err := txMsgs.AppendTx(tx, msg); err != nil {
			return err
		}
		return s.appendEventTx(tx, botID, nil, "", model.BotEventDelta, botEventEnvelope{
			Kind:    "lifecycle",
			Payload: map[string]interface{}{"event": "run_interrupted", "message_id": msgID},
		})
	})
	if err != nil {
		// 提示追加失败不改变 run 终态，只记日志。
		s.logger.Warn("追加强制停止提示失败", zap.String("bot_id", botID), zap.Error(err))
		return
	}
	s.publishEvent(botID)
}

func addToPtr(p *int64, v int64) *int64 {
	if p == nil {
		out := v
		return &out
	}
	*p += v
	return p
}

// ---------- 流式增量批量落事件 ----------

// botDeltaBuffer 合并正文流式片段（约 100ms 一批写入），完整 assistant 消息
// 仍只在模型调用结束时追加一次；流式片段不改消息。msgID 是预分配的
// assistant 消息 ID：增量信封带上它，客户端才能把片段挂到正确草稿上。
type botDeltaBuffer struct {
	svc   *BotService
	botID string
	runID string
	msgID string

	mu    sync.Mutex
	buf   strings.Builder
	timer *time.Timer
}

func (s *BotService) newDeltaBuffer(botID, runID, msgID string) *botDeltaBuffer {
	return &botDeltaBuffer{svc: s, botID: botID, runID: runID, msgID: msgID}
}

func (d *botDeltaBuffer) onDelta(text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buf.WriteString(text)
	if d.timer == nil {
		d.timer = time.AfterFunc(100*time.Millisecond, d.flush)
	}
}

// flush 取走当前缓冲整体落一次事件；定时器与流末尾并发调用时由互斥与
// 空缓冲判空保证不重复。写完即复位，后续增量继续按 100ms 批量落库。
func (d *botDeltaBuffer) flush() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	text := d.buf.String()
	d.buf.Reset()
	if text == "" {
		return
	}
	runID := d.runID
	err := d.svc.db.Transaction(func(tx *gorm.DB) error {
		return d.svc.appendEventTx(tx, d.botID, &runID, "", model.BotEventDelta, botEventEnvelope{
			Kind:      "text_delta",
			MessageID: d.msgID,
			Payload:   map[string]interface{}{"text": text, "run_id": d.runID},
		})
	})
	if err != nil {
		d.svc.logger.Warn("写入流式事件失败", zap.String("bot_id", d.botID), zap.Error(err))
		return
	}
	d.svc.publishEvent(d.botID)
}

// discard 请求客户端丢弃已挂出的部分草稿：模型请求失败（含用户停止打断）
// 时草稿不会等来 message_commit，不丢弃会被当成完整回复一直挂着。
func (d *botDeltaBuffer) discard() {
	runID := d.runID
	err := d.svc.db.Transaction(func(tx *gorm.DB) error {
		return d.svc.appendEventTx(tx, d.botID, &runID, "", model.BotEventDelta, botEventEnvelope{
			Kind:      "message_discard",
			MessageID: d.msgID,
		})
	})
	if err != nil {
		d.svc.logger.Warn("写入草稿丢弃事件失败", zap.String("bot_id", d.botID), zap.Error(err))
		return
	}
	d.svc.publishEvent(d.botID)
}
