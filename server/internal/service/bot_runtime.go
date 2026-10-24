package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// botRuntimeState 维护每机器人的串行执行：一个 bot 同一时刻至多一个主线
// goroutine。数据库是事实来源，激活表只负责「有 run 在跑就别再起新的」；
// 丢失唤醒由轻量扫描补回。
type botRuntimeState struct {
	mu      sync.Mutex
	active  map[string]*botActivation
	started bool

	// memSnaps 是冻结记忆注入缓存：按投影 generation 冻结，压缩发布或
	// 进程重启才刷新（见 frozenMemorySnapshot）。
	memMu    sync.Mutex
	memSnaps map[string]*botFrozenMemory

	// usageBase 是最近一次成功主线请求的实际投影基线（进程内）：用前缀
	// JSON 追加关系与序列化字节差做增量估算；重启或基线失效则整包回退。
	usageMu   sync.Mutex
	usageBase map[string]*botUsageBaseline
}

type botActivation struct {
	cancel context.CancelFunc
	// runID 是当前绑定执行的 run：硬停止取消前核对它，槽被重入换成新 run
	// 时不得取消到别人的 context（INT-137）。空值表示尚未绑定（不取消）。
	runID string
	// requeue 标记执行期间又有唤醒：主线循环退出后由收尾重启。
	requeue bool
	// pending 是重启循环要用的最新触发描述。
	pending *botWake
}

func newBotRuntimeState() *botRuntimeState {
	return &botRuntimeState{
		active:    map[string]*botActivation{},
		memSnaps:  map[string]*botFrozenMemory{},
		usageBase: map[string]*botUsageBaseline{},
	}
}

// acquire 抢占该 bot 的执行槽；已被占（在跑）返回 false——本线程的唤醒会
// 在安全点被看到。
func (rt *botRuntimeState) acquire(botID string, wake *botWake) *botActivation {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if _, busy := rt.active[botID]; busy {
		return nil
	}
	a := &botActivation{pending: wake}
	rt.active[botID] = a
	return a
}

func (rt *botRuntimeState) setCancel(botID string, cancel context.CancelFunc) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if a, ok := rt.active[botID]; ok {
		a.cancel = cancel
	}
}

// bindRun 记录执行槽当前绑定的 run：硬停止取消前按它核对目标（INT-137）。
func (rt *botRuntimeState) bindRun(key, runID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if a, ok := rt.active[key]; ok {
		a.runID = runID
	}
}

// bindSlotRun 把 run 绑定到它所在的执行槽（主线槽 key 是 botID，schedule 槽
// key 是 "schedule:"+runID，与 cancelBotContext 同一映射）：botLoop 每次进入
// 都重绑，槽重入新 run 时同步换绑，硬停止就不会取消到别人的 context。
func (s *BotService) bindSlotRun(bot *model.Bot, run *model.BotRun) {
	key := bot.ID
	if run.SessionKind == model.BotSessionKindSchedule {
		key = botScheduleSlotKey(run.ID)
	}
	s.rt.bindRun(key, run.ID)
}

// markRequeue 执行期间再收到唤醒时置位并记录触发描述。
func (rt *botRuntimeState) markRequeue(botID string, wake *botWake) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if a, ok := rt.active[botID]; ok {
		a.requeue = true
		if wake != nil {
			a.pending = wake
		}
	}
}

// release 释放执行槽；返回 requeue 与待处理的触发描述。
// hasSlot 报告某个执行槽是否被占（扫描补唤醒时跳过已在跑的 run）。
func (rt *botRuntimeState) hasSlot(key string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	_, ok := rt.active[key]
	return ok
}

func (rt *botRuntimeState) release(botID string) (requeue bool, wake *botWake) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	a, ok := rt.active[botID]
	if !ok {
		return false, nil
	}
	requeue = a.requeue
	wake = a.pending
	delete(rt.active, botID)
	return requeue, wake
}

// ---------- 唤醒与启动 ----------

// Start 启动主线调度与后台 worker（在启动恢复之后调用）。
func (s *BotService) Start() {
	s.rt.mu.Lock()
	if s.rt.started {
		s.rt.mu.Unlock()
		return
	}
	s.rt.started = true
	s.rt.mu.Unlock()

	// 经 ensureWorkers 的 workOnce 守卫启动：Start 之前若恢复竞速已入队
	// 工作并触发过一次初始化，这里不得重建 worker 池（会搁浅原 worker、
	// 丢失已登记的取消句柄）。
	s.ensureWorkers()
	s.ensureMaintWorker()
	go s.scanLoop()
	go s.nightlyLoop()
	go s.scheduleLoop()
	s.scanNightlyOnce()
}

