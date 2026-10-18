package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
)

// T3 shell 订阅复用 hub（INT-116）：每个「用户 × 环境」持两条长驻 WS 连接
// （活跃 / 归档各一条），把原来每个请求一次的全量 shell 快照拉取摊销成
// 常驻订阅 + 增量推送。设计见 INT-116 v2：
//   - 两条连接而非一条多路复用：t3RPCSession 是串行客户端（call 会吞掉其他
//     requestId 的帧），复用一条需自建 requestId 派发；两条单流连接读循环
//     无需派发，取消/重订阅互不影响。代价是每环境两条 WS，可接受。
//   - 完成标记能力来自 server.getConfig 的 shellResumeCompletionMarker
//     （ws.ts:1731）；官方客户端据此决定是否请求 synchronized 标记
//     （client-runtime/src/state/shell.ts:338,386-398）：不支持时不请求并
//     立即视为追平。
//   - Ping/Pong 是 Effect RPC 应用层帧 {"_tag":"Ping"} → {"_tag":"Pong"}
//     （effect rpc/RpcServer.ts:806、RpcMessage.ts constPing/constPong），
//     不是 WS 控制帧。
//
// 锁边界：hub 锁只护 entry 注册表与 floor；entry 锁只护 state/游标/标记/
// 通知与连接状态。网络 I/O、Close、等 goroutine 退出全部在锁外。锁序恒为
// entry.mu → state.mu（state 方法内部自带锁），不存在反向。

const (
	t3RPCSubscribeShell         = "orchestration.subscribeShell"
	t3RPCSubscribeArchivedShell = "orchestration.subscribeArchivedShell"
	t3RPCServerGetConfig        = "server.getConfig"
)

// errT3ShellConfigStale 是 View 的版本守卫哨兵：请求携带的环境版本不晚于
// 已知的 floor/entry 版本（请求是在配置变更前读出的迟到请求）。调用方应当
// 重读环境后重试，而不是用旧配置建订阅。
var errT3ShellConfigStale = fmt.Errorf("环境配置已更新，请重试")

// t3ShellHubConfig 汇集 hub 的时间参数，测试可整体注入小值。
type t3ShellHubConfig struct {
	// frameIdleTimeout 单帧读等待上限：专用 reader 每次 Receive 前设此
	// deadline，到点即判死重连（x/net 超时后不可续读，不做「重设继续读」）。
	FrameIdleTimeout time.Duration
	// PingInterval 距最近完整帧超过该时长就发应用层 Ping 保活。
	PingInterval time.Duration
	// BackoffBase/BackoffMax 重连退避 1s→2s→4s…上限 30s。
	BackoffBase time.Duration
	BackoffMax  time.Duration
	// BackoffJitter 退避抖动比例（±25%）。
	BackoffJitter float64
	// StableResetAfter 同步稳定超过该时长的连接断开后，退避从 1s 重来。
	StableResetAfter time.Duration
	// CredentialBackoff 凭证被拒的长退避（重试再快也没有意义，等用户换凭证）。
	CredentialBackoff time.Duration
	// MarkerWaitTimeout 请求了 synchronized 标记的等待预算。预算内没等到就
	// 不把未追平的缓存提升为 Ready（旧实现按追平处理会把重连慢全量期间的
	// 旧 idle 缓存当可信运行态）：置强制全量重同步并结束本连接，下次重连
	// 不带 afterSequence，由上游权威全量快照重新建立追平。
	MarkerWaitTimeout time.Duration
	// DialBudget 建连独立预算（不由任何请求 ctx 决定）。
	DialBudget time.Duration
	// IdleCheckInterval 空闲回收 ticker 周期（每 entry 一个 goroutine）。
	IdleCheckInterval time.Duration
	// IdleDisconnect 距最近一次 View 超过该时长就先断开两条连接、保留内存
	// 快照（下次 View 用 afterSequence 增量恢复，不必重付全量快照）。
	// <= 0 表示不暂停，只按 IdleTTL 整条回收。
	IdleDisconnect time.Duration
	// IdleTTL 距最近一次 View 超过该时长即自停并摘除（连内存快照一起释放）。
	IdleTTL time.Duration
}

func defaultT3ShellHubConfig() t3ShellHubConfig {
	return t3ShellHubConfig{
		FrameIdleTimeout:  75 * time.Second,
		PingInterval:      25 * time.Second,
		BackoffBase:       1 * time.Second,
		BackoffMax:        30 * time.Second,
		BackoffJitter:     0.25,
		StableResetAfter:  60 * time.Second,
		CredentialBackoff: 5 * time.Minute,
		MarkerWaitTimeout: 10 * time.Second,
		DialBudget:        15 * time.Second,
		IdleCheckInterval: 30 * time.Second,
		// 空闲 1 小时先断连保值（千级线程环境重连走增量，远比全量快照便宜）；
		// 空闲 6 小时再整条淘汰，避免给长期不用的环境一直挂连接。
		IdleDisconnect: 60 * time.Minute,
		IdleTTL:        6 * time.Hour,
	}
}

// t3ShellViewState 是 View 对活跃面可信程度的判定结果。
type t3ShellViewState int

