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

// 定时任务调度（INT-103）：单进程一个 10s ticker，与审查调度同一纪律——
// 数据库是事实来源，MaxOpenConns(1) 下事务内绝不发网络调用。每个
// tick 短事务内逐任务处理：到期 →（错过超宽限或上一轮未结束记 skipped |
// 建运行行）→ CAS 推进 next_run_at；随后事务外唤醒执行槽。同一任务不并行
// （INT-148）：上一轮未结束时到点的这一轮记 skipped，与手动触发的忙碌拒绝
// 同语义。重启不补跑：错过的计划时间按「跳过并记录」处理，未完成的
// schedule run 由启动恢复统一置 failed。

const (
	botScheduleTickInterval = 10 * time.Second
	// botScheduleMissGrace 是计划时间的错过宽限：超过即视为错过（须大于
	// tick 间隔，容忍调度抖动）。
	botScheduleMissGrace = 90 * time.Second
	// botScheduleDispatchBatch 每次读取的到期任务条数。
	botScheduleDispatchBatch = 50
	// botScheduleDrainPerTick 单轮最多排空的到期任务数：宽限是 90s、tick 10s，
	// 所以单 tick 至少要把「90s 内会堆积的数量」排空，否则有效任务会被当成
	// 错过的计划记成 skipped。1000 条/tick 对应约 9000 条同刻到期的容量。
	botScheduleDrainPerTick = 1000
	// botScheduleCallSettleWait 是等待本 run 异步调用结算的上限：单次异步调用
	// 自身的执行超时是 botAsyncExecTimeout，这里多给一段余量；超过就显式失败，
	// 不谎报成功。
	botScheduleCallSettleWait = botAsyncExecTimeout + 5*time.Minute
	// botScheduleCallSettlePoll 是等待结算的轮询间隔。
	botScheduleCallSettlePoll = 500 * time.Millisecond
	// botScheduleWakePerScan 是单轮扫描最多补唤醒的 run 数；已唤醒的会占住槽，
	// 下一轮自然轮到后面的 run，不会饿死。
	botScheduleWakePerScan = 200
)

// botScheduleSlotKey 是 schedule run 的执行槽 key（与 wakeScheduleRun 一致）。
func botScheduleSlotKey(runID string) string {
	return "schedule:" + runID
}

// scheduleWaitOutcome 是等待本次运行异步调用结算的结果。
type scheduleWaitOutcome int

const (
	scheduleWaitSettled scheduleWaitOutcome = iota
	scheduleWaitStopped
	scheduleWaitTimeout
)

// waitScheduleCallsSettled 等到本次 schedule run 的异步写都落终态。
// 等待期间不发模型请求（否则模型会看不到新内容反复空跑），结算后循环重建
// 上下文，模型就能据实汇报调用的真实成败。
func (s *BotService) waitScheduleCallsSettled(ctx context.Context, run *model.BotRun) scheduleWaitOutcome {
	return s.waitScheduleCallsSettledUntil(ctx, run, time.Now().Add(botScheduleCallSettleWait))
}

// waitScheduleCallsSettledUntil 同 waitScheduleCallsSettled，deadline 可注入。
func (s *BotService) waitScheduleCallsSettledUntil(ctx context.Context, run *model.BotRun, deadline time.Time) scheduleWaitOutcome {
	for {
		unsettled, err := s.calls.CountUnsettledByRun(run.ID)
		if err != nil {
			// 查询失败不阻塞运行：宁可让模型看旧结果，也不把一次瞬时数据库
			// 抖动变成运行失败。
			s.logger.Warn("查询未结算调用失败", zap.String("run_id", run.ID), zap.Error(err))
			return scheduleWaitSettled
		}
		if unsettled == 0 {
			return scheduleWaitSettled
		}
		if s.safePoint(run).StopRequested {
			return scheduleWaitStopped
		}
		if time.Now().After(deadline) {
			return scheduleWaitTimeout
		}
		select {
		case <-ctx.Done():
			return scheduleWaitStopped
		case <-time.After(botScheduleCallSettlePoll):
		}
	}
}