// wakeRuntime 唤醒某机器人的主线：有 goroutine 在跑就标记 requeue（安全点
// 会看到新输入），否则为它起一个执行 goroutine。run 的创建在循环内完成。
func (s *BotService) wakeRuntime(botID string, wake botWake) {
	if a := s.rt.acquire(botID, &wake); a == nil {
		s.rt.markRequeue(botID, &wake)
		return
	}
	go s.runBotSlot(botID)
}

// finishRunAfterPanic 把 panic 槽位上仍在 running 的落库 run 置 failed
// 并发 done；CAS 兜底，已终态的 run 不动。scanOnce 对有活动 run 的 bot
// 不再唤醒，不落终态这条 run 会永远挂着并阻塞删除。
func (s *BotService) finishRunAfterPanic(botID string, cause interface{}) {
	run, err := s.runs.FindActiveByBot(botID)
	if err != nil {
		return
	}
	bot, err := s.bots.Get(botID)
	if err != nil {
		return
	}
	s.finishFailed(bot, run, "panicked", errs.New(errs.ErrInternal.Code, fmt.Sprintf("主线执行 panic: %v", cause)))
}

// takePending 取出该槽待处理的触发描述（无则用回退值）。
func (rt *botRuntimeState) takePending(botID string, fallback botWake) botWake {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	a, ok := rt.active[botID]
	if !ok || a.pending == nil {
		return fallback
	}
	wake := *a.pending
	a.pending = nil
	return wake
}

// runBotSlot 是该 bot 的串行执行槽：每次重入都换新的 context——停止后的
// 重启不能用被取消的旧 context。
func (s *BotService) runBotSlot(botID string) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("机器人主线执行 panic", zap.String("bot_id", botID), zap.Any("panic", r))
			// 落库的 run 不能留在 running：scanOnce 对有活动 run 的 bot
			// 不再唤醒，否则这条 run 永远无人收尾、阻塞删除。
			s.finishRunAfterPanic(botID, r)
			// 释放槽；本轮放弃，等下一次唤醒或扫描。
			s.rt.release(botID)
		}
	}()
	wake := s.rt.takePending(botID, botWake{Trigger: model.BotTriggerUserMessage})
	for {
		ctx, cancel := context.WithCancel(context.Background())
		s.rt.setCancel(botID, cancel)
		// cancel 挂在 runBot 的 panic 展开上：主线 panic 也不能泄漏 context。
		func() {
			defer cancel()
			s.runBot(botID, wake, ctx)
		}()
		requeue, pending := s.rt.release(botID)
		if !requeue {
			return
		}
		if pending != nil {
			wake = *pending
		} else {
			wake = s.rt.takePending(botID, wake)
		}
		// 执行期间又有唤醒：重启循环继续消化新输入。
		if s.rt.acquire(botID, nil) == nil {
			return
		}
	}
}

// scanLoop 轻量扫描：补回丢失的唤醒（主线无 run 但有未消费输入）并驱动
// 后台 worker 认领。
func (s *BotService) scanLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.scanOnce()
	}
}

func (s *BotService) scanOnce() {
	// 未消费输入：粗筛（水位落后）后按真实模型输入判定——后台结果等非
	// 输入消息也推高账本水位，裸水位比较会让空闲 bot 永远被扫中空转。
	var bots []model.Bot
	if err := s.db.Where("offered_input_seq < next_message_seq - 1").Find(&bots).Error; err == nil {
		for _, b := range bots {
			if !s.hasUnconsumedInput(&b) {
				continue
			}
			if _, busy := s.activeRunOf(b.ID); !busy {
				s.wakeRuntime(b.ID, botWake{
					Trigger: model.BotTriggerUserMessage,
					Key:     "scan:" + itoa64(b.NextMessageSeq-1),
				})
			}
		}
	}
	// 后台调用：排队的调用推给 worker（重复推送无害，CAS 认领挡住并发）。
	s.scanQueuedCalls()
	// fs_thread/wait（INT-160）：受理即 running 的等待调用由本轮扫描评估——
	// 触发条件命中、启动宽限到期、超时或取消请求，都由评估器统一收尾。
	s.evaluateThreadWaits()
	// 定时任务运行：补回丢失的唤醒——wakeScheduleRun 的瞬态失败或执行槽异常
	// 结束会让 run 永远卡在 running（未完成的 schedule run 还会挡住机器人删除）。
	// 任务允许并行，未完成 run 数量无上限，所以扫完整个集合，但只唤醒「还没有
	// 执行槽」的那些（已占槽的本来就在跑，不需要补）；单轮唤醒数另有上限，
	// 下一轮会自动照顾到剩下的——已唤醒的占了槽，不会再被重复唤醒，因此不会
	// 永远停在同一批最早的 run 上。
	woken := 0
	for _, run := range s.listUnfinishedScheduleRuns() {
		if woken >= botScheduleWakePerScan {
			return
		}
		if s.rt.hasSlot(botScheduleSlotKey(run.ID)) {
			continue
		}
		s.wakeScheduleRun(run.BotID, run.ID)
		woken++
	}
}

