package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// T3 shell 订阅的纯内存状态（INT-116）：活跃 / 归档两面各自维护 map+order。
// 活跃面由 hub 的常驻订阅流喂入；归档面自 INT-173 起由 hub 的按需快照读取
// （orchestration.getArchivedShellSnapshot）喂入——上游 #26238 删除了
// subscribeArchivedShell，归档不再有常驻流。读时组装成既有消费方使用的
// t3ShellSnapshots。无任何 I/O，可独立单测。
//
// 协议依据（全部核对自 T3 源码，2026-10）：
//   - 活跃流（orchestration.subscribeShell）流项 OrchestrationV2ShellStreamItem
//     （contracts orchestrationV2.ts:2036-2069）：snapshot 带
//     resolvedRepositoryIdentityRoots 字段即 enrichment/元数据帧（threads 恒空、
//     projects 只含已解析 identity 的项，ShellStream.ts:186-222），字段不存在才是
//     权威全量；另有 synchronized 标记（仅 requestCompletionMarker:true 时发出，
//     位于初始快照/回放之后、live 之前，ws.ts:1100-1105）与四类增量。
//   - 活跃流实际只产生 location:"active" 的 thread 增量（ShellStream.ts:292-320）：
//     归档动作也表现为 thread.removed{location:"active"}，不是线程全局删除。
//   - 归档快照（orchestration.getArchivedShellSnapshot）返回的全量归档面：
//     归档线程在 threads 字段，形状与旧归档订阅首项的 snapshot 字段一致。
//   - 旧上游（#26238 之前）归档面只有常驻订阅 subscribeArchivedShell：
//     流项为 t3ArchiveStreamItem（首项快照 + thread.updated/removed 增量），
//     由 hub 的旧面回退路径恢复（见 t3_shell_archive.go 的 archiveLegacy）。
//   - 恢复语义：afterSequence 超前、gap>1000（ws.ts:550）或回放 payload>8MiB
//     （ws.ts:685）时上游直接改发权威全量快照——序号不需要连续，权威帧把游标
//     重置为 snapshotSequence（可以变小）。

// errT3ShellArchiveNotReady 表示归档面还没有可用的全量快照：对调用方而言与
// 「归档读取失败」同义——归档面不可信，既不得据此断言线程不存在，也不得声称
// 列表完整。
var errT3ShellArchiveNotReady = fmt.Errorf("归档线程尚未同步完成")

// t3ShellStreamItem 是活跃流流项的宽松解码。两点解码精度是刻意的：
//   - ResolvedRepositoryIdentityRoots 用 RawMessage 而非 []string：必须区分
//     「字段不存在」（权威）与「字段存在（哪怕空数组/null）」（元数据帧，
//     整体忽略），[]string 无法区分不存在与 null。
//   - Sequence 用 *int：增量缺 sequence 应当算坏帧而不是静默当 0（0 会把
//     游标错误地回卷）。
type t3ShellStreamItem struct {
	Kind                            string           `json:"kind"`
	Snapshot                        *t3ShellSnapshot `json:"snapshot"`
	ResolvedRepositoryIdentityRoots json.RawMessage  `json:"resolvedRepositoryIdentityRoots"`
	Sequence                        *int             `json:"sequence"`
	Project                         *t3ProjectShell  `json:"project"`
	ProjectID                       string           `json:"projectId"`
	Location                        string           `json:"location"`
	Thread                          *t3ThreadShell   `json:"thread"`
	ThreadID                        string           `json:"threadId"`
}

// t3ArchiveStreamItem 是旧归档订阅（subscribeArchivedShell）流项的宽松解码
// （无 location / enrichment 字段）。只在旧面回退路径使用——新上游没有该流。
type t3ArchiveStreamItem struct {
	Kind     string                   `json:"kind"`
	Snapshot *t3ArchivedShellSnapshot `json:"snapshot"`
	Sequence *int                     `json:"sequence"`
	Thread   *t3ThreadShell           `json:"thread"`
	ThreadID string                   `json:"threadId"`
}

// t3ShellFace 是单面的存储：ID → shell 的 map 加插入序。upsert 已有 ID 只换值
// 不动序；删除从 order 里过滤掉（不留 tombstone、不重复）。
type t3ShellFace[T any] struct {
	byID  map[string]*T
	order []string
}

func newT3ShellFace[T any]() t3ShellFace[T] {
	return t3ShellFace[T]{byID: make(map[string]*T)}
}

func (f *t3ShellFace[T]) reset(items []T, idOf func(*T) string) error {
	// 先整体校验再替换：坏帧不得留下被清了一半的面。
	for i := range items {
		if idOf(&items[i]) == "" {
			return fmt.Errorf("流项缺少 ID")
		}
	}
	clear(f.byID)
	f.order = f.order[:0]
	for i := range items {
		item := &items[i]
		id := idOf(item)
		if _, exists := f.byID[id]; !exists {
			f.order = append(f.order, id)
		}
		f.byID[id] = item
	}
	return nil
}

func (f *t3ShellFace[T]) upsert(item *T, id string) {
	if _, exists := f.byID[id]; !exists {
		f.order = append(f.order, id)
	}
	f.byID[id] = item
}

func (f *t3ShellFace[T]) remove(id string) {
	if _, exists := f.byID[id]; !exists {
		return
	}
	delete(f.byID, id)
	kept := f.order[:0]
	for _, existing := range f.order {
		if existing != id {
			kept = append(kept, existing)
		}
	}
	f.order = kept
}

func (f *t3ShellFace[T]) list() []T {
	items := make([]T, 0, len(f.order))
	for _, id := range f.order {
		items = append(items, *f.byID[id])
	}
	return items
}

func (f *t3ShellFace[T]) len() int {
	return len(f.order)
}

// t3ShellFaceStatus 是 hub 观察状态翻转用的只读快照。
type t3ShellFaceStatus struct {
	ActiveReady  bool
	ActiveSynced bool
	ArchiveReady bool
	ArchiveErr   error
	ActiveCursor int
}

// errT3ShellArchiveStale 表示归档快照在两次可信读取之间被作废（写后失效，
// 或 TTL 过期后重拉尚未落地）：归档面此刻不可信（G1 同口径——不得据此
// 断言线程不存在、不得声称列表完整），但缓存里最近一次成功快照的行继续
// 如实展示。与读取失败的区别仅在文案：数据还在、只是一次写操作或一次
// 过期重拉让它暂时落后于上游真相。
var errT3ShellArchiveStale = fmt.Errorf("归档快照已失效，正在重新拉取")