const (
	// t3ShellSyncing 活跃面还没有权威快照（从未拿到数据；Snaps 为 nil）。
	t3ShellSyncing t3ShellViewState = iota
	// t3ShellReady 活跃面有权威快照且当前连接已追平。
	t3ShellReady
	// t3ShellStale 活跃面曾有数据但当前连接断开/重连中/追平中；Snaps 是
	// 最近确认可信的数据，SyncedAgo 标注其新鲜度。
	t3ShellStale
	// t3ShellFailed 从未拿到数据且最近一次建连失败（Err 用
	// errT3CredentialRejected 等哨兵包装）。
	t3ShellFailed
)

func (s t3ShellViewState) String() string {
	switch s {
	case t3ShellSyncing:
		return "syncing"
	case t3ShellReady:
		return "ready"
	case t3ShellStale:
		return "stale"
	default:
		return "failed"
	}
}

// t3ShellView 是一次 shell 状态读取。归档面是否可信由 Snaps.ArchiveErr 单独
// 表达，不进入 State；Stale/Failed 时 Err 携带最近一次连接失败（可能为 nil，
// 例如仅在读超时后判死），凭证被拒可用 errors.Is(err, errT3CredentialRejected)。
type t3ShellView struct {
	State     t3ShellViewState
	Snaps     *t3ShellSnapshots
	Err       error
	SyncedAgo time.Duration
}

// t3ShellConnState 是单条订阅面的连接状态。
type t3ShellConnState int

const (
	t3ShellConnConnecting t3ShellConnState = iota
	t3ShellConnSubscribed
	t3ShellConnFailed
)

// t3ShellHub 管理 entry 注册表与生命周期。
type t3ShellHub struct {
	cfg    t3ShellHubConfig
	logger *zap.Logger
	http   *t3HTTPClient

	mu      sync.Mutex
	entries map[string]*t3ShellEntry
	floors  map[string]t3ShellFloor
	closed  bool

	// lifecycle 是 hub 级生命周期在飞计数：每个 entry Add(1)，entry 的全部
	// goroutine（监督循环在退出前汇合各自的订阅 reader）退出后 Done。Close
	// 等它排空——只遍历注册表会漏掉已被 Drop/凭证替换/空闲淘汰摘除、但
	// goroutine 还没退干净的 entry。
	lifecycle sync.WaitGroup

	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
}

// NewT3ShellHub 创建 shell 订阅 hub（服务生命周期内复用，Close 供停机与测试）。
func NewT3ShellHub(logger *zap.Logger) *t3ShellHub {
	return newT3ShellHubWithConfig(defaultT3ShellHubConfig(), logger)
}