// listUnfinishedScheduleRuns 按键集分页取全部未完成的 schedule run（扫完即止，
// 行数上限只是防止异常积累把扫描拖长的安全阀）。分页键是 (created_at, id)，
// 查询期间集合变化不会像 offset 那样跳行。
func (s *BotService) listUnfinishedScheduleRuns() []model.BotRun {
	const pageSize = 100
	const maxRows = 10000
	out := make([]model.BotRun, 0, pageSize)
	var lastCreated time.Time
	lastID := ""
	for len(out) < maxRows {
		q := s.db.Where("finished_at IS NULL AND session_kind = ?", model.BotSessionKindSchedule)
		if !lastCreated.IsZero() {
			q = q.Where("created_at > ? OR (created_at = ? AND id > ?)", lastCreated, lastCreated, lastID)
		}
		var page []model.BotRun
		if err := q.Order("created_at ASC, id ASC").Limit(pageSize).Find(&page).Error; err != nil {
			s.logger.Warn("扫描未完成定时任务失败", zap.Error(err))
			return out
		}
		out = append(out, page...)
		if len(page) < pageSize {
			return out
		}
		lastCreated = page[len(page)-1].CreatedAt
		lastID = page[len(page)-1].ID
	}
	// 触到安全阀：未完成 run 病态积累，更晚的 run 这一轮扫不到。告警而不是
	// 静默截断（与调度排空上限触顶告警对齐）。
	s.logger.Warn("未完成的定时任务运行过多，本轮扫描触到上限",
		zap.Int("scanned", len(out)))
	return out
}

// hasUnconsumedInput 判断 bot 是否确有未交给模型的输入（主线会话内的 user /
// background_result / schedule_event）；账本水位差可能来自非输入消息与
// schedule 会话的消息。
func (s *BotService) hasUnconsumedInput(b *model.Bot) bool {
	hasNew, err := s.messages.HasModelInputSince(b.ID, model.BotSessionKindMain, "", b.OfferedInputSeq)
	return err == nil && hasNew
}

func itoa64(v int64) string {
	return strconv.FormatInt(v, 10)
}

// ---------- 主线 run 生命周期 ----------

// ensureRun 确保存在可执行的活动 run：有就直接用；没有且确有未消费输入时
// 创建（部分唯一索引兜底并发，唯一 (bot_id, trigger_key) 挡重复触发）。
func (s *BotService) ensureRun(bot *model.Bot, wake botWake) (*model.BotRun, bool, error) {
	if run, err := s.runs.FindActiveByBot(bot.ID); err == nil {
		return run, true, nil
	} else if err != gorm.ErrRecordNotFound {
		return nil, false, err
	}

	// 没有未消费输入就不建 run：判断对象是真正的模型输入（user /
	// background_result），不是账本末位——我们自己的 assistant/回复消息也会
	// 推高账本水位，用末位比对会让重复投递的触发产生空跑。
	// recovery 唤醒例外：恢复通知是 lifecycle 消息，不属于模型输入类，
	// HasModelInputSince 看不见它；通知已带 dedup 落库，直接放行建 run，
	// 否则重启后的恢复通知永远到不了模型。
	if wake.Trigger != model.BotTriggerRecovery {
		hasNew, err := s.messages.HasModelInputSince(bot.ID, model.BotSessionKindMain, "", bot.OfferedInputSeq)
		if err != nil {
			return nil, false, err
		}
		if !hasNew {
			return nil, false, nil
		}
	}

	now := time.Now().UTC()
	snapRaw, _ := json.Marshal(botSnapshotOf(bot))
	run := &model.BotRun{
		ID:                 uuid.NewString(),
		BotID:              bot.ID,
		TriggerSource:      wake.Trigger,
		TriggerKey:         wake.Key,
		TriggerContextJSON: wake.Context,
		Status:             model.BotRunStatusRunning,
		SessionKind:        model.BotSessionKindMain,
		ConfigSnapshotJSON: string(snapRaw),
		InputThroughSeq:    0,
		CreatedAt:          now,
		StartedAt:          &now,
	}
	if run.TriggerKey == "" {
		run.TriggerKey = "wake:" + run.ID
	}
	err := s.runs.CreateTx(s.db, run)
	if err != nil {
		// 唯一索引冲突：已有 run（并发唤醒或重复触发），回落为复用活动 run。
		if existing, findErr := s.runs.FindActiveByBot(bot.ID); findErr == nil {
			return existing, true, nil
		}
		return nil, false, err
	}
	s.emitRunEvent(bot.ID, run, model.BotEventDelta, botEventEnvelope{
		Kind: "run_started",
		Payload: map[string]interface{}{
			"trigger":    string(wake.Trigger),
			"created_at": run.CreatedAt.UTC().Format(time.RFC3339Nano),
		},
	})
	return run, true, nil
}