// scheduleLoop 每 10s 扫描一次到期任务；启动后立即跑一轮（错过处理由
// next_run_at 与宽限决定，不依赖停机时长）。
func (s *BotService) scheduleLoop() {
	ticker := time.NewTicker(botScheduleTickInterval)
	defer ticker.Stop()
	s.scheduleTick(time.Now().UTC())
	for range ticker.C {
		s.scheduleTick(time.Now().UTC())
	}
}

// scheduleTick 排空到期集合：单次只读一批的话，同刻到期任务数超过一批时
// 剩下的会被拖过宽限、被误记成「错过」。按 (next_run_at, id) 键集分页逐批
// 处理，直到取空或触到单轮上限；触上限时告警，不静默丢弃。
func (s *BotService) scheduleTick(now time.Time) {
	var afterRunAt *time.Time
	afterID := ""
	for drained := 0; drained < botScheduleDrainPerTick; {
		due, err := s.schedTasks.ListDueAfter(now, afterRunAt, afterID, botScheduleDispatchBatch)
		if err != nil {
			s.logger.Warn("扫描到期定时任务失败", zap.Error(err))
			return
		}
		if len(due) == 0 {
			return
		}
		for i := range due {
			s.dispatchDueScheduleTask(&due[i], now)
		}
		drained += len(due)
		afterRunAt = due[len(due)-1].NextRunAt
		afterID = due[len(due)-1].ID
		if len(due) < botScheduleDispatchBatch {
			return
		}
	}
	s.logger.Warn("单轮到期任务超过排空上限，剩余放到下一轮",
		zap.Int("drained", botScheduleDrainPerTick))
}

