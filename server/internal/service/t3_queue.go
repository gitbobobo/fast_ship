package service

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/api"
)

// 线程队列与活动状态派生（INT-85）。
//
// 口径对齐 T3 客户端 packages/client-runtime/src/state/threadWorkflows.ts 的
// deriveThreadQueueWorkflowState（2026-10 核对）：
//   - activeRun = runs 中状态 ∈ {preparing,starting,running,waiting} 的最近一个
//   - 排队 run = status=="queued" 且其 userMessage 不是自动完成/通知消息
//     （delegatedCompletion / notification 不在用户队列里），按
//     (queuePosition ?? ordinal, ordinal) 升序
//   - isHeld = 任一排队 run queueHeld===true（Stop/重启恢复挂起）
//   - canReorder = 会话 capabilities.turns.supportsQueuedMessages === true
//   - canPromoteToSteer = 活动 run 正在 provider 回合中且会话声明
//     supportsActiveSteering 或 supportsSteeringByInterruptRestart
//
// 会话解析同样对齐 resolveThreadProviderSession：活动 run 的 providerThread →
// 线程的 activeProviderThread → 最近一个未停止会话。

// t3ActiveRunStatuses 是活动 run 状态集（threadWorkflows.ts ACTIVE_RUN_STATUSES）。
var t3ActiveRunStatuses = map[string]bool{
	"preparing": true, "starting": true, "running": true, "waiting": true,
}

// t3SettledRunStatuses 是 run 的已知终态集（INT-158 归档收尾判定用）：只有
// 这些状态能证明回合已结束。「不在活动集」不等于「已收尾」——queued 在排
// 队未执行，未来新增的活动态同样未知；未列出的状态一律按未收尾处理，等宽
// 限兜底。
var t3SettledRunStatuses = map[string]bool{
	"completed": true, "failed": true, "stopped": true,
	"interrupted": true, "cancelled": true, "error": true,
}

// resolveT3ActiveRun 返回活动 run（最近一个活动状态），静止时 nil。
func resolveT3ActiveRun(runs []t3Run) *t3Run {
	for i := len(runs) - 1; i >= 0; i-- {
		if t3ActiveRunStatuses[runs[i].Status] {
			return &runs[i]
		}
	}
	return nil
}

// resolveT3ThreadProviderSession 按 resolveThreadProviderSession 解析线程的
// 当前 provider 会话；找不到返回 nil。
func resolveT3ThreadProviderSession(projection *t3ThreadProjection) *t3ProviderSession {
	activeRun := resolveT3ActiveRun(projection.Runs)
	providerThreadID := projection.Thread.ActiveProviderThreadId
	if activeRun != nil && activeRun.ProviderThreadID != nil && *activeRun.ProviderThreadID != "" {
		providerThreadID = activeRun.ProviderThreadID
	}
	var attached *t3ProviderThread
	if providerThreadID != nil && *providerThreadID != "" {
		for i := range projection.ProviderThreads {
			if projection.ProviderThreads[i].ID == *providerThreadID {
				attached = &projection.ProviderThreads[i]
				break
			}
		}
	}
	if attached == nil {
		for i := range projection.ProviderThreads {
			t := &projection.ProviderThreads[i]
			if t.AppThreadID != nil && *t.AppThreadID == projection.Thread.ID &&
				t.ProviderSessionID != nil && *t.ProviderSessionID != "" {
				attached = t
				break
			}
		}
	}
	if attached != nil && attached.ProviderSessionID != nil && *attached.ProviderSessionID != "" {
		for i := range projection.ProviderSessions {
			if projection.ProviderSessions[i].ID == *attached.ProviderSessionID {
				return &projection.ProviderSessions[i]
			}
		}
		// 上游 resolveThreadProviderSession：session id 非空时 find(...) ?? null，
		// 找不到不回退到别的会话（换过 provider 后 Steer 能力不能串台）。
		return nil
	}
	// 没有挂到线程的 providerThread 时取最近一个未停止会话
	// （与上游 findLast(status!=="stopped" && status!=="error") 一致）。
	for i := len(projection.ProviderSessions) - 1; i >= 0; i-- {
		s := &projection.ProviderSessions[i]
		if s.Status != "stopped" && s.Status != "error" {
			return s
		}
	}
	return nil
}

