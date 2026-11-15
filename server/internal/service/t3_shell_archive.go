package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// T3 归档面按需快照（INT-173）：上游 #26238 删除了常驻订阅
// orchestration.subscribeArchivedShell，归档面改为
// orchestration.getArchivedShellSnapshot 一次性 RPC + TTL 缓存 + 写后失效。
// 语义对齐官方客户端——归档读取失败不得被解释成「线程不存在」或
// 「归档列表完整」：失败只写 ArchiveErr，不清空已有快照、不编造「同步完成」。
//
// 三面保证：
//   - TTL：一次成功快照只在 t3ShellHubConfig.ArchiveTTL 内当可信复用，过期
//     重拉——归档面没有增量通道，快照不得被长期当作实时完整数据。撤可信
//     锚在**重拉在途时长**上（R20 P2）：过期只触发一次重拉，读取仍返回上
//     一份快照（TTL 到期不等于数据作废，空闲后首次读不该被标降级）；只有
//     在途超过 ArchiveStaleGrace 仍未落地（或重拉失败写真实错误）才撤为
//     不可信（noteArchiveExpired，留行但不封窗），新快照落地才恢复——否则
//     慢/挂起的重拉窗口里 Peek/wait 会拿过期快照当完整结论（Finding R2）。
//   - 合并：同一环境并发的归档读取由 arch.inFlight 哨兵合并成一次上游
//     调用——锁内判定才放行，先放行并发再看缓存会重复打上游。
//   - 失效：归档相关写操作（archive/unarchive/settle/unsettle 等改变归档集
//     的命令派发成功后）与别的客户端造成的活跃面移除（活跃流的
//     thread.removed 与 location != "active" 的 thread.updated——离开活跃
//     面即归档集可能变了，两种帧都不携带去向，见 applyActiveValues）都经
//     InvalidateArchive 立即作废缓存，并让归档面在视图层变成「不完整但
//     保留旧行」（ArchiveErr 非空、archivedThreads 照旧）——线程离开活跃面
//     的这段窗口里归档面若仍报可信，wait 会把线程误判为不存在、详情会
//     谎报列表完整。
//   - 世代护栏：拉取发起时记一个失效计数（generation），落地时计数已变
//     （拉取途中发生失效）则该结果是写前状态——只换数据不封可信窗口、不
//     记失败冷却；EndFetch 以 Effects.Refetch 表达补拉诉求，落地收尾
//     （runEffects）立即补发一次写后拉取，不依赖外部再读一次。
//   - 坏负载分派（复审遗留：不得与世代不符合并成同一种 false）：
//     applyArchiveSnapshot 拒收的快照（如线程缺 ID 的结构性坏数据）是
//     「本次读取失败」，记 ArchiveErr 真实原因 + retryAfter 冷却——否则
//     毒快照每次读取都立即重拨、永不冷却；世代语义由失败路径自行判定
//     （写前坏负载不记冷却、经 Refetch 表达补拉）。
//     簿记提交（validUntil/retryAfter）在同一临界区内随世代校验收尾
//     （Finding R1）——「应用成功后、封窗前发生失效」没有缝隙；没有
//     这道护栏，写前快照会被 applyArchiveSnapshot + validUntil 重新
//     封为可信整一个 TTL，「下次读拉到写后状态」的承诺落空。

// 归档面读取通道与按需簿记已并入 t3ShellState（INT-216）：archiveLegacy
// 布尔取代原三态 mode（生产代码只比较「是否 legacy」），
// validUntil/retryAfter/inFlight/refetchStartedAt 与可信度转移同在 s.mu
// 一个临界区内完成——原 s.mu → arch.mu 的嵌套锁序不再存在（R3 P1-A 的
// ABBA 隐患随之消失，全局锁序简化为 entry.mu → s.mu）。