// dispatchDueScheduleTask 处理一个到期任务：短事务内核对 enabled + 旧
// next_run_at 未变，写 skipped/running 运行行并在同一事务内 CAS 推进
// next_run_at。重复触发由 UNIQUE(bot_id, trigger_key) 挡下（0 行推进 = 被并发
// 抢走，直接跳过）。
func (s *BotService) dispatchDueScheduleTask(task *model.BotScheduleTask, now time.Time) {
	planned := *task.NextRunAt
	wakeRunID := ""
	appendedEvent := false
	eventRunID := ""
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		var current model.BotScheduleTask
		if err := tx.Where("id = ? AND enabled = ?", task.ID, true).First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil // 已被停用/删除，跳过
			}
			return err
		}
		if current.NextRunAt == nil || !current.NextRunAt.Equal(planned) {
			return nil // next_run_at 已被并发推进或重排，跳过
		}

		// 派发时冻结有效配置：同事务读一次机器人记下当时的 model/base_url，
		// wakeScheduleRun 之后（载入执行槽才 Get 机器人）即使配置被编辑，
		// 这次运行仍按快照执行——与主线 ensureRun 的 botSnapshotOf 同语义。
		// 只做一次主键查询，不发网络调用。经仓库层读取：provider 派生的
		// 地址与密钥在读取时补齐（INT-133）。
		botRow, err := repository.NewBotRepository(tx).Get(current.BotID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil // 机器人已删除（任务会级联删除），跳过
			}
			return err
		}
		snap := botSnapshotOf(botRow)
		if current.Model != "" {
			snap.Model = current.Model
		}

		// provider 熔断（INT-137）：认证失效/额度用完打开的窗口内，到点不再
		// 发起模型请求，直接记 skipped。事务内只读库，不发网络。
		var circuit *model.AIProvider
		if botRow.ProviderID != "" {
			p, err := repository.NewAIProviderRepository(tx).GetByID(botRow.ProviderID)
			if err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
			} else if p.CircuitOpenUntil != nil && p.CircuitOpenUntil.After(now) {
				circuit = p
			}
		}

		// 同一任务上一轮未结束（INT-148）：本轮到点不并行执行。查询在本次
		// tx.Create(run) 之前做——新 run 还没建，不会把自己算进去。取最早一条
		// 未结束 run：既判重叠，又给跳过事件的去重键提供 run id。熔断优先：
		// 熔断窗口内无论上一轮是否结束都记熔断跳过，不再多查一次。
		var overlapRun *model.BotRun
		if circuit == nil {
			first, err := repository.NewBotRunRepository(tx).FirstUnfinishedByScheduleTaskTx(tx, current.BotID, current.ID)
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			overlapRun = first
		}

		run := &model.BotRun{
			ID: uuid.NewString(), BotID: current.BotID,
			TriggerSource: model.BotTriggerSchedule,
			TriggerKey:    botScheduleTriggerKey(current.ID, planned),
			Status:        model.BotRunStatusRunning,
			SessionKind:   model.BotSessionKindSchedule,
			Summary:       "",
			// 密钥与 base_url 成对冻结：派发后用户换地址又换密钥时，
			// 不能把新密钥发往旧地址（运行结尾直接用这份密文）。
			APIKeyEncryptedSnapshot: append([]byte(nil), botRow.APIKeyEncrypted...),
		}
		taskID := current.ID
		run.ScheduleTaskID = &taskID
		run.ScheduledFor = &planned
		run.ConfigSnapshotJSON = marshalJSONString(snap)
		missed := now.Sub(planned) > botScheduleMissGrace
		if circuit != nil {
			// 熔断中到点：不执行任务，与「错过」同型落 skipped 终态；摘要
			// 写明原因、自动恢复时间与手动恢复方式。
			run.Status = model.BotRunStatusSkipped
			run.Summary = botCircuitSkipSummary(circuit)
			finished := now
			run.FinishedAt = &finished
		} else if overlapRun != nil {
			// 上一轮未结束：本轮不并行，直接落 skipped 终态。不设 StartedAt 与
			// 触发上下文（与错过的 skipped 同型），也不唤醒执行槽；next_run_at
			// 照常推进，否则任务每 tick 空转。带出阻塞 run 的 ID：排查卡死的
			// run 时从本行摘要就能直接定位是哪一轮在阻塞。
			run.Status = model.BotRunStatusSkipped
			run.Summary = fmt.Sprintf("上一次运行尚未结束（run %s），本轮跳过（不并行）", overlapRun.ID)
			finished := now
			run.FinishedAt = &finished
		} else if missed {
			// 错过的计划时间：跳过并记录，不执行、不补跑；直接落终态。
			run.Status = model.BotRunStatusSkipped
			// 写明原定计划时间：跨越很长停机时次数不会逐个补记，至少留得住
			// 「哪次没跑」这条线索。
			run.Summary = "错过的计划时间（原定 " + planned.UTC().Format(time.RFC3339) + "），已跳过"
			finished := now
			run.FinishedAt = &finished
		} else {
			nowUTC := now
			run.StartedAt = &nowUTC
			run.TriggerContextJSON = marshalJSONString(map[string]interface{}{
				"task_id": current.ID, "task_name": current.Name,
				"needs_report":  current.NeedsReport,
				"scheduled_for": planned.UTC().Format(time.RFC3339Nano),
			})
		}
		if err := tx.Create(run).Error; err != nil {
			// 唯一索引冲突：同一计划时间已建过 run（重启/并发/回滚快照）。
			// 该次计划已由既有 run 覆盖，但 next_run_at 仍必须推进——否则任务
			// 会永远停在到期集合里，每 tick 空转一次。
			if isBotRunDuplicateTriggerKey(err) {
				s.logger.Warn("定时任务的计划时间已有运行记录，跳过重复创建并推进计划",
					zap.String("task_id", current.ID),
					zap.Time("planned", planned))
				_, next, parseErr := parseScheduleCron(current.CronExpression, current.Timezone, now)
				if parseErr != nil {
					return repository.NewBotScheduleTaskRepository(tx).PauseTx(tx, current.ID)
				}
				_, advErr := repository.NewBotScheduleTaskRepository(tx).AdvanceNextRunTx(tx, current.ID, planned, next)
				return advErr
			}
			return err
		}
		if !missed && overlapRun == nil && circuit == nil {
			wakeRunID = run.ID
		} else if circuit != nil {
			// 熔断期间不逐次打扰主线：同一次熔断只留一条去重事件（含
			// needs_report 的任务也一样），后续到点只记 skipped run。
			appended, err := s.appendCircuitScheduleEventTx(tx, current.BotID, &current, run, circuit, now)
			if err != nil {
				return err
			}
			appendedEvent = appended
			eventRunID = run.ID
		} else if overlapRun != nil && current.NeedsReport {
			// 需要汇报的任务：重叠跳过也是一次「没跑」，但去重键钉在未结束的
			// 上一轮 run 上——周期短于运行时长（或一条 run 卡死等重启恢复）时，
			// 同一轮未结束期间只追加一条主线事件，不逐次刷屏；上一轮结束后
			// 再重叠才会产生下一条。
			dedup := botScheduleOverlapDedupKey(current.ID, overlapRun.ID)
			appended, err := s.appendScheduleEventTxDeduped(tx, current.BotID, &current, run,
				model.BotRunStatusSkipped, run.Summary, "", "", dedup, now)
			if err != nil {
				return err
			}
			appendedEvent = appended
			eventRunID = run.ID
		} else if current.NeedsReport {
			// 需要汇报的任务：跳过也是一次「没跑」，不能零信号。一条停机只记
			// 一条，不会刷屏。
			if err := s.appendScheduleEventTx(tx, current.BotID, &current, run,
				model.BotRunStatusSkipped, run.Summary, "", "", now); err != nil {
				return err
			}
			appendedEvent = true
			eventRunID = run.ID
		}

		// CAS 推进 next_run_at 到严格晚于 now 的下一次；0 行 = 被并发抢走。
		_, next, err := parseScheduleCron(current.CronExpression, current.Timezone, now)
		if err != nil {
			// 配置损坏（如表达式被手工改库）：暂停调度并告警，不无限循环。
			s.logger.Error("定时任务 cron 解析失败，暂停调度",
				zap.String("task_id", current.ID), zap.Error(err))
			return repository.NewBotScheduleTaskRepository(tx).PauseTx(tx, current.ID)
		}
		_, err = repository.NewBotScheduleTaskRepository(tx).AdvanceNextRunTx(tx, current.ID, planned, next)
		return err
	})
	if txErr != nil {
		s.logger.Warn("处理到期定时任务失败", zap.String("task_id", task.ID), zap.Error(txErr))
		return
	}
	if wakeRunID != "" {
		s.wakeScheduleRun(task.BotID, wakeRunID)
	}
	if appendedEvent {
		// 跳过事件已落账本，但本路径没有别的发布点：不 publish 的话订阅方要等
		// 下一次别的事件才看得到这一行。
		s.publishEvent(task.BotID)
		s.wakeScheduleEvent(task.BotID, eventRunID, model.BotRunStatusSkipped)
	}
}

