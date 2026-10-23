package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"go.uber.org/zap"
)

// fs_thread/wait（INT-160）：机器人发完指令后挂上等待，线程进入 idle/需要
// 回复/出错时被唤醒，而不是只能等下一轮巡检。建立在两套既有机制上：平台
// 订阅 hub 按「用户 × 环境」维持的内存 ThreadShell（t3_shell_hub.go）与异步
// 调用的「先回执、完成后以后台结果唤醒」管线（bot_tool_runner.go 的
// finalizeCall → wakeForCallResult）。
//
// 关键行为契约：
//   - 受理即 running、不进 queued：ListQueued 会把 queued 调用推给 worker，
//     而 runThreadWrite 没有 wait 实现，会被错误认领。受理置 running 并写
//     started_at，由本文件的评估器调 finalizeCall 收尾（该函数支持从 running
//     做条件更新 CAS）。
//   - 不新增订阅、不新增常驻 goroutine：评估器挂在既有 5s 扫描（scanOnce）
//     上，每轮只读 hub 内存；只在触发或超时那一刻做一次详情读取
//     （s.threads.Detail），成本为每次 wait 一次请求，与事件数量无关。
//   - 全程不出现 watch/watching 字样（避免与 T3 的 PR 监听
//     pull_requests[].watching 混淆），对外统一叫「挂起等待」。

const (
	// botWaitDefaultTimeoutMinutes / botWaitMaxTimeoutMinutes 是等待上限的
	// 缺省与范围（分钟）。
	botWaitDefaultTimeoutMinutes = 10
	botWaitMaxTimeoutMinutes     = 30
	// botWaitScheduleMaxMinutes 是定时任务会话内的等待上限：schedule run 会等
	// 全部异步调用落终态（500ms 轮询，上限 botScheduleCallSettleWait ≈ 20 分钟），
	// wait 超过 15 分钟会吃掉整个结算预算。
	botWaitScheduleMaxMinutes = 15
	// botWaitMaxPerBot 每机器人同时挂起的 wait 上限（防循环）。
	botWaitMaxPerBot = 3
	// botWaitMaxPerThreadHour 同一线程 1 小时内的重挂上限（含重复挂的调用行）。
	botWaitMaxPerThreadHour = 6
	// botWaitRecentItems 唤醒结果携带的最近条目摘要条数。
	botWaitRecentItems = 3
	// botWaitScanLimit 是评估器单轮处理与兜底扫描的行数安全阀（每 bot 并发
	// 上限只有 3，远小于该值）。
	botWaitScanLimit = 64
)

// botWaitAllConditions 是触发条件全集与缺省子集（pr 暂不作为触发条件）；
// botWaitErrorStatusSet 是 error 条件的 run_status 集合——T3 的 shell.status
// 原文取值见 t3_int148_thread_test.go（completed/failed/cancelled/interrupted/
// idle/running），其中 failed 表示最近一次根 run 失败；error 保留给其它
// provider 形态的同义取值。cancelled/interrupted 是主动停止，不算出错。
var (
	botWaitAllConditions  = []string{"idle", "needs_reply", "error"}
	botWaitErrorStatusSet = map[string]bool{"error": true, "failed": true}
)

// botWaitEvalConfig 汇集评估器的时间参数，测试可整体注入小值（扫描节奏本身
// 由测试直接调用 evaluateThreadWaits 驱动，不需要注入间隔）。
type botWaitEvalConfig struct {
	// IdleStable 是 idle 触发的稳定窗：连续观察到 idle 达到该时长才算命中
	//（生产 20s，防「短暂空闲」误唤醒）。
	IdleStable time.Duration
	// StartGrace 是启动宽限：挂上后该时长内线程始终没有开始运行（未观察到
	// ActiveRunID 或新一轮 LatestRunStartedAt）即以 not_started 收尾
	//（生产 60s）。
	StartGrace time.Duration
	// DetailBudget 是收尾时唯一一次详情读取的预算。
	DetailBudget time.Duration
}

func defaultBotWaitEvalConfig() botWaitEvalConfig {
	return botWaitEvalConfig{
		IdleStable:   20 * time.Second,
		StartGrace:   60 * time.Second,
		DetailBudget: 10 * time.Second,
	}
}

// botWaitSnapshot 是挂上时/收尾时的内存观察投影：只含 hub 内存里有的字段
// （queue.held 与条目不在内存里，收尾时由唯一一次详情读取补齐）。
type botWaitSnapshot struct {
	known              bool
	status             string
	runStatus          string
	activeRunID        string
	latestRunID        string
	latestRunStartedAt string
	activity           map[string]interface{}
	pullRequests       []string
}