// activeRunOf 返回活动 run 及是否存在。
func (s *BotService) activeRunOf(botID string) (*model.BotRun, bool) {
	run, err := s.runs.FindActiveByBot(botID)
	if err != nil {
		return nil, false
	}
	return run, true
}

// hardStopRun 硬停止主线 run（停止宽限到期或重复请求）。acceptedOrdinal 是
// 受理停止时该 run 的最大模型请求序号；-1 表示跳过进展判断（重复点击的立即
// 硬取消）。「条件写停止标记」在一条 UPDATE 里同时要求「run 未结束」与
// 「受理后没有新请求（NOT EXISTS ordinal > accepted）」——判定与写入原子，
// 宽限期间 run 已带着停止消息发起新请求时落空：用户消息已随新上下文送出，
// 此时硬取消会把停止之后的新消息一起丢掉。命中后把「停止」消息标记为已
// 消费（不这样做，run 落 stopped 后扫描器会因「未消费输入」立刻拉起新 run
// 处理「停止」文本；只推进到停止消息的 seq，之后到达的新消息不受影响），
// 最后取消执行槽 context，循环在安全点落 stopped。
func (s *BotService) hardStopRun(botID, runID string, stopSeq int64, acceptedOrdinal int64) {
	ok, err := s.runs.MarkStopRequested(runID, acceptedOrdinal, stopSeq, time.Now().UTC())
	if err != nil || !ok {
		return
	}
	run, err := s.runs.FindByID(runID)
	if err != nil {
		return
	}
	s.advanceInputWatermarks(run, stopSeq)
	s.cancelBotContext(botID, run)
}