// t3ShellState 汇聚一个环境的活跃 + 归档两面 shell 数据。
//
// 可信标记分离（设计 B1 规则 5）：
//   - ActiveReady：收到过权威活跃快照；
//   - ActiveSynced：追平。权威帧本身即视为追平（快照是完整的）；resume 回放
//     期间只有元数据帧与增量，须等 synchronized 标记——由 hub 调
//     markActiveSynced 控制；能力不支持时 hub 在发出订阅后立即置位（对齐
//     官方客户端 packages/client-runtime/src/state/shell.ts:386-398）；
//   - ArchiveReady：收到过归档全量快照（INT-173 起是一次性 RPC 的返回，
//     语义不变）。
//
// 活跃恢复游标 activeCursor 只由活跃面推进（规则 2）：权威快照把它重置为
// snapshotSequence（可以变小，不是 max），活跃增量推进为 item.sequence；
// 归档、元数据帧、Ping/Pong、synchronized 都不碰它。
type t3ShellState struct {
	mu sync.Mutex

	activeProjects  t3ShellFace[t3ProjectShell]
	activeThreads   t3ShellFace[t3ThreadShell]
	archivedThreads t3ShellFace[t3ThreadShell]

	activeSchemaVersion   int
	activeCursor          int
	archivedSchemaVersion int
	archivedSeq           int

	activeReady  bool
	activeSynced bool
	archiveReady bool
	archiveErr   error
	// archiveGeneration 是失效计数：noteArchiveInvalidated 每次推进，拉取
	// 发起方用它做世代护栏（见 applyArchiveSnapshot）。
	archiveGeneration int64
	// archiveStale 表示「归档面留有最近一次成功快照的行、但当前不可信」：
	// 写后失效（noteArchiveInvalidated）与 TTL 过期重拉发起
	// （noteArchiveExpired）都置位——两种情形保留旧行，只是不得把快照当
	// 完整结论。noteArchiveFetchFailed 用它区分「失效/过期后留行」与
	// 「冷启动无快照」（清表只发生在后者）；下一次可信快照应用成功时
	// 清除。
	archiveStale bool
	// archiveUncertain 是「归档归属待对账」的线程集合（ID → 自愈截止时刻，
	// 跟进 PR② / #156 R1）：订阅模式下活跃面宣告线程离开、而归档增量还没
	// 到时登记，非空期间归档面报 stale（不是完整结论）。按 ID 记账，无关线程的
	// 增量不会替它解账；解除只有三条路径——含该线程的权威全量快照、线程在活跃面
	// 回现、宽限到期（归档增量不解账，见 #156 R15）。守 s.mu。
	archiveUncertain map[string]time.Time
	// archiveUncertainSince 记每个待对账 ID 的登记时刻（#156 R16）：只有**晚于**
	// 该时刻采集的归档快照才能算作它的对账证据——重连快照可能在移除之前就已
	// 采好、之后才送达，把它的包含关系当作「本轮已对账」会误清标记。
	archiveUncertainSince map[string]time.Time
	// archiveFreshnessFloor 是「最近一次归属变更」的时刻下界（#156 R28）：待对账
	// 到期会被清掉，但**早于它采集的归档快照仍然不能算证据**——否则一条在移除
	// 之前采好、迟到超过宽限的快照会把面恢复可信、而线程两面都没有，wait 会
	// 把存在的线程判成 thread_gone。快照采集时刻早于它一律按「变更前状态」处理。
	archiveFreshnessFloor time.Time
	// archiveReconnectPendingGen 是旧面订阅路径独有的「请重建归档订阅」
	// 备忘（守旧游标），以单调世代表达：0 表示无未决请求，>0 表示最新一
	// 次请求的世代。常驻流没有按需拉取可发——除重连外上游只推增量、不
	// 自发重发全量，写后失效要取回写后全量只能靠
	// 断开重连（新 subscribe 即收权威快照）。备忘由读循环的
	// legacyReconnect 分支消费后主动断开（R3 P1-B：增量落地后整表重置
	// 会让归档面长期不可信）。只被 archiveLegacy 模式读写，统一记在
	// s.mu 下（R3 P1-A，全局锁序 entry.mu → s.mu，INT-216 起簿记并入
	// s.mu、arch.mu 已删除）。
	//
	// 世代护栏（R4 P2，Preserve newer legacy reconnect requests）：一条
	// reconnect 请求只能被「确在该请求之后发出的快照」清掉——每条连接
	// 在发 subscribe 之前记一个出发邮戳（当时已发出的请求世代，发送前
	// 取值保证发送后的新请求世代必然大于邮戳，审修 R6-A），快照落地
	// 时以「邮戳 ≥ 备忘世代」为清备忘条件：备忘更新（请求记在本连接
	// 建立之后）说明本连接的订阅取的是变更前状态，其快照不得吞掉备忘——
	// 否则已排队的 reconnect 信号被无视，wait 拿写前数据判 thread_gone
	// 直到下一个事件/重连。兑现路径不变：读循环的信号分支消费备忘即断
	// 开本连接，监督循环重连后新连接的 subscribe 必在该请求之后发出，
	// 其快照必为写后状态，落地自然清掉备忘（邮戳 ≥ 备忘世代）。
	archiveReconnectPendingGen int64
	// archiveReconnectDedupGen 记「已为哪条连接请求过重建」（#156 R41）：同一
	// 连接重复丢弃只 bump 一次世代，避免反复重建订阅。
	archiveReconnectDedupGen int64
	// archiveReconnectDedupSet 区分「尚未请求过」与「connectionStart=0」。
	archiveReconnectDedupSet bool
	// archiveReconnectIssued 是已发出的 reconnect 请求总数（最新请求的世
	// 代编号）。与 pending 分离：consume/连接终止只清 pending，不回卷本
	// 计数——它是新请求世代的来源，也是连接出发邮戳的取值来源。
	archiveReconnectIssued int64

	// 归档面通道（INT-173）：false=按需快照，true=旧面常驻订阅。
	// 原 t3ArchiveFetchState 的三态 mode 收敛为布尔——生产代码只比较
	// 「是否 legacy」；armed 待对账登记也由它推导（legacy ≡ 活跃移除即在
	// 同一临界区登记待对账）。簿记字段全部并入 s.mu——原 s.mu → arch.mu
	// 的嵌套锁序消失（R3 P1-A 的 ABBA 隐患随之不存在）。
	archiveLegacy bool
	// validUntil 是「当前 state 里的归档快照可信到哪一刻」：世代相符的按需
	// 落地时刻 + archiveTTL。零值表示从未成功/已失效。legacy 模式下不参与
	// 判定（订阅面由连接健康承载可信度）。
	validUntil time.Time
	// retryAfter 是下一次允许发起拉取的时刻：失败同样记账（= 失败时刻 +
	// archiveTTL），否则归档面持续失败时每次读取都会各拨一条连接重试——
	// 按需面没有常驻退避循环，并发错误风暴靠这个窗口压住。
	retryAfter time.Time
	// inFlight 已在途拉取的哨兵：登记「已发起」，TTL/冷却外的并发读出
	// 合并到这一次而不重复拨号——由发起方在拉取结束后清。
	inFlight bool
	// refetchStartedAt 是本次在途拉取的发起时刻（R20 P2）：撤可信锚在这里
	// 而不是 validUntil 的年龄。拉取结束（落地/失败/中止）时清零。
	refetchStartedAt time.Time
	// archiveTTL 是按需快照的可信窗口（hub 配置注入，entry 建立后不变）。
	archiveTTL time.Duration
	// archiveStaleGrace 是「重拉在途超过多久仍未落地就撤可信」的宽限，
	// 兼作待对账集合的自愈上界（R16 P2-2 / R20 P2）。
	archiveStaleGrace time.Duration
	// archiveRev 是归档面修订号（INT-216）：由 Fingerprint 在 s.mu 内对
	// 「行摘要 + 可信度文案」做前后比较派生——内容一变 rev 即推进，无
	// 需各写路径手工打点。发布端（archAnnounced）只接受 rev 单调递增的
	// 写入：指纹取值（s.mu）与宣布基准（e.mu）分属两把锁，rev 保证后
	// 取的旧指纹不会覆盖先到的新基准。
	archiveRev int64
	// fpInit/fpRows/fpErr 缓存最近一次指纹计算结果，供 archiveRev 派生。
	fpInit bool
	fpRows []t3ArchiveRowDigest
	fpErr  string
	// now 是时钟注入点（生产 = time.Now，测试可替换）：deadline/
	// registration-since/validUntil/retryAfter/refetchStartedAt/快照
	// 采集时刻默认值全部经它取值。
	now func() time.Time
}

func newT3ShellState() *t3ShellState {
	return &t3ShellState{
		activeProjects:  newT3ShellFace[t3ProjectShell](),
		activeThreads:   newT3ShellFace[t3ThreadShell](),
		archivedThreads: newT3ShellFace[t3ThreadShell](),
		now:             time.Now,
	}
}

// applyActiveSnapshotLocked 应用权威活跃快照（调用方须已持有锁）：整体替换
// projects + active threads，游标重置为 snapshotSequence；archivedThreads
// 字段（真实服务恒为空）一律丢弃——归档数据只由归档流维护，不能被活跃帧
// 清空（规则 1/3）。返回被本快照从活跃面移除的线程 ID（应用前在活跃面、
// 新快照里已不存在的 ID，R13 P2-1）：权威全量是「整体替换」，被替换掉的
// 旧线程与增量帧的 removal 一样是「线程离开活跃面」——断连重连、marker
// 超时强制重同步、序号失效回退都走全量快照，断连窗口里被别的客户端归档
// 的线程会从新快照里消失，hub 层据此失效归档缓存，否则它既不在新活跃面、
// 也不在旧归档缓存，ArchiveErr 却仍 nil（列表谎报完整、详情落「未找到该
// 线程」），最长一个 TTL 才自愈。
func (s *t3ShellState) applyActiveSnapshotLocked(snapshot t3ShellSnapshot) ([]string, bool, []string, error) {
	if err := s.activeProjects.reset(snapshot.Projects, func(p *t3ProjectShell) string { return p.ID }); err != nil {
		return nil, false, nil, fmt.Errorf("活跃快照项目缺少 ID: %w", err)
	}
	// reset 前 diff：新快照的 ID 集是权威的活跃面全量，旧 order 里不在
	// 集中的即被替换掉（与 reset 同锁内计算，不留交错缝隙）。
	incoming := make(map[string]bool, len(snapshot.Threads))
	for i := range snapshot.Threads {
		incoming[snapshot.Threads[i].ID] = true
	}
	var removed []string
	for _, id := range s.activeThreads.order {
		if !incoming[id] {
			removed = append(removed, id)
		}
	}
	if err := s.activeThreads.reset(snapshot.Threads, func(t *t3ThreadShell) string { return t.ID }); err != nil {
		return nil, false, nil, fmt.Errorf("活跃快照线程缺少 ID: %w", err)
	}
	// 活跃全量快照带回来的线程同样解账（#156 R12）：线程已在活跃面，其归档
	// 归属不再影响「两面都没有」的判定。
	if len(s.archiveUncertain) > 0 {
		for i := range snapshot.Threads {
			delete(s.archiveUncertain, snapshot.Threads[i].ID)
			delete(s.archiveUncertainSince, snapshot.Threads[i].ID)
		}
		s.refreshArchiveMembershipTrustLocked()
	}
	s.activeSchemaVersion = snapshot.SchemaVersion
	s.activeCursor = snapshot.SnapshotSequence
	s.activeReady = true
	s.activeSynced = true
	armedRegistered, proven := s.armUncertainForRemovalsLocked(removed, false)
	return removed, armedRegistered, proven, nil
}

