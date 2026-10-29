package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
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
//     重拉——归档面没有增量通道，快照不得被长期当作实时完整数据。过期
//     重拉发起的同时视图层把归档面撤为不可信（noteArchiveExpired，留行
//     但不封窗），新快照落地才恢复——否则在途期间 Peek/wait 会拿过期
//     快照当完整结论（与写后失效同型缺口，Finding R2）。
//   - 合并：同一环境并发的归档读取由 arch.inFlight 哨兵合并成一次上游
//     调用——锁内判定才放行，先放行并发再看缓存会重复打上游。
//   - 失效：归档相关写操作（archive/unarchive/settle/unsettle 等改变归档集
//     的命令派发成功后）与别的客户端造成的活跃面移除（活跃流 thread.removed，
//     归档与彻底删除都发这一帧、去向不可辨，见 applyActiveValues）都经
//     InvalidateArchive 立即作废缓存，并让归档面在视图层变成「不完整但
//     保留旧行」（ArchiveErr 非空、archivedThreads 照旧）——线程离开活跃面
//     的这段窗口里归档面若仍报可信，wait 会把线程误判为不存在、详情会
//     谎报列表完整。
//   - 世代护栏：拉取发起时记一个失效计数（generation），落地时计数已变
//     （拉取途中发生失效）则该结果是写前状态——只换数据不封可信窗口、不
//     记失败冷却，并置 arch.needsRefetch 让收尾后的下一次 kick 立刻重拉。
//   - 坏负载分派（复审遗留：不得与世代不符合并成同一种 false）：
//     applyArchiveSnapshot 拒收的快照（如线程缺 ID 的结构性坏数据）是
//     「本次读取失败」，走 noteArchiveFetchFailed 记 ArchiveErr 真实原因 +
//     retryAfter 冷却——否则毒快照每次读取都立即重拨、永不冷却；世代
//     语义由失败路径自行判定（写前坏负载不记冷却、只留 needsRefetch）。
//     簿记提交（validUntil/retryAfter）在同一临界区内随世代校验收尾
//     （Finding R1）——「应用成功后、封窗前发生失效」没有缝隙；没有
//     这道护栏，写前快照会被 applyArchiveSnapshot + validUntil 重新
//     封为可信整一个 TTL，「下次读拉到写后状态」的承诺落空。

// t3ArchiveFetchState 是 entry 级归档快照簿记：锁内只做「是否需要发一次
// 拉取」的判定，网络 I/O 全在锁外。
type t3ArchiveFetchState struct {
	mu sync.Mutex
	// validUntil 是「当前 state 里的归档快照可信到哪一刻」：来自
	// applyArchiveSnapshot 可信化成功时刻 + TTL。零值表示从未成功/已失效。
	validUntil time.Time
	// retryAfter 是下一次允许发起拉取的时刻：失败同样记账（noEarlierThan
	// = 失败时刻 + TTL），否则归档面持续失败时每次读取都会各拨一条连接
	// 重试——按需面没有常驻退避循环，并发错误风暴靠这个窗口压住。
	retryAfter time.Time
	// inFlight 已在途拉取的哨兵：登记「已发起」，TTL/冷却外的并发读出
	// 合并到这一次而不重复拨号——由发起方在拉取结束后清。
	inFlight bool
	// needsRefetch 表示「在途拉取期间发生过失效」：在途结果已是写前状态，
	// inFlight 落地后需要紧跟一次新拉取补回写后状态。kick 的放行条件
	// 把它当作「簿记已无效」处理（validUntil/retryAfter 均被失效清零），
	// 真正起决定作用的是世代护栏——它只是簿记语义上的备忘，防止误读成
	// 「失败冷却还在生效」。
	needsRefetch bool
}