// RequestStop 停止主线：追加一条「停止」消息并唤醒机器人，由它结合上下文
// 决定后续动作（继续、调整或结束）；宽限期（stop_grace）内 run 未自然到达
// 安全点，或已受理过停止仍再次点击，则硬取消当前模型/同步调用 context，
// 循环在安全点落 stopped。不再硬编码为取消全部后台调用或远端线程——那些由
// 机器人按目标决定；真正要结束自身运行时它仍可用 fs_stop。已结束的 run 不
// 接受停止输入（409）。
func (s *BotService) RequestStop(userID, botID, runID string) (*BotRunResponse, error) {
	if _, err := s.bots.FindByIDAndUser(botID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrBotNotFound
		}
		return nil, errs.ErrInternal
	}
	run, err := s.runs.FindByID(runID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrBotRunNotFound
		}
		return nil, errs.ErrInternal
	}
	if run.BotID != botID {
		return nil, errs.ErrBotRunNotFound
	}
	if run.SessionKind != model.BotSessionKindMain {
		// 停止入口只作用于主线：定时任务运行是独立会话，主线「停止」消息它
		// 看不到，也不能在这里改用主线唤醒。
		return nil, errs.New(errs.ErrInvalidParams.Code, "停止入口只作用于主线运行；定时任务运行请等其自行结束")
	}
	if run.FinishedAt != nil {
		return nil, errs.ErrBotRunStateConflict
	}

	// 「run 仍未结束」与「追加停止消息」在同一事务内判定与写入：run 若在
	// 两者之间自然完成，这里直接返回 409，不会留下一条驱动新 run 的陈旧
	// 停止消息。同一 run 重复点击用 client_message_id 幂等，只落一条。
	clientMsgID := "stop:" + runID
	var msgSeq int64
	var already bool
	var acceptedOrdinal int64 = -1
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		fresh, err := repository.NewBotRunRepository(tx).FindByID(runID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrBotRunNotFound
			}
			return errs.ErrInternal
		}
		if fresh.FinishedAt != nil {
			return errs.ErrBotRunStateConflict
		}
		txMsgs := repository.NewBotMessageRepository(tx)
		if existing, findErr := txMsgs.FindByClientMessageID(botID, clientMsgID); findErr == nil {
			msgSeq = existing.Seq
			already = true
			return nil
		} else if !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return errs.ErrInternal
		}
		seq, seqErr := s.bots.NextMessageSeqTx(tx, botID)
		if seqErr != nil {
			return errs.ErrInternal
		}
		// 序号基准在追加停止消息之前、于同一事务内采样：单连接下事务持有唯一
		// 连接，采样之后不会再有请求插进来；而这条消息提交后的新请求要么
		// 包含它（进展），要么是更早构建的上下文（由 stopSeq 条件排除）。
		// 采样失败必须让整个事务失败：-1 表示「跳过进展判断」，拿它兜底会把
		// 一次数据库抖动降级成无条件硬取消，可能丢掉已带上停止消息的请求。
		ord, ordErr := repository.NewBotModelRequestRepository(tx).MaxOrdinalTx(tx, runID)
		if ordErr != nil {
			return errs.ErrInternal
		}
		acceptedOrdinal = ord
		raw, marshalErr := json.Marshal(botUserContent{Text: "停止"})
		if marshalErr != nil {
			return errs.ErrInternal
		}
		msg := &model.BotMessage{
			ID:              uuid.NewString(),
			BotID:           botID,
			Seq:             seq,
			Kind:            model.BotMessageKindUser,
			ContentJSON:     string(raw),
			SessionKind:     model.BotSessionKindMain,
			ClientMessageID: &clientMsgID,
			CreatedAt:       time.Now().UTC(),
		}
		if err := txMsgs.AppendTx(tx, msg); err != nil {
			return errs.ErrInternal
		}
		msgSeq = seq
		return s.appendEventTx(tx, botID, nil, "", model.BotEventDelta, botEventEnvelope{
			Kind:      "user_message",
			MessageID: msg.ID,
			Payload:   botMessageResponseOf(msg),
		})
	})
	if txErr != nil {
		if errors.Is(txErr, errs.ErrBotRunStateConflict) || errors.Is(txErr, errs.ErrBotRunNotFound) {
			return nil, txErr
		}
		return nil, errs.ErrInternal
	}
	if !already {
		// 宽限计时：到时 run 仍未结束则硬取消（acceptedOrdinal 已在追加事务内
		// 采样）。计时器随进程重启丢失可接受——重启恢复本就会把活动 run 置
		// failed。
		s.publishEvent(botID)
		s.wakeRuntime(botID, botWake{
			Trigger: model.BotTriggerUserMessage,
			Key:     fmt.Sprintf("user_message:%d", msgSeq),
			Context: fmt.Sprintf(`{"message_seq":%d,"client_message_id":%q,"kind":"stop"}`, msgSeq, clientMsgID),
		})
		time.AfterFunc(s.cfg.Bots.Runtime.StopGrace(), func() {
			s.hardStopRun(botID, runID, msgSeq, acceptedOrdinal)
		})
	} else {
		// 已受理过停止仍再次点击：不再等宽限、跳过进展判断，立即硬取消。
		s.hardStopRun(botID, runID, msgSeq, -1)
	}

	fresh, err := s.runs.FindByID(runID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	resp := s.botRunResponseWithDisplayWrite(fresh, "request_stop")
	return &resp, nil
}

// cancelBotContext 按 run 的会话解析执行槽并取消（若在跑）：schedule run 的
// 槽 key 是 "schedule:"+runID（见 wakeScheduleRun），主线 run 用 botID。取消
// 前核对槽绑定的 run：槽已被重入换绑成别的 run 时不得取消——硬停止与重入
// 竞速时，取消别人的 context 会丢掉新 run 正在处理的输入（INT-137）。
func (s *BotService) cancelBotContext(botID string, run *model.BotRun) {
	key := botID
	if run.SessionKind == model.BotSessionKindSchedule {
		key = "schedule:" + run.ID
	}
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	a, ok := s.rt.active[key]
	if !ok || a.cancel == nil {
		return
	}
	if a.runID != run.ID {
		// 只有槽明确绑定着同一个 run 才取消：绑定前（空值）与已换绑成新 run
		// 都不能取消——旧停止晚到一步时，绝不能打中别人的 context。
		return
	}
	a.cancel()
}