// applyActiveItemDetailedLocked 是无锁内核（调用方须持 s.mu），ApplyActive
// 的批处理在同一临界区内逐帧调用它。
func (s *t3ShellState) applyActiveItemDetailedLocked(item *t3ShellStreamItem) (removed []string, armedRegistered bool, proven []string, err error) {
	switch item.Kind {
	case "snapshot":
		if len(item.ResolvedRepositoryIdentityRoots) > 0 {
			// 元数据/enrichment 帧：Fast Ship 不使用 repositoryIdentity，
			// 整体忽略（只 Ack，不改数据、不动游标、不动可信标记）。
			return nil, false, nil, nil
		}
		if item.Snapshot == nil {
			return nil, false, nil, fmt.Errorf("活跃快照帧缺少 snapshot 字段")
		}
		removed, armedRegistered, proven, err = s.applyActiveSnapshotLocked(*item.Snapshot)
		if err != nil {
			return nil, false, nil, err
		}
		// 返回 applyActiveSnapshotLocked 里 armUncertainForRemovalsLocked 的
		// **真实结果**（#156 R27）：早先按「有移除 && armed」推断，在
		// settle<=0（不登记）或部分已对账时说成「已登记」，帧后回调于是
		// 漏掉补登记。
		return removed, armedRegistered, proven, nil
	case "synchronized":
		// 追平标记：不携带数据，只推进可信标记（不碰游标，规则 2）。
		s.activeSynced = true
		return nil, false, nil, nil
	case "project.updated":
		if item.Sequence == nil || item.Project == nil || item.Project.ID == "" {
			return nil, false, nil, fmt.Errorf("project.updated 帧缺少 sequence/project/projectId")
		}
		s.activeProjects.upsert(item.Project, item.Project.ID)
		s.advanceActiveCursorLocked(*item.Sequence)
		return nil, false, nil, nil
	case "project.removed":
		if item.Sequence == nil || item.ProjectID == "" {
			return nil, false, nil, fmt.Errorf("project.removed 帧缺少 sequence/projectId")
		}
		s.activeProjects.remove(item.ProjectID)
		s.advanceActiveCursorLocked(*item.Sequence)
		return nil, false, nil, nil
	case "thread.updated":
		if item.Sequence == nil || item.Thread == nil || item.Thread.ID == "" {
			return nil, false, nil, fmt.Errorf("thread.updated 帧缺少 sequence/thread/threadId")
		}
		// 活跃流实际只产生 location:"active"（ShellStream.ts:292-320）。旧版服务端
		// 也可能在这条流上发 location:"archive"，上游 reducer 对它的处理是「不进
		// 活跃面」（shellReducer.ts:169-190），归档面归归档订阅维护；当坏帧处理
		// 会把协议演进拖成重连循环，代价远大于收益。走到 remove 分支即线程离开
		// 活跃面：归档缓存的失效与 thread.removed 一样由 hub 层在应用后触发
		// （applyActiveValues → invalidateArchiveEntry），state 只管活跃面。
		if item.Location == "active" {
			s.activeThreads.upsert(item.Thread, item.Thread.ID)
			// 回到活跃面（unarchive）：线程在活跃面即确定存在，「归档面里有没有」
			// 对「两面都没有」的判定已无意义，直接解账（#156 R12）。
			s.noteArchiveMembershipSettledLocked(item.Thread.ID)
		} else {
			// 只有「确实从活跃面移走」才需要等归档对账（#156 R9）：对不在活跃
			// 面的 ID 的重复移除是噪声，登记只会把整个环境降级一个宽限窗口。
			_, present := s.activeThreads.byID[item.Thread.ID]
			s.activeThreads.remove(item.Thread.ID)
			if present {
				removed = []string{item.Thread.ID}
				armedRegistered, proven = s.armUncertainForRemovalsLocked(removed, true)
			}
		}
		s.advanceActiveCursorLocked(*item.Sequence)
		// 必须把 removed 交回 hub（#156 R11）：否则 armed 登记了却没人调度到期
		// 清理、也不发事件，归档面会停在一个既无定时器也无通知的 stale 上。
		return removed, armedRegistered, proven, nil
	case "thread.removed":
		if item.Sequence == nil || item.ThreadID == "" {
			return nil, false, nil, fmt.Errorf("thread.removed 帧缺少 sequence/threadId")
		}
		// 只从活跃面删除：归档动作在上游也发 active removal
		// （ShellStream.ts:292-320），不代表线程从归档面消失（规则 3）；
		// removal 的 location 不改变语义（上游对移除不看 location）。
		// state 是纯内存、不持 hub 引用：归档缓存的失效由 hub 层在应用离开
		// 活跃面的帧（本帧与 location!="active" 的 updated）后触发
		// （applyActiveValues → invalidateArchiveEntry），这里只管活跃面。
		_, present := s.activeThreads.byID[item.ThreadID]
		s.activeThreads.remove(item.ThreadID)
		if present {
			removed = []string{item.ThreadID}
			armedRegistered, proven = s.armUncertainForRemovalsLocked(removed, false)
		}
		s.advanceActiveCursorLocked(*item.Sequence)
		return removed, armedRegistered, proven, nil
	default:
		return nil, false, nil, fmt.Errorf("活跃流未知帧 kind %q", item.Kind)
	}
}

// advanceActiveCursorLocked 把游标推进为 delta 序号（只前进不回卷；权威快照
// 的重置走 applyActiveSnapshotLocked，不受此限制）。
func (s *t3ShellState) advanceActiveCursorLocked(sequence int) {
	if sequence > s.activeCursor {
		s.activeCursor = sequence
	}
}

// applyOnDemandSnapshotCommitted 是按需快照落地的无锁内核（调用方须持
// s.mu）：世代护栏与 freshnessFloor 预检在本函数内完成，EndFetch 把
// 「应用 + 封窗/补拉簿记」收进同一临界区（Finding R1）。
func (s *t3ShellState) applyOnDemandSnapshotCommitted(snapshot t3ArchivedShellSnapshot, generation int64, observedAt time.Time) (bool, error) {
	if observedAt.IsZero() {
		observedAt = s.now()
	}
	// 采集早于最近一次归属变更的快照整条丢弃（#156 R28/R39）：它既不能封窗、
	// 也不能拿变更前的行覆盖现有行；不动可信度，避免「撤 ready 后宽限到期也
	// 恢复不了」的死角。
	if observedAt.Before(s.archiveFreshnessFloor) {
		// 未落地同样返回 false（#156 R41 Note）：EndFetch 据此按「未封窗」
		// 收尾，不会把「丢弃」当成功落地。
		return false, nil
	}
	// 坏负载先于世代判定：reset 失败时面保持原样，err 点名原因交调用方
	// 按「本次读取失败」记账——不得吞成「不可信但没错误」，否则上游结构
	// 性坏快照（毒快照）会引发无冷却重试风暴：每次读取立即重拨、永不记账。
	if err := s.archivedThreads.reset(snapshot.Threads, func(t *t3ThreadShell) string { return t.ID }); err != nil {
		return false, fmt.Errorf("归档快照线程缺少 ID: %w", err)
	}
	s.archivedSchemaVersion = snapshot.SchemaVersion
	s.archivedSeq = snapshot.SnapshotSequence
	if generation != s.archiveGeneration {
		// 写前快照：换掉数据但不封窗。archiveReady=false 让 snapshots() 继续
		// 把归档面报为不可信；archiveErr 保持失效标记时的语义。
		s.archiveReady = false
		if s.archiveErr == nil {
			s.archiveErr = errT3ShellArchiveStale
		}
		return false, nil
	}

	s.archiveReady = true
	s.archiveErr = nil
	s.archiveStale = false
	// 对账裁决带时序（#156 R16）：只有**晚于登记时刻**采集的快照才是证据。
	// 重连快照可能在移除之前就采好、之后才送达——那时它的包含关系不能清掉
	// 本轮待对账，否则排队的 removed 删行后两面皆空而面可信，wait 会判
	// thread_gone。
	if len(s.archiveUncertain) > 0 {
		incoming := make(map[string]struct{}, len(snapshot.Threads))
		for i := range snapshot.Threads {
			incoming[snapshot.Threads[i].ID] = struct{}{}
		}
		for id := range s.archiveUncertain {
			if _, ok := incoming[id]; !ok {
				continue
			}
			if since, ok := s.archiveUncertainSince[id]; ok && observedAt.Before(since) {
				continue
			}
			delete(s.archiveUncertain, id)
			delete(s.archiveUncertainSince, id)
		}
		if len(s.archiveUncertain) > 0 {
			s.archiveErr = errT3ShellArchiveStale
		}
	}
	return true, nil
}

// noteArchiveInvalidatedLocked 是无锁变体（调用方须持 s.mu）。失效同时把
// archiveFreshnessFloor 推到本时刻：失效即「归档集可能变了」，早于此刻采集
// 的快照（含随后切到 legacy 才送达的 connStartAt 快照）一律按变更前状态
// 丢弃——否则按需模式下「移除→切 legacy→迟到旧快照封可信」会让两面都缺的
// 线程被判 thread_gone（INT-216 补的 R7/R8 形态竞态）。
func (s *t3ShellState) noteArchiveInvalidatedLocked() {
	s.archiveGeneration++
	s.archiveStale = true
	s.archiveReady = false
	if s.archiveErr == nil {
		s.archiveErr = errT3ShellArchiveStale
	}
	if now := s.now(); now.After(s.archiveFreshnessFloor) {
		s.archiveFreshnessFloor = now
	}
}