// botWaitEntry 是一条活跃 wait 的评估现场（进程内存驻，数据库调用行是事实
// 来源；进程重启后由启动恢复统一落 server_restarted 终态，不恢复评估）。
type botWaitEntry struct {
	callID     string
	botID      string
	userID     string
	envID      string
	threadID   string
	conditions []string
	timeout    time.Duration

	attachedAt time.Time
	deadline   time.Time

	baseline botWaitSnapshot
	// started 一经置位不复位：触发条件只在观察到线程确实开始运行后才评估，
	// 否则「挂上时本来就 idle」的线程会在 20s 稳定窗后抢在 not_started 之前
	// 以 idle 命中，掩盖「根本没开始」的真相。
	started bool
	// firstIdleAt 是连续 idle 的起点（每次观察到非 idle 即清零）。
	firstIdleAt time.Time
	// ticks/notReadyTicks 是评估轮数与 hub 视图非 Ready 的轮数：收尾时按占比
	// 判定这次等待是否可信。
	ticks         int
	notReadyTicks int
}

// ---------- 参数解析 ----------

// botWaitParseOn 归一化触发条件子集：逗号分隔、去空白；空缺省全集合
// （idle/needs_reply/error）；未知取值报错（pr 暂不作为触发条件）。
func botWaitParseOn(on string) ([]string, error) {
	on = strings.TrimSpace(on)
	if on == "" {
		return append([]string(nil), botWaitAllConditions...), nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, 3)
	for _, part := range strings.Split(on, ",") {
		name := strings.TrimSpace(strings.ToLower(part))
		if name == "" {
			continue
		}
		switch name {
		case "idle", "needs_reply", "error":
		default:
			return nil, errs.New(errs.ErrInvalidParams.Code,
				fmt.Sprintf("on 不支持触发条件 %s（可选 idle/needs_reply/error，逗号分隔）", name))
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return append([]string(nil), botWaitAllConditions...), nil
	}
	return out, nil
}

// botWaitParseTimeout 归一化等待上限：0 视为缺省 10 分钟；1–30 之外报错
// （定时任务会话内的 15 分钟上限由受理路径按会话判别单独校验）。
func botWaitParseTimeout(minutes int) (time.Duration, error) {
	if minutes == 0 {
		minutes = botWaitDefaultTimeoutMinutes
	}
	if minutes < 1 || minutes > botWaitMaxTimeoutMinutes {
		return 0, errs.New(errs.ErrInvalidParams.Code,
			fmt.Sprintf("timeout_minutes 取值 1-%d（缺省 %d）", botWaitMaxTimeoutMinutes, botWaitDefaultTimeoutMinutes))
	}
	return time.Duration(minutes) * time.Minute, nil
}

// botThreadWaitParams 受理校验入口：从原始参数解析并校验 on/timeout_minutes。
func botThreadWaitParams(raw map[string]json.RawMessage) ([]string, time.Duration, error) {
	var args botThreadArgs
	if len(raw) > 0 {
		// 只取两个字段，未知字段已由字段白名单拒绝；解析失败按参数错误处理。
		if v, ok := raw["on"]; ok {
			_ = json.Unmarshal(v, &args.On)
		}
		if v, ok := raw["timeout_minutes"]; ok {
			if err := json.Unmarshal(v, &args.TimeoutMinutes); err != nil {
				return nil, 0, errs.New(errs.ErrInvalidParams.Code, "timeout_minutes 需要是整数分钟")
			}
		}
	}
	conditions, err := botWaitParseOn(args.On)
	if err != nil {
		return nil, 0, err
	}
	timeout, err := botWaitParseTimeout(args.TimeoutMinutes)
	if err != nil {
		return nil, 0, err
	}
	return conditions, timeout, nil
}

// ---------- 受理端限额与注册 ----------

// botWaitArgsOfCall 从调用账本行解析 wait 参数（受理时已校验，失败按无参数
// 处理，只影响限额计数的匹配精度）。
func botWaitArgsOfCall(call *model.BotToolCall) botThreadArgs {
	var args botThreadArgs
	_ = json.Unmarshal([]byte(call.ArgumentsJSON), &args)
	return args
}

// threadWaitAcceptance 是 wait 的受理端防循环限额：返回已存在的同线程 wait
// 调用 ID（重复挂不新建，调用方立即以 already_waiting 落终态）；超出限额
// 返回 40001。重复挂优先返回已有调用，不再累计限额（重复挂写下的
// already_waiting 账本行也计入 1 小时额度，限额判定始终发生在写行之前）。
// extraRunning 是同批已受理的 wait 数——限额只查已提交行会漏掉同批成员，
// 由调用方把它们计入。查询在 SQL 侧按 bot（及目标线程）过滤：全局 LIMIT
// 扫描被其他机器人的行占满时，本 bot 会被误判为 0。
func (s *BotService) threadWaitAcceptance(botID, envID, threadID string, extraRunning int) (string, error) {
	running, err := s.calls.ListRunningWaitCallsByBot(botID, botWaitScanLimit)
	if err != nil {
		return "", errs.ErrInternal
	}
	duplicate := ""
	for i := range running {
		c := &running[i]
		args := botWaitArgsOfCall(c)
		if args.EnvID == envID && args.ThreadID == threadID && duplicate == "" {
			duplicate = c.ID
		}
	}
	if duplicate != "" {
		return duplicate, nil
	}
	if len(running)+extraRunning >= botWaitMaxPerBot {
		return "", errs.New(errs.ErrInvalidParams.Code,
			fmt.Sprintf("同时挂起的 wait 已达上限（%d 个），请等待现有等待结束或用 fs_cancel 取消后再挂；已达上限，请向用户汇报", botWaitMaxPerBot))
	}
	sameThread, err := s.calls.CountWaitCallsByThreadSince(botID, envID, threadID, time.Now().UTC().Add(-time.Hour))
	if err != nil {
		return "", errs.ErrInternal
	}
	if sameThread >= botWaitMaxPerThreadHour {
		return "", errs.New(errs.ErrInvalidParams.Code,
			fmt.Sprintf("同一线程 1 小时内最多挂 %d 次 wait，已达上限，请向用户汇报", botWaitMaxPerThreadHour))
	}
	return "", nil
}

// threadWaitAttach 在受理事务提交后注册评估现场：基线取当前 hub 内存快照
// （不读详情——queue.held 与条目只在收尾那一刻读取一次）。
func (s *BotService) threadWaitAttach(bot *model.Bot, call *model.BotToolCall, conditions []string, timeout time.Duration) {
	if s.threads == nil {
		return
	}
	args := botWaitArgsOfCall(call)
	if args.EnvID == "" || args.ThreadID == "" {
		return
	}
	now := time.Now().UTC()
	entry := &botWaitEntry{
		callID:     call.ID,
		botID:      bot.ID,
		userID:     bot.UserID,
		envID:      args.EnvID,
		threadID:   args.ThreadID,
		conditions: conditions,
		timeout:    timeout,
		attachedAt: now,
		deadline:   now.Add(timeout),
	}
	if view, obs := s.waitObserve(bot.UserID, args.EnvID, args.ThreadID); view != nil {
		entry.ticks = 1
		if view.State != t3ShellReady {
			entry.notReadyTicks = 1
		}
		entry.baseline = obs
		// 挂上时线程已在跑：启动宽限不适用，触发条件立即可评估。
		if waitObsRunning(obs) {
			entry.started = true
		}
	}
	s.waitMu.Lock()
	if s.waits == nil {
		s.waits = map[string]*botWaitEntry{}
	}
	s.waits[call.ID] = entry
	s.waitMu.Unlock()
}

// waitDrop 摘除已收尾的评估现场。
func (s *BotService) waitDrop(callID string) {
	s.waitMu.Lock()
	delete(s.waits, callID)
	s.waitMu.Unlock()
}

// waitEntrySnapshot 复制当前活跃评估现场（评估在锁外进行，收尾各自落库）。
func (s *BotService) waitEntrySnapshot() []*botWaitEntry {
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	out := make([]*botWaitEntry, 0, len(s.waits))
	for _, e := range s.waits {
		out = append(out, e)
	}
	return out
}

// waitRegistered 报告某调用是否已在注册表。
func (s *BotService) waitRegistered(callID string) bool {
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	_, ok := s.waits[callID]
	return ok
}

// ---------- 评估（挂在既有 5s 扫描上） ----------

// evaluateThreadWaits 评估全部活跃 wait：先兜底收编注册表缺失的 running
// wait（受理与注册之间异常丢失的行不能永远无人评估，按首次观察到的快照
// 补建基线），再逐条评估。由 scanOnce 每轮调用。
func (s *BotService) evaluateThreadWaits() {
	if s.threads == nil {
		return
	}
	s.waitAdoptUnregistered()
	for _, e := range s.waitEntrySnapshot() {
		s.evaluateThreadWait(e)
	}
}

// waitAdoptUnregistered 把数据库里 running 的 fs_thread/wait 调用收进注册表：
// 注册基线取首次观察到的快照（与受理时注册的差异只在基线采集时点）。
func (s *BotService) waitAdoptUnregistered() {
	running, err := s.calls.ListRunningWaitCalls(botWaitScanLimit)
	if err != nil {
		return
	}
	for i := range running {
		call := &running[i]
		if s.waitRegistered(call.ID) {
			continue
		}
		args := botWaitArgsOfCall(call)
		if args.EnvID == "" || args.ThreadID == "" {
			continue
		}
		conditions, condErr := botWaitParseOn(args.On)
		timeout, timeoutErr := botWaitParseTimeout(args.TimeoutMinutes)
		if condErr != nil || timeoutErr != nil {
			continue
		}
		attachedAt := nowOr(call.StartedAt, &call.CreatedAt)
		entry := &botWaitEntry{
			callID:     call.ID,
			botID:      call.BotID,
			userID:     s.botUserIDOf(call.BotID),
			envID:      args.EnvID,
			threadID:   args.ThreadID,
			conditions: conditions,
			timeout:    timeout,
			attachedAt: attachedAt,
			deadline:   attachedAt.Add(timeout),
		}
		if view, obs := s.waitObserve(entry.userID, entry.envID, entry.threadID); view != nil {
			entry.ticks = 1
			if view.State != t3ShellReady {
				entry.notReadyTicks = 1
			}
			entry.baseline = obs
			if waitObsRunning(obs) {
				entry.started = true
			}
		}
		s.waitMu.Lock()
		if _, exists := s.waits[call.ID]; exists {
			// 锁内再判一次：注册表检查与写入之间 threadWaitAttach 可能刚把
			// 受理时的基线挂上，无条件覆盖会丢掉那个更早的基线。
			s.waitMu.Unlock()
			continue
		}
		s.waits[call.ID] = entry
		s.waitMu.Unlock()
	}
}

func nowOr(preferred, fallback *time.Time) time.Time {
	if preferred != nil && !preferred.IsZero() {
		return preferred.UTC()
	}
	if fallback != nil {
		return fallback.UTC()
	}
	return time.Now().UTC()
}

// waitObserve 读取 hub 内存里该线程的当前快照：view 为 nil 表示环境不可读；
// obs.known=false 表示可信面上没有该线程。
func (s *BotService) waitObserve(userID, envID, threadID string) (*t3ShellView, botWaitSnapshot) {
	view, err := s.threads.PeekShell(userID, envID)
	if err != nil || view == nil {
		return view, botWaitSnapshot{}
	}
	if view.Snaps == nil || view.State != t3ShellReady {
		return view, botWaitSnapshot{}
	}
	shell, _, ok := view.Snaps.findThread(threadID)
	if !ok {
		return view, botWaitSnapshot{}
	}
	return view, botWaitSnapshotOf(shell)
}

// botWaitSnapshotOf 把 shell 投影成可对比的快照（挂上基线与收尾对比共用同一
// 投影，字段变化才进结果的 changes）。
func botWaitSnapshotOf(shell *t3ThreadShell) botWaitSnapshot {
	obs := botWaitSnapshot{
		known:        true,
		status:       string(t3ThreadStatus(shell)),
		runStatus:    shell.Status,
		activity:     botWaitShellActivity(shell),
		pullRequests: botWaitPullRequestKeys(shell),
	}
	if shell.ActiveRunID != nil {
		obs.activeRunID = *shell.ActiveRunID
	}
	if shell.LatestRunID != nil {
		obs.latestRunID = *shell.LatestRunID
	}
	if shell.LatestRunStartedAt != nil {
		obs.latestRunStartedAt = *shell.LatestRunStartedAt
	}
	return obs
}

// waitObsRunning 报告快照此刻是否处于 running 语义：根 run 在跑，或根
// run 已结束但后台任务/subagent 仍把投影撑为 running。挂上、收编与评估
// 器共用同一判定——只认 ActiveRunID 会把「挂上时靠后台任务撑着 running、
// 扫描前转 idle」的线程误报成 not_started。
func waitObsRunning(obs botWaitSnapshot) bool {
	return obs.activeRunID != "" ||
		obs.status == string(api.T3ThreadSummaryStatusRunning)
}

// botWaitShellActivity 从内存 shell 投影运行活动摘要（与 botSlimThreadActivity
// 的字段对齐；stopping 只存在于详情派生活动里，内存快照不带）。
func botWaitShellActivity(shell *t3ThreadShell) map[string]interface{} {
	out := map[string]interface{}{}
	if shell.ActiveRunID != nil && *shell.ActiveRunID != "" {
		out["active_run_id"] = *shell.ActiveRunID
	}
	if shell.ActivityRunStatus != nil && *shell.ActivityRunStatus != "" {
		out["active_run_status"] = *shell.ActivityRunStatus
	}
	if shell.PendingRuntimeRequest != nil {
		out["has_pending_requests"] = true
	}
	bg := 0
	for _, task := range shell.PendingBackgroundTasks {
		if task.Kind != "command" {
			bg++
		}
	}
	if bg > 0 {
		out["pending_background_task_count"] = bg
	}
	return out
}

// botWaitPullRequestKeys 把 PR 状态压成稳定排序的 "repo#N=state" 串数组，
// 前后对比按数组相等判定（snapshot 未同步时 state 缺省记空）。
func botWaitPullRequestKeys(shell *t3ThreadShell) []string {
	prs := normalizeT3PullRequests(shell.PullRequests)
	out := make([]string, 0, len(prs))
	for _, pr := range prs {
		state := ""
		if pr.State != nil {
			state = *pr.State
		}
		out = append(out, fmt.Sprintf("%s#%d=%s", pr.Repo, pr.Number, state))
	}
	sort.Strings(out)
	return out
}

// waitEqualActivity 比较两份活动投影（map 序列化后按字节比较，避免 nil/空
// map 语义分歧）。
func waitEqualActivity(a, b map[string]interface{}) bool {
	return marshalJSONString(a) == marshalJSONString(b)
}

// evaluateThreadWait 单条 wait 的一轮评估：取消 → 启动宽限 → 触发 → 超时，
// 收尾统一走 finalizeCall（CAS 落终态并以后台结果唤醒所属会话）。
func (s *BotService) evaluateThreadWait(e *botWaitEntry) {
	call, err := s.calls.FindByID(e.callID)
	if err != nil {
		s.waitDrop(e.callID)
		return
	}
	if call.Status.IsTerminal() {
		s.waitDrop(e.callID)
		return
	}
	// 取消专门分支：cancelBotCall 对 running 只记 cancel_requested_at（wait
	// 没有 worker context 可取消），评估器在这里落 cancelled 终态。
	if call.CancelRequestedAt != nil {
		s.waitResolveCancelled(e, call)
		return
	}

	now := time.Now().UTC()
	view, obs := s.waitObserve(e.userID, e.envID, e.threadID)
	e.ticks++
	ready := view != nil && view.State == t3ShellReady
	if !ready {
		e.notReadyTicks++
		// 不可信轮次不累计 idle 稳定窗：盲区之后重见的 idle 不能接着盲区前
		// 的计时立刻命中。
		e.firstIdleAt = time.Time{}
	}
	if ready {
		if !obs.known {
			e.firstIdleAt = time.Time{}
			// 可信面上没有该线程且归档面可信：线程已不存在，等待失去意义。
			if view.Snaps != nil && view.Snaps.ArchiveErr == nil {
				s.waitResolveThreadGone(e, call)
				return
			}
			// 归档面不可信（G1）：不能断言不存在，按不可信窗口处理。
			e.notReadyTicks++
		} else {
			if !e.baseline.known {
				e.baseline = obs
				if waitObsRunning(obs) {
					e.started = true
				}
			}
			// 开始运行的观察：线程此刻处于 running 语义（ActiveRunID 在跑，
			// 或根 run 已结束但后台任务/subagent 仍把投影撑为 running——
			// botWaitSnapshotOf 的 status 复用 t3ThreadStatus），或最新 run
			// 换成了新的一轮（LatestRunID / LatestRunStartedAt 任一变化）。
			if waitObsRunning(obs) ||
				(obs.latestRunID != "" && obs.latestRunID != e.baseline.latestRunID) ||
				(obs.latestRunStartedAt != "" && obs.latestRunStartedAt != e.baseline.latestRunStartedAt) {
				e.started = true
			}
			if e.started {
				if matched, ok := s.waitMatchConditions(e, obs, now, s.waitTrusted(e, view)); ok {
					s.waitResolveTriggered(e, call, obs, matched)
					return
				}
			} else if now.Sub(e.attachedAt) >= s.waitCfg.StartGrace {
				// 启动宽限到期仍未开始：无法可信观察时按 watch_degraded，
				// 否则如实落 not_started。
				s.waitResolveNotStarted(e, call, view)
				return
			}
		}
	}
	if now.After(e.deadline) {
		s.waitResolveTimeout(e, call, view)
	}
}

// waitMatchConditions 评估触发条件：error 优先于 needs_reply 优先于 idle（同一
// 轮命中多个时报最紧急的一个）；idle 需连续稳定 IdleStable 才算命中，且整体
// 观察可信（trusted）——不可信轮次攒出的稳定窗不得直接放行。
func (s *BotService) waitMatchConditions(e *botWaitEntry, obs botWaitSnapshot, now time.Time, trusted bool) ([]string, bool) {
	has := func(name string) bool {
		for _, c := range e.conditions {
			if c == name {
				return true
			}
		}
		return false
	}
	if has("error") && botWaitErrorStatusSet[strings.ToLower(obs.runStatus)] {
		return []string{"error"}, true
	}
	if has("needs_reply") && obs.activity["has_pending_requests"] == true {
		return []string{"needs_reply"}, true
	}
	if has("idle") && obs.status == string(api.T3ThreadSummaryStatusIdle) {
		if !trusted {
			return nil, false
		}
		if e.firstIdleAt.IsZero() {
			e.firstIdleAt = now
		}
		if now.Sub(e.firstIdleAt) >= s.waitCfg.IdleStable {
			return []string{"idle"}, true
		}
	} else {
		e.firstIdleAt = time.Time{}
	}
	return nil, false
}

// waitResolveCancelled 落 cancelled 终态：wait 无任何副作用，effect 恒 false
// （finalizeCall 对 wait 不再因取消标记强制翻 true）。
func (s *BotService) waitResolveCancelled(e *botWaitEntry, call *model.BotToolCall) {
	raw := marshalJSONString(map[string]interface{}{
		"outcome":     "cancelled",
		"waiting_for": e.conditions,
		"attached_at": rfc3339(&e.attachedAt),
		"resolved_at": rfc3339(nowPtr()),
	})
	s.waitFinalize(e, call, model.BotCallStatusCancelled, raw, nil)
}

// waitResolveNotStarted 启动宽限到期仍未观察到线程开始运行。监听占比过大或
// 收尾时视图非 Ready 时改判 watch_degraded——「没开始」是一个强断言，观察
// 不可信时不得下结论。
func (s *BotService) waitResolveNotStarted(e *botWaitEntry, call *model.BotToolCall, view *t3ShellView) {
	outcome := "not_started"
	note := "启动宽限期内线程始终没有开始运行（未观察到活动 run 或新一轮运行）"
	if !s.waitTrusted(e, view) {
		outcome = "watch_degraded"
		note = "启动宽限期内未能可信观察线程状态，无法确认线程是否开始运行"
	}
	raw := marshalJSONString(map[string]interface{}{
		"outcome":     outcome,
		"waiting_for": e.conditions,
		"attached_at": rfc3339(&e.attachedAt),
		"resolved_at": rfc3339(nowPtr()),
		"note":        note,
	})
	s.waitFinalize(e, call, model.BotCallStatusSucceeded, raw, nil)
}

// waitResolveThreadGone 可信面（活跃+归档，归档可信）上线程已不存在。
func (s *BotService) waitResolveThreadGone(e *botWaitEntry, call *model.BotToolCall) {
	raw := marshalJSONString(map[string]interface{}{
		"outcome":     "thread_gone",
		"waiting_for": e.conditions,
		"attached_at": rfc3339(&e.attachedAt),
		"resolved_at": rfc3339(nowPtr()),
		"note":        "线程已不在（已删除或不可见）",
	})
	s.waitFinalize(e, call, model.BotCallStatusSucceeded, raw, nil)
}

// waitResolveTriggered 触发条件命中：做唯一一次详情读取补 queue.held 与最近
// 条目摘要后落 succeeded。详情读取失败不推翻命中事实（条件是在可信视图上
// 观察到的），只省略对应字段并在 note 说明。
func (s *BotService) waitResolveTriggered(e *botWaitEntry, call *model.BotToolCall, obs botWaitSnapshot, matched []string) {
	note := ""
	payload := map[string]interface{}{
		"outcome":         "triggered",
		"waiting_for":     e.conditions,
		"matched":         matched,
		"attached_at":     rfc3339(&e.attachedAt),
		"resolved_at":     rfc3339(nowPtr()),
		"timeout_minutes": int(e.timeout.Minutes()),
		"changes":         botWaitChanges(e.baseline, obs),
	}
	detail, _ := s.waitDetail(e)
	if detail != nil {
		payload["queue_held"] = detail.held
		payload["item_count"] = detail.itemCount
		payload["new_items"] = detail.newItems
		payload["recent_items"] = detail.recent
	} else {
		note = "详情读取失败，queue 与最近条目摘要缺省；线程状态以 hub 内存视图为准"
	}
	if note != "" {
		payload["note"] = note
	}
	s.waitFinalize(e, call, model.BotCallStatusSucceeded, marshalJSONString(payload), matched)
}

// waitResolveTimeout 超时收尾：必须再做一次权威读取（hub 视图 + 详情）与挂上
// 时基线对比，区分「没动」（timeout_unchanged）与「在动但未到终态」
// （timeout_progressing）；监听不可信或权威读取失败时如实报 watch_degraded，
// 不把「没听到」说成「没动」。
func (s *BotService) waitResolveTimeout(e *botWaitEntry, call *model.BotToolCall, view *t3ShellView) {
	// 权威读取：重新取一次视图（不沿用本轮早前的观察）。
	freshView, obs := s.waitObserve(e.userID, e.envID, e.threadID)
	if freshView != nil {
		view = freshView
	}
	payload := map[string]interface{}{
		"waiting_for":     e.conditions,
		"attached_at":     rfc3339(&e.attachedAt),
		"resolved_at":     rfc3339(nowPtr()),
		"timeout_minutes": int(e.timeout.Minutes()),
	}
	if !s.waitTrusted(e, view) {
		payload["outcome"] = "watch_degraded"
		payload["note"] = "等待期间线程视图长时间不可读或收尾时仍不可读，无法给出线程是否变化的可信结论"
		s.waitFinalize(e, call, model.BotCallStatusSucceeded, marshalJSONString(payload), nil)
		return
	}
	if !obs.known {
		if view.Snaps != nil && view.Snaps.ArchiveErr == nil {
			payload["outcome"] = "thread_gone"
			payload["note"] = "线程已不在（已删除或不可见）"
			s.waitFinalize(e, call, model.BotCallStatusSucceeded, marshalJSONString(payload), nil)
			return
		}
		payload["outcome"] = "watch_degraded"
		payload["note"] = "收尾时线程视图不可读，无法给出线程是否变化的可信结论"
		s.waitFinalize(e, call, model.BotCallStatusSucceeded, marshalJSONString(payload), nil)
		return
	}

	detail, detailErr := s.waitDetail(e)
	// 进展证据（INT-160 复审）：字段前后对比只是证据之一——同一 run 持续产出
	// 条目时 status/run_status/activity/PRs 全都不变，latest_run 身份变化与
	// 等待期间更新过的条目数同样计入。
	changed := len(botWaitChanges(e.baseline, obs)) > 0 ||
		(obs.latestRunID != "" && obs.latestRunID != e.baseline.latestRunID)
	if detail != nil && detail.newItems > 0 {
		changed = true
	}
	switch {
	case detailErr != nil:
		// 权威读取（详情）失败：无法完成「挂上 vs 现在」的完整对比，不把
		// 「没读到」说成「没动」。
		payload["outcome"] = "watch_degraded"
		payload["note"] = "收尾详情读取失败，无法完成权威对比；线程状态以 hub 内存视图为准"
	case changed:
		payload["outcome"] = "timeout_progressing"
		payload["note"] = "等待期间线程有更新但未命中触发条件（未到终态）"
	default:
		payload["outcome"] = "timeout_unchanged"
		payload["note"] = "监听可信、线程确实无变化"
		if detail != nil && detail.untimestampedItems > 0 {
			// 部分条目缺更新时间戳时 newItems 保守不计，证据不完整：note 说明
			// 证据范围，不把「没看到时间戳」说成「必然没动」。
			payload["note"] = "监听可信、可见字段与可计时条目均无变化；部分条目缺更新时间戳，进展判断以可见证据为准"
		}
	}
	if detail != nil {
		payload["changes"] = botWaitChanges(e.baseline, obs)
		payload["queue_held"] = detail.held
		payload["item_count"] = detail.itemCount
		payload["new_items"] = detail.newItems
		payload["recent_items"] = detail.recent
	}
	s.waitFinalize(e, call, model.BotCallStatusSucceeded, marshalJSONString(payload), nil)
}

// waitTrusted 报告这次等待的观察是否可信：视图 Ready 且非 Ready 轮数占比
// 未过半。占比过大或收尾仍非 Ready 时超时/未启动结论一律降级为
// watch_degraded。
func (s *BotService) waitTrusted(e *botWaitEntry, view *t3ShellView) bool {
	if view == nil || view.State != t3ShellReady {
		return false
	}
	return e.notReadyTicks*2 <= e.ticks
}

// botWaitDetailInfo 是收尾详情读取补齐的字段（queue.held 与条目不在 hub
// 内存里）。newItems 是等待期间更新过的条目数（updated_at 晚于挂上时刻；
// 上游缺 updated_at 的条目不计入，数量记在 untimestampedItems 供证据范围
// 说明）。
type botWaitDetailInfo struct {
	held               bool
	itemCount          int
	newItems           int
	untimestampedItems int
	recent             []map[string]string
}

// waitDetail 收尾时唯一一次详情读取；失败返回 (nil, err)。
func (s *BotService) waitDetail(e *botWaitEntry) (*botWaitDetailInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.waitCfg.DetailBudget)
	defer cancel()
	detail, err := s.threads.Detail(ctx, e.userID, e.envID, e.threadID)
	if err != nil {
		return nil, err
	}
	info := &botWaitDetailInfo{
		held:      detail.Queue.Held,
		itemCount: len(detail.Items),
		recent:    make([]map[string]string, 0, botWaitRecentItems),
	}
	// 条目按时间升序（botSlimThreadDetail 的约定），取末尾 N 条。
	start := len(detail.Items) - botWaitRecentItems
	if start < 0 {
		start = 0
	}
	for i := start; i < len(detail.Items); i++ {
		item := &detail.Items[i]
		entry := map[string]string{
			"id":   item.Id,
			"type": string(item.Type),
		}
		if item.Summary != "" {
			entry["summary"] = truncateBotRunes(item.Summary, botThreadTextPreview)
		} else if item.Text != "" {
			entry["summary"] = truncateBotRunes(firstT3Line(item.Text), botThreadTextPreview)
		}
		info.recent = append(info.recent, entry)
	}
	// 等待期间的条目更新数：条目 updated_at 晚于挂上时刻即计入（基线不在
	// 内存里，用时间戳代替计数差值；上游缺 updated_at 的条目保守不计）。
	for i := range detail.Items {
		if u := detail.Items[i].UpdatedAt; u != nil && *u != "" {
			if ts, perr := time.Parse(time.RFC3339, *u); perr == nil && ts.After(e.attachedAt) {
				info.newItems++
			}
		} else {
			info.untimestampedItems++
		}
	}
	return info, nil
}