// kickArchiveRefresh 是归档面按需拉取的入口：缓存仍可信（validUntil
// 未到）或失败冷却中（retryAfter 未到）则不发；需要拉取时经 inFlight
// 哨兵合并并发发起。调用方（waitView/Peek）在每次读之前走它，拉取完成
// 与否不阻塞本次视图。TTL 过期撤可信（revoked）时除 refresh 广播外还
// 发布一次归档面事件——撤可信是面状态翻转，浏览器必须被告知（见下方
// revoked 分支）。
//
// inFlight 期间的失效由世代护栏兜底：在途拉取已登记、但期间发生过失效时，
// 「等它落地」不承诺写后状态——kick 把它当作「已经有人在拉」跳过即可；
// 写前结果落地不封窗，EndFetch 经 Effects.Refetch 表达补拉诉求，
// runEffects 自动补发真正写后的拉取，不需要浏览器再发一次读：否则写命令
// 与在途拉取交错的窗口里，归档面会停在「已失效重拉中」直到外部读取——
// 停在降级态的页面收不到事件（落地指纹若与宣布过的相同，发布层也不发）
// 就永远不会再来读，两面消失。世代护栏保证写前那次落地不会把窗口封回
// 可信；补拉链条的终止条件同样由世代判定承担：补拉拿到的是写后快照，
// 世代相符即落地封窗、不再请求补拉——只有补拉期间又发生失效才会再
// 补拉，写停止后链条自然收敛，无空转。
func (e *t3ShellEntry) kickArchiveRefresh() {
	// 准入、撤可信与 inFlight 哨兵在 BeginFetch 的同一临界区内收尾：
	// 读者不可能观测到「inFlight 已置位而 archiveReady 未撤」的中间态。
	// 撤可信（revoked）的发布经 Effects 表达——指纹去重保证只有真实翻转
	// 才发声（P1，R12）。
	ticket, admitted, eff := e.state.BeginFetch()
	e.hub.runEffects(e, eff)
	if !admitted {
		return
	}

	// 拉取跑在 hub 生命周期 ctx 上、不感知单个请求 ctx（拉取服务的是
	// 面，请求取消不该中断在途读取）；再叠一层 TTL 上限——按需面没有
	// 常驻重试循环，上游完全沉默时读取也只能在 HTTP 传输超时（15s）才
	// 落地，TTL 上限保证一次挂起的读至多占住一个缓存窗口，窗口过后正常
	// 到期重试；entry 停/hub 关即随 connCtx 终止。
	// 并发合并由 inFlight 哨兵承担（锁内判定后才放行），不走
	// singleflight.Do——它的「等共享结果返回」会把后台拉取的挂起传染给
	// 调用方；按需面所有读者都是「发起即走」，没有等结果的强拉入口。
	fetchCtx, cancelFetch := context.WithTimeout(e.hub.ctx, e.hub.cfg.ArchiveTTL)
	// 计入 entry 生命周期（#156 R21）：否则 Close 等不到这次拉取，可能在
	// ensureArchive 仍在收尾/发布失败时就返回。停机后不再接收新计数——
	// Wait 已开始，Add 会 panic。
	e.wgMu.Lock()
	if e.wgClosed {
		e.wgMu.Unlock()
		cancelFetch()
		// 准入已经置过 inFlight/refetchStartedAt，这里不落 goroutine 就不会有人
		// 收尾（#156 R35 Note）：停机后仍持有该 entry 的读会永远看到「重拉在途」。
		e.hub.runEffects(e, e.state.EndFetch(ticket, t3FetchOutcome{kind: t3FetchAborted}))
		return
	}
	e.wg.Add(1)
	e.wgMu.Unlock()
	go func() {
		defer e.wg.Done()
		defer cancelFetch()
		outcome := e.ensureArchive(fetchCtx)
		eff := e.state.EndFetch(ticket, outcome)
		if eff.Err != nil {
			e.hub.logger.Warn("t3 归档快照读取失败",
				zap.String("environment_id", e.envID),
				zap.Error(eff.Err))
		}
		// 补拉（Effects.Refetch）在 runEffects 末尾执行：世代不符/被
		// freshnessFloor 丢弃的写前结果未封窗，收尾后紧跟一次写后拉取
		// ——世代护栏保证写前那次落地不会把窗口封回可信，链条的终止
		// 条件同样由世代判定承担（写停止后自然收敛，无空转）。
		e.hub.runEffects(e, eff)
	}()
}

