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

// T3 shell 订阅复用 hub（INT-116）：每个「用户 × 环境」持一条活跃面长驻
// WS 订阅连接，把原来每个请求一次的全量 shell 快照拉取摊销成常驻订阅 +
// 增量推送；归档面自 INT-173 起改为按需一次性快照——上游 #26238 删除了
// orchestration.subscribeArchivedShell，官方客户端已改用
// getArchivedShellSnapshot（见 t3ShellEntry 的 arch* 字段与 t3ArchiveFetchState）。
// 旧上游（#26238 之前）归档面只有常驻 subscribeArchivedShell：按需快照
// 首次读取被判方法拒绝（确定性 Exit Failure）时，entry 永久切到回退的
// 常驻订阅路径（见 t3_shell_archive.go 的 mode 判定与
// runArchiveLegacySubscription）。
// INT-132 起订阅跟环境走（启动预热、环境增删改时增删），不再按读请求的
// 生灭做空闲回收；变更经 t3_shell_events.go 推给浏览器。
// 设计见 INT-116 v2：
//   - 常驻连接只有活跃面一条：t3RPCSession 是串行客户端（call 会吞掉其他
//     requestId 的帧），归档按需快照另拨一次性会话，不复用订阅连接；
//     取消/重订阅互不牵扯。
//   - 完成标记能力来自 server.getConfig 的 shellResumeCompletionMarker
//     （ws.ts:1731）；官方客户端据此决定是否请求 synchronized 标记
//     （client-runtime/src/state/shell.ts:338,386-398）：不支持时不请求并
//     立即视为追平。
//   - Ping/Pong 是 Effect RPC 应用层帧 {"_tag":"Ping"} → {"_tag":"Pong"}
//     （effect rpc/RpcServer.ts:806、RpcMessage.ts constPing/constPong），
//     不是 WS 控制帧。
//
// 锁边界：hub 锁只护 entry 注册表与 floor；entry 锁只护 state/游标/标记/
// 通知与连接状态；subsMu 只护事件订阅注册表（INT-132），发布点全部在 entry
// 锁之外调用且绝不拿 hub.mu——现有锁序 hub.mu → entry.mu，任何反向持锁都会
// 死锁。网络 I/O、Close、等 goroutine 退出全部在锁外。锁序恒为
// entry.mu → state.mu（state 方法内部自带锁），不存在反向。

const (
	t3RPCSubscribeShell = "orchestration.subscribeShell"
	// t3RPCGetArchivedShellSnapshot 是归档面的一次性快照 RPC（上游 #26238
	// 起保留的唯一归档读取口）：返回 t3ArchivedShellSnapshot 形状的全量
	// 数据，失败以 Exit Failure 明确表达。
	t3RPCGetArchivedShellSnapshot = "orchestration.getArchivedShellSnapshot"
	// t3RPCSubscribeArchivedShell 是旧上游的归档常驻订阅（#26238 之前）：
	// 仅在按需快照被判方法拒绝后由旧面回退路径使用（t3_shell_archive.go）。
	t3RPCSubscribeArchivedShell = "orchestration.subscribeArchivedShell"
	t3RPCServerGetConfig        = "server.getConfig"
)