// botScheduleTriggerKey 计划时间幂等键：同一计划时间（含重启后重放）只建
// 一次 run。
func botScheduleTriggerKey(taskID string, planned time.Time) string {
	return "schedule:" + taskID + ":" + planned.UTC().Format(time.RFC3339Nano)
}

// botScheduleOverlapDedupKey 是「上一轮未结束」跳过事件的去重键：钉在未结束
// 的那一轮 run 上，同一轮未结束期间多次到点只留一条主线事件（INT-148）。
func botScheduleOverlapDedupKey(taskID, runID string) string {
	return "schedule_overlap:" + taskID + ":" + runID
}

// wakeScheduleEvent 在定时任务运行事件（schedule_event）提交进主线后立即
// 唤醒主线（INT-138），5s 扫描仍作兜底。Key 用 runID+status 幂等：同一运行
// 同一终态的重复唤醒撞上 (bot_id, trigger_key) 唯一约束，不会重复建 run。
func (s *BotService) wakeScheduleEvent(botID, runID string, status model.BotRunStatus) {
	s.wakeRuntime(botID, botWake{
		Trigger: model.BotTriggerScheduleEvent,
		Key:     "schedule_event:" + runID + ":" + string(status),
	})
}

// ---------- provider 熔断（INT-137） ----------