// noteArchiveExpiredLocked 在 TTL 过期超过宽限期后把归档面撤为不可信（Finding R2 / R16 P2-2）：
// 过期不等于数据作废，旧行照旧展示，但 validUntil 已过意味着「快照不再被
// 信任为当前完整」——重拉在途期间若不撤 archiveReady，Peek/wait 会拿过期
// 快照判 thread_gone、谎报归档列表完整（与写后失效同型缺口）。落地成功
// （applyArchiveSnapshot 世代相符）恢复可信；落地失败由 noteArchiveFetchFailed
// 记真实失败原因。不推进世代：本次重拉正是为恢复可信而发，落地要按当前
// 世代正常封窗。已不可信的面（未就绪/已失效/已标记）不重复标记——既有
// archiveErr 文案（真实失败/失效）比过期语义更准确。
// noteArchiveExpiredLocked 的调用方须已持有 s.mu：kick 的准入临界区经 commit
// 回调在同一 s.mu 持锁内执行（INT-216 起簿记并入 s.mu，无嵌套锁）。
func (s *t3ShellState) noteArchiveExpiredLocked() {
	if !s.archiveReady {
		return
	}
	s.archiveReady = false
	s.archiveStale = true
	if s.archiveErr == nil {
		s.archiveErr = errT3ShellArchiveStale
	}
}

// applyArchiveItemLocked 是无锁内核（调用方须持 s.mu），ApplyLegacy 的批处理
// 在同一临界区内逐项调用它。
func (s *t3ShellState) applyArchiveItemLocked(item *t3ArchiveStreamItem, connectionStart int64, observedAt time.Time) error {
	switch item.Kind {
	case "snapshot":
		if item.Snapshot == nil {
			return fmt.Errorf("归档快照帧缺少 snapshot 字段")
		}
		return s.applyArchiveSnapshotLocked(*item.Snapshot, connectionStart, observedAt)
	case "thread.updated":
		if item.Sequence == nil || item.Thread == nil || item.Thread.ID == "" {
			return fmt.Errorf("归档 thread.updated 帧缺少 sequence/thread/threadId")
		}
		s.archivedThreads.upsert(item.Thread, item.Thread.ID)
		// 归档增量**不**解除待对账（#156 R15）：两条流顺序无保证，这条 updated
		// 可能来自更早的一轮归档周期（unarchive 前就被排队的更新），拿它当
		// 「本次移除已对账」会让随后的 thread.removed 把缓存行删掉、而新增量
		// 还没到——两面都没有 + 面可信 = wait 把线程判成 thread_gone。
		// 解除只有三条路径：归档全量快照包含该 ID（见
		// applyActiveSnapshotLocked）、活跃面重新出现（location=active 或活跃快照
		// 带回）、或宽限到期（expireArchiveMembershipUncertain）。
		return nil
	case "thread.removed":
		if item.Sequence == nil || item.ThreadID == "" {
			return fmt.Errorf("归档 thread.removed 帧缺少 sequence/threadId")
		}
		// 归档流删行本身可能来自更早一轮的 unarchive（两条流无顺序保证）：若该
		// 线程此刻也不在活跃面，就会出现「两面都没有且面可信」的窗口（#156 R24）。
		// 只删行不够——要把它记成归属待对账，等权威快照/宽限到期再下结论。
		s.archivedThreads.remove(item.ThreadID)
		if _, active := s.activeThreads.byID[item.ThreadID]; !active {
			if settle := s.archiveStaleGrace; settle > 0 && s.archiveLegacy {
				_, _ = s.noteArchiveMembershipUncertainLocked([]string{item.ThreadID}, settle, false)
			}
		}
		// 归档流的 thread.removed **不能**当作「该线程归属已对账」（#156 R9）：
		// 两条流顺序无保证，这条 removed 可能来自更早的一次 unarchive；把它当
		// 对账完成会提前恢复可信，wait 随即把「两面都没有」判成 thread_gone。
		// 只让 thread.updated（行确实存在）解账，removed 交给到期兜底。
		return nil
	default:
		return fmt.Errorf("归档流未知帧 kind %q", item.Kind)
	}
}

// applyArchiveSnapshotLocked 是订阅模式下的快照整体替换（调用方须已持有
// 锁）：与按需路径的 applyArchiveSnapshot 同体但不带失效世代/commit——常
// 驻流的数据来自权威推送，没有「拉取途中被失效」的交错，但其「可信化
// 何时成立」由 reconnect 世代护栏裁决（见下），不是无条件成立。快照里
// 的 Projects 一律忽略（F7）：项目表只有活跃面一份权威来源。
//
// reconnect 世代护栏（R4 P2 + 审修 R8 P2）：connectionStart 是本连接的
// 出发邮戳（发 subscribe 之前读到的请求世代）——本快照必在该连接之后
// 发出，故它一定晚于所有世代 ≤ 邮戳的请求。
//   - 邮戳 < 备忘世代：本快照先于最近一次写后失效发出，是变更前状态。
//     只换数据、不恢复可信——若照常封 Ready+清 Err，从消费备忘到连接
//     被拆开的窗口里，并发 View/Peek 会拿写前归档集判 thread_gone、谎报
//     列表完整（审修 R8 P2）。面保持「已失效重拉中」，可信由兑现那条
//     连接的写后快照恢复。数据照换：写前快照不比其他行更旧，留行优
//     于清表。
//   - 邮戳 ≥ 备忘世代（含备忘为零）：本快照确在最后一次失效请求之后发
//     出，是写后权威状态——置可信、清备忘（撤销尚存的重建请求，避免
//     晚到的信号再把刚恢复的面拆掉一次）。
//
// 被变更前快照挡下的备忘由读循环的信号分支兑现：信号必随该请求送达
// 本连接的读循环（chan 缓冲 1 且每连接只有一个读循环消费者，不会被快
// 照吞掉），消费即断开本连接，下一条连接的 subscribe 必在请求之后发
// 出，其快照必为写后状态。
func (s *t3ShellState) applyArchiveSnapshotLocked(snapshot t3ArchivedShellSnapshot, connectionStart int64, observedAt time.Time) error {
	// 变更前采集的快照整条丢弃（#156 R28/R39）：不覆盖行、不动可信度。
	if !observedAt.IsZero() && observedAt.Before(s.archiveFreshnessFloor) {
		// 但若这条快照是**仅有的一次机会**（面还没就绪、之后不会再推），就必须
		// 请求重连取新全量（#156 R40）；面已就绪时保持安静，避免批量归档期间
		// 的重连风暴（#156 R38）。
		// 同一条连接只请求一次（#156 R41 Note）：否则每次快照被丢弃都会 bump
		// 世代，形成「反复重建订阅」的循环。
		if !s.archiveReady && !(s.archiveReconnectDedupSet && s.archiveReconnectDedupGen == connectionStart) {
			s.archiveReconnectDedupSet = true
			s.archiveReconnectDedupGen = connectionStart
			s.noteArchiveReconnectRequestedLocked()
		}
		return nil
	}
	if err := s.archivedThreads.reset(snapshot.Threads, func(t *t3ThreadShell) string { return t.ID }); err != nil {
		return fmt.Errorf("归档快照线程缺少 ID: %w", err)
	}
	s.archivedSchemaVersion = snapshot.SchemaVersion
	s.archivedSeq = snapshot.SnapshotSequence
	// 权威快照只对账它**确实包含**的线程（#156 R6）：命中的 ID 成员关系已定；
	// 未命中的仍可能「被归档但增量还在队列里」，或「被彻底删除」——交给
	// 各自的到期时刻兜底，不能因为快照没包含它们就断言不存在。

	if len(s.archiveUncertain) > 0 {
		incoming := make(map[string]struct{}, len(snapshot.Threads))
		for i := range snapshot.Threads {
			incoming[snapshot.Threads[i].ID] = struct{}{}
		}
		for id := range s.archiveUncertain {
			if _, ok := incoming[id]; !ok {
				continue
			}
			// 只有**晚于登记时刻**采集的快照才算对账证据（#156 R16）：重连快照
			// 可能在移除之前就采好、之后才送达。
			if since, ok := s.archiveUncertainSince[id]; ok && observedAt.Before(since) {
				continue
			}
			delete(s.archiveUncertain, id)
			delete(s.archiveUncertainSince, id)
		}
	}
	if connectionStart < s.archiveReconnectPendingGen {
		// 写前快照：数据照换但不封窗（审修 R8 P2）。与按需路径
		// applyArchiveSnapshot 世代不符分支同型——面保持不可信，
		// 「已失效重拉中」语义留到写后快照落地才解除。
		s.archiveReady = false
		s.archiveStale = true
		if s.archiveErr == nil {
			s.archiveErr = errT3ShellArchiveStale
		}
		return nil
	}

	// 邮戳 ≥ 备忘世代：权威全量快照即是重连取写后状态的完成——置可信
	// 并撤销尚存的重建备忘，避免晚到的信号再把刚恢复的面拆掉一次。
	s.archiveReady = true
	s.archiveErr = nil
	s.archiveStale = false
	s.archiveReconnectPendingGen = 0
	// 仍有未对账的移除：面就绪但不能宣称归档集完整（#156 R6）。
	if len(s.archiveUncertain) > 0 {
		s.archiveErr = errT3ShellArchiveStale
	}
	return nil
}