func newT3ShellHubWithConfig(cfg t3ShellHubConfig, logger *zap.Logger) *t3ShellHub {
	if logger == nil {
		logger = zap.NewNop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &t3ShellHub{
		cfg:     cfg,
		logger:  logger,
		http:    newT3HTTPClient(),
		entries: map[string]*t3ShellEntry{},
		floors:  map[string]t3ShellFloor{},
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
}

func t3ShellHubKey(userID, envID string) string {
	return userID + "\x00" + envID
}

func t3ShellNormalizedAddress(address string) string {
	return strings.TrimSuffix(strings.TrimSpace(address), "/")
}

// t3ShellFingerprint 绑定订阅要用的地址与凭证：规范化 address（去首尾空白与
// 结尾斜杠，与 HTTP 客户端的拼接习惯一致）加 \0 分隔的 token 求哈希。任一
// 变更都产生新指纹，触发停旧建新。
func t3ShellFingerprint(address, token string) string {
	sum := sha256.Sum256([]byte(t3ShellNormalizedAddress(address) + "\x00" + token))
	return hex.EncodeToString(sum[:])
}

// View 读取环境的 shell 状态，必要时建 entry 并等待活跃面出现权威快照。
//
// 版本守卫：entry 记 env.UpdatedAt 与凭证指纹。指纹不一致且 env.UpdatedAt 更新
// 才停旧建新；否则拒绝（errT3ShellConfigStale），不打断现有 entry。entry 不存在
// 但 floor[key] 不晚于请求版本时同样拒绝——防「删除前读出的请求在 Drop 之后
// 把 entry 复活」。
//
// ctx 只用于取消等待，不参与建连：entry 的订阅跑在 hub 的服务生命周期 ctx 上，
// 请求取消不影响后台同步。等待结束条件：活跃面出现权威快照、或首次建连失败、
// 或 ctx 结束。
func (h *t3ShellHub) View(ctx context.Context, env *model.T3Environment, token string) (*t3ShellView, error) {
	key := t3ShellHubKey(env.UserID, env.ID)
	fingerprint := t3ShellFingerprint(env.Address, token)

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, fmt.Errorf("t3 shell hub 已关闭")
	}
	e := h.entries[key]
	if e != nil && e.fingerprint != fingerprint {
		if !env.UpdatedAt.After(e.version) {
			h.mu.Unlock()
			return nil, fmt.Errorf("%w: 环境凭证或地址正在变更", errT3ShellConfigStale)
		}
		// 凭证/地址已变：新 entry 先注册占住 key，旧 entry 异步自停
		// （关连接与 goroutine 退出都在锁外），不打断新 entry。
		delete(h.entries, key)
		old := e
		old.shutdown()
		e = nil
	}
	if e == nil {
		if floor, ok := h.floors[key]; ok && !env.UpdatedAt.After(floor.version) {
			h.mu.Unlock()
			return nil, fmt.Errorf("%w: 环境配置已被替换或删除", errT3ShellConfigStale)
		}
		e = h.newEntryLocked(key, env, token)
	}
	e.mu.Lock()
	e.lastUsed = time.Now()
	e.resumeLocked()
	notify := e.notify
	e.mu.Unlock()
	h.mu.Unlock()

	return e.waitView(ctx, notify)
}

// t3ShellFloorTTL 是版本 floor 的保留时长。floor 的用途是拒掉「配置变更前
// 读出、变更后才到达」的在途请求，这类请求的生命周期是几十秒量级；按**记录
// 时刻**保留 1 小时后清理，floors 表不随时间增长。不按 floor 值（旧版本的
// UpdatedAt）计时：长期未动的环境其旧版本可能远早于 1 小时前，按值清理会让
// floor 一插入就被清掉、守卫失效。
const t3ShellFloorTTL = time.Hour

// t3ShellFloor 是一个环境的版本 floor：version 参与「不晚于即拒」的判定，
// at 是记录时刻（只用于过期清理）。
type t3ShellFloor struct {
	version time.Time
	at      time.Time
}

// Drop 停掉环境的订阅并记版本 floor。幂等。环境 Update（凭证/地址变更）与
// Delete 都调用它。
//
// floor 语义（「不晚于即拒」）：请求携带的 env.UpdatedAt 不晚于 floor 就拒绝
// View，迟到请求不会用旧凭证/旧地址复活 entry；携带新版本的正常请求可以重建。
//   - Update 传**变更前**的版本：floor 必须低于新版本的任何合法请求版本，若用
//     变更后的版本做 floor，更新后的合法请求会因相等被永久拒绝。
//   - Delete 传删除时刻（time.Now().UTC()）：删除必然发生在任何在途请求读出
//     UpdatedAt 之后，因此能拒掉全部在途版本，关闭「Delete 读到 v1、并发
//     Update 提交 v2、迟到请求带 v2 复活已删环境」的路径。
//
// 只淘汰旧代 entry：entry 的版本（建立时的 env.UpdatedAt）不晚于 floor 才停。
// 若 View 已用新配置建了新 entry（版本晚于 floor——例如重配提交后新请求先到、
// 迟到的 Drop 后到），保留新 entry，不得把它关掉。
func (h *t3ShellHub) Drop(userID, envID string, floorVersion time.Time) {
	key := t3ShellHubKey(userID, envID)
	h.mu.Lock()
	now := time.Now().UTC()
	if cur, ok := h.floors[key]; !ok || floorVersion.After(cur.version) {
		h.floors[key] = t3ShellFloor{version: floorVersion, at: now}
	}
	// floors 防膨胀：按记录时刻清理过期 floor（见 t3ShellFloorTTL 的说明）。
	cutoff := now.Add(-t3ShellFloorTTL)
	for k, f := range h.floors {
		if f.at.Before(cutoff) {
			delete(h.floors, k)
		}
	}
	e := h.entries[key]
	if e != nil && e.version.After(floorVersion) {
		e = nil // 新代 entry：保留
	}
	if e != nil {
		delete(h.entries, key)
	}
	h.mu.Unlock()
	if e != nil {
		e.shutdown()
	}
}

// Close 幂等停机：取消所有 entry 并等待全部 goroutine 退出——含已被摘除的
// entry（Drop/替换/淘汰）与各订阅 reader。等待在锁外进行。
func (h *t3ShellHub) Close() {
	h.closeOnce.Do(func() {
		h.mu.Lock()
		h.closed = true
		entries := make([]*t3ShellEntry, 0, len(h.entries))
		for _, e := range h.entries {
			entries = append(entries, e)
		}
		h.entries = map[string]*t3ShellEntry{}
		h.mu.Unlock()

		h.cancel()
		close(h.done)
		for _, e := range entries {
			e.shutdown()
		}
		// closed 已置位，之后不会再有 newEntryLocked（也就不会再 Add），
		// 此时等待排空是安全的。
		h.lifecycle.Wait()
	})
}

// newEntryLocked 创建 entry 并启动三条 goroutine（活跃/归档监督循环 + 空闲
// 回收 ticker）。只做内存操作与 go 语句，网络 I/O 都在 goroutine 内、锁外。
// 调用方持有 hub 锁。
func (h *t3ShellHub) newEntryLocked(key string, env *model.T3Environment, token string) *t3ShellEntry {
	e := &t3ShellEntry{
		hub:         h,
		key:         key,
		envID:       env.ID,
		version:     env.UpdatedAt,
		fingerprint: t3ShellFingerprint(env.Address, token),
		// 连接目标与指纹用同一规范化地址，避免结尾斜杠让 WS 路径拼出 //ws。
		address:    t3ShellNormalizedAddress(env.Address),
		token:      token,
		state:      newT3ShellState(),
		notify:     make(chan struct{}),
		wake:       make(chan struct{}),
		lastUsed:   time.Now(),
		activeConn: t3ShellConnConnecting,
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	h.entries[key] = e

	// 生命周期计数：entry 的全部 goroutine（监督循环经 readStreamLoop 退出前
	// 会汇合各自的订阅 reader）退出后释放。
	h.lifecycle.Add(1)
	e.wg.Add(3)
	go func() {
		defer e.wg.Done()
		h.runFaceSubscription(e, false)
	}()
	go func() {
		defer e.wg.Done()
		h.runFaceSubscription(e, true)
	}()
	go func() {
		defer e.wg.Done()
		h.runIdleReaper(e)
	}()
	// done 在全部监督 goroutine 退出后关闭，供测试汇合；同时释放 hub 的
	// 生命周期计数（Close 经它等待已被摘除的 entry）。
	go func() {
		e.wg.Wait()
		close(e.done)
		h.lifecycle.Done()
	}()
	return e
}

// t3ShellEntry 是一个「用户 × 环境」的常驻订阅。
type t3ShellEntry struct {
	hub *t3ShellHub
	key string
	// envID 只用于日志。
	envID string

	// 以下四个在 entry 生命周期内不变（凭证/地址变更走停旧建新）。
	version     time.Time
	fingerprint string
	address     string
	token       string

	mu sync.Mutex
	// state 的锁在 t3ShellState 内部；锁序恒为 mu → state.mu。
	state        *t3ShellState
	notify       chan struct{} // 关闭并替换以广播状态变化，View select 等待
	lastUsed     time.Time
	activeConn   t3ShellConnState
	activeErr    error
	obsReady     bool
	obsSynced    bool
	obsConn      t3ShellConnState
	lastSyncedAt time.Time

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
	done     chan struct{}

	// forceResync 是活跃面的强制全量重同步请求（F1）：请求了 synchronized 但
	// 预算内未等到时置位；下次活跃重连读后清除，不带 afterSequence，由上游
	// 权威全量快照重新建立追平。同一时刻只有一条活跃连接（监督循环串行），
	// entry 锁足以保护。
	forceResync bool

	// 空闲暂停：paused 期间两条监督循环停在 waitIfPaused，wake 关闭并替换
	// 即唤醒（与 notify 同一「关旧换新」模式）。activeCancel/archivedCancel
	// 是当前在飞连接的取消函数，暂停时用它断开连接但保住内存快照。
	paused         bool
	wake           chan struct{}
	activeCancel   context.CancelFunc
	archivedCancel context.CancelFunc
}

// setConnCancel 登记/清除当前连接取消函数。调用方不得持有 entry 锁。
//
// 竞态守卫（F4）：监督循环先过 waitIfPaused 再登记 cancel，reaper 可能恰在
// 这两步之间置暂停——若登记时不复查，这条连接会漏掉取消、一直挂到空闲淘汰。
// 因此在同一把 entry 锁内复查 paused/isStopped，已暂停或已停止就立刻取消
// 刚登记的这条连接（cancel 调用在锁外，关连接的等待不持锁）。
func (e *t3ShellEntry) setConnCancel(archived bool, cancel context.CancelFunc) {
	e.mu.Lock()
	if archived {
		e.archivedCancel = cancel
	} else {
		e.activeCancel = cancel
	}
	mustCancel := cancel != nil && (e.paused || e.isStopped())
	e.mu.Unlock()
	if mustCancel {
		cancel()
	}
}

// setForceResync 置强制全量重同步标记（F1：marker 预算耗尽仍未追平时）。
func (e *t3ShellEntry) setForceResync() {
	e.mu.Lock()
	e.forceResync = true
	e.mu.Unlock()
}

// takeForceResync 读取并清除强制全量重同步标记（构造订阅 payload 时用：
// 置位时本次重连不带 afterSequence）。
func (e *t3ShellEntry) takeForceResync() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	resync := e.forceResync
	e.forceResync = false
	return resync
}

// resumeLocked 结束暂停并唤醒监督循环。调用方持有 entry 锁。
func (e *t3ShellEntry) resumeLocked() {
	if !e.paused {
		return
	}
	e.paused = false
	close(e.wake)
	e.wake = make(chan struct{})
}

// pauseLocked 置暂停标记并收走当前两条连接的取消函数。调用方持有 entry 锁，
// 且「lastUsed 采样 → 暂停决策 → 置标记」必须在同一临界区内完成——决策若
// 基于锁外旧采样，刚被 View 刷新过 lastUsed 的 entry 会被误暂停（F4）。返回
// 的取消函数由调用方在锁外逐个调用（关连接的等待不持锁）。
func (e *t3ShellEntry) pauseLocked() []context.CancelFunc {
	e.paused = true
	return []context.CancelFunc{e.activeCancel, e.archivedCancel}
}

// waitIfPaused 在空闲暂停期间阻塞。返回 false 表示 entry 已停止或 hub 已关闭，
// 监督循环应当退出。
func (e *t3ShellEntry) waitIfPaused() bool {
	for {
		e.mu.Lock()
		paused := e.paused
		wake := e.wake
		stopped := e.isStopped()
		e.mu.Unlock()
		if stopped {
			return false
		}
		if !paused {
			return true
		}
		select {
		case <-wake:
		case <-e.stop:
			return false
		case <-e.hub.ctx.Done():
			return false
		}
	}
}

func (e *t3ShellEntry) shutdown() {
	e.stopOnce.Do(func() { close(e.stop) })
}

func (e *t3ShellEntry) isStopped() bool {
	select {
	case <-e.stop:
		return true
	default:
		return false
	}
}

// refreshLocked 在活跃面可信标记或连接状态变化时更新观察值并广播（关闭并
// 替换 notify）。调用方持有 entry 锁。
func (e *t3ShellEntry) refreshLocked() {
	st := e.state.status()
	changed := false
	if st.ActiveReady != e.obsReady {
		e.obsReady = st.ActiveReady
		changed = true
	}
	if st.ActiveSynced != e.obsSynced {
		e.obsSynced = st.ActiveSynced
		if st.ActiveSynced {
			e.lastSyncedAt = time.Now()
		}
		changed = true
	}
	if e.activeConn != e.obsConn {
		e.obsConn = e.activeConn
		changed = true
	}
	if changed {
		close(e.notify)
		e.notify = make(chan struct{})
	}
}

// noteConnState 更新指定面的连接状态并检查翻转。connErr 只在非空时覆盖：
// 停机取消产生的空错误不抹掉上一次的真实失败原因。
func (e *t3ShellEntry) noteConnState(archived bool, state t3ShellConnState, connErr error) {
	e.mu.Lock()
	if !archived {
		e.activeConn = state
		if connErr != nil {
			e.activeErr = connErr
		}
	}
	e.refreshLocked()
	e.mu.Unlock()
}

// refresh 在状态可能变化后（应用增量/标记翻转）从锁外调用。
func (e *t3ShellEntry) refresh() {
	e.mu.Lock()
	e.refreshLocked()
	e.mu.Unlock()
}

// viewLocked 组装当前视图。调用方持有 entry 锁。
func (e *t3ShellEntry) viewLocked() *t3ShellView {
	st := e.state.status()
	view := &t3ShellView{}
	switch {
	case !st.ActiveReady:
		if e.activeConn == t3ShellConnFailed {
			view.State = t3ShellFailed
			view.Err = e.activeErr
		} else {
			view.State = t3ShellSyncing
		}
	case e.activeConn == t3ShellConnSubscribed && st.ActiveSynced:
		view.State = t3ShellReady
		view.Snaps = e.state.snapshots()
	default:
		view.State = t3ShellStale
		view.Snaps = e.state.snapshots()
		view.Err = e.activeErr
		if !e.lastSyncedAt.IsZero() {
			view.SyncedAgo = time.Since(e.lastSyncedAt)
		}
	}
	return view
}

// waitView 等到可回答（有权威快照 / 首次建连失败 / entry 已停止 / ctx 或 hub
// 结束）后返回视图。notify 是进入等待前抓住的广播 channel，状态翻转后 entry
// 会关闭它唤醒全部等待者。
func (e *t3ShellEntry) waitView(ctx context.Context, notify chan struct{}) (*t3ShellView, error) {
	for {
		e.mu.Lock()
		view := e.viewLocked()
		hasAuthority := e.state.status().ActiveReady
		firstFailed := !hasAuthority && e.activeConn == t3ShellConnFailed
		stopped := e.isStopped()
		notifyCh := e.notify
		e.mu.Unlock()

		// 等待目标：活跃面有权威快照（设计 B3）；或首次建连失败（让调用方
		// 尽快把凭证类失败转成「请重新配对」）；或 entry 已停止（Drop/替换/
		// 回收竞态下按当前状态尽力回答，不再等）。
		if hasAuthority || firstFailed || stopped {
			return view, nil
		}
		select {
		case <-notifyCh:
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				// 预算内活跃面还没就绪：如实返回当前视图（Syncing/Stale），
				// 由调用方按新鲜度呈现；把慢冷启动伪装成「上游不可达」才是
				// 错的。请求被取消（客户端断开）仍按取消传播。
				return view, nil
			}
			return nil, ctx.Err()
		case <-e.hub.done:
			return nil, fmt.Errorf("t3 shell hub 已关闭")
		}
	}
}