// ensureArchive 执行一次归档快照拉取：探测 → 拨一次性会话 →
// getArchivedShellSnapshot → applyArchiveSnapshot 进 state → 记账 TTL →
// refresh（归档面就绪/失败翻转，唤醒等待者并发布事件）。发起时刻先取样
// 失效计数（generation）：拉取途中归档面被失效作废时，本次结果按写前
// 状态处理（见落地处的世代护栏）。
//
// 旧面上游回退（INT-173 审修）：getArchivedShellSnapshot 在上游 #26238 之
// 前的环境里不存在（只有常驻 subscribeArchivedShell）。上游没有可查询的
// 「归档面能力」字段（getConfig 的 capabilities 不覆盖归档读取），切换凭
// 据只能是 RPC 层的「方法不存在」信号：Exit Failure 里 message 形如
// "method not found: <方法>" / "unknown method"、或 _tag 为
// MethodNotFound/RpcMethodNotFound。**只在拿到这个明确信号时切订阅面**
// ——任意的 RPC 失败（归档数据损坏、上游 5xx、鉴权错误）不等于「方法不
// 存在」，切了反而掩盖真实故障；传输层不确定性（rpcUncertainError：请
// 求未写出/响应未读回）也不切——那次调用根本没到过方法分派。切换按
// 「环境不可能中途有该方法」记为 entry 的 mode，此后 kickArchiveRefresh
// 不再发按需拨号，归档由常驻订阅流喂；环境原地升级/误判的对称自愈在
// 订阅面（subscribe 被判方法不存在 → mode 回 Unknown 重探测，审修
// R8 P2，见 t3_shell_state.go 的 LegacyMethodMissing）。
//
// 失败只记 ArchiveErr（noteArchiveFetchFailed），不动已有快照——归档面
// 没有增量通道，失败时保留旧快照比「清空并谎报归档列表为空」更不坏；
// 归档读取失败与「未就绪」共用 ArchiveErr 通道，调用方据此决定能否把
// 「归档面没有」当结论。
//
// legacy 判定不是钉死的（审修 R8 P2）：切换凭据是「按需方法不存在」的
// 明确信号，但环境可能中途升级回新版（地址与凭证不变、entry 不重建），
// 或一次误判（代理转发层返回的 unknown method 文案并非方法分派的结
// 果）。对称的自愈路径在订阅面：subscribeArchivedShell 被判「方法不存
// 在」（本该有订阅面的环境反而没有，正是升级/误判的证据）时 mode 重
// 置回 Unknown，下一次 kick 重新探测按需快照——真旧环境的订阅故障
// 文案不命中该窄条件，不会在按需面上反复空拨。
//
// 后台路径（kickArchiveRefresh）的拉取跑在 hub 生命周期 ctx 上、不感知
// 单个请求 ctx：按需拉取服务的是「面」，发起它的请求取消不该中断在途
// 读取——若随请求取消，下次读取会另起一次，上游反而被更频繁地打扰
// （与标记兜底会话的 orphan 问题同理）。上游完全沉默时，后台拉取由
// kickArchiveRefresh 叠加的 TTL 上限兜底（至多占住一个缓存窗口），单次
// RPC 自身另受 DialBudget/单帧超时约束。
func (e *t3ShellEntry) ensureArchive(ctx context.Context) t3FetchOutcome {
	// entry 停/hub 关：不拨号。拉取是按需的，不给已停的面补位（中止结局
	// 由 EndFetch 清在途标记，R35）。
	if e.isStopped() {
		return t3FetchOutcome{kind: t3FetchAborted}
	}
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-e.stop:
			cancel()
		case <-connCtx.Done():
		}
	}()
	defer func() {
		cancel()
		<-watchDone
	}()

	status, probeMsg := e.hub.http.probeSession(connCtx, e.address, e.token)
	if status != T3EnvironmentStatusOK {
		err := fmt.Errorf("归档快照会话探测失败: %s", probeMsg)
		if status == T3EnvironmentStatusRepair {
			err = fmt.Errorf("%w: %s", errT3CredentialRejected, probeMsg)
		}
		return t3FetchOutcome{kind: t3FetchFailed, err: err}
	}

	dialCtx, cancelDial := context.WithTimeout(connCtx, e.hub.cfg.DialBudget)
	session, err := dialT3RPC(dialCtx, wsBaseOf(e.address), e.token)
	cancelDial()
	if err != nil {
		return t3FetchOutcome{kind: t3FetchFailed, err: err}
	}
	defer session.Close()
	unregisterCancel := session.closeOnCancel(connCtx)
	defer unregisterCancel()

	value, err := session.call(connCtx, t3RPCGetArchivedShellSnapshot, map[string]any{})
	if err != nil {
		// 旧面回退：仅「方法不存在」的明确信号才切订阅面——
		// rpcUncertainError 与任意业务失败都不构成该证据（见函数头）。
		// 「先记失败、后切 legacy」由 EndFetch 在同一临界区内完成（R5 P2）。
		if isArchiveSnapshotMethodMissing(err) {
			return t3FetchOutcome{kind: t3FetchMethodMissing, err: err}
		}
		return t3FetchOutcome{kind: t3FetchFailed, err: err}
	}
	// 应答形状校验（审修 B + R8 P2）：归档 RPC 是宽松解码——Exit Success
	// 的 value 为 null、应答多包一层（{snapshot:{...}}）、字段改名都零错误
	// 解成零值快照，直接应用会把归档表整体重置为空并封可信：谎报「归
	// 档列表确定为空且可信」，与「失败不清表、不谎报完整」相反；且 RPC
	// 是成功应答，不触发旧面回退。判别字段用 threads「存在（含空数组）」
	// vs「缺失/null」区分——上游快照恒带该字段（threads:[] 也是合法完
	// 整应答，len>0 会把合法空归档误拒）。注意 null 也不得放行：解进
	// RawMessage 的 "threads":null 是 4 字节 "null" 而非 nil（审修 R8 P2），
	// 往下游解是零值切片、reset 照成空表——与字段缺失同样是坏形状。
	// 形状不符按本次读取失败记账（ArchiveErr 真实原因 + 冷却），不清表、
	// 不封窗。
	var shape struct {
		Threads json.RawMessage `json:"threads"`
	}
	if err := json.Unmarshal(value, &shape); err != nil || shape.Threads == nil || string(shape.Threads) == "null" {
		if err == nil {
			err = fmt.Errorf("归档快照应答缺少 threads 判别字段")
		} else {
			err = fmt.Errorf("归档快照应答解析失败: %w", err)
		}
		return t3FetchOutcome{kind: t3FetchFailed, err: err}
	}
	var snapshot t3ArchivedShellSnapshot
	if err := json.Unmarshal(value, &snapshot); err != nil {
		return t3FetchOutcome{kind: t3FetchFailed, err: fmt.Errorf("归档快照响应解析失败: %w", err)}
	}
	// 世代护栏/簿记提交/坏负载分流全部由 EndFetch 在同一临界区内收尾
	// （Finding R1）；快照采集时刻取 ticket.startedAt（#156 R16）。
	return t3FetchOutcome{kind: t3FetchSnapshot, snapshot: snapshot}
}