// markArchiveFailedLocked 是无锁内核（调用方须持 s.mu）。
func (s *t3ShellState) markArchiveFailedLocked(err error) {
	s.archivedThreads = newT3ShellFace[t3ThreadShell]()
	s.archiveReady = false
	s.archiveStale = false
	s.archiveErr = err
	// 连接终止即弃用归档面：重建备忘随本连接一起作废——连接已死，兑
	// 现搭监督循环自然重连的车：下一条连接的 subscribe 必在该请求之后
	// 发出，其快照必为写后状态。只清备忘不回卷 issued 计数：它是后序
	// 请求世代与连接邮戳的取值来源。
	s.archiveReconnectPendingGen = 0
}

// noteArchiveReconnectRequestedLocked 同 noteArchiveReconnectRequested（调用方
// 须持 s.mu）。
func (s *t3ShellState) noteArchiveReconnectRequestedLocked() int64 {
	s.archiveReconnectIssued++
	s.archiveReconnectPendingGen = s.archiveReconnectIssued
	s.archiveReady = false
	s.archiveStale = true
	if s.archiveErr == nil {
		s.archiveErr = errT3ShellArchiveStale
	}
	return s.archiveReconnectPendingGen
}

// noteArchiveMembershipUncertainLocked 是 noteArchiveMembershipUncertain 的
// 无锁变体（调用方须持 s.mu）：活跃面移除与应用在同一临界区登记，
// 保证读者看不到「已删除但可信」的中间态（#156 R5）。
func (s *t3ShellState) noteArchiveMembershipUncertainLocked(ids []string, settle time.Duration, skipArchived bool) (pending int, proven []string) {
	if len(ids) == 0 || settle <= 0 {
		return 0, nil
	}
	// 登记不依赖 archiveReady（#156 R6）：切入订阅模式时归档面先是不可信的，
	// 这期间发生移除若跳过登记，等订阅快照把它恢复可信时，对应的归档增量
	// 可能还在队列里，wait 会把缺失线程当定论。面不可信时这里只记账、不改
	// 可信度（本来就不信）。
	deadline := s.now().Add(settle)
	for _, id := range ids {
		if id == "" {
			continue
		}
		// 已在归档面里的线程不需要对账（#156 R2-1）：两条流顺序不定，归档
		// 增量先到是常见顺序（活跃面重连的权威快照移除同理），此时成员关系
		// 已由归档数据确定，登记只会让归档面白降级一个宽限窗口。
		if skipArchived {
			if _, archived := s.archivedThreads.byID[id]; archived {
				// 帧自带归档行、且归档面已有该行：本次移除无需对账（#156 R13）。
				proven = append(proven, id)
				continue
			}
		}
		if s.archiveUncertain == nil {
			s.archiveUncertain = make(map[string]time.Time, len(ids))
		}
		// 同一 ID 在待对账期间再次离开活跃面时要**续期**（截止时刻后移）；
		// 续期同样必须能调度到期清理（#156 R4）：已有定时器可能在续期后的
		// 截止时刻之前就触发，那时裁剪不到它、也不会再安排下一次回调。所以
		// 这里计的是「本次落到了多少个待对账 ID」，而不是「新增了几个」。
		pending++
		s.archiveUncertain[id] = deadline
		if s.archiveUncertainSince == nil {
			s.archiveUncertainSince = make(map[string]time.Time, len(ids))
		}
		s.archiveUncertainSince[id] = s.now()
		if s.archiveUncertainSince[id].After(s.archiveFreshnessFloor) {
			s.archiveFreshnessFloor = s.archiveUncertainSince[id]
		}
	}
	if pending == 0 {
		return 0, proven
	}
	// 只在面本来没有错误时写 stale（#156 R12 Note）：真实失败文案不得被通用
	// stale 覆盖——否则到期路径会把它当作「归属不确定已解除」清成 nil，把一次
	// 真实拉取失败静默改写成可信。
	if s.archiveReady && s.archiveErr == nil {
		s.archiveErr = errT3ShellArchiveStale
	}
	return pending, proven
}

// clearArchiveMembershipUncertainLocked 清空待对账集合（#156 R8-2）：离开订阅
// 模式时调用——按需快照路径的失效/可信由 TTL 簿记管，遗留条目既没有定时器
// 清理（调度在 hub 帧后回调里），又会让后续订阅快照永远报 stale。
func (s *t3ShellState) clearArchiveMembershipUncertainLocked() {
	s.archiveUncertain = nil
	// 登记时刻表必须同步清空（#156 R20）：它只为待对账集合服务，残留条目会
	// 让同一 ID 之后重新登记时取到旧时刻，把本该有效的快照误判成「采集太早」。
	s.archiveUncertainSince = nil
}

// armUncertainForRemovalsLocked 在 legacy 状态下，把刚离开活跃面的线程就地
// 登记为待对账（调用方须持 s.mu）。armed 判定已并入模式（armed ≡
// archiveLegacy，INT-216）：模式与登记不可分，读者不会观测到「线程已离开
// 活跃面、归档面却仍宣称完整」的中间态（#156 R5）。
func (s *t3ShellState) armUncertainForRemovalsLocked(ids []string, skipArchived bool) (registered bool, proven []string) {
	if !s.archiveLegacy {
		return false, nil
	}
	settle := s.archiveStaleGrace
	if settle <= 0 {
		// 没有宽限就没有定时器（scheduleArchiveMembershipSettle 同样早退）：
		// 登记了也永远清不掉，归档面会一直 stale（#156 R9 Note）。宁可相信
		// 归档面也不要制造不可恢复的不确定态。
		return false, nil
	}
	pending, proven := s.noteArchiveMembershipUncertainLocked(ids, settle, skipArchived)
	return pending > 0, proven
}

// noteArchiveMembershipSettledLocked 撤掉某个线程的不确定标记（调用方须持
// s.mu）。当前唯一调用方是活跃面回现（unarchive / 活跃快照带回）：线程在活跃
// 面即确定存在，归档归属不再影响判定。归档增量**不**走这里（#156 R15），归档
// 流 data 落地也不解账。
func (s *t3ShellState) noteArchiveMembershipSettledLocked(id string) {
	if _, ok := s.archiveUncertain[id]; !ok {
		return
	}
	delete(s.archiveUncertain, id)
	delete(s.archiveUncertainSince, id)
	s.refreshArchiveMembershipTrustLocked()
}

// refreshArchiveMembershipTrustLocked 清理过期的不确定项并在全部对账完成时
// 恢复可信（调用方须持 s.mu）。只清本机制自己打的 stale 标记——
// noteArchiveExpiredLocked 的过期标记会把 archiveReady 一并撤掉（这里要求
// ready），真实失败原因也不会被抹掉。
func (s *t3ShellState) refreshArchiveMembershipTrustLocked() {
	if len(s.archiveUncertain) == 0 && s.archiveReady && errors.Is(s.archiveErr, errT3ShellArchiveStale) &&
		!s.archiveStale {
		s.archiveErr = nil
	}
}

// excludeActiveThreadsLocked 是无锁内核（调用方须持 s.mu）。
func (s *t3ShellState) excludeActiveThreadsLocked(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, active := s.activeThreads.byID[id]; active {
			continue
		}
		out = append(out, id)
	}
	return out
}

// earliestUncertainDeadlineLocked 是无锁内核（调用方须持 s.mu）。
func (s *t3ShellState) earliestUncertainDeadlineLocked() (time.Time, bool) {
	var earliest time.Time
	found := false
	for _, deadline := range s.archiveUncertain {
		if !found || deadline.Before(earliest) {
			earliest = deadline
			found = true
		}
	}
	return earliest, found
}

// expireArchiveMembershipUncertainLocked 是无锁内核（调用方须持 s.mu）。
func (s *t3ShellState) expireArchiveMembershipUncertainLocked() bool {
	if len(s.archiveUncertain) == 0 {
		return false
	}
	now := s.now()
	for id, deadline := range s.archiveUncertain {
		if now.After(deadline) {
			if since, ok := s.archiveUncertainSince[id]; ok && since.After(s.archiveFreshnessFloor) {
				s.archiveFreshnessFloor = since
			}
			delete(s.archiveUncertain, id)
			// 到期同样要清登记时刻（#156 R20）：否则同一 ID 再次登记时取到的
			// 是旧时刻，会把之后的权威快照误判成「采集太早」而多降级一轮。
			delete(s.archiveUncertainSince, id)
		}
	}
	if len(s.archiveUncertain) > 0 {
		return false
	}
	if s.archiveReady && errors.Is(s.archiveErr, errT3ShellArchiveStale) && !s.archiveStale {
		s.archiveErr = nil
		return true
	}
	return false
}

// status 返回可信标记只读快照（hub 观察翻转、决定通知用）。
func (s *t3ShellState) status() t3ShellFaceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return t3ShellFaceStatus{
		ActiveReady:  s.activeReady,
		ActiveSynced: s.activeSynced,
		ArchiveReady: s.archiveReady,
		ArchiveErr:   s.archiveErr,
		ActiveCursor: s.activeCursor,
	}
}