// kickArchiveRefresh 是归档面按需拉取的入口：缓存仍可信（validUntil
// 未到）或失败冷却中（retryAfter 未到）则不发；需要拉取时经 inFlight
// 哨兵合并并发发起。调用方（waitView/Peek）在每次读之前走它，拉取完成
// 与否不阻塞本次视图。
//
// inFlight 与 needsRefetch 协作：在途拉取已登记、但期间发生过失效
// （needsRefetch）时，「等它落地」不承诺写后状态——kick 在这里把它当作
// 「已经有人在拉」跳过即可，落地收尾会留下 needsRefetch，下一次 kick
// （由落地后的 refresh 唤醒的读、或失效后的第一次读触发）立刻补发真正
// 写后的拉取。世代护栏保证写前那次落地不会把窗口封回可信。
func (e *t3ShellEntry) kickArchiveRefresh() {
	// 准入、撤可信与 inFlight 哨兵在同一临界区收尾（锁序 s.mu → arch.mu）：
	// 读者不可能观测到「inFlight 已置位而 archiveReady 未撤」的中间态——
	// 旧实现在放 arch.mu 后才调 noteArchiveExpired，后到读者见 inFlight
	// 直接返回，拿到的视图仍报可信（复审遗留竞态）。
	admitted, revoked := e.admitArchiveRefresh()
	if !admitted {
		return
	}
	if revoked {
		e.refresh()
	}

	// 拉取跑在 hub 生命周期 ctx 上、不感知单个请求 ctx（拉取服务的是
	// 面，请求取消不该中断在途读取）；再叠一层 TTL 上限——按需面没有
	// 常驻重试循环，上游完全沉默时读取也只能在 HTTP 传输超时（15s）才
	// 落地，TTL 上限保证一次挂起的读至多占住一个缓存窗口，窗口过后正常
	// 到期重试；entry 停/hub 关即随 connCtx 终止。
	// 并发合并由 inFlight 哨兵承担（锁内判定后才放行），不走
	// singleflight.Do——它的「等共享结果返回」会把后台拉取的挂起传染给
	// 调用方；RefreshArchive 需要等结果才用它。
	fetchCtx, cancelFetch := context.WithTimeout(e.hub.ctx, e.archTTL)
	go func() {
		defer cancelFetch()
		defer func() {
			e.arch.mu.Lock()
			e.arch.inFlight = false
			e.arch.mu.Unlock()
		}()
		_ = e.ensureArchive(fetchCtx)
	}()
}

// admitArchiveRefresh 登记一次按需拉取：返回 (admitted, revoked)。admitted
// 为假表示缓存仍可信、失败冷却中、或已有在途拉取（并发读合并到那一次）；
// revoked 为真表示本次准入同时把已过期的可信面撤为不可信（Finding R2），
// 调用方应在临界区外 refresh 广播翻转。
//
// 原子性不变量（复审遗留竞态）：「判定过期 + 撤可信 + 设 inFlight」必须对
// 读者不可分。临界区按 s.mu → arch.mu 嵌套——与 applyArchiveSnapshot 的
// commit（s.mu 持锁内取 arch.mu）同向，全局锁序 e.mu → s.mu → arch.mu 上
// 不存在 arch.mu → s.mu 的反向嵌套（RefreshArchive/InvalidateArchive/落地
// 收尾都只在锁外或同级顺序取 arch.mu），无成环可能。inFlight 在 arch.mu
// 下置位、archiveReady 在 s.mu 下撤回，但二者在同一持锁序列内提交：任何
// 能读到 inFlight=true 的读者（需 arch.mu 已过准入或走 arch.mu 之外的
// s.mu 视图）见到的 archiveReady 必已撤回——视图组装必经 s.mu，而 s.mu
// 在整个准入序列中被持有。
func (e *t3ShellEntry) admitArchiveRefresh() (admitted bool, revoked bool) {
	e.state.mu.Lock()
	defer e.state.mu.Unlock()
	e.arch.mu.Lock()
	defer e.arch.mu.Unlock()
	now := time.Now()
	if e.arch.inFlight || now.Before(e.arch.validUntil) || now.Before(e.arch.retryAfter) {
		return false, false
	}
	// 准入判定通过：validUntil 一定是零值或已过（上面已 return）。非零即
	// 「曾信任但已过期」——发起重拉的同时把归档面撤为不可信（Finding
	// R2）：过期不等于数据作废，旧行照旧展示，但在新快照落地前
	// Peek/wait 不得拿过期快照判 thread_gone、谎报归档列表完整。
	expired := !e.arch.validUntil.IsZero()
	e.arch.inFlight = true
	e.arch.needsRefetch = false
	if expired {
		e.state.noteArchiveExpiredLocked()
	}
	return true, expired
}