// t3ShellConnResult 是一次连接尝试的结局，供监督循环决定退避。
type t3ShellConnResult struct {
	credential bool      // 凭证被拒（探测 repair / Exit 鉴权失败）→ 长退避
	syncedAt   time.Time // 本连接追平确认时刻；零值表示从未追平
}

// runFaceSubscription 单条订阅的监督循环（每条连接一个 goroutine）：
// 探测 → 拨号 → 订阅 → 读循环，结束后按分类退避重连。
func (h *t3ShellHub) runFaceSubscription(e *t3ShellEntry, archived bool) {
	attempts := 0
	for {
		if !e.waitIfPaused() {
			return
		}

		result := h.connectFaceOnce(e, archived)

		// 退避：1s→2s→4s…上限 30s，±25% jitter。同步稳定超过
		// StableResetAfter 的连接断开后从 1s 重来——长期健康的连接不应
		// 背着历史失败的重试深度。
		if !result.syncedAt.IsZero() && time.Since(result.syncedAt) > h.cfg.StableResetAfter {
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

// backoffFor 计算下一次重连前的等待。凭证被拒走固定长退避（换凭证前重试
// 没有意义）；其余按指数退避加 ±jitter 抖动。
func (h *t3ShellHub) backoffFor(attempts int, credential bool) time.Duration {
	if credential {
		return h.cfg.CredentialBackoff
	}
	raw := h.cfg.BackoffBase
	for i := 1; i < attempts && raw < h.cfg.BackoffMax; i++ {
		raw *= 2
	}
	if raw > h.cfg.BackoffMax {
		raw = h.cfg.BackoffMax
	}
	jitter := 1 - h.cfg.BackoffJitter + 2*h.cfg.BackoffJitter*rand.Float64()
	return time.Duration(float64(raw) * jitter)
}

// connectFaceOnce 跑一次完整的连接尝试，阻塞到连接结束（失败/流终止/停机）。
// connCtx 派生自 hub 服务生命周期 ctx，entry 停止或 hub 关闭时关连接解开
// 全部阻塞 I/O；调用方的请求 ctx 不参与。
func (h *t3ShellHub) connectFaceOnce(e *t3ShellEntry, archived bool) t3ShellConnResult {
	connCtx, cancelConn := context.WithCancel(h.ctx)
	defer cancelConn()
	e.setConnCancel(archived, cancelConn)
	defer e.setConnCancel(archived, nil)
	go func() {
		select {
		case <-e.stop:
			cancelConn()
		case <-connCtx.Done():
		}
	}()

	// HTTP 会话探测先行：WS 握手失败不带状态码，凭证失效与网络不可达在
	// WS 侧无法区分；探测 repair 说明凭证被拒，长退避并把错误分类给 View。
	status, probeMsg := h.http.probeSession(connCtx, e.address, e.token)
	if status != T3EnvironmentStatusOK {
		probeErr := fmt.Errorf("会话探测失败: %s", probeMsg)
		credential := false
		if status == T3EnvironmentStatusRepair {
			probeErr = fmt.Errorf("%w: %s", errT3CredentialRejected, probeMsg)
			credential = true
		}
		h.endFaceConnection(e, archived, probeErr)
		return t3ShellConnResult{credential: credential}
	}

	// 拨号独立预算，不由任何请求 ctx 决定。
	dialCtx, cancelDial := context.WithTimeout(connCtx, h.cfg.DialBudget)
	session, err := dialT3RPC(dialCtx, wsBaseOf(e.address), e.token)
	cancelDial()
	if err != nil {
		h.endFaceConnection(e, archived, err)
		return t3ShellConnResult{}
	}
	defer session.Close()
	unregisterCancel := session.closeOnCancel(connCtx)
	defer unregisterCancel()

	e.noteConnState(archived, t3ShellConnConnecting, nil)

	method := t3RPCSubscribeShell
	if archived {
		method = t3RPCSubscribeArchivedShell
	}

	wantMarker := false
	if !archived {
		// 完成标记能力查询（订阅前发：此刻这条连接上只有本请求的帧，
		// one-shot call 不会误吞订阅流）。缺能力时不请求标记并立即视为
		// 追平，与官方客户端一致。
		value, err := session.call(connCtx, t3RPCServerGetConfig, map[string]any{})
		if err != nil {
			result := t3ShellConnResult{}
			if errors.Is(err, errT3CredentialRejected) {
				result.credential = true
			}
			h.endFaceConnection(e, archived, err)
			return result
		}
		var config struct {
			ShellResumeCompletionMarker bool `json:"shellResumeCompletionMarker"`
		}
		if err := json.Unmarshal(value, &config); err != nil {
			err := fmt.Errorf("getConfig 响应解析失败: %w", err)
			h.endFaceConnection(e, archived, err)
			return t3ShellConnResult{}
		}
		wantMarker = config.ShellResumeCompletionMarker
	}

	// 订阅。活跃面带恢复游标（有过权威快照才有意义）；归档订阅没有
	// afterSequence，重连即重新拉全量归档快照。
	payload := map[string]any{}
	if !archived {
		// 强制全量重同步（F1）：上一条活跃连接请求了 marker 但预算内未等到
		// ——不认为追平，本次重连不带 afterSequence。上游收到无游标的订阅
		// 会直接发权威全量快照（与序号失效回退同一语义，ws.ts:550,685,1141），
		// 权威帧到达才重新视为追平；期间视图保持 Stale/Syncing。
		if e.takeForceResync() {
			// 置位时已未追平，这里再显式清一次：任何交错都不得把旧缓存
			// 在权威快照到达前当成可信。
			e.state.markActiveSynced(false)
			e.refresh()
		} else if st := e.state.status(); st.ActiveReady {
			payload["afterSequence"] = st.ActiveCursor
			// 进入追平阶段：清标记，等权威帧/标记重新置位——追平前的
			// 窗口 View 应当是 Stale 而不是 Ready。
			e.state.markActiveSynced(false)
			e.refresh()
		}
		if wantMarker {
			payload["requestCompletionMarker"] = true
		}
	}
	requestID := session.nextID
	session.nextID++
	if err := session.sendRequest(connCtx, requestID, method, payload); err != nil {
		h.endFaceConnection(e, archived, err)
		return t3ShellConnResult{}
	}
	e.noteConnState(archived, t3ShellConnSubscribed, nil)

	if !archived && !wantMarker {
		// 能力不支持：不等标记，立即视为追平（对齐官方客户端）。
		e.state.markActiveSynced(true)
		e.refresh()
	}

	// marker 等待截止时刻：预算内没等到 synchronized 就不再假装追平，由
	// readStreamLoop 的 ticker 分支置强制全量重同步并结束本连接。零值表示
	// 未请求 marker（能力不支持 / 归档面），不存在等待。
	var markerDeadline time.Time
	if !archived && wantMarker {
		markerDeadline = time.Now().Add(h.cfg.MarkerWaitTimeout)
	}

	return h.readStreamLoop(e, session, connCtx, requestID, archived, markerDeadline)
}

// readStreamLoop 订阅后的读循环：专用 reader 只读（每次 Receive 前设 read
// deadline，到点即判死），本循环只写（Ack/Ping）。markerDeadline 是本连接的
// synchronized 等待截止时刻（零值 = 未请求 marker）：预算内未追平不得把未
// 追平的缓存提升为 Ready（F1），置强制全量重同步并结束本连接，交给监督循环
// 重连。返回连接结局。
func (h *t3ShellHub) readStreamLoop(e *t3ShellEntry, session *t3RPCSession, connCtx context.Context, requestID int, archived bool, markerDeadline time.Time) t3ShellConnResult {
	// reader：x/net/websocket 的 Receive 超时后连接不可续读，任何读错误
	// （含超时）都终结本连接，交给监督循环重连。stop 与 connCtx 一起进
	// select：主循环收尾时叫停 reader，保证 frames 缓冲满时 reader 也不会
	// 卡在发送上（F8）。
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
	// 收尾顺序（F8）：先关连接——x/net 的 Receive 只能靠连接关闭或读 deadline
	// 解开，等 deadline 会让收尾挂上一个帧超时（默认 75s）；再叫停并汇合
	// reader。连接的其余清理由 connectFaceOnce 的 defer 完成（Close 幂等）。
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
	for {
		select {
		case <-connCtx.Done():
			// 停机/暂停取消：归档面同样终止（F2 统一撤销归档可信度），
			// err 为 nil 时记「未就绪」而不抹掉既有失败原因。
			h.endFaceConnection(e, archived, nil)
			return t3ShellConnResult{syncedAt: e.syncConfirmedAt()}

		case frame, ok := <-frames:
			if !ok {
				// reader 退出：读错误（超时/断开）即判死重连。
				select {
				case connErr = <-readErrCh:
				default:
					connErr = fmt.Errorf("shell 订阅连接读取中断")
				}
				h.endFaceConnection(e, archived, connErr)
				return t3ShellConnResult{credential: credential, syncedAt: e.syncConfirmedAt()}
			}
			lastFrame = time.Now()
			switch {
			case frame.Tag == "Chunk" && frame.requestIDMatches(requestID):
				// 先应用全部 value（任一出错即整条流失败），成功才 Ack。
				var applyErr error
				if archived {
					applyErr = h.applyArchiveValues(e, frame.Values)
				} else {
					applyErr = h.applyActiveValues(e, frame.Values)
				}
				if applyErr != nil {
					// 归档流数据路径失败经 endFaceConnection 整体弃用归档面
					// （G1），活跃面照常可用。
					h.endFaceConnection(e, archived, applyErr)
					return t3ShellConnResult{syncedAt: e.syncConfirmedAt()}
				}
				e.refresh()
				if err := session.sendAck(connCtx, frame.RequestID); err != nil {
					h.endFaceConnection(e, archived, fmt.Errorf("发送 Ack 失败: %w", err))
					return t3ShellConnResult{syncedAt: e.syncConfirmedAt()}
				}

			case frame.Tag == "Exit" && frame.requestIDMatches(requestID):
				if _, err := frame.exitValue(); err != nil {
					credential = errors.Is(err, errT3CredentialRejected)
					h.endFaceConnection(e, archived, err)
				} else {
					// Exit(Success)：订阅流被服务端正常收尾，对本客户端
					// 而言等同失去数据来源，按会话失败重连。
					h.endFaceConnection(e, archived, fmt.Errorf("shell 订阅流提前结束"))
				}
				return t3ShellConnResult{credential: credential, syncedAt: e.syncConfirmedAt()}

			default:
				// Pong 等其余帧：按「收到帧」刷新活性计时即可。
			}

		case <-pingTicker.C:
			// marker 截止检查（F1）：请求了 synchronized 但预算内仍未追平，
			// 就置强制全量重同步并结束本连接；已追平（标记或权威帧已到）
			// 则撤销等待，后续只做保活。
			if !markerDeadline.IsZero() && time.Now().After(markerDeadline) {
				if st := e.state.status(); !st.ActiveSynced {
					e.setForceResync()
					h.endFaceConnection(e, archived, fmt.Errorf("未在预算内等到 synchronized 标记，改走强制全量重同步"))
					return t3ShellConnResult{credential: credential, syncedAt: e.syncConfirmedAt()}
				}
				markerDeadline = time.Time{}
			}
			// 距最近完整帧超过 PingInterval 就发应用层 Ping（Effect RPC
			// {"_tag":"Pong"} 应答）。写失败视同连接死亡。
			if time.Since(lastFrame) > h.cfg.PingInterval {
				if err := session.sendRaw(connCtx, map[string]any{"_tag": "Ping"}); err != nil {
					h.endFaceConnection(e, archived, fmt.Errorf("发送 Ping 失败: %w", err))
					return t3ShellConnResult{credential: credential, syncedAt: e.syncConfirmedAt()}
				}
			}
		}
	}
}

// endFaceConnection 收尾连接状态。归档面（F2）：任何终止路径——读错误/写
// 失败/正常 Exit/取消/探测或拨号失败——统一撤销归档面可信度（清空归档两表 +
// ArchiveErr），直到下一次归档权威快照到达才恢复；归档数据只有伴随健康连接
// 才可信，凭缓存继续宣称归档命中可信会违反 G1。err 为 nil（停机/暂停取消）
// 时记「未就绪」。活跃面只更新连接状态与失败分类：单面终止不推翻活跃数据
// （G1），View 的 Stale 判定用得上最近一次真实失败原因。
func (h *t3ShellHub) endFaceConnection(e *t3ShellEntry, archived bool, connErr error) {
	if archived {
		e.state.markArchiveFailed(connErr)
	}
	e.noteConnState(archived, t3ShellConnFailed, connErr)
	if connErr != nil {
		h.logFaceFailure(e, archived, connErr)
	}
}

// syncConfirmedAt 返回本 entry 最近一次追平确认时刻（仅在仍处追平态时有效，
// 供退避的稳定判定使用）。
func (e *t3ShellEntry) syncConfirmedAt() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.obsSynced {
		return e.lastSyncedAt
	}
	return time.Time{}
}

// applyActiveValues 把活跃流 Chunk 的全部 value 依次解码并应用到状态。
func (h *t3ShellHub) applyActiveValues(e *t3ShellEntry, values []json.RawMessage) error {
	for _, raw := range values {
		var item t3ShellStreamItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return fmt.Errorf("活跃流帧解析失败: %w", err)
		}
		if err := e.state.applyActiveItem(item); err != nil {
			return err
		}
	}
	return nil
}

// applyArchiveValues 把归档流 Chunk 的全部 value 依次解码并应用到状态。
func (h *t3ShellHub) applyArchiveValues(e *t3ShellEntry, values []json.RawMessage) error {
	for _, raw := range values {
		var item t3ArchiveStreamItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return fmt.Errorf("归档流帧解析失败: %w", err)
		}
		if err := e.state.applyArchiveItem(item); err != nil {
			return err
		}
	}
	return nil
}