// errT3ShellConfigStale 是 View 的版本守卫哨兵：请求携带的环境配置世代
// （config_revision）小于已知的 floor/entry 世代——请求是在配置变更前读出的
// 迟到请求。调用方应当重读环境后重试，而不是用旧配置建订阅。
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
	// ArchiveTTL 归档快照的缓存有效期（INT-173）：归档面不再是常驻推送的
	// 实时面，一次 getArchivedShellSnapshot 结果只在 TTL 内当可信复用，
	// 过期重拉。取值依据：归档集变化只发生在归档/恢复归档等写命令之后
	// （派发成功即主动失效），TTL 只需兜住「别处的归档操作」与快照竞态，
	// 60s 既能摊销连续列表/详情的读取，也不会让陈旧归档数据停留太久。
	// 改变归档集的写操作经 InvalidateArchive 立即失效，不靠 TTL 兜底。
	ArchiveTTL time.Duration
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
		ArchiveTTL:        60 * time.Second,
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

	// 事件订阅注册表（INT-132）：按用户分组的订阅者集合。subsMu 独立于
	// hub.mu：发布发生在 entry 数据路径上，若与 entry 锁共用一把锁，一条
	// 流帧的合并就要和 View 准入互斥；独立锁让发布只与其它发布互斥。
	subsMu      sync.Mutex
	subsStopped bool
	subs        map[string]map[*T3ShellEventSubscription]struct{}

	// lifecycle 是 hub 级生命周期在飞计数：每个 entry Add(1)，entry 的全部
	// goroutine（监督循环在退出前汇合各自的订阅 reader）退出后 Done。Close
	// 等它排空——只遍历注册表会漏掉已被 Drop/凭证替换摘除、但 goroutine
	// 还没退干净的 entry。
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
		subs:    map[string]map[*T3ShellEventSubscription]struct{}{},
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

// admitEntry 是 entry 的统一准入：closed 检查、指纹+世代停旧建新、floor 拒绝、
// 不存在则建。View 与 Ensure 共用，保证「读」与「预热」对同一环境只会得到
// 同一个 entry，不会各自建一套订阅。
//
// 版本守卫（世代 = t3_environments.config_revision，只在地址/凭证变更时 SQL
// 自增、改名不递增）：entry 记建立时的世代与凭证指纹。指纹不一致且请求世代
// 更高才停旧建新；否则拒绝（errT3ShellConfigStale），不打断现有 entry——迟到
// 的旧世代不得替换新 entry。entry 不存在但 floor[key] 高于请求世代时同样拒绝
// （请求 revision < cutoff 即拒）——防「删除前读出的请求在 Drop 之后把 entry
// 复活」。
func (h *t3ShellHub) admitEntry(env *model.T3Environment, token string) (*t3ShellEntry, error) {
	key := t3ShellHubKey(env.UserID, env.ID)
	fingerprint := t3ShellFingerprint(env.Address, token)

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, fmt.Errorf("t3 shell hub 已关闭")
	}
	e := h.entries[key]
	if e != nil && e.fingerprint != fingerprint {
		if env.ConfigRevision <= e.configRevision {
			return nil, fmt.Errorf("%w: 环境凭证或地址正在变更", errT3ShellConfigStale)
		}
		// 凭证/地址已变且世代更高：新 entry 先注册占住 key，旧 entry 异步自停
		// （关连接与 goroutine 退出都在锁外），不打断新 entry。
		delete(h.entries, key)
		old := e
		old.shutdown()
		e = nil
	}
	if e == nil {
		if floor, ok := h.floors[key]; ok && env.ConfigRevision < floor.revision {
			return nil, fmt.Errorf("%w: 环境配置已被替换或删除", errT3ShellConfigStale)
		}
		e = h.newEntryLocked(key, env, token)
	}
	return e, nil
}

// Ensure 确保环境的常驻订阅 entry 存在，不等待同步（INT-132）：订阅在后台
// 建立，首个权威快照到达前 View 照常回答 Syncing，语义不变。启动预热、环境
// 落库后调用——读取路径从「读请求驱动建连」改为「环境常驻连接 + 推送」后，
// entry 的生命周期跟环境走，不再跟随请求出现与消失。
func (h *t3ShellHub) Ensure(env *model.T3Environment, token string) error {
	_, err := h.admitEntry(env, token)
	return err
}

// View 读取环境的 shell 状态，必要时建 entry 并等待活跃面出现权威快照。
//
// ctx 只用于取消等待，不参与建连：entry 的订阅跑在 hub 的服务生命周期 ctx 上，
// 请求取消不影响后台同步。等待结束条件：活跃面出现权威快照、或首次建连失败、
// 或 ctx 结束。
func (h *t3ShellHub) View(ctx context.Context, env *model.T3Environment, token string) (*t3ShellView, error) {
	// 请求预算已到（典型是同一轮里前面的环境吃完了整轮预算）：此时不要建
	// entry、更不要拉起两条常驻订阅——它们是跟环境走的常驻资源，一次取消的
	// 请求没有资格决定建与不建。
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e, err := h.admitEntry(env, token)
	if err != nil {
		return nil, err
	}
	return e.waitView(ctx)
}