// snapshots 组装既有消费方使用的两份快照。合并视图 active 优先（规则 4）：
// Active 原样输出活跃面；Archived.Threads 跳过活跃面已有的 ID，压过
// unarchive 后可能残留的归档行。返回的切片与元素都是新拷贝，发布后不会被
// 后续增量原地修改（规则 7：增量一律换新结构体，face.list 拷贝出值）。
func (s *t3ShellState) snapshots() *t3ShellSnapshots {
	s.mu.Lock()
	defer s.mu.Unlock()
	activeThreads := s.activeThreads.list()
	archivedThreads := make([]t3ThreadShell, 0, s.archivedThreads.len())
	for _, id := range s.archivedThreads.order {
		if _, live := s.activeThreads.byID[id]; live {
			continue
		}
		archivedThreads = append(archivedThreads, *s.archivedThreads.byID[id])
	}

	// 归档面未就绪/已失效/过期重拉在途也要如实标不可信：ArchiveErr == nil
	// 只说明「读到了且没出错」，不代表归档当前可信（按需快照首次拉取是
	// 异步补位的，活跃面常先就绪；写后失效与 TTL 过期重拉发起也会把
	// archiveReady 撤回 false）。失效标记与真实读取失败都已在 archiveErr
	// 里，未就绪兜底补 not-ready。
	archiveErr := s.archiveErr
	if !s.archiveReady && archiveErr == nil {
		archiveErr = errT3ShellArchiveNotReady
	}

	snaps := &t3ShellSnapshots{
		Active: t3ShellSnapshot{
			SchemaVersion:    s.activeSchemaVersion,
			SnapshotSequence: s.activeCursor,
			Projects:         s.activeProjects.list(),
			Threads:          activeThreads,
		},
		Archived: t3ArchivedShellSnapshot{
			SchemaVersion:    s.archivedSchemaVersion,
			SnapshotSequence: s.archivedSeq,
			// Projects 恒空（F7）：项目表只有活跃面一份权威来源，归档面只
			// 维护线程；projectNames 只读 Active.Projects。
			Threads: archivedThreads,
		},
		ArchiveErr: archiveErr,
	}
	return snaps
}

// t3ArchiveRowDigest 是归档行参与发布去重的对外可见摘要（R13 Note-2，
// R14 Note-1 补关联/运行字段）：ID + 列表摘要实际渲染的字段（标题/运行态
// status=run_status 原文/activityRunStatus/分支/provider=modelSelection.
// instanceID/模型/更新与归档时刻/收进历史标记/最近 run 起始时刻/关联
// PR 数（thread-row 的 PR 徽标，与 normalizeT3ThreadShell 的
// pull_request_count 同一推导）/子代理父线程（rows-compose 按它嵌套行），
// 与 normalizeT3ThreadShell 从 shell 读的字段同源）。指纹若只含线程 ID
// 序列，TTL 重拉同集合但行内容变了（改名/状态翻转/关联或解关联 PR——
// 外部关联操作通常不推进 updatedAt）会被判「无新话可说」不发事件——服
// 务端内存已换新而浏览器停在旧行（旧 PR 徽标），直到无关事件或手动刷
// 新。全字段可直接 slices.Equal。
type t3ArchiveRowDigest struct {
	ID              string
	Title           string
	Status          string
	ActivityStatus  string
	Branch          string
	ModelInstance   string
	Model           string
	UpdatedAt       string
	ArchivedAt      string
	SettledOverride string
	RunStartedAt    string
	PullRequests    int
	ParentThreadID  string
}

// archiveRowDigestOf 从一行归档线程提取可见摘要（指针字段空值归空串）。
// PR 计数与父线程字段与 normalizeT3ThreadShell 同一推导（t3PullRequestCount
// 的字段存在性判定、t3IsSubagentThread 的 lineage 关系判定），保证指纹
// 的「对外可见」口径与 snapshots() 交给浏览器的列表一致。
func archiveRowDigestOf(t *t3ThreadShell) t3ArchiveRowDigest {
	d := t3ArchiveRowDigest{
		ID:        t.ID,
		Title:     t.Title,
		Status:    t.Status,
		UpdatedAt: t.UpdatedAt,
	}
	if t.ArchivedAt != nil {
		d.ArchivedAt = *t.ArchivedAt
	}
	if t.ActivityRunStatus != nil {
		d.ActivityStatus = *t.ActivityRunStatus
	}
	if t.Branch != nil {
		d.Branch = *t.Branch
	}
	d.ModelInstance = t.ModelSelection.InstanceID
	d.Model = t.ModelSelection.Model
	if t.SettledOverride != nil {
		d.SettledOverride = *t.SettledOverride
	}
	if t.LatestRunStartedAt != nil {
		d.RunStartedAt = *t.LatestRunStartedAt
	}
	d.PullRequests = t3PullRequestCount(t, len(normalizeT3PullRequests(t.PullRequests)))
	if t3IsSubagentThread(t) && t.Lineage.ParentThreadID != "" {
		d.ParentThreadID = t.Lineage.ParentThreadID
	}
	return d
}

// ==================== INT-216 短接口 ====================
//
// t3ShellState 本身就是「活跃/归档两面 + 信任字段 + 按需簿记」的单一
// in-process 状态机：所有信任转移在 s.mu 单个临界区内完成，调用方不再
// 拼接 note*/arm*/Locked 方法模拟原子序列，只执行返回的声明式
// t3ShellEffects（runEffects，锁外副作用）；内部无锁内核由短接口与测试
// 观测中间量复用。

// t3PublishKind 是 Effects.Publish 的取值：是否发归档面事件、以何种强度发。
type t3PublishKind int

const (
	// t3PublishNone 不发布。
	t3PublishNone t3PublishKind = iota
	// t3PublishDedup 经归档指纹去重发布（publishArchiveFaceEvent forceFull=false）：
	// 集合真变发 Full、行内容/可信度文案变发轻量 State、无变化静默。
	t3PublishDedup
	// t3PublishForceFull 写后失效专用：归档集「可能」变了但去向不可辨，
	// 强制一次整环境 Full，不去重。
	t3PublishForceFull
)

func (k t3PublishKind) stronger(o t3PublishKind) t3PublishKind {
	if o > k {
		return o
	}
	return k
}

// t3ShellEffects 是状态机写方法返回的声明式副作用清单。hub 侧统一执行器
// runEffects 按固定顺序执行：排定时器 → 重连信号 → 激活 legacy → 发布 →
// refresh → 补拉。多个转移的 Effects 按 OR 合并（Publish 取强者）。
type t3ShellEffects struct {
	Publish         t3PublishKind
	Refresh         bool      // e.refresh()：可信标记可能翻转，唤醒等待者
	SignalReconnect bool      // legacyReconnect 信号：旧面订阅断开重连取写后全量
	ActivateLegacy  bool      // legacyCh 信号：放行停泊中的旧面订阅监督循环
	SettleAt        time.Time // 待对账最早截止时刻，零值不排定时器
	Refetch         bool      // 写前结果未封窗/被丢弃：补发一次按需拉取
	// Err 携带需要调用方代记日志的失败原因（状态机纯内存不持 logger）；
	// runEffects 不消费它，由调用方按需 Warn。
	Err error
}

// merge 按 OR 合并另一份 Effects（同一次转移的多个子效果聚合）。
func (e *t3ShellEffects) merge(o t3ShellEffects) {
	e.Publish = e.Publish.stronger(o.Publish)
	e.Refresh = e.Refresh || o.Refresh
	e.SignalReconnect = e.SignalReconnect || o.SignalReconnect
	e.ActivateLegacy = e.ActivateLegacy || o.ActivateLegacy
	if e.SettleAt.IsZero() || (!o.SettleAt.IsZero() && o.SettleAt.Before(e.SettleAt)) {
		e.SettleAt = o.SettleAt
	}
	e.Refetch = e.Refetch || o.Refetch
	if e.Err == nil {
		e.Err = o.Err
	}
}

// t3FetchTicket 是 BeginFetch 准入时取样的拉取证：失效世代（落地世代护栏）
// 与本次拉取的发起时刻（快照采集时刻，#156 R16）。
type t3FetchTicket struct {
	generation int64
	startedAt  time.Time
}

// t3FetchOutcomeKind 区分 EndFetch 的落地结局。
type t3FetchOutcomeKind int

const (
	// t3FetchAborted 拉取未落地（停机/关闭路径，R35）：只清在途标记。
	t3FetchAborted t3FetchOutcomeKind = iota
	// t3FetchSnapshot 取回快照（坏负载在 EndFetch 内部按读取失败分流）。
	t3FetchSnapshot
	// t3FetchFailed 读取失败（探测/拨号/RPC/应答形状校验）。
	t3FetchFailed
	// t3FetchMethodMissing getArchivedShellSnapshot 方法不存在：同一临界
	// 区内先记失败、后切 legacy（R5 P2）。
	t3FetchMethodMissing
)

// t3FetchOutcome 是一次按需拉取的结局，交给 EndFetch 在同一临界区内收尾。
type t3FetchOutcome struct {
	kind     t3FetchOutcomeKind
	snapshot t3ArchivedShellSnapshot
	err      error
}