// logFaceFailure 记录连接失败；归档面失败不推翻活跃面（G1），日志只作观测。
func (h *t3ShellHub) logFaceFailure(e *t3ShellEntry, archived bool, err error) {
	face := "active"
	if archived {
		face = "archive"
	}
	h.logger.Warn("t3 shell 订阅连接结束",
		zap.String("environment_id", e.envID),
		zap.String("face", face),
		zap.Error(err))
}

// runIdleReaper 每 entry 一个轻量 ticker：超过 IdleDisconnect 先断连保值，
// 超过 IdleTTL 自停并从 hub 摘除（仅当注册表里仍是本 entry）。不做独立 janitor。
func (h *t3ShellHub) runIdleReaper(e *t3ShellEntry) {
	ticker := time.NewTicker(h.cfg.IdleCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-h.ctx.Done():
			return
		case <-ticker.C:
		}
		if h.reapTick(e) {
			return
		}
	}
}

// reapTick 执行一次空闲检查，返回 true 表示 entry 已淘汰、reaper 应退出。
// lastUsed 的采样与 pause/淘汰决策在同一临界区内完成（F4）：决策若基于锁外
// 采样的旧值，刚被 View 刷新过 lastUsed 的 entry 会被误暂停/误淘汰。行动
// （注册表摘除、关连接）都在锁外进行。
func (h *t3ShellHub) reapTick(e *t3ShellEntry) bool {
	e.mu.Lock()
	idle := time.Since(e.lastUsed)
	evict := idle > h.cfg.IdleTTL
	var cancels []context.CancelFunc
	pause := !evict && h.cfg.IdleDisconnect > 0 && !e.paused && idle > h.cfg.IdleDisconnect
	if pause {
		cancels = e.pauseLocked()
	}
	e.mu.Unlock()

	if evict {
		h.mu.Lock()
		if h.entries[e.key] == e {
			delete(h.entries, e.key)
		}
		h.mu.Unlock()
		e.shutdown()
		return true
	}
	for _, cancel := range cancels {
		if cancel != nil {
			cancel()
		}
	}
	return false
}