// Peek 读取环境的 shell 状态当前值，不等待首次同步：entry 不存在时只注册
// （订阅照常在后台建立），随即按内存现状回答（Syncing/Failed 时无快照）。
// wait 评估器（INT-160）每轮扫描只读内存，绝不阻塞等待。
// 归档面是按需快照：Peek 同样触发一次后台补齐（TTL 内命中只标「已在途」，
// 不会真正拨号；拉取跑在服务 ctx 上，不受请求生灭影响）。wait 评估器的
// 线程消迩判定要的就是「下一轮看到的归档面尽量新」这个保证。
func (h *t3ShellHub) Peek(env *model.T3Environment, token string) (*t3ShellView, error) {
	e, err := h.admitEntry(env, token)
	if err != nil {
		return nil, err
	}
	e.kickArchiveRefresh()
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.viewLocked(), nil
}

// t3ShellFloorTTL 是配置世代 floor 的保留时长。floor 的用途是拒掉「配置变更前
// 读出、变更后才到达」的在途请求，这类请求的生命周期是几十秒量级；按**记录
// 时刻**保留 1 小时后清理，floors 表不随时间增长。清理只能以记录时刻为准：
// floor 值（revision）本身没有时间含义，按值清理无从谈起。
const t3ShellFloorTTL = time.Hour

// t3ShellFloor 是一个环境的配置世代 floor：revision 是「最早可接受的新世代」，
// at 是记录时刻（只用于过期清理）。
type t3ShellFloor struct {
	revision int64
	at       time.Time
}

// Drop 停掉环境的订阅并记配置世代 floor（cutoff）。幂等。环境 Update（凭证/
// 地址变更）与 Delete 都调用它。
//
// floor 语义（**请求 revision < cutoff 即拒**）。cutoff 就是「变更后的新世代」：
//   - Update 传落库后重读出的 config_revision（本次 SQL 自增产生的值）：变更前
//     读出的请求全部被拒——包括并发改名后仍带着旧凭证的行，改名不递增 revision，
//     它们的世代与变更前相同，同样小于 cutoff。
//   - Delete 传 math.MaxInt64 哨兵：淘汰条件 e.configRevision < cutoff 恒真，
//     entry 无条件关闭；floor 也记 MaxInt64，之后任何迟到的 Ensure/View（在途
//     请求携带的世代必然有限）都被拒。环境 ID 不可复用，永久拒绝正是该语义的
//     正确表达。不能传「删除前 revision + 1」：并发重配先把世代升到 r+1 再被
//     删除时，旧 cutoff=r+1 会保留同代 entry，迟到的 Ensure 也能借同代指纹
//     通过——用哨兵不给这类交错留窗口。floor 过期后（记录时刻起 1 小时）被
//     GC 无碍：环境行已删，预热（ListAll）与读写路径都不会再为它建 entry，
//     能撞上 floor 的只剩几分钟生命周期的在途请求。
//
// 只淘汰旧代 entry：entry 的世代（建立时的 env.ConfigRevision）小于 cutoff 才
// 停；已用新配置建立的同代 entry（revision == cutoff）保留，不得关掉——真实
// 流程里「重配 Drop 后新请求重建 entry，又一次幂等 Drop」就是这个形状。哨兵
// cutoff（MaxInt64）恒大于任何世代，delete 必然发生，是该规则的自然特例。
func (h *t3ShellHub) Drop(userID, envID string, cutoffRevision int64) {
	key := t3ShellHubKey(userID, envID)
	h.mu.Lock()
	now := time.Now().UTC()
	if cur, ok := h.floors[key]; !ok || cutoffRevision > cur.revision {
		h.floors[key] = t3ShellFloor{revision: cutoffRevision, at: now}
	}
	// floors 防膨胀：按记录时刻清理过期 floor（见 t3ShellFloorTTL 的说明）。
	gcCutoff := now.Add(-t3ShellFloorTTL)
	for k, f := range h.floors {
		if f.at.Before(gcCutoff) {
			delete(h.floors, k)
		}
	}
	e := h.entries[key]
	if e != nil && e.configRevision < cutoffRevision {
		delete(h.entries, key)
	} else {
		e = nil // 同代或更新代 entry：保留
	}
	h.mu.Unlock()
	if e != nil {
		e.shutdown()
	}
}