// ---------- 响应投影 ----------

func rfc3339(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339Nano)
	return &s
}

// BotRunResponse 是 run 的 API 投影；config_snapshot 不含明文 key，原样返回。
type BotRunResponse struct {
	ID               string `json:"id"`
	BotID            string `json:"bot_id"`
	TriggerSource    string `json:"trigger_source"`
	TriggerKey       string `json:"trigger_key"`
	Status           string `json:"status"`
	ConfigSnapshot   string `json:"config_snapshot"`
	InputThroughSeq  int64  `json:"input_through_seq"`
	StopRequested    bool   `json:"stop_requested"`
	PromptTokens     *int64 `json:"prompt_tokens"`
	CompletionTokens *int64 `json:"completion_tokens"`
	TotalTokens      *int64 `json:"total_tokens"`
	CachedTokens     *int64 `json:"cached_tokens"`
	UsageComplete    bool   `json:"usage_complete"`
	// CostEstimate 是本 run 已计价请求的成本累计（USD）；目录无价时为
	// null，前端整项隐藏（缺失 ≠ 0，INT-139）。
	CostEstimate *float64 `json:"cost_estimate"`
	CostSource   *string  `json:"cost_source"`
	// DisplayCostEstimate 是显示口径成本（INT-162）：本 run 已计价成本 +
	// 关联维护运行已计价成本；两者都为 null 时为 null。cost_estimate 列
	// 本身口径不变。
	DisplayCostEstimate *float64 `json:"display_cost_estimate"`
	ErrorCode           *string  `json:"error_code"`
	ErrorMessage        *string  `json:"error_message"`
	CreatedAt           string   `json:"created_at"`
	StartedAt           *string  `json:"started_at"`
	FinishedAt          *string  `json:"finished_at"`
}

// botRunResponseOf run 的 API 投影；maintCost 是关联维护运行的已计价成本
// 合计（无已计价维护传 nil）。显示口径成本在这里与 run 自身成本合成；列表
// 调用方批量取全（displayCostEstimatesOf / MaintenanceCostsByParentRun），
// 不做逐 run 查询。
func botRunResponseOf(run *model.BotRun, maintCost *float64) BotRunResponse {
	return BotRunResponse{
		ID:                  run.ID,
		BotID:               run.BotID,
		TriggerSource:       string(run.TriggerSource),
		TriggerKey:          run.TriggerKey,
		Status:              string(run.Status),
		ConfigSnapshot:      run.ConfigSnapshotJSON,
		InputThroughSeq:     run.InputThroughSeq,
		StopRequested:       run.StopRequestedAt != nil,
		PromptTokens:        run.PromptTokens,
		CompletionTokens:    run.CompletionTokens,
		TotalTokens:         run.TotalTokens,
		CachedTokens:        run.CachedTokens,
		UsageComplete:       run.UsageComplete,
		CostEstimate:        run.CostEstimate,
		CostSource:          run.CostSource,
		DisplayCostEstimate: botSumOptional(run.CostEstimate, maintCost),
		ErrorCode:           run.ErrorCode,
		ErrorMessage:        run.ErrorMessage,
		CreatedAt:           run.CreatedAt.UTC().Format(time.RFC3339Nano),
		StartedAt:           rfc3339(run.StartedAt),
		FinishedAt:          rfc3339(run.FinishedAt),
	}
}

// botRunResponseWithDisplay 单条 run 的投影：现查关联维护成本（单条响应，
// 非列表热路径）。维护成本查询失败返回 error（与列表路径一致），不把失败
// 伪装成「没有维护」。
func (s *BotService) botRunResponseWithDisplay(run *model.BotRun) (BotRunResponse, error) {
	maint, err := s.maintenanceCostsOf([]string{run.ID})
	if err != nil {
		return BotRunResponse{}, err
	}
	return botRunResponseOf(run, maint[run.ID]), nil
}