// ensureArchive 执行一次归档快照拉取：探测 → 拨一次性会话 →
// getArchivedShellSnapshot → applyArchiveSnapshot 进 state → 记账 TTL →
// refresh（归档面就绪/失败翻转，唤醒等待者并发布事件）。发起时刻先取样
// 失效计数（generation）：拉取途中归档面被失效作废时，本次结果按写前
// 状态处理（见落地处的世代护栏）。
//
// 失败只记 ArchiveErr（noteArchiveFetchFailed），不动已有快照——归档面
// 没有增量通道，失败时保留旧快照比「清空并谎报归档列表为空」更不坏；
// 归档读取失败与「未就绪」共用 ArchiveErr 通道，调用方据此决定能否把
// 「归档面没有」当结论。
//
// 后台路径（kickArchiveRefresh）的拉取跑在 hub 生命周期 ctx 上、不感知
// 单个请求 ctx：按需拉取服务的是「面」，发起它的请求取消不该中断在途
// 读取——若随请求取消，下次读取会另起一次，上游反而被更频繁地打扰
// （与标记兜底会话的 orphan 问题同理）。上游完全沉默时，后台拉取由
// kickArchiveRefresh 叠加的 TTL 上限兜底（至多占住一个缓存窗口），单次
// RPC 自身另受 DialBudget/单帧超时约束。
func (e *t3ShellEntry) ensureArchive(ctx context.Context) error {
	// entry 停/hub 关：不拨号。拉取是按需的，不给已停的面补位。
	if e.isStopped() {
		return nil
	}
	// 世代取样先于任何网络 I/O：保证「失效发生在拉取途中」恒能被
	// 落地判定识别——取样之后再发生的失效一定把计数推高。
	generation := e.state.archiveGenerationNow()
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
		return e.noteArchiveFetchFailed(err, generation)
	}

	dialCtx, cancelDial := context.WithTimeout(connCtx, e.hub.cfg.DialBudget)
	session, err := dialT3RPC(dialCtx, wsBaseOf(e.address), e.token)
	cancelDial()
	if err != nil {
		return e.noteArchiveFetchFailed(err, generation)
	}
	defer session.Close()
	unregisterCancel := session.closeOnCancel(connCtx)
	defer unregisterCancel()

	value, err := session.call(connCtx, t3RPCGetArchivedShellSnapshot, map[string]any{})
	if err != nil {
		return e.noteArchiveFetchFailed(err, generation)
	}
	var snapshot t3ArchivedShellSnapshot
	if err := json.Unmarshal(value, &snapshot); err != nil {
		return e.noteArchiveFetchFailed(fmt.Errorf("归档快照响应解析失败: %w", err), generation)
	}
	// 世代校验与簿记提交原子化（Finding R1）：commit 在 apply 的同一个
	// 临界区内收尾，「应用成功到封窗之间发生失效」没有可插入的缝隙——
	// 失效要么先于落地（世代不符 → needsRefetch 备忘，不封窗），要么在
	// commit 之后（由 InvalidateArchive 清零），不存在「已失效却被
	// validUntil 封窗」的交错。
	// 落地分派（复审遗留：坏负载不得与世代不符合并）：apply 返回 err 即
	// 坏负载（reset 拒收，如线程缺 ID）——走失败路径：ArchiveErr 记真实
	// 解析/校验错误、retryAfter 记冷却窗口，阻断毒快照的无冷却重试风暴；
	// 世代语义由 noteArchiveFetchFailed 自行判定（写前坏负载不记冷却、
	// 只留 needsRefetch），该路径自带 publishThreadEvent/refresh。err=nil
	// （世代不符或相符）时簿记已由 apply 临界区内的 commit 收尾。
	if _, applyErr := e.state.applyArchiveSnapshot(snapshot, generation, e.commitArchiveApply); applyErr != nil {
		return e.noteArchiveFetchFailed(applyErr, generation)
	}
	e.hub.publishThreadEvent(e, nil, true)
	e.refresh()
	return nil
}

// commitArchiveApply 是 applyArchiveSnapshot 的临界区簿记收尾：世代相符
// （本次确实是写后/当前状态）才封可信窗口——可信窗口 = 应用成功时刻 +
// TTL（不是探测/拨号时刻——失败不应把窗口起算点往后挪，下一轮换新快照）。
// 世代不符说明拉取途中发生过失效，本次快照是写前状态——只留
// needsRefetch 备忘（validUntil/retryAfter 已被失效清零，kick 本来就会
// 放行；它存在的意义是让「需要再拉」这个意图在簿记里显式可查，而不是
// 隐式依赖两个零值时间）。坏负载（reset 拒收）不经本回调——其簿记由
// 失败路径 commitArchiveFailure 收尾，两处不得双记。本回调在 s.mu 临界
// 区内执行；锁序 s.mu →
// arch.mu 全局一致（kick/失效/RefreshArchive 都在锁外或同级顺序取
// arch.mu），无反向嵌套、不与 waitView/refresh 成环。
func (e *t3ShellEntry) commitArchiveApply(trusted bool) {
	e.arch.mu.Lock()
	defer e.arch.mu.Unlock()
	if trusted {
		e.arch.validUntil = time.Now().Add(e.archTTL)
		e.arch.retryAfter = time.Time{}
		e.arch.needsRefetch = false
		return
	}
	e.arch.needsRefetch = true
}

// commitArchiveFailure 是 noteArchiveFetchFailed 的临界区簿记收尾：当前
// 世代失败记冷却窗口（retryAfter = now + TTL，失败窗口内不重复打上游）；
// 写前失败不记冷却（不封窗），只留 needsRefetch 备忘——validUntil/
// retryAfter 已随失效清零，kick 会立即放行补拉写后状态。锁序与
// commitArchiveApply 一致。
func (e *t3ShellEntry) commitArchiveFailure(applied bool) {
	e.arch.mu.Lock()
	defer e.arch.mu.Unlock()
	if applied {
		e.arch.retryAfter = time.Now().Add(e.archTTL)
		e.arch.needsRefetch = false
		return
	}
	e.arch.needsRefetch = true
}