// t3LegacyStamp 是旧面订阅连接的出发邮戳（LegacyStamp）：gen 是发
// subscribe 之前读到的 reconnect 请求世代，at 是连接建立时刻（快照采集
// 时刻的保守下界，#156 R17/R18）。
type t3LegacyStamp struct {
	gen int64
	at  time.Time
}

// ApplyActive 按批应用活跃流流项：逐帧应用、批尾统一处理「线程离开活跃
// 面」——legacy 模式就地登记待对账（armed ≡ legacy，登记与模式判定在同一
// 临界区），按需模式就地失效归档缓存（推世代 + 撤 ready + 清簿记）。整个
// 批次在 s.mu 单个临界区内完成，「活跃面已删、归档面尚未失效」的可读窗口
// 不复存在（INT-216 修掉的按需模式移除窗口）。失效以 Chunk 为单位：即使
// 中途出错，已应用帧造成的移除也随返回的 Effects 执行（R15 P2）。
// 事件 ID 与 Full 判定由 hub 的 batch.observeActive 自算，不在此返回。
func (s *t3ShellState) ApplyActive(items []t3ShellStreamItem) (t3ShellEffects, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var eff t3ShellEffects
	hadUncertain := len(s.archiveUncertain) > 0
	leftActiveFace := false
	var applyErr error
	for i := range items {
		removed, _, _, err := s.applyActiveItemDetailedLocked(&items[i])
		if err != nil {
			applyErr = err
			break
		}
		if len(removed) > 0 || isActiveLeaveFrame(&items[i]) {
			leftActiveFace = true
		}
	}
	if leftActiveFace {
		if s.archiveLegacy {
			// 待对账登记已在逐帧应用时就地完成；这里只补发布与到期排程。
			if len(s.archiveUncertain) > 0 {
				eff.Publish = eff.Publish.stronger(t3PublishDedup)
				if d, ok := s.earliestUncertainDeadlineLocked(); ok {
					eff.SettleAt = d
				}
			}
		} else {
			// 按需模式：离开活跃面即归档集可能变了（R16 P2-1），写后失效。
			s.noteArchiveInvalidatedLocked()
			s.validUntil = time.Time{}
			s.retryAfter = time.Time{}
			eff.Publish = eff.Publish.stronger(t3PublishForceFull)
			eff.Refresh = true
		}
	}
	// 活跃面回现/快照带回直接解账（#156 R32）：解账翻转同样要过统一发布口
	// 同步宣布基准，否则下一次登记在归档行集合未变时被指纹判静默。
	if hadUncertain && len(s.archiveUncertain) == 0 {
		eff.Publish = eff.Publish.stronger(t3PublishDedup)
	}
	return eff, applyErr
}

// isActiveLeaveFrame 判定该帧是否宣告线程离开活跃面：thread.removed 与
// location != "active" 的 thread.updated（上游对移除不看 location，两种
// 帧都不携带去向——可能是归档也可能是删除，按需缓存统一按失效处理）。
func isActiveLeaveFrame(item *t3ShellStreamItem) bool {
	return item.Kind == "thread.removed" ||
		(item.Kind == "thread.updated" && item.Location != "active")
}

// MarkActiveSynced 由 hub 控制追平标记：resume 时在重发订阅前清 false、
// 等 synchronized（或能力不支持/超时兜底）后置 true。不碰数据与游标。
func (s *t3ShellState) MarkActiveSynced(synced bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeSynced = synced
}

// BeginFetch 登记一次按需拉取：准入判定（legacy 短路 → inFlight 合并 →
// 冷却/有效期封窗）、世代取样、refetchStartedAt/inFlight 置位在同一临界区
// 内完成，返回 ticket 交 EndFetch 收尾。在途超过宽限且缓存已过期时撤可信
// 并返回 revoked 的 Effects（Finding R2 / R20 P2）。
func (s *t3ShellState) BeginFetch() (t3FetchTicket, bool, t3ShellEffects) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var eff t3ShellEffects
	// 订阅模式由常驻流供数，按需拉取在准入内短路——避免周期空拨。
	if s.archiveLegacy {
		return t3FetchTicket{}, false, eff
	}
	now := s.now()
	if s.inFlight {
		// 在途过久才撤可信（R17 P2-1 / R20 P2）：判据锚在**本次拉取发起
		// 时刻**上，而不是 validUntil 的年龄——过期只说明该重拉了，空闲
		// 回来后首次读自己刚发起的那次拉取不该被判「过期太久」。撤可信与
		// 在途状态在同一临界区内完成，对读者不可分。
		// 仅在缓存仍过期时才撤（跟进 PR①，Codex :214）：重拉可能已经成功
		// 落地（archiveReady=true、validUntil 在未来），只是 kick 的收尾
		// 还没来得及清 inFlight/refetchStartedAt——这个窗口里按「在途过
		// 久」撤可信会把刚恢复的面重新打回不可信；而 inFlight 清空后，后
		// 续准入看到未来的 validUntil 不再重拉，降级就会持续整个 TTL。
		if !s.refetchStartedAt.IsZero() &&
			now.Sub(s.refetchStartedAt) > s.archiveStaleGrace &&
			!now.Before(s.validUntil) {
			s.noteArchiveExpiredLocked()
			eff.Publish = t3PublishDedup
			eff.Refresh = true
		}
		return t3FetchTicket{}, false, eff
	}
	if now.Before(s.validUntil) || now.Before(s.retryAfter) {
		return t3FetchTicket{}, false, eff
	}
	// 准入判定通过：validUntil 一定是零值或已过。过期只表示「该重拉了」，
	// 不等于数据作废（R16 P2-2）：本次读取仍返回上一份快照，不因「触发重拉
	// 的这次读」就把面标 degraded。撤可信改由在途时长兜底（上面的 inFlight
	// 分支）：重拉真的挂了超过 ArchiveStaleGrace 才降级。
	s.inFlight = true
	s.refetchStartedAt = now
	return t3FetchTicket{generation: s.archiveGeneration, startedAt: now}, true, eff
}

// EndFetch 收尾一次按需拉取：清 inFlight/refetchStartedAt，按结局在同一
// 临界区内完成可信度转移与簿记——
//   - 快照：apply 世代护栏（写前快照只换数据不封窗）、freshnessFloor 丢弃；
//     世代相符才封 validUntil 窗口（Finding R1：应用与封窗不可分）。
//   - 失败：当前世代记 ArchiveErr 真实原因 + retryAfter 冷却；写前失败不
//     覆盖失效语义、不记冷却。
//   - 方法不存在：先记失败、后切 legacy（R5 P2——随后落地的订阅权威快照
//     必然覆盖本次失败记账），返回 ActivateLegacy 放行旧面订阅。
//   - 中止：只清在途标记（R35）。
//
// 补拉不再经状态字段备忘：ticket 世代≠当前世代或被 freshnessFloor
// 丢弃且非 legacy 时，直接由 Effects.Refetch 表达（原 kick defer 的
// 「清 inFlight+消费标志+补拉」三步合进一个临界区）。
func (s *t3ShellState) EndFetch(ticket t3FetchTicket, outcome t3FetchOutcome) t3ShellEffects {
	s.mu.Lock()
	defer s.mu.Unlock()
	var eff t3ShellEffects
	s.inFlight = false
	s.refetchStartedAt = time.Time{}
	switch outcome.kind {
	case t3FetchAborted:
		return eff
	case t3FetchMethodMissing:
		s.recordFetchFailureLocked(ticket.generation, outcome.err, &eff)
		s.archiveLegacy = true
		// 与模式翻转同一临界区开启「移除即登记待对账」（#156 R6）：armed
		// 判定已并入模式（armed ≡ legacy），此后到达的活跃面移除在同一
		// 临界区内登记，不存在「mode 已是 legacy 而尚未 arm」的窗口。
		s.validUntil = time.Time{}
		// retryAfter 保留（R14 P2-2）：刚记的失败冷却是回按需探测后的现成
		// 探测冷却，双方法缺失的环境至多每冷却窗口重探测一次。
		s.noteArchiveExpiredLocked() // archiveReady → false，留行但报已失效
		eff.ActivateLegacy = true
		eff.Refetch = false // legacy 下按需补拉无意义（BeginFetch 也会拒）
		return eff
	case t3FetchFailed:
		s.recordFetchFailureLocked(ticket.generation, outcome.err, &eff)
		return eff
	default: // t3FetchSnapshot
		applied, applyErr := s.applyOnDemandSnapshotCommitted(outcome.snapshot, ticket.generation, ticket.startedAt)
		if applyErr != nil {
			// 坏负载按本次读取失败记账（ArchiveErr 真实原因 + 冷却），阻断
			// 毒快照的无冷却重试风暴（复审遗留：不得与世代不符合并）。
			s.recordFetchFailureLocked(ticket.generation, applyErr, &eff)
			return eff
		}
		eff.Publish = t3PublishDedup
		eff.Refresh = true
		if applied {
			s.validUntil = s.now().Add(s.archiveTTL)
			s.retryAfter = time.Time{}
			return eff
		}
		// 世代不符或 freshnessFloor 丢弃：写前状态未封窗，补拉写后快照。
		if !s.archiveLegacy {
			eff.Refetch = true
		}
		return eff
	}
}