// deriveT3ThreadQueue 派生排队消息工作流状态。
func deriveT3ThreadQueue(projection *t3ThreadProjection) *T3ThreadQueue {
	state := &T3ThreadQueue{
		Held:       false,
		CanReorder: false,
		CanSteer:   false,
		Items:      []T3ThreadQueueItem{},
	}

	// 自动完成/通知消息不在用户队列（getUserQueuedThreadRuns）。
	automatic := make(map[string]bool, len(projection.Messages))
	for i := range projection.Messages {
		m := &projection.Messages[i]
		if m.DelegatedCompletion != nil || m.NotificationPayload != nil {
			automatic[m.ID] = true
		}
	}

	var queued []t3Run
	held := false
	for i := range projection.Runs {
		run := &projection.Runs[i]
		if run.Status != "queued" || automatic[run.UserMessageID] {
			continue
		}
		if run.QueueHeld != nil && *run.QueueHeld {
			held = true
		}
		queued = append(queued, *run)
	}
	sort.SliceStable(queued, func(i, j int) bool {
		li, lj := t3QueueRank(queued[i]), t3QueueRank(queued[j])
		if li != lj {
			return li < lj
		}
		return queued[i].Ordinal < queued[j].Ordinal
	})

	messagesByID := make(map[string]t3Message, len(projection.Messages))
	for _, m := range projection.Messages {
		messagesByID[m.ID] = m
	}
	for _, run := range queued {
		message, ok := messagesByID[run.UserMessageID]
		// 上游 message?.text ?? "Queued message"：空串合法（只带图片），只有行缺失才用占位。
		text := "Queued message"
		var attachments []T3ChatAttachment
		if ok {
			text = message.Text
			attachments = normalizeT3ChatAttachments(message.Attachments)
		}
		item := T3ThreadQueueItem{
			RunId:       run.ID,
			MessageId:   run.UserMessageID,
			Text:        text,
			Attachments: []T3ChatAttachment{},
		}
		if len(attachments) > 0 {
			item.Attachments = attachments
		}
		state.Items = append(state.Items, item)
	}
	state.Held = held

	session := resolveT3ThreadProviderSession(projection)
	activeRun := resolveT3ActiveRun(projection.Runs)
	if session != nil {
		state.CanReorder = api.Deref(session.Capabilities.Turns.SupportsQueuedMessages)
	}

	// canPromoteToSteer：活动 run 有进行中的 provider 回合，且会话声明支持
	// 活动转向或经中断重启转向。
	steerable := false
	if activeRun != nil && activeRun.Status == "running" && activeRun.ActiveAttemptID != nil {
		for i := range projection.ProviderTurns {
			turn := &projection.ProviderTurns[i]
			if turn.RunAttemptID != nil && *turn.RunAttemptID == *activeRun.ActiveAttemptID && turn.Status == "running" {
				steerable = true
				break
			}
		}
	}
	if steerable && session != nil {
		state.CanSteer = api.Deref(session.Capabilities.Turns.SupportsActiveSteering) ||
			api.Deref(session.Capabilities.Turns.SupportsSteeringByInterruptRestart)
	}
	return state
}

// t3InterruptSignal 是 run_interrupt_request/result 条目里派生停止态所需的最小字段。
type t3InterruptSignal struct {
	RunID *string `json:"runId"`
	Type  string  `json:"type"`
	Ord   int     `json:"ordinal"`
}

// deriveT3ThreadActivity 派生活动摘要。stopping = 活动 run 的最近一条
// run_interrupt_request 晚于同 run 的最近一条 run_interrupt_result
// （Stop 已请求、尚未落定；T3 UI 的「Stopping…」按钮态对应同一事实）。
func deriveT3ThreadActivity(projection *t3ThreadProjection, shell *t3ThreadShell) *T3ThreadActivity {
	activity := &T3ThreadActivity{
		Stopping:           false,
		HasPendingRequests: shell.PendingRuntimeRequest != nil,
	}
	// 占完成位的后台工作数：与状态映射 t3ThreadStatus 同口径，统一走
	// t3TaskHoldsCompletion（awaited=true 的 command 也计入）。
	for _, task := range shell.PendingBackgroundTasks {
		if t3TaskHoldsCompletion(task) {
			activity.PendingBackgroundTaskCount++
		}
	}

	activeRun := resolveT3ActiveRun(projection.Runs)
	if activeRun == nil {
		return activity
	}
	activity.ActiveRunId = api.Ptr(activeRun.ID)
	activity.ActiveRunStatus = api.Ptr(activeRun.Status)

	// 扫投影 turnItems 找该 run 的最新中断请求/结果序号（结构性条目，
	// 不进可见时间线，故从 turnItems 全量读）。
	lastRequest, lastResult := -1, -1
	for _, raw := range projection.TurnItems {
		var signal t3InterruptSignal
		if err := json.Unmarshal(raw, &signal); err != nil {
			continue
		}
		if signal.RunID == nil || *signal.RunID != activeRun.ID {
			continue
		}
		switch signal.Type {
		case "run_interrupt_request":
			if signal.Ord > lastRequest {
				lastRequest = signal.Ord
			}
		case "run_interrupt_result":
			if signal.Ord > lastResult {
				lastResult = signal.Ord
			}
		}
	}
	activity.Stopping = lastRequest > lastResult && lastRequest >= 0
	return activity
}

// t3QueueRank 对齐上游 queuePosition ?? ordinal：缺省位置不能当成 0 排到队首。
func t3QueueRank(run t3Run) int {
	if run.QueuePosition != nil {
		return *run.QueuePosition
	}
	return run.Ordinal
}

// t3HasInterruptibleBackgroundWork 是「可被停止」判定，与上面的
// t3TaskHoldsCompletion「占完成位」是两个独立口径，不得互相套用：占位看
// 「谁会唤醒代理」，可停止看「Stop 还能对什么下手」——一条 awaited 的
// command 占完成位，但 Stop 回退路径不把它当可中断目标。
//
// 对齐上游 interruptThreadTurn（无 threadStop 能力的回退分支）：没有活动
// run 时，用后台工作 roster（不含 PR 链接，即「不含 pullRequests」）决定
// 要不要中断最近 run；command/monitor/subagent/background_task 都在可中断
// 后台之列（run.interrupt 连带取消该 run 挂的后台工作），唯独 PR watch
// 不算——taskId 前缀 pull-request-watch: 的 monitor 由专门的解监听分支
// 处理（thread.pull-request.watch watching=false），不该成为 run.interrupt
// 的理由。Fast Ship 在此之上额外排除 command（dev server 之类的常驻命令
// 不作为回退路径的停止目标），是比上游更窄的本地口径。
func t3HasInterruptibleBackgroundWork(tasks []t3PendingBackgroundTask) bool {
	for _, task := range tasks {
		if task.Kind == "command" {
			continue
		}
		if task.Kind == "monitor" && strings.HasPrefix(task.TaskID, "pull-request-watch:") {
			continue
		}
		return true
	}
	return false
}