// botProviderCircuitDedupKey 是一次熔断的主线事件去重键：同一 provider 同一
// 截止时间视为同一次熔断，窗口内多次到点只追加一条事件。
func botProviderCircuitDedupKey(providerID string, until time.Time) string {
	return fmt.Sprintf("circuit:provider:%s:%d", providerID, until.UTC().Unix())
}

// botCircuitReasonText 把熔断原因分类码翻成给用户看的中文短语。
func botCircuitReasonText(code string) string {
	switch code {
	case botModelErrAuthFailed:
		return "认证失败：密钥无效或已失效"
	case botModelErrQuotaExhausted:
		return "额度已用完或余额不足"
	default:
		return "暂时不可用"
	}
}

// botCircuitSkipSummary 是熔断跳过的 run 摘要：写明原因、自动恢复时间
// （窗口过后到点照常派发）与手动恢复方式。
func botCircuitSkipSummary(p *model.AIProvider) string {
	return "AI 提供商" + botCircuitReasonText(p.CircuitReason) +
		"，已暂停定时任务（熔断至 " + p.CircuitOpenUntil.UTC().Format("2006-01-02 15:04") +
		"）；在设置页更新密钥并测试连接成功后恢复"
}

// appendCircuitScheduleEventTx 在调用方事务内向主线追加「定时任务因熔断被
// 跳过」的事件；dedup_key 钉在这次熔断上，同一熔断窗口内多次到点只留一条
// （唯一索引兜底，预查走常规路径）。返回是否真正追加了消息。
func (s *BotService) appendCircuitScheduleEventTx(tx *gorm.DB, botID string, task *model.BotScheduleTask, run *model.BotRun, p *model.AIProvider, now time.Time) (bool, error) {
	dedup := botProviderCircuitDedupKey(p.ID, *p.CircuitOpenUntil)
	msgs := repository.NewBotMessageRepository(tx)
	if _, err := msgs.FindByDedupKey(botID, dedup); err == nil {
		return false, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	content := botScheduleEventContent{
		TaskID: task.ID, TaskName: task.Name, RunID: run.ID,
		Status: string(model.BotRunStatusSkipped), Summary: run.Summary,
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return false, err
	}
	seq, err := s.bots.NextMessageSeqTx(tx, botID)
	if err != nil {
		return false, err
	}
	msg := &model.BotMessage{
		ID: uuid.NewString(), BotID: botID, Seq: seq,
		Kind: model.BotMessageKindScheduleEvent, ContentJSON: string(raw),
		SessionKind: model.BotSessionKindMain, DedupKey: &dedup, CreatedAt: now,
	}
	if err := msgs.AppendTx(tx, msg); err != nil {
		return false, err
	}
	if err := s.appendEventTx(tx, botID, nil, "", model.BotEventDelta, botEventEnvelope{
		Kind:      "schedule_event",
		MessageID: msg.ID,
		Payload:   botMessageResponseOf(msg),
	}); err != nil {
		return false, err
	}
	return true, nil
}

// openProviderCircuitOnFailure 在 schedule run 因认证失效/额度用完落 failed
// 终态后打开 provider 熔断：窗口内该提供商的定时任务到点直接记 skipped，
// 不再反复打一个注定失败的上游。provider 取 run 派发时冻结的归属；打开前读
// 一次 provider——运行期间用户已更新过凭证/地址（含测试连接/清除熔断）的话，
// 这次失败针对的可能是已被换掉的旧配置，不再重新打开。窗口已开着时不延长、
// 不换去重键；打开失败只记日志，不改变已落的 run 终态。
func (s *BotService) openProviderCircuitOnFailure(bot *model.Bot, run *model.BotRun, code string) {
	if !botCircuitTrippingCode(code) {
		return
	}
	providerID := runProviderIDOf(run, bot)
	if providerID == "" {
		return
	}
	p, err := s.providers.GetByID(providerID)
	if err != nil {
		// provider 行已删除：没有可熔断的对象，跳过即可。
		return
	}
	if run.StartedAt != nil && p.ConfigVersionAt != nil && p.ConfigVersionAt.After(*run.StartedAt) {
		// 版本号为空的旧记录不参与判断（OpenCircuit 的 WHERE 同样放行 NULL）：
		// 用 updated_at 兜底会让「只改名字」也阻止熔断。
		return
	}
	// 配置版本条件同样写进 OpenCircuit 的 WHERE（检查与写入原子）：上面
	// 的预检只是快速路径，真正防竞态的是那句 config_version_at <= 本 run
	// 开始时刻。
	configSince := time.Now().UTC()
	if run.StartedAt != nil {
		configSince = *run.StartedAt
	}
	opened, err := s.providers.OpenCircuit(providerID, time.Now().UTC().Add(s.cfg.Bots.Runtime.ProviderCircuitWindow()), code, configSince)
	if err != nil {
		s.logger.Warn("打开 provider 熔断失败",
			zap.String("provider_id", providerID), zap.Error(err))
		return
	}
	if !opened {
		// 同一熔断窗口内的并行/重试失败：窗口与去重键保持不变，不重复记录。
		return
	}
	s.logger.Info("provider 熔断已打开",
		zap.String("provider_id", providerID), zap.String("reason", code))
}

// botScheduleTaskNameFromContext 取派发时记下的任务名；缺失时给一个可读占位，
// 让任务被删除后仍能报出可辨认的失败事件。
func botScheduleTaskNameFromContext(run *model.BotRun) string {
	var ctx struct {
		TaskName string `json:"task_name"`
	}
	if run.TriggerContextJSON != "" && json.Unmarshal([]byte(run.TriggerContextJSON), &ctx) == nil && ctx.TaskName != "" {
		return ctx.TaskName
	}
	return "已删除的定时任务"
}

// botScheduleNeedsReportFromContext 取派发时冻结的「需要汇报」；缺失按 false。
func botScheduleNeedsReportFromContext(run *model.BotRun) bool {
	var ctx struct {
		NeedsReport bool `json:"needs_report"`
	}
	if run.TriggerContextJSON != "" && json.Unmarshal([]byte(run.TriggerContextJSON), &ctx) == nil {
		return ctx.NeedsReport
	}
	return false
}

// isBotRunDuplicateTriggerKey 识别 (bot_id, trigger_key) 唯一冲突。
func isBotRunDuplicateTriggerKey(err error) bool {
	return err != nil && (errors.Is(err, gorm.ErrDuplicatedKey) ||
		strings.Contains(err.Error(), "UNIQUE constraint failed: bot_runs.bot_id, bot_runs.trigger_key"))
}

// ---------- 执行槽（D6） ----------

// wakeScheduleRun 把已存在的 schedule run 载入执行槽。同一 run 不重复执行：
// 槽占用即放弃（运行中循环会在安全点看到新输入）；run 已终态（如迟到结果）
// 不再续跑。
func (s *BotService) wakeScheduleRun(botID, runID string) {
	run, err := s.runs.FindByID(runID)
	if err != nil || run.BotID != botID || run.FinishedAt != nil ||
		run.SessionKind != model.BotSessionKindSchedule {
		return
	}
	slot := botScheduleSlotKey(runID)
	if s.rt.acquire(slot, &botWake{Trigger: run.TriggerSource, Key: run.TriggerKey}) == nil {
		return
	}
	go s.runScheduleSlot(botID, runID)
}

// runScheduleSlot 是单个 schedule run 的执行槽：载入 run 进 botLoop 后立即
// 释放；schedule run 只执行一次，没有 requeue 循环。
func (s *BotService) runScheduleSlot(botID, runID string) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("定时任务执行 panic", zap.String("run_id", runID), zap.Any("panic", r))
			if run, err := s.runs.FindByID(runID); err == nil {
				if bot, err := s.bots.Get(botID); err == nil {
					s.finishFailed(bot, run, "panicked", errs.New(errs.ErrInternal.Code, "定时任务执行 panic"))
				}
			}
			s.rt.release(botScheduleSlotKey(runID))
		}
	}()
	run, err := s.runs.FindByID(runID)
	if err != nil || run.FinishedAt != nil || run.SessionKind != model.BotSessionKindSchedule {
		s.rt.release(botScheduleSlotKey(runID))
		return
	}
	bot, err := s.bots.Get(botID)
	if err != nil {
		s.rt.release(botScheduleSlotKey(runID))
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.rt.setCancel(botScheduleSlotKey(runID), cancel)
	func() {
		defer cancel()
		s.botLoop(bot, run, ctx)
	}()
	s.rt.release(botScheduleSlotKey(runID))
}