// Close 幂等停机：取消所有 entry 并等待本 hub 启动的 goroutine 退出——含已被
// 摘除的 entry（Drop/替换/淘汰）、各订阅 reader 与连接取消监听。等待在锁外
// 进行。
//
// 明确不纳入等待的：会话层 context.AfterFunc 取消回调（t3_ws.go 的
// closeOnCancel，其 stop 函数按 Go 语义不等待已启动的回调），它只做一次
// conn.Close()，可能在 Close 返回后的极短时间内完成；不影响连接已关闭这一
// 事实，也不构成泄漏。
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

		// 事件订阅全部关闭：消费方（SSE handler）以信号 channel 关闭为退出
		// 条件。先在 subsMu 内摘空注册表，再在锁外逐个 Close——sub.Close 会
		// 回调 unsubscribe（拿 subsMu），锁内直接调会自锁。
		h.subsMu.Lock()
		subs := make([]*T3ShellEventSubscription, 0)
		for _, set := range h.subs {
			for sub := range set {
				subs = append(subs, sub)
			}
		}
		h.subs = map[string]map[*T3ShellEventSubscription]struct{}{}
		h.subsStopped = true
		h.subsMu.Unlock()
		for _, sub := range subs {
			sub.Close()
		}

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

// newEntryLocked 创建 entry 并启动两条 goroutine：活跃面监督循环，与停泊
// 中的归档面 goroutine（默认等待旧面回退激活，见 t3_shell_archive.go 的
// mode 判定——entry 创建时不知道上游新旧，旧面订阅只能在第一次按需拉取
// 被判方法失败后启动；停泊避免「运行中途 wg.Add」与 done 观察者的
// Wait 竞态）。只做内存操作与 go 语句，网络 I/O 都在 goroutine 内、锁外。
// 调用方持有 hub 锁。归档面正常情况下不建常驻连接：INT-173 起按需快照，
// 快照到达前的归档面表达为「未就绪」。
func (h *t3ShellHub) newEntryLocked(key string, env *model.T3Environment, token string) *t3ShellEntry {
	e := &t3ShellEntry{
		hub:            h,
		key:            key,
		userID:         env.UserID,
		envID:          env.ID,
		configRevision: env.ConfigRevision,
		fingerprint:    t3ShellFingerprint(env.Address, token),
		// 连接目标与指纹用同一规范化地址，避免结尾斜杠让 WS 路径拼出 //ws。
		address:    t3ShellNormalizedAddress(env.Address),
		token:      token,
		state:      newT3ShellState(),
		notify:     make(chan struct{}),
		activeConn: t3ShellConnConnecting,
		archTTL:    h.cfg.ArchiveTTL,
		legacyCh:   make(chan struct{}),
		// 缓冲 1：信号丢失/重复都无害（读循环消费前再核对 state 备忘，
		// 见 readArchiveLegacyLoop 的 legacyReconnect 分支）。
		legacyReconnect: make(chan struct{}, 1),
		stop:            make(chan struct{}),
		done:            make(chan struct{}),
	}
	h.entries[key] = e

	// 生命周期计数：entry 的全部 goroutine（监督循环经 readStreamLoop 退出前
	// 会汇合各自的订阅 reader）退出后释放。归档按需拉取是后台一次性任务
	// （arch.inFlight 合并并发，entry 停/hub 关即中断），不占常驻 goroutine，
	// 不计入 e.wg；旧面订阅 goroutine 则常驻，占 e.wg 一份。
	h.lifecycle.Add(1)
	e.wg.Add(2)
	go func() {
		defer e.wg.Done()
		h.runFaceSubscription(e, false)
	}()
	go func() {
		defer e.wg.Done()
		// 停泊：旧面回退从未激活（新上游）时随 entry 停/hub 关退出；激活后
		// 转为常驻订阅监督循环。
		select {
		case <-e.legacyCh:
		case <-e.stop:
			return
		case <-h.ctx.Done():
			return
		}
		h.runArchiveLegacySubscription(e)
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
	// userID 是发布事件时的订阅注册表键（key 的 \x00 前段）；envID 除进 key
	// 外还进事件负载与日志。
	userID string
	envID  string

	// 以下四项在 entry 生命周期内不变（凭证/地址变更走停旧建新）。世代取建立
	// 时的 env.ConfigRevision：与请求世代比对决定停旧建新与 Drop 的淘汰范围。
	configRevision int64
	fingerprint    string
	address        string
	token          string

	mu sync.Mutex
	// state 的锁在 t3ShellState 内部；锁序恒为 mu → state.mu。
	state        *t3ShellState
	notify       chan struct{} // 关闭并替换以广播状态变化，View select 等待
	activeConn   t3ShellConnState
	activeErr    error
	obsReady     bool
	obsSynced    bool
	obsConn      t3ShellConnState
	lastSyncedAt time.Time

	// archTTL 归档快照缓存有效期（取自 hub 配置，entry 建立后不变）。
	archTTL time.Duration
	// arch 是归档按需快照的簿记（TTL/失败冷却/在途合并），见
	// t3_shell_archive.go——并发读由 arch.inFlight 哨兵合并成一次上游调用。
	arch t3ArchiveFetchState

	// 已发布的事件状态缓存（INT-132）：publishStateIfChanged 在 entry 锁内
	// 比较，只有翻转才对外发布；errText 一并缓存，stale/failed 下失败原因
	// 的变化（如「网络不可达」→「凭证被拒」）对用户是不同的提示，也要触发。
	publishedState string
	publishedErr   string

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
	done     chan struct{}

	// legacyCh 是旧面归档订阅 goroutine 的激活信号：entry 创建时停泊在
	// select 上（不占连接、不打上游）；按需快照被判「方法明确拒绝」后由
	// switchToArchiveLegacy 关闭，goroutine 随即进入常驻订阅监督循环。
	// 用 channel 而不是「发现失败时直接 go」——entry 创建时 wg.Add 已完成，
	// 中途 Add 会与 done 观察者的 Wait 竞态（sync 文档禁止）。
	legacyCh chan struct{}

	// legacyReconnect 是旧面归档订阅的「请重建连接」信号：写后失效在订阅
	// 模式下要取回写后全量，只能让常驻订阅断开重连
	// （新 subscribe 即收权威快照），不能靠增量落地后整表重置（R3 P1-B）。
	legacyReconnect chan struct{}

	// forceResync 是活跃面的强制全量重同步请求（F1）：请求了 synchronized 但
	// 预算内未等到时置位；下次活跃重连读后清除，不带 afterSequence，由上游
	// 权威全量快照重新建立追平。同一时刻只有一条活跃连接（监督循环串行），
	// entry 锁足以保护。
	forceResync bool
}

// setForceResync 置强制全量重同步标记（F1：marker 预算耗尽仍未追平时）。
func (e *t3ShellEntry) setForceResync() {
	e.mu.Lock()
	e.forceResync = true
	e.mu.Unlock()
}

// forceResyncPending 报告是否还欠一次强制全量重同步（构造订阅 payload 时用）。
// 只读不清：要求一直保留到权威全量快照真正落地（applyActiveValues 里清除），
// 否则本次连接若在快照到达前断线，下一次重连会又带上旧游标并（无 marker 时）
// 乐观追平，把旧缓存当可信。
func (e *t3ShellEntry) forceResyncPending() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.forceResync
}