// botRunResponseWithDisplayWrite 写路径的展示投影降级（INT-162 复审）：
// 写操作在事务提交后才做展示成本聚合，聚合失败不能把已生效的写报成
// 500——降级为 display_cost_estimate=null 并记日志。只读接口仍用严格版。
func (s *BotService) botRunResponseWithDisplayWrite(run *model.BotRun, op string) BotRunResponse {
	resp, err := s.botRunResponseWithDisplay(run)
	if err != nil {
		s.logger.Warn("写路径运行投影的维护成本聚合失败，降级为未计价显示",
			zap.String("op", op), zap.String("run_id", run.ID), zap.Error(err))
		resp = botRunResponseOf(run, nil)
		resp.DisplayCostEstimate = nil
	}
	return resp
}

// BotMessageResponse 是消息的 API 投影；content 原样透传（已是 JSON）。
type BotMessageResponse struct {
	ID        string          `json:"id"`
	BotID     string          `json:"bot_id"`
	Seq       int64           `json:"seq"`
	RunID     *string         `json:"run_id"`
	RequestID *string         `json:"request_id"`
	CallID    *string         `json:"call_id"`
	Kind      string          `json:"kind"`
	Content   json.RawMessage `json:"content"`
	CreatedAt string          `json:"created_at"`
}

func botMessageResponseOf(m *model.BotMessage) BotMessageResponse {
	return BotMessageResponse{
		ID:        m.ID,
		BotID:     m.BotID,
		Seq:       m.Seq,
		RunID:     m.RunID,
		RequestID: m.RequestID,
		CallID:    m.CallID,
		Kind:      string(m.Kind),
		Content:   json.RawMessage(m.ContentJSON),
		CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// ---------- 消息内容形态（ContentJSON 的 kind 专属结构） ----------

type botUserContent struct {
	Text string `json:"text"`
}

type botAssistantToolCall struct {
	CallID             string `json:"call_id"`
	ProviderToolCallID string `json:"provider_tool_call_id"`
	ToolName           string `json:"tool_name"`
	Action             string `json:"action"`
	// Arguments 是规范化后的 JSON 字符串；协议投影原样回传。
	Arguments string `json:"arguments"`
}

type botAssistantContent struct {
	Text      string                 `json:"text"`
	ToolCalls []botAssistantToolCall `json:"tool_calls,omitempty"`
}

// botToolReplyContent 是同步工具回复与异步回执共用的 tool 角色消息正文；
// Content 即发送给模型的协议字符串。
type botToolReplyContent struct {
	CallID             string `json:"call_id"`
	ProviderToolCallID string `json:"provider_tool_call_id"`
	ToolName           string `json:"tool_name"`
	// Action 用于出口围栏的豁免判定（fs_schedule/state_set 这类平台自有回执）；
	// INT-136 之前的存量行没有该字段，按「未豁免」处理（方向安全）。
	Action  string `json:"action,omitempty"`
	Status  string `json:"status"`
	Content string `json:"content"`
}

// botBackgroundResultContent 是后台完成投影出的「平台后台结果」消息。
type botBackgroundResultContent struct {
	CallID                string `json:"call_id"`
	ToolName              string `json:"tool_name"`
	Action                string `json:"action"`
	Status                string `json:"status"`
	ErrorCode             string `json:"error_code,omitempty"`
	ErrorMessage          string `json:"error_message,omitempty"`
	EffectMayHaveOccurred bool   `json:"effect_may_have_occurred,omitempty"`
	Summary               string `json:"summary,omitempty"`
	// Policy 是分级放行（INT-160）的评估结论（tier/rule_id/auto），进模型
	// 上下文与前端渲染共用同一份；历史行为空即缺省。
	Policy json.RawMessage `json:"policy,omitempty"`
	// WaitResult 是 fs_thread/wait 的完整收尾对象（INT-160）：唤醒时带现场
	// 信息（outcome、命中条件、变化前后对比、最近条目摘要），醒来不必再调
	// get。整条 background_result 在上下文里已被围栏，不再内层转义。
	WaitResult json.RawMessage `json:"wait_result,omitempty"`
}

type botDecisionContent struct {
	CallID   string `json:"call_id"`
	Decision string `json:"decision"`
}

type botLifecycleContent struct {
	Event string `json:"event"`
	// Render 是事件投影的渲染标记（INT-137 R6）："user" 表示按 user 角色 +
	// [平台事件] 前缀进请求。缺省为空表示旧消息——部分兼容后端要求 system
	// 只能出现在开头，但不能改写已有会话的前缀（prefix_hash），所以只有新追加
	// 的消息带标记；旧消息保持 system 角色，随压缩归档离开投影。
	Render string          `json:"render,omitempty"`
	Detail json.RawMessage `json:"detail,omitempty"`
}

// ---------- 事件追加与发布 ----------

// botEventEnvelope 是 SSE data 的统一形状。
type botEventEnvelope struct {
	BotID     string      `json:"bot_id"`
	RunID     string      `json:"run_id,omitempty"`
	MessageID string      `json:"message_id,omitempty"`
	CallID    string      `json:"call_id,omitempty"`
	Kind      string      `json:"kind"`
	Phase     string      `json:"phase,omitempty"`
	Payload   interface{} `json:"payload,omitempty"`
}

// botRunSessionKindTx 在事务内解析 run 的会话判别；runID 为空或缺行按主线
// 处理（lifecycle / 用户消息等无 run 事件都属于主线）。
func botRunSessionKindTx(tx *gorm.DB, runID string) string {
	if runID == "" {
		return model.BotSessionKindMain
	}
	var kind string
	if err := tx.Raw("SELECT session_kind FROM bot_runs WHERE id = ?", runID).Scan(&kind).Error; err != nil || kind == "" {
		return model.BotSessionKindMain
	}
	return kind
}

// appendEventTx 在事务内取事件号并落库；调用方提交后自行 publish。会话判别
// 从 run 推导，定时任务 run 的流式事件不会进入聊天页（读者只返回 main）。
func (s *BotService) appendEventTx(tx *gorm.DB, botID string, runID *string, callID string, eventType string, env botEventEnvelope) error {
	seq, err := s.bots.NextEventSeqTx(tx, botID)
	if err != nil {
		return err
	}
	runKey := ""
	if runID != nil {
		runKey = *runID
		env.RunID = *runID
	}
	env.BotID = botID
	env.CallID = callID
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	event := &model.BotEvent{
		BotID:       botID,
		Seq:         seq,
		RunID:       runID,
		EventType:   eventType,
		SessionKind: botRunSessionKindTx(tx, runKey),
		PayloadJSON: string(raw),
		CreatedAt:   time.Now().UTC(),
	}
	return repository.NewBotEventRepository(tx).AppendTx(tx, event)
}

// emitEvent 独立短事务落一条事件并在提交后发布给 SSE 订阅者。
func (s *BotService) emitEvent(botID string, runID *string, callID string, eventType string, env botEventEnvelope) {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		return s.appendEventTx(tx, botID, runID, callID, eventType, env)
	})
	if err != nil {
		s.logger.Warn("追加机器人事件失败", zap.String("bot_id", botID), zap.Error(err))
		return
	}
	s.publishEvent(botID)
}