// ---------- schedule run 收尾（D3/D4：终态与事件同一事务） ----------

// tryFinishScheduleRun 完成前最后一次检查与 completed 落定在同一短事务；
// needs_report 且非静默时同事务追加运行事件（唤醒主线）。
func (s *BotService) tryFinishScheduleRun(bot *model.Bot, run *model.BotRun) bool {
	silent, summary := s.botSilentSummaryOf(bot.ID, run.ID)
	finished := false
	appendedEvent := false
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		// 本次运行自己的异步写还没结算（queued/running）时不收尾：晚到的结果
		// 可能就是失败，是运行结果的一部分。等结算后循环会重建上下文。
		unsettled, err := repository.NewBotToolCallRepository(tx).CountUnsettledByRun(run.ID)
		if err != nil || unsettled > 0 {
			return err
		}
		hasNew, err := repository.NewBotMessageRepository(tx).HasModelInputSinceTx(
			tx, bot.ID, model.BotSessionKindSchedule, botInputRunID(run), run.InputThroughSeq)
		if err != nil || hasNew {
			return err
		}
		now := time.Now().UTC()
		ok, err := repository.NewBotRunRepository(tx).UpdateColumnsWhereTx(tx, run.ID,
			map[string]interface{}{"finished_at": nil, "stop_requested_at": nil},
			map[string]interface{}{
				"status":            model.BotRunStatusCompleted,
				"finished_at":       now,
				"input_through_seq": run.InputThroughSeq,
				"summary":           summary,
				"silent":            silent,
			})
		finished = ok
		if err != nil || !ok {
			return err
		}
		if err := s.appendEventTx(tx, bot.ID, runIDPtr(run.ID), "", model.BotEventDone, botEventEnvelope{
			Kind: "run_done", Phase: string(model.BotRunStatusCompleted),
			Payload: map[string]interface{}{"status": string(model.BotRunStatusCompleted)},
		}); err != nil {
			return err
		}
		// 失败或「需要汇报且非静默」才追加事件；成功静默只记录。任务在运行
		// 期间被删掉时用派发时冻结的「需要汇报」与任务名兜底：这次运行已经
		// 跑完并产出了要汇报的内容，用户也再无法从任务查询看到它。
		if !silent && run.ScheduleTaskID != nil {
			task := model.BotScheduleTask{ID: *run.ScheduleTaskID, Name: botScheduleTaskNameFromContext(run)}
			needsReport := botScheduleNeedsReportFromContext(run)
			var stored model.BotScheduleTask
			if err := tx.Where("id = ?", *run.ScheduleTaskID).First(&stored).Error; err == nil {
				task = stored
				needsReport = stored.NeedsReport
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if needsReport {
				if err := s.appendScheduleEventTx(tx, bot.ID, &task, run,
					model.BotRunStatusCompleted, summary, "", "", now); err != nil {
					return err
				}
				appendedEvent = true
			}
		}
		return nil
	})
	if txErr != nil {
		s.logger.Warn("完成定时任务 run 失败", zap.String("run_id", run.ID), zap.Error(txErr))
		return false
	}
	if finished {
		s.publishEvent(bot.ID)
		if appendedEvent {
			s.wakeScheduleEvent(bot.ID, run.ID, model.BotRunStatusCompleted)
		}
	}
	return finished
}