// clearForceResync 在权威全量快照应用成功后清除强制重同步要求。
func (e *t3ShellEntry) clearForceResync() {
	e.mu.Lock()
	e.forceResync = false
	e.mu.Unlock()
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
// 停机取消产生的空错误不抹掉上一次的真实失败原因。状态事件在锁外发布
// （发布路径不持 entry 锁，见锁边界说明）。
func (e *t3ShellEntry) noteConnState(archived bool, state t3ShellConnState, connErr error) {
	e.mu.Lock()
	if !archived {
		e.activeConn = state
		if connErr != nil {
			e.activeErr = connErr
		}
	}
	e.refreshLocked()
	newState, changed := e.publishStateIfChanged()
	e.mu.Unlock()
	if changed {
		e.hub.publishState(e, newState)
	}
}

// refresh 在状态可能变化后（应用增量/标记翻转）从锁外调用。绝大多数帧不翻转
// 状态，发布在锁内被比较短路，开销只是一次小结构拷贝。
func (e *t3ShellEntry) refresh() {
	e.mu.Lock()
	e.refreshLocked()
	newState, changed := e.publishStateIfChanged()
	e.mu.Unlock()
	if changed {
		e.hub.publishState(e, newState)
	}
}

// stateLocked 只推导视图状态，不组装快照。viewLocked 与事件发布的比较共用，
// 保证对「当前状态」的判定永不漂移；事件发布每帧都跑，绝不能为此整份拷贝
// 快照。调用方持有 entry 锁。
func (e *t3ShellEntry) stateLocked() t3ShellViewState {
	st := e.state.status()
	switch {
	case !st.ActiveReady:
		if e.activeConn == t3ShellConnFailed {
			return t3ShellFailed
		}
		return t3ShellSyncing
	case e.activeConn == t3ShellConnSubscribed && st.ActiveSynced:
		return t3ShellReady
	default:
		return t3ShellStale
	}
}

// publishStateIfChanged 比较当前视图状态与已发布缓存，翻转则更新缓存并返回
// (新状态, true)。调用方持有 entry 锁。stale/failed 下失败原因文案一并缓存：
// 同状态下的 Err 变化（如「网络不可达」→「凭证被拒」）对用户是不同的提示，
// 也要触发发布。
func (e *t3ShellEntry) publishStateIfChanged() (string, bool) {
	state := e.stateLocked().String()
	errText := ""
	if state == t3ShellStale.String() || state == t3ShellFailed.String() {
		if e.activeErr != nil {
			errText = e.activeErr.Error()
		}
	}
	if state == e.publishedState && errText == e.publishedErr {
		return "", false
	}
	e.publishedState = state
	e.publishedErr = errText
	return state, true
}

// viewLocked 组装当前视图。调用方持有 entry 锁。
func (e *t3ShellEntry) viewLocked() *t3ShellView {
	view := &t3ShellView{State: e.stateLocked()}
	switch view.State {
	case t3ShellFailed:
		view.Err = e.activeErr
	case t3ShellStale:
		view.Snaps = e.state.snapshots()
		view.Err = e.activeErr
		if !e.lastSyncedAt.IsZero() {
			view.SyncedAgo = time.Since(e.lastSyncedAt)
		}
	default:
		// Ready：带快照；Syncing：无数据。
		if view.State == t3ShellReady {
			view.Snaps = e.state.snapshots()
		}
	}
	return view
}

// waitView 等到可回答（有权威快照 / 首次建连失败 / entry 已停止 / ctx 或 hub
// 结束）后返回视图。状态翻转后 entry 会关闭并替换 notify 唤醒全部等待者；
// 每轮循环重新读取，保证抓到最新的广播 channel。
func (e *t3ShellEntry) waitView(ctx context.Context) (*t3ShellView, error) {
	for {
		// 归档面按需快照（INT-173）：TTL 过期才发一次共享拉取，TTL 内命中
		// 只标「已在途」不重复拨号。归档是非阻塞面——拉取在后台补位，
		// 快照落地/失败时经 refresh 广播，本循环下一轮拿到新视图。
		e.kickArchiveRefresh()
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
		case <-e.stop:
			// Drop/替换：监督循环在退避窗口里直接退出，不会再经
			// refreshLocked 广播 notify；不监听 stop 就会挂到 ctx 预算耗尽
			// （列表 15s、详情 25s）才作答。回到循环头顶以 stopped 视图返回。
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
		result := h.connectFaceOnce(e, archived)

		// 停机（Drop/替换/Close）结束的连接直接退出；hub 关闭同理。
		if e.isStopped() {
			return
		}
		select {
		case <-h.ctx.Done():
			return
		default:
		}

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
	// 连接取消监听：entry 停止时断开本条连接。它是本连接的辅助 goroutine，
	// 必须汇合，否则 Close 的「所有 goroutine 已退出」不成立。
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-e.stop:
			cancelConn()
		case <-connCtx.Done():
		}
	}()
	// 先取消、再等监听器退出（LIFO：本 defer 早于上面的 cancelConn 注册，
	// 所以先执行；随后的 cancelConn 是幂等的重复调用）。
	defer func() {
		cancelConn()
		<-watchDone
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

	// 完成标记能力查询（订阅前发：此刻这条连接上只有本请求的帧，
	// one-shot call 不会误吞订阅流）。缺能力时不请求标记并立即视为
	// 追平，与官方客户端一致。
	wantMarker := false
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

	// 订阅。活跃面带恢复游标（有过权威快照才有意义）。
	payload := map[string]any{}
	// resumed 表示本次带了恢复游标：只有 resume 才需要等 synchronized 追平
	// 信号。冷启动（无游标）与强制全量重同步的完成标志是权威全量快照本身，
	// 不对它们计时——强慢快照（>MarkerWaitTimeout）能被反复截断就永远
	// Ready 不了（例如首帧需要 14s、ticker 每 12.5s 就判超时）。
	resumed := false
	// 强制全量重同步（F1）：上一条活跃连接请求了 marker 但预算内未等到
	// ——不认为追平，本次重连不带 afterSequence。上游收到无游标的订阅
	// 会直接发权威全量快照（与序号失效回退同一语义，ws.ts:550,685,1141），
	// 权威帧到达才重新视为追平；期间视图保持 Stale/Syncing。
	if e.forceResyncPending() {
		// 置位时已未追平，这里再显式清一次：任何交错都不得把旧缓存
		// 在权威快照到达前当成可信。
		e.state.markActiveSynced(false)
		e.refresh()
	} else if st := e.state.status(); st.ActiveReady {
		if wantMarker {
			// 有完成标记能力才带游标恢复：上游会在回放之后发
			// synchronized，我们据此确认追平。
			payload["afterSequence"] = st.ActiveCursor
			resumed = true
		}
		// 进入追平阶段：清标记，等权威帧/标记重新置位——追平前的窗口
		// View 应当是 Stale 而不是 Ready。没有 marker 能力时不带游标
		// （退回权威全量）：没有任何新数据就宣称追平会把旧缓存当成可信
		// 运行态，违反「陈旧快照不得当作可信」——官方客户端的新 session
		// 也是先加载权威快照、失败才退回全量，而不是直接沿用上一会话缓存。
		e.state.markActiveSynced(false)
		e.refresh()
	}
	if wantMarker {
		payload["requestCompletionMarker"] = true
	}
	requestID := session.nextID
	session.nextID++
	if err := session.sendRequest(connCtx, requestID, t3RPCSubscribeShell, payload); err != nil {
		h.endFaceConnection(e, archived, err)
		return t3ShellConnResult{}
	}
	e.noteConnState(archived, t3ShellConnSubscribed, nil)

	// marker 等待截止时刻：只对带游标的 resume 生效。预算内没等到
	// synchronized 就不再假装追平，由 readStreamLoop 的 ticker 分支置强制
	// 全量重同步并结束本连接。零值表示无需等待（未请求 marker / 冷启动 /
	// 强制全量重同步）。
	var markerDeadline time.Time
	if wantMarker && resumed {
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
			// 停机取消：err 为 nil 时记「未就绪」而不抹掉既有失败原因。
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
				if applyErr := h.applyActiveValues(e, frame.Values); applyErr != nil {
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

// endFaceConnection 收尾连接状态。归档面分两条路径：按需快照模式不在
// 此终止（它没有常驻连接）；旧面订阅模式（archived=true）下任何终止
// ——读错误/写失败/正常 Exit/取消/探测或拨号失败——统一撤销归档面可
// 信度（清空归档表 + ArchiveErr，同旧 F2），直到下一次归档权威快照到
// 达才恢复，并发布一次 Full 事件（列表需要把「归档暂不可读」呈现）。
// 活跃面只更新连接状态与失败分类：单面终止不推翻活跃数据（G1），View
// 的 Stale 判定用得上最近一次真实失败原因。err 为 nil（停机取消）时记
// 「未就绪」而不抹掉既有失败原因。
func (h *t3ShellHub) endFaceConnection(e *t3ShellEntry, archived bool, connErr error) {
	if archived {
		e.state.markArchiveFailed(connErr)
		h.publishThreadEvent(e, nil, true)
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

// applyActiveValues 把活跃流 Chunk 的全部 value 依次解码并应用到状态，应用
// 成功后把本批变化发布给该用户的订阅者。应用失败（坏帧）整条流失败重连，
// 不发布线程事件——紧随其后的连接终止会发布状态事件，浏览器据此整面刷新。
//
// 批内出现过「线程离开活跃面」的帧时同步失效归档缓存——离开活跃面即归
// 档集可能变了。两种帧都算离开：thread.removed，与 location != "active"
// 的 thread.updated（applyActiveItem 对它执行 activeThreads.remove——上游
// 若以该帧传递归档迁移，不失效的话线程会同时从活跃面与 TTL 缓存的归档
// 集合消失，ArchiveErr 却仍 nil，列表谎报完整、详情落「未找到该线程」，
// 最长一个 TTL 才自愈）。离开帧不携带「去向」——可能是被归档（进归档
// 面），也可能是被彻底删除（归档集不变），上游对移除不看 location，Fast
// Ship 无法区分。两者统一按「归档集可能变了」失效是保守正确的选择：多付
// 一次快照重拉，不会出错；若只在「确认归档」时失效，就得先猜去向，猜错
// （把删除当无效不失效）不会导致错误，但把归档当删除跳过失效会让线程在
// TTL 内从活跃、归档两边同时消失——useThreads 不轮询，TTL 过期也不主动
// 调度拉取，要缺到下一次事件/聚焦/手动刷新。这个窗口主要威胁的是「别的
// 客户端归档了线程」场景：我们自己的写命令已在写后失效，这里再失效一次
// 是幂等的冗余标记，无额外拉取（在途合并/同一世代内的标记不重拨）。失效
// 只是标不可信+留旧行，不在这里同步发起网络拉取（不拖慢 readStreamLoop），
// 下一次读经 kickArchiveRefresh 重拉；离开帧是低频帧，且每条 Chunk 至多
// 失效一次，不构成对流式的过度拉取。location == "active" 的 updated 是
// 正常字段更新，线程没离开活跃面，不触发失效。
func (h *t3ShellHub) applyActiveValues(e *t3ShellEntry, values []json.RawMessage) error {
	batch := t3ShellEventBatch{}
	leftActiveFace := false
	for _, raw := range values {
		var item t3ShellStreamItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return fmt.Errorf("活跃流帧解析失败: %w", err)
		}
		if err := e.state.applyActiveItem(item); err != nil {
			return err
		}
		// 线程离开活跃面即归档集可能变了（见函数头）：removal 与
		// location != "active" 的 updated 都把线程从活跃面摘除，去向不可辨，
		// 统一失效归档缓存。
		if item.Kind == "thread.removed" || (item.Kind == "thread.updated" && item.Location != "active") {
			leftActiveFace = true
		}
		batch.observeActive(&item)
		// 权威全量快照落地（无 enrichment 标记）→ 强制重同步要求到此结束。
		// 元数据帧不算：它不是权威数据。
		if item.Kind == "snapshot" && item.Snapshot != nil && len(item.ResolvedRepositoryIdentityRoots) == 0 {
			e.clearForceResync()
		}
	}
	h.publishThreadEvent(e, batch.ids, batch.full)
	if leftActiveFace {
		h.invalidateArchiveEntry(e)
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