// isArchiveSnapshotMethodMissing 判定 getArchivedShellSnapshot 的失败是否
// 为「该方法在上游不存在」的明确信号。错误文本由 t3RPCCauseError 组装为
// "<_tag>: <message>"；上游对未注册方法的应答以 "method not found" /
// "unknown method" 消息或 MethodNotFound 类 _tag 表达——凭据比任意 RPC
// 失败窄得多，保证瞬时业务失败不会把面误切到订阅模式。匹配统一小写后
// 比对（审修 N3）：上游文案的大小写没有保证（"Method not found"/
// "Unknown method" 首字母大写变体同样存在），按原文匹配会让回退永不触
// 发——归档面长期停 ArchiveErr、每 TTL 盲重试。
func isArchiveSnapshotMethodMissing(err error) bool {
	var uncertain *rpcUncertainError
	if errors.As(err, &uncertain) {
		// 传输层不确定：调用可能根本没到方法分派，不构成「方法不存在」。
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "method not found") ||
		strings.Contains(msg, "unknown method") ||
		strings.Contains(msg, "methodnotfound")
}

// InvalidateArchive 作废环境的归档快照缓存：下一次读取重新拉取。归档相关
// 写命令派发成功后调用（见 t3_settle.go/t3_review.go），不依赖 TTL 兜底。
// entry 不存在时无需处理（下一个 View 会建 entry 并走未就绪路径）。
func (h *t3ShellHub) InvalidateArchive(userID, envID string) {
	key := t3ShellHubKey(userID, envID)
	h.mu.Lock()
	e := h.entries[key]
	h.mu.Unlock()
	if e == nil {
		return
	}
	h.runEffects(e, e.state.Invalidate())
}

// runEffects 是状态机返回的 t3ShellEffects 的统一执行器（INT-216）：固定
// 顺序排定时器 → 重连信号 → 激活 legacy → 发布 → refresh → 补拉。全部
// 副作用在锁外执行——状态机是纯内存的，不持 hub/entry 引用。
func (h *t3ShellHub) runEffects(e *t3ShellEntry, eff t3ShellEffects) {
	if !eff.SettleAt.IsZero() {
		h.scheduleArchiveMembershipSettle(e, eff.SettleAt)
	}
	if eff.SignalReconnect {
		e.signalArchiveReconnect()
	}
	if eff.ActivateLegacy {
		// 放行停在闸门上的旧面订阅 goroutine：mode 已在 EndFetch 临界区内
		// 提交为 legacy，信号只是唤醒——闸门醒来复核现值。缓冲 1 + 非阻塞
		// 发送：重复激活时丢弃多余信号，丢信号不丢激活。
		select {
		case e.legacyCh <- struct{}{}:
		default:
		}
	}
	switch eff.Publish {
	case t3PublishDedup:
		h.publishArchiveFaceEvent(e, false)
	case t3PublishForceFull:
		h.publishArchiveFaceEvent(e, true)
	}
	if eff.Refresh {
		e.refresh()
	}
	if eff.Refetch && !e.isStopped() {
		e.kickArchiveRefresh()
	}
}