// finishScheduleFailed 落 failed 终态并同事务追加运行事件（失败必报）。
func (s *BotService) finishScheduleFailed(bot *model.Bot, run *model.BotRun, code string, cause error) {
	now := time.Now().UTC()
	msg := botErrMessage(cause)
	_, summary := s.botSilentSummaryOf(bot.ID, run.ID)
	ok := false
	appendedEvent := false
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		cols := map[string]interface{}{
			"status":        model.BotRunStatusFailed,
			"finished_at":   now,
			"error_code":    code,
			"error_message": msg,
			"summary":       summary,
		}
		runCostPersistCols(run, cols)
		ok, err = repository.NewBotRunRepository(tx).UpdateColumnsWhereTx(tx, run.ID,
			map[string]interface{}{"finished_at": nil, "stop_requested_at": nil}, cols)
		if err != nil || !ok {
			return err
		}
		if err := s.appendEventTx(tx, bot.ID, runIDPtr(run.ID), "", model.BotEventError, botEventEnvelope{
			Kind: "run_error",
			Payload: map[string]interface{}{
				"code": code, "message": msg, "status": string(model.BotRunStatusFailed),
			},
		}); err != nil {
			return err
		}
		if err := s.appendEventTx(tx, bot.ID, runIDPtr(run.ID), "", model.BotEventDone, botEventEnvelope{
			Kind: "run_done", Phase: string(model.BotRunStatusFailed),
			Payload: map[string]interface{}{"status": string(model.BotRunStatusFailed)},
		}); err != nil {
			return err
		}
		// 主线事件只在 run.ScheduleTaskID 可解析到任务时追加。任务在运行失败
		// 之前被删掉时用派发时记在触发上下文里的名字兜底——失败必报，而且用户
		// 已经无法通过任务查询看到这次失败。
		if run.ScheduleTaskID == nil {
			return nil
		}
		var task model.BotScheduleTask
		if err := tx.Where("id = ?", *run.ScheduleTaskID).First(&task).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			task = model.BotScheduleTask{
				ID:   *run.ScheduleTaskID,
				Name: botScheduleTaskNameFromContext(run),
			}
		}
		if err := s.appendScheduleEventTx(tx, bot.ID, &task, run,
			model.BotRunStatusFailed, summary, code, msg, now); err != nil {
			return err
		}
		appendedEvent = true
		return nil
	})
	if txErr != nil {
		s.logger.Warn("落定时任务失败终态", zap.String("run_id", run.ID), zap.Error(txErr))
		return
	}
	if ok {
		s.openProviderCircuitOnFailure(bot, run, code)
		s.publishEvent(bot.ID)
		if appendedEvent {
			s.wakeScheduleEvent(bot.ID, run.ID, model.BotRunStatusFailed)
		}
		return
	}
	// CAS 失败：可能已被受理的停止抢占（错误检出后停止落库）。落 stopped，
	// 否则 run 会永远停在 running + stop_requested，无人再消费，还会让机器人
	// 删除被一直拒绝（与主线 finishFailed 同型兜底）。
	s.finishScheduleStopped(bot, run)
}