// botWaitFieldChange 是单个字段的前后对比。
type botWaitFieldChange struct {
	Before interface{} `json:"before"`
	After  interface{} `json:"after"`
}

// botWaitChanges 对比基线与当前快照：只列有变化的字段（status/run_status/
// activity/pull_requests）。queue.held 不在内存基线里，由收尾详情读取以
// queue_held 单字段给出。
func botWaitChanges(before, after botWaitSnapshot) map[string]interface{} {
	if !before.known || !after.known {
		return nil
	}
	changes := map[string]interface{}{}
	if before.status != after.status {
		changes["status"] = botWaitFieldChange{Before: before.status, After: after.status}
	}
	if before.runStatus != after.runStatus {
		changes["run_status"] = botWaitFieldChange{Before: before.runStatus, After: after.runStatus}
	}
	if !waitEqualActivity(before.activity, after.activity) {
		changes["activity"] = botWaitFieldChange{Before: before.activity, After: after.activity}
	}
	if marshalJSONString(before.pullRequests) != marshalJSONString(after.pullRequests) {
		changes["pull_requests"] = botWaitFieldChange{Before: before.pullRequests, After: after.pullRequests}
	}
	if len(changes) == 0 {
		return nil
	}
	return changes
}

// waitFinalize 统一收尾：CAS 落终态、写后台结果（wait_result 携带完整结构化
// 结果）并唤醒所属会话；CAS 失败说明终态已被取消/恢复路径落定，迟到返回
// 不覆盖。先读机器人再摘注册表条目：读取失败时条目保留，下一轮评估器重试
// 收尾——先摘再读会留下 running 孤儿，只能等兜底收编。
func (s *BotService) waitFinalize(e *botWaitEntry, call *model.BotToolCall, status model.BotToolCallStatus, resultRaw string, matched []string) {
	bot, err := s.bots.Get(call.BotID)
	if err != nil {
		s.logger.Warn("wait 收尾读取机器人失败", zap.String("call_id", call.ID), zap.Error(err))
		return
	}
	s.waitDrop(e.callID)
	outcome := botToolOutcome{
		ResultJSON:  resultRaw,
		PreviewJSON: resultRaw,
		ResultChars: int64(len([]rune(resultRaw))),
		WaitResult:  json.RawMessage(resultRaw),
	}
	s.finalizeCall(bot, s.runIDOfCall(call), call, status, outcome, nil, false)
}

// waitFinalizeDuplicate 受理后立即对重复挂落 succeeded 终态：结果指向已存在
// 的同线程 wait，让模型不必等待即可继续。
func (s *BotService) waitFinalizeDuplicate(bot *model.Bot, call *model.BotToolCall, existingCallID string, conditions []string) {
	raw := marshalJSONString(map[string]interface{}{
		"outcome":         "already_waiting",
		"already_waiting": true,
		"waiting_call_id": existingCallID,
		"waiting_for":     conditions,
	})
	outcome := botToolOutcome{
		ResultJSON:  raw,
		PreviewJSON: raw,
		ResultChars: int64(len([]rune(raw))),
		WaitResult:  json.RawMessage(raw),
	}
	s.finalizeCall(bot, s.runIDOfCall(call), call, model.BotCallStatusSucceeded, outcome, nil, false)
}

func nowPtr() *time.Time {
	t := time.Now().UTC()
	return &t
}