// scheduleArchiveMembershipSettle 在宽限到期时清理「归属待对账」并发布可信度
// 恢复事件（#156 R2）。线程可能是被彻底删除、永远等不到归档增量，所以不确定
// 状态必须有时间上界，且这条转变要让浏览器知道。entry 停机后丢弃回调。
// deadline 由状态机在登记/清理的同一临界区内算好的最早截止（Effects.SettleAt）：
// 定时器按它排程，每条目的上界就是它自己的截止时刻——早先登记的条目不会被
// 后来的登记顺延（#156 R23）。
func (h *t3ShellHub) scheduleArchiveMembershipSettle(e *t3ShellEntry, deadline time.Time) {
	if deadline.IsZero() {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.isStopped() {
		// 停机之后再装定时器没人会 Stop 它（#156 R19）。
		return
	}
	if e.settleTimer != nil && !e.settleAt.After(deadline) {
		// 已有更早（或同刻）的排程，不后移。
		return
	}
	delay := time.Until(deadline)
	if delay <= 0 {
		// 截止已过的剩余条目立刻再看一眼（不能把负延迟交给 AfterFunc
		// 语义含糊化）：expire 会按 now 裁掉它们（#156 R23 Note）。
		delay = time.Millisecond
	}
	if e.settleTimer == nil {
		e.settleTimer = time.AfterFunc(delay, func() { h.settleArchiveMembership(e) })
	} else {
		e.settleTimer.Reset(delay)
	}
	e.settleAt = deadline
}

// settleArchiveMembership 是一次到期清理（#156 R23）：状态机内清掉已过截止
// 的条目并返回下一个最早截止（Effects.SettleAt 续排），可信翻转经
// runEffects 发布。定时器槽位先清再执行 effects，保证续排不被旧槽位挡下。
func (h *t3ShellHub) settleArchiveMembership(e *t3ShellEntry) {
	// 与停机串行（#156 R12-codex）：shutdown 在同一把锁里 Stop，回调一旦
	// 开跑就持锁到结束，shutdown 返回后不再有在途回调（Close 才算真正收干净）。
	e.settleMu.Lock()
	defer e.settleMu.Unlock()
	if e.isStopped() {
		return
	}
	// 先清定时器槽位再跑 Settle（审修：先 Settle 后清槽会留下窗口——并发
	// 新登记在 schedule 里看到旧槽位仍占着且截止更早而跳过排程，随后槽位
	// 被这里清空，新条目就丢了定时器、面长期 stale）。槽位清空后，并发的
	// scheduleArchiveMembershipSettle 会看到空槽位自行装新定时器。
	e.mu.Lock()
	e.settleTimer = nil
	e.settleAt = time.Time{}
	e.mu.Unlock()
	eff := e.state.Settle()
	h.runEffects(e, eff)
}

// ------ 旧面订阅回退（INT-173 审修） ------

// errArchiveReconnectRequested 是按「请重建连接」备忘主动断开归档订阅的
// 结束原因（写后失效留下的，不是故障）：记入 ArchiveErr
// 与日志供观测，监督循环按普通重连处理。
var errArchiveReconnectRequested = errors.New("按失效备忘重建归档订阅")

// signalArchiveReconnect 通知旧面读循环断开重连。缓冲 1：信号丢失或重复
// 都无害——读循环只在 state 备忘仍置位时才真断开（consume 判定），连接
// 已死时监督循环的退避重连本来就是兜底。
func (e *t3ShellEntry) signalArchiveReconnect() {
	select {
	case e.legacyReconnect <- struct{}{}:
	default:
	}
}

// waitArchiveLegacyActive 是旧面订阅监督循环的 mode 闸门（审修 R9 P2-1）：
// mode 仍处 legacy 时立即放行；已离开 legacy（自愈回 Unknown、按需面重新
// 接管）则停泊在 legacyCh 上待命，直到按需快照被判方法不存在经
// Effects.ActivateLegacy 再次放行或
// entry/hub 停机。返回 false 表示停机，监督循环应退出。停泊不打上游——
// 非 legacy 环境里 subscribe 只会得到「方法不存在」，重连只是反复挨拒、
// 每次失败还把归档面再清一遍。读 mode 只取 s.mu（纯读不涉及
// 「archiveReady 尚可信但 mode 已翻」的中间态问题）。
func (e *t3ShellEntry) waitArchiveLegacyActive() bool {
	for {
		if e.state.IsLegacy() {
			return true
		}
		select {
		case <-e.legacyCh:
			// 唤醒后复核现值：信号可能在 mode 已再次离开 legacy 时到达，
			// 那就继续停泊，不发起连接。
		case <-e.stop:
			return false
		case <-e.hub.ctx.Done():
			return false
		}
	}
}

//
// 旧上游（#26238 之前）的归档面只有常驻订阅 subscribeArchivedShell：注册
// 后上游先发全量快照（首个 Chunk 的 snapshot 流项），再推
// thread.updated/removed 增量。与按需快照的生命周期不同——它是常驻流：
// 连接健康期间数据持续可信，连接断开即整体弃用（G1：伴随健康连接才可
// 信），交由监督循环重连。按需路径的 TTL/冷却/世代簿记不适用于它：数据
// 可信度由「连接是否活着」而不是「快照多新」承载。
//
// 恢复自 57c4f583^ 的归档订阅实现（runFaceSubscription/archived=true 分支），
// 收窄为只服务旧面回退：探活与拨号复用活跃面的 connectFaceOnce 骨架，但
// 去掉完成标记/游标（归档流没有 afterSequence 恢复语义，重连即重新拉全
// 量）；流项改喂 t3ArchiveStreamItem；连接终止统一经 markArchiveFailed
// 弃用归档面（订阅面没有「留旧行等 TTL」的按需语义，清空比留陈旧更如
// 实）。

// runArchiveLegacySubscription 是旧面归档常驻订阅的监督循环：探测 → 拨号
// → 订阅 → 读循环，结束后按分类退避重连（复用活跃面的 backoffFor）。随
// entry/hub 生命周期退出；mode 闸门让它只在 legacy 模式下发起连接——
// entry 创建时停在闸门上待命（不占连接、不打上游），EndFetch 的
// ActivateLegacy 效果放行；自愈回 Unknown（LegacyMethodMissing）后
// 下一轮闸门把它重新停泊，订阅不再重连（审修 R9 P2-1：否则旧面连接
// 的持续失败会反复清空已由按需面接管的归档表）。
//
// 稳定判定用归档面自己的数据落地时刻（审修 R6-C，syncedAt 取自本连接
// 的读循环记录而非活跃面的 syncConfirmedAt）：活跃面长期健康时其追平时
// 刻恒为旧值，归档订阅持续失败（上游拒收/立即 Exit/掐断）也会被每轮重
// 置 attempts=0、按 BackoffBase 无限重连——每环境每秒一次探活+拨号的重
// 连风暴；活跃面刚重连时又错误从 2s 起算。归档面没有 synchronized/追平
// 态，「本连接收到过快照/增量」即它的健康信号：读循环把最近一次
// applyArchiveValues 成功时刻记入 connResult.syncedAt，全程无数据的连接
// 返回零值，attempts 持续加深；连接活着且数据流过 StableResetAfter 才重
// 置退避——「连接健康期间数据持续可信」正是订阅面的可信度模型。
func (h *t3ShellHub) runArchiveLegacySubscription(e *t3ShellEntry) {
	attempts := 0
	for {
		// mode 闸门：非 legacy（Unknown/Snapshot）不发起任何订阅连接，
		// 停泊等 ActivateLegacy 的放行信号或停机。闸门在每轮重连
		// 前复核，mode 已在临界区提交后才可能放行。
		if !e.waitArchiveLegacyActive() {
			return
		}
		result := h.connectArchiveLegacyOnce(e)
		if e.isStopped() {
			return
		}
		select {
		case <-h.ctx.Done():
			return
		default:
		}
		if result.voluntary {
			// 主动断开（consume 重建备忘）：断开的目的是拿写后状态，不是
			// 故障——attempts 不加深（审修 R8 P2：连续批量归档的写后失效
			// 重连若按故障加深，退避会涨到上限，归档面消失数十秒）。按
			// BackoffBase 立即重连；连上后未能兑现（新方法不存在/中途故
			// 障）的下一轮结果不再带 voluntary，退避照常加深。
			attempts = 0
		} else if !result.syncedAt.IsZero() && time.Since(result.syncedAt) > h.cfg.StableResetAfter {
			attempts = 0
		} else {
			attempts++
		}
		backoff := h.backoffFor(attempts, result.credential)
		timer := time.NewTimer(backoff)
		select {
		case <-e.stop:
			timer.Stop()
			return
		case <-h.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// connectArchiveLegacyOnce 跑一次完整的旧归档订阅连接，阻塞到连接结束。
// 与 connectFaceOnce 同构，但方法固定为 subscribeArchivedShell、无能力查
// 询与恢复游标（旧订阅没有该协议面）；终止统一按归档面记失败。
func (h *t3ShellHub) connectArchiveLegacyOnce(e *t3ShellEntry) t3ShellConnResult {
	connCtx, cancelConn := context.WithCancel(h.ctx)
	defer cancelConn()
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-e.stop:
			cancelConn()
		case <-connCtx.Done():
		}
	}()
	defer func() {
		cancelConn()
		<-watchDone
	}()

	status, probeMsg := h.http.probeSession(connCtx, e.address, e.token)
	if status != T3EnvironmentStatusOK {
		probeErr := fmt.Errorf("归档订阅会话探测失败: %s", probeMsg)
		credential := false
		if status == T3EnvironmentStatusRepair {
			probeErr = fmt.Errorf("%w: %s", errT3CredentialRejected, probeMsg)
			credential = true
		}
		h.endFaceConnection(e, true, probeErr)
		return t3ShellConnResult{credential: credential}
	}

	dialCtx, cancelDial := context.WithTimeout(connCtx, h.cfg.DialBudget)
	session, err := dialT3RPC(dialCtx, wsBaseOf(e.address), e.token)
	cancelDial()
	if err != nil {
		h.endFaceConnection(e, true, err)
		return t3ShellConnResult{}
	}
	defer session.Close()
	unregisterCancel := session.closeOnCancel(connCtx)
	defer unregisterCancel()

	// 出发邮戳必须在发 subscribe 之前取（审修 R6-A，邮戳时机）：保证
	// 「发送后窗口内记录的新请求世代必然大于邮戳」。若在发送后、或任何
	// 可阻塞调用（noteConnState 取 e.mu，可能等一次大快照拷贝）之后取，
	// 窗口内发生的归档写后失效记入的请求世代会被算进邮戳——该连接送达
	// 的变更前快照落地时 stamp.gen >= pendingGen 成立，备忘被误清，
	// 排队的 reconnect 信号被当无备忘忽略，写后全量不再重拉。拨号/探测
	// 是订阅发出之前的事，在它们之前的请求本就该计入邮戳——唯一不能计
	// 入的是「本连接 subscribe 已发出之后」的那批。LegacyStamp 一次取回
	// 请求世代与连接建立时刻（后者即快照采集时刻的保守下界，#156 R18：
	// 服务端可能在收到订阅时立刻采样）。
	stamp := e.state.LegacyStamp()
	e.noteConnState(true, t3ShellConnConnecting, nil)
	requestID := session.nextID
	session.nextID++
	if err := session.sendRequest(connCtx, requestID, t3RPCSubscribeArchivedShell, map[string]any{}); err != nil {
		h.endFaceConnection(e, true, err)
		return t3ShellConnResult{}
	}
	e.noteConnState(true, t3ShellConnSubscribed, nil)
	return h.readArchiveLegacyLoop(e, session, connCtx, requestID, stamp)
}

// readArchiveLegacyLoop 是旧归档订阅的读循环：只认本 requestID 的 Chunk/
// Exit；Chunk 全部 value 先应用再 Ack（任一出错整条流失败）；其余帧只
// 刷新活性计时，Ping 保活同活跃面。connectionStart 是本连接的 reconnect
// 出发邮戳，随流项交给落地路径做备忘世代判定（R4 P2）。另有第三条出
// 路：legacyReconnect 信号在守旧备忘仍置位时主动断开，交监督循环重连
// 取权威全量（R3 P1-B）。返回连接结局。
func (h *t3ShellHub) readArchiveLegacyLoop(e *t3ShellEntry, session *t3RPCSession, connCtx context.Context, requestID int, stamp t3LegacyStamp) t3ShellConnResult {
	frames := make(chan *rpcFrame, 16)
	readErrCh := make(chan error, 1)
	readerDone := make(chan struct{})
	stop := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(frames)
		for {
			frame, err := session.readFrameWithIdleTimeout(h.cfg.FrameIdleTimeout)
			if err != nil {
				readErrCh <- err
				return
			}
			select {
			case frames <- frame:
			case <-connCtx.Done():
				return
			case <-stop:
				return
			}
		}
	}()
	defer func() {
		session.Close()
		close(stop)
		<-readerDone
	}()

	lastFrame := time.Now()
	pingTicker := time.NewTicker(h.cfg.PingInterval / 2)
	defer pingTicker.Stop()

	var connErr error
	credential := false
	// dataSeenAt 是本连接最近一次权威数据（快照/增量）落地时刻：归档面
	// 没有追平态，退避的稳定判定以「本连接是否收到过数据且活了多久」为
	// 凭（审修 R6-C）——全程无数据的连接返回零值 syncedAt，attempts 持续
	// 加深，不会被活跃面的追平时刻误重置。
	var dataSeenAt time.Time
	for {
		select {
		case <-connCtx.Done():
			h.endFaceConnection(e, true, nil)
			return t3ShellConnResult{syncedAt: dataSeenAt}
		case frame, ok := <-frames:
			if !ok {
				select {
				case connErr = <-readErrCh:
				default:
					connErr = fmt.Errorf("归档订阅连接读取中断")
				}
				h.endFaceConnection(e, true, connErr)
				return t3ShellConnResult{credential: credential, syncedAt: dataSeenAt}
			}
			lastFrame = time.Now()
			switch {
			case frame.Tag == "Chunk" && frame.requestIDMatches(requestID):
				if applyErr := h.applyArchiveValues(e, frame.Values, stamp); applyErr != nil {
					h.endFaceConnection(e, true, applyErr)
					return t3ShellConnResult{syncedAt: dataSeenAt}
				}
				dataSeenAt = time.Now()
				e.refresh()
				if err := session.sendAck(connCtx, frame.RequestID); err != nil {
					h.endFaceConnection(e, true, fmt.Errorf("发送 Ack 失败: %w", err))
					return t3ShellConnResult{syncedAt: dataSeenAt}
				}
			case frame.Tag == "Exit" && frame.requestIDMatches(requestID):
				if _, err := frame.exitValue(); err != nil {
					credential = errors.Is(err, errT3CredentialRejected)
					// 方法不存在的 Exit 是「环境中途升级/上次误判」的证据：本
					// 该有订阅面的环境反而没有，说明按需快照路径可能已可用。
					// 先退出 legacy、再收尾连接（审修 R9 P2-1）：之后
					// endFaceConnection 经 LegacyEnded 看到的是非 legacy 面，
					// 不再清表/写 ArchiveErr——这次失败只证明「订阅通道不存在」，
					// 不证明「归档数据作废」，清表会让按需面接管前的窗口里归
					// 档行凭空消失。下一次读 BeginFetch 重探测按需快照；真
					// 旧环境的其他订阅失败文案不命中窄条件，不会误复活按需拨号。
					if isArchiveSnapshotMethodMissing(err) {
						wasLegacy := e.state.IsLegacy()
						h.runEffects(e, e.state.LegacyMethodMissing())
						if wasLegacy {
							e.hub.logger.Info("t3 归档订阅被判方法不存在，回按需探测重试",
								zap.String("environment_id", e.envID))
						}
					}
					h.endFaceConnection(e, true, err)
				} else {
					h.endFaceConnection(e, true, fmt.Errorf("归档订阅流提前结束"))
				}
				return t3ShellConnResult{credential: credential, syncedAt: dataSeenAt}
			default:
			}
		case <-e.legacyReconnect:
			// 守旧备忘消费：订阅面没有按需拉取可发，写后失效的正确动作
			// 是断开后由监督循环重连——新 subscribe 即收权威
			// 全量快照。常驻流只收增量，「增量落地后整表重置」会把归档面
			// 清空到下一次断线（R3 P1-B）。信号可早到/重复：备忘已被权威
			// 快照（邮戳 ≥ 备忘世代的那张）或连接终止清掉时 consume 为假，
			// 忽略即可。备忘世代比本连接邮戳新时不做门控——那正是「本连接
			// 的订阅取的是变更前状态」的证据，断开它就是兑现（R4 P2）。
			if !e.state.ConsumeLegacyReconnect() {
				continue
			}
			h.endFaceConnection(e, true, errArchiveReconnectRequested)
			// voluntary=true：主动断开是兑现写后拉取、不是故障——退避
			// 不加深（审修 R8 P2）。
			return t3ShellConnResult{syncedAt: dataSeenAt, voluntary: true}
		case <-pingTicker.C:
			if time.Since(lastFrame) > h.cfg.PingInterval {
				if err := session.sendRaw(connCtx, map[string]any{"_tag": "Ping"}); err != nil {
					h.endFaceConnection(e, true, fmt.Errorf("发送 Ping 失败: %w", err))
					return t3ShellConnResult{credential: credential, syncedAt: dataSeenAt}
				}
			}
		}
	}
}

// applyArchiveValues 把旧归档流 Chunk 的全部 value 依次解码并经
// ApplyLegacy 按批应用，发布语义同活跃面。stamp 是本连接的出发邮戳，
// 交给快照项做守旧备忘的世代判定（R4 P2）与快照采集时刻判定（R17）。
// 归档流自己也可能登记待对账（#156 R24）、变更前快照被拒时记重建备忘
// （#156 R29）——SettleAt/SignalReconnect 都由 ApplyLegacy 的 Effects
// 带回，经 runEffects 统一执行；宣布基准随每次落地同步（R17 P2-2，由
// Effects.Publish=dedup 表达）。
func (h *t3ShellHub) applyArchiveValues(e *t3ShellEntry, values []json.RawMessage, stamp t3LegacyStamp) error {
	items := make([]t3ArchiveStreamItem, 0, len(values))
	var decodeErr error
	for _, raw := range values {
		var item t3ArchiveStreamItem
		if err := json.Unmarshal(raw, &item); err != nil {
			decodeErr = fmt.Errorf("归档流帧解析失败: %w", err)
			break
		}
		items = append(items, item)
	}
	eff, applyErr := e.state.ApplyLegacy(stamp, items)
	h.runEffects(e, eff)
	if decodeErr != nil {
		return decodeErr
	}
	if applyErr != nil {
		return applyErr
	}
	batch := t3ShellEventBatch{}
	for i := range items {
		batch.observeArchive(&items[i])
	}
	h.publishThreadEvent(e, batch.ids, batch.full)
	return nil
}