// finishScheduleStopped 落 stopped 终态：被取消的 schedule run 不追加主线
// 事件（用户主动停止不是需要汇报的运行结果）。
func (s *BotService) finishScheduleStopped(bot *model.Bot, run *model.BotRun) {
	now := time.Now().UTC()
	_, summary := s.botSilentSummaryOf(bot.ID, run.ID)
	ok := false
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		cols := map[string]interface{}{
			"status":      model.BotRunStatusStopped,
			"finished_at": now,
			"summary":     summary,
		}
		runCostPersistCols(run, cols)
		ok, err = repository.NewBotRunRepository(tx).UpdateColumnsWhereTx(tx, run.ID,
			map[string]interface{}{"finished_at": nil}, cols)
		if err != nil || !ok {
			return err
		}
		return s.appendEventTx(tx, bot.ID, runIDPtr(run.ID), "", model.BotEventDone, botEventEnvelope{
			Kind: "run_done", Phase: string(model.BotRunStatusStopped),
			Payload: map[string]interface{}{"status": string(model.BotRunStatusStopped)},
		})
	})
	if txErr != nil {
		s.logger.Warn("落定时任务停止终态失败", zap.String("run_id", run.ID), zap.Error(txErr))
		return
	}
	if ok {
		s.publishEvent(bot.ID)
	}
}