// noteArchiveFetchFailed 统一归档拉取失败路径：写 ArchiveErr（不推翻已有
// 快照）、冷却记账（retryAfter = now + TTL，失败窗口内不重复打上游）、
// refresh（失败/就绪翻转会广播给等待者与订阅者），返回传入的错误供
// 日志/诊断使用。
//
// 世代护栏：拉取期间发生过失效（generation 与当前失效计数不一致）时，
// 「这次读失败」只说明写前那次读取失败——既不覆盖失效留下的「已失效
// 重拉中」语义，也不记冷却窗口（否则已失效的窗口会被 retryAfter 封死，
// 失效后本该立刻重拉的读要等到冷却结束）。此时仍记 needsRefetch：写前
// 失败不回答写后状态，落地后需要一次新拉取。
func (e *t3ShellEntry) noteArchiveFetchFailed(err error, generation int64) error {
	// 世代校验与冷却簿记原子化（Finding R1 对称路径）：commit 在同一
	// 临界区内收尾，不存在「应用失败后、记冷却前发生失效」把已作废
	// 的窗口用 retryAfter 封死的交错。
	e.state.noteArchiveFetchFailed(err, generation, e.commitArchiveFailure)
	e.hub.publishThreadEvent(e, nil, true)
	e.refresh()
	e.hub.logger.Warn("t3 归档快照读取失败",
		zap.String("environment_id", e.envID),
		zap.Error(err))
	return err
}

// RefreshArchive 手动强拉归档快照（bypass TTL）：作废缓存后在调用方 ctx
// 上同步拉取并返回结果，供「立即刷新」类入口使用——TTL 未过期的读取不会
// 自己触发网络 I/O。并发强拉与后台 kick 共用 inFlight 哨兵，合并成一次
// 上游调用；失败语义与后台路径一致（ArchiveErr，不推翻已有快照），错误
// 原样返回给调用方。
func (h *t3ShellHub) RefreshArchive(ctx context.Context, env *model.T3Environment, token string) error {
	e, err := h.admitEntry(env, token)
	if err != nil {
		return err
	}
	e.arch.mu.Lock()
	e.arch.validUntil = time.Time{}
	e.arch.retryAfter = time.Time{}
	e.arch.mu.Unlock()
	// 已在途的拉取（后台 kick 或另一次强拉）即最新一次读，等它落地——
	// 它的结果就是写后状态最早的可见答案，不需要另发一次；没有并发在
	// 飞时这里自己发起。inFlight 哨兵与后台 kick 互斥，同一时刻至多一次
	// 上游调用。
	for {
		e.arch.mu.Lock()
		busy := e.arch.inFlight
		if !busy {
			e.arch.inFlight = true
		}
		e.arch.mu.Unlock()
		if !busy {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	fetchErr := e.ensureArchive(ctx)
	e.arch.mu.Lock()
	e.arch.inFlight = false
	e.arch.mu.Unlock()
	return fetchErr
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
	h.invalidateArchiveEntry(e)
}

// invalidateArchiveEntry 是 InvalidateArchive 的 entry 级入口：hub 内部已持
// entry 句柄的调用方（活跃流 thread.removed，见 applyActiveValues）不必再查
// 一次注册表。
//
// 失效即标不可信：簿记清零之外，还要让归档面在视图层立刻变成「不完整但
// 保留旧行」——noteArchiveInvalidated 撤销 archiveReady 并把 ArchiveErr
// 标为「已失效重拉中」；archivedThreads 旧行保留供展示（清表会把「不
// 确定」变成「确定为空」，比保留旧行更糟）。世代计数推进同时给所有在途
// 拉取下了护栏：写前那次落地时 generation 对不上，数据照换但不再把窗口
// 封回可信（见 ensureArchive 的世代护栏）。
func (h *t3ShellHub) invalidateArchiveEntry(e *t3ShellEntry) {
	e.state.noteArchiveInvalidated()
	e.arch.mu.Lock()
	e.arch.validUntil = time.Time{}
	e.arch.retryAfter = time.Time{}
	e.arch.needsRefetch = false
	// 在途拉取落地时会因世代不符改走「只换数据」路径并留下
	// needsRefetch——此处不预置：在途拉取尚未结束，下一 kick 仍被
	// inFlight 合并，由落地收尾决定是否需要补拉。
	e.arch.mu.Unlock()
	// 归档面可信度翻转，等归档读的等待者/订阅者需要看到「已失效」；
	// 顺手发一次 Full 事件，浏览器据此整环境重拉。
	e.hub.publishThreadEvent(e, nil, true)
	e.refresh()
}