// recordFetchFailureLocked 失败记账的无锁内核（调用方须持 s.mu）：当前
// 世代失败写 ArchiveErr 真实原因 + retryAfter 冷却；写前失败（世代不符）
// 不覆盖失效语义、不封窗，只表达补拉诉求。清表条件收窄为「从未有过成功
// 快照」（Finding R3）：失效后留行的面在当前世代重拉失败时仍须保留旧行。
func (s *t3ShellState) recordFetchFailureLocked(generation int64, err error, eff *t3ShellEffects) {
	eff.Publish = eff.Publish.stronger(t3PublishDedup)
	eff.Refresh = true
	eff.Err = err
	if generation != s.archiveGeneration {
		if !s.archiveLegacy {
			eff.Refetch = true
		}
		return
	}
	if !s.archiveReady && !(s.archiveStale && s.archivedThreads.len() != 0) {
		s.archivedThreads = newT3ShellFace[t3ThreadShell]()
	}
	s.archiveErr = err
	s.retryAfter = s.now().Add(s.archiveTTL)
}

// Invalidate 作废归档快照缓存（写后失效）：世代计数推进（在途拉取的结果
// 失去可信化资格）、归档面标「已失效重拉中」、旧行保留。legacy 模式记
// 「请重建订阅」备忘并返回 SignalReconnect（R3 P1-B：常驻流只收增量，
// 写后全量只能靠断开重连的权威快照）；按需模式清簿记，由下一次读经
// BeginFetch 重拉。发布强制 Full——归档集「可能」变了但去向不可辨。
func (s *t3ShellState) Invalidate() t3ShellEffects {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noteArchiveInvalidatedLocked()
	eff := t3ShellEffects{Publish: t3PublishForceFull, Refresh: true}
	if s.archiveLegacy {
		s.noteArchiveReconnectRequestedLocked()
		eff.SignalReconnect = true
	} else {
		s.validUntil = time.Time{}
		s.retryAfter = time.Time{}
	}
	return eff
}

// LegacyStamp 在旧面订阅发 subscribe 之前取一次出发邮戳（审修 R6-A 邮戳
// 时机 + #156 R18 采集时刻）：gen 之后记录的 reconnect 请求世代必然大于
// 它，本连接送达的变更前快照才不能误清后到的备忘（R4 P2）；at 作快照
// 采集时刻的保守下界（服务端可能在收到订阅时立刻采样）。一次调用取代
// 原 archiveReconnectTarget + connStartAt 两次取值。
func (s *t3ShellState) LegacyStamp() t3LegacyStamp {
	s.mu.Lock()
	defer s.mu.Unlock()
	return t3LegacyStamp{gen: s.archiveReconnectIssued, at: s.now()}
}

// ApplyLegacy 按批应用旧面归档订阅流项：快照按邮戳做 reconnect 世代护栏
// （邮戳 < 备忘世代 → 写前快照只换数据不封窗），增量落地但**不**解除待对账
// （#156 R15）。批尾检查「本连接之后记录的 reconnect 请求」
// （pendingGen > stamp.gen，INT-216 收口原 archiveReconnectTarget 比较）→
// SignalReconnect 交读循环断开重连；待对账非空 → SettleAt 排到期清理。
// 每次落地后经 Effects.Publish=dedup 同步宣布基准（R17 P2-2）。
func (s *t3ShellState) ApplyLegacy(stamp t3LegacyStamp, items []t3ArchiveStreamItem) (t3ShellEffects, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var eff t3ShellEffects
	for i := range items {
		if err := s.applyArchiveItemLocked(&items[i], stamp.gen, stamp.at); err != nil {
			return eff, err
		}
	}
	if s.archiveReconnectPendingGen > stamp.gen {
		eff.SignalReconnect = true
	}
	if len(s.archiveUncertain) > 0 {
		if d, ok := s.earliestUncertainDeadlineLocked(); ok {
			eff.SettleAt = d
		}
	}
	eff.Publish = eff.Publish.stronger(t3PublishDedup)
	return eff, nil
}

// LegacyEnded 是旧面订阅连接的统一收尾（G1）：legacy 模式下任何终止都
// 清表 + ArchiveErr（订阅面数据只有伴随健康连接才可信），非 legacy（自愈
// 回按需后尚未收尾的残余连接）时 no-op——残余连接的终止不得推翻已由按需
// 面接管的归档数据（审修 R9 P2-1）。
func (s *t3ShellState) LegacyEnded(err error) t3ShellEffects {
	s.mu.Lock()
	defer s.mu.Unlock()
	var eff t3ShellEffects
	if s.archiveLegacy {
		s.markArchiveFailedLocked(err)
		eff.Publish = t3PublishDedup
	}
	return eff
}

// ConsumeLegacyReconnect 原子读取并清除重建备忘：返回 true 表示有未消费
// 的请求，旧面读循环应断开本连接交监督循环重连。不做邮戳门控——备忘世代
// 比本连接邮戳新正是「须断开本连接」的情形（R4 P2）。
func (s *t3ShellState) ConsumeLegacyReconnect() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := s.archiveReconnectPendingGen != 0
	s.archiveReconnectPendingGen = 0
	return pending
}

// LegacyMethodMissing 处理「subscribeArchivedShell 被判方法不存在」（审修
// R8 P2）：本该有订阅面的环境反而没有，是升级/误判的证据——同一临界区内
// 退出 legacy、清簿记与遗留待对账、撤归档面可信（R13 P2-2），下一次读经
// BeginFetch 重探测按需快照。retryAfter 保留作探测冷却（R14 P2-2）。
// 非 legacy 时 no-op。
func (s *t3ShellState) LegacyMethodMissing() t3ShellEffects {
	s.mu.Lock()
	defer s.mu.Unlock()
	var eff t3ShellEffects
	if !s.archiveLegacy {
		return eff
	}
	s.archiveLegacy = false
	s.validUntil = time.Time{}
	// 离开订阅模式：清空遗留待对账条目（#156 R5/R8-2）——armed 判定已并入
	// 模式，archiveLegacy=false 即撤登记；遗留条目没有定时器清理，又会让
	// 后续订阅快照永远报 stale。
	s.clearArchiveMembershipUncertainLocked()
	s.noteArchiveExpiredLocked()
	// 不发布：撤回后到落地前的窗口里读取的响应自身携带 stale 文案，同数据
	// 落地后指纹与宣布基准一致照旧静默；落地失败则失败路径经指纹比对发
	// State。
	return eff
}

// IsLegacy 报告归档面当前是否由旧面订阅供数。
func (s *t3ShellState) IsLegacy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.archiveLegacy
}

// Settle 是一次待对账到期清理（#156 R2/R23）：清掉已过截止的条目，全部
// 对账完成时恢复可信并置 Publish/Refresh；仍有剩余时把下一个最早截止
// 放进 SettleAt 续排——每条目的上界就是它自己的截止时刻，早先条目不
// 被后来的登记顺延。
func (s *t3ShellState) Settle() t3ShellEffects {
	s.mu.Lock()
	defer s.mu.Unlock()
	var eff t3ShellEffects
	if s.expireArchiveMembershipUncertainLocked() {
		eff.Publish = t3PublishDedup
		eff.Refresh = true
	}
	if d, ok := s.earliestUncertainDeadlineLocked(); ok {
		eff.SettleAt = d
	}
	return eff
}

// Snapshots 组装既有消费方使用的两份快照（原 snapshots()）。
func (s *t3ShellState) Snapshots() *t3ShellSnapshots {
	return s.snapshots()
}

// Status 返回可信标记只读快照（原 status()）。
func (s *t3ShellState) Status() t3ShellFaceStatus {
	return s.status()
}

// Fingerprint 返回归档面发布去重三元组：行摘要序列、可信度文案、单调
// 修订号。archiveRev 由指纹内容派生——行摘要或文案任一变化的计算之间
// 自动推进，无需各写路径手工打点；publishArchiveFaceEvent 的宣布基准
// 只接受 rev 单调递增的写入，「指纹取锁与基准比较两锁不原子、旧指纹后
// 写覆盖基准」的窗口由此关闭。
func (s *t3ShellState) Fingerprint() ([]t3ArchiveRowDigest, string, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, errText := s.fingerprintLocked()
	if !s.fpInit || !slices.Equal(rows, s.fpRows) || errText != s.fpErr {
		s.archiveRev++
		s.fpInit = true
		s.fpRows = rows
		s.fpErr = errText
	}
	return rows, errText, s.archiveRev
}

// fingerprintLocked 计算归档面指纹（调用方须持 s.mu）：过滤活跃面残留后
// 的行摘要序列 + ArchiveErr 文案（含「未就绪」兜底，与 snapshots() 的
// archiveErr 同一推导）。
func (s *t3ShellState) fingerprintLocked() ([]t3ArchiveRowDigest, string) {
	rows := make([]t3ArchiveRowDigest, 0, s.archivedThreads.len())
	for _, id := range s.archivedThreads.order {
		if _, live := s.activeThreads.byID[id]; !live {
			rows = append(rows, archiveRowDigestOf(s.archivedThreads.byID[id]))
		}
	}
	errText := ""
	if s.archiveErr != nil {
		errText = s.archiveErr.Error()
	} else if !s.archiveReady {
		errText = errT3ShellArchiveNotReady.Error()
	}
	return rows, errText
}