// emitRunEvent 带 run 的便捷封装。
func (s *BotService) emitRunEvent(botID string, run *model.BotRun, eventType string, env botEventEnvelope) {
	runID := run.ID
	s.emitEvent(botID, &runID, "", eventType, env)
}

// publishEvent 通知所有订阅者有新事件（订阅方回读数据库，按 seq 去重）。
func (s *BotService) publishEvent(botID string) {
	maxSeq, err := s.events.MaxSeq(botID)
	if err != nil {
		return
	}
	s.streamHub.publish(botID, maxSeq)
}

// ---------- SSE 订阅注册表 ----------

// botStreamHub 只传「有新事件、最新 seq 是多少」；订阅方从数据库补读，
// 保证先订阅后补读之间不漏事件，也不把负载挤过内存。
type botStreamHub struct {
	mu   sync.Mutex
	subs map[string]map[chan int64]struct{}
}

func newBotStreamHub() *botStreamHub {
	return &botStreamHub{subs: map[string]map[chan int64]struct{}{}}
}

func (h *botStreamHub) subscribe(botID string) (<-chan int64, func()) {
	ch := make(chan int64, 8)
	h.mu.Lock()
	if h.subs[botID] == nil {
		h.subs[botID] = map[chan int64]struct{}{}
	}
	h.subs[botID][ch] = struct{}{}
	h.mu.Unlock()
	cancel := func() {
		h.mu.Lock()
		if set, ok := h.subs[botID]; ok {
			delete(set, ch)
			if len(set) == 0 {
				delete(h.subs, botID)
			}
		}
		h.mu.Unlock()
	}
	return ch, cancel
}

func (h *botStreamHub) publish(botID string, seq int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[botID] {
		select {
		case ch <- seq:
		default:
			// 订阅方堆积时丢弃信号：它会按 seq 主动补读。
		}
	}
}
