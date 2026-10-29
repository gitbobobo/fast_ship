package service

import (
	"encoding/json"
	"fmt"
	"sync"
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

	// archiveReconnectPendingGen 是旧面订阅路径独有的「请重建归档订阅」
	// 备忘（守旧游标），以单调世代表达：0 表示无未决请求，>0 表示最新一
	// 次请求的世代。常驻流没有按需拉取可发——除重连外上游只推增量、不
	// 自发重发全量，RefreshArchive 强拉与写后失效要取回写后全量只能靠
	// 断开重连（新 subscribe 即收权威快照）。备忘由读循环的
	// legacyReconnect 分支消费后主动断开（R3 P1-B：增量落地后整表重置
	// 会让归档面长期不可信）。只被 archiveLegacy 模式读写，统一记在
	// s.mu 下——arch.mu 掺与 state 判定会造成 arch.mu → s.mu 的反向嵌套
	// （R3 P1-A，全局锁序 entry.mu → s.mu → arch.mu）。
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
	// archiveReconnectIssued 是已发出的 reconnect 请求总数（最新请求的世
	// 代编号）。与 pending 分离：consume/连接终止只清 pending，不回卷本
	// 计数——它是新请求世代的来源，也是连接出发邮戳的取值来源。
	archiveReconnectIssued int64
}

func newT3ShellState() *t3ShellState {
	return &t3ShellState{
		activeProjects:  newT3ShellFace[t3ProjectShell](),
		activeThreads:   newT3ShellFace[t3ThreadShell](),
		archivedThreads: newT3ShellFace[t3ThreadShell](),
	}
}

// applyActiveSnapshotLocked 应用权威活跃快照（调用方须已持有锁）：整体替换
// projects + active threads，游标重置为 snapshotSequence；archivedThreads
// 字段（真实服务恒为空）一律丢弃——归档数据只由归档流维护，不能被活跃帧
// 清空（规则 1/3）。
func (s *t3ShellState) applyActiveSnapshotLocked(snapshot t3ShellSnapshot) error {
	if err := s.activeProjects.reset(snapshot.Projects, func(p *t3ProjectShell) string { return p.ID }); err != nil {
		return fmt.Errorf("活跃快照项目缺少 ID: %w", err)
	}
	if err := s.activeThreads.reset(snapshot.Threads, func(t *t3ThreadShell) string { return t.ID }); err != nil {
		return fmt.Errorf("活跃快照线程缺少 ID: %w", err)
	}
	s.activeSchemaVersion = snapshot.SchemaVersion
	s.activeCursor = snapshot.SnapshotSequence
	s.activeReady = true
	s.activeSynced = true
	return nil
}

// applyActiveItem 应用一个活跃流流项。坏帧 / 未知 kind 返回错误，由调用方
// 整条流失败重连，不得静默跳过然后继续宣称 ready（规则 8）。
func (s *t3ShellState) applyActiveItem(item t3ShellStreamItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch item.Kind {
	case "snapshot":
		if len(item.ResolvedRepositoryIdentityRoots) > 0 {
			// 元数据/enrichment 帧：Fast Ship 不使用 repositoryIdentity，
			// 整体忽略（只 Ack，不改数据、不动游标、不动可信标记）。
			return nil
		}
		if item.Snapshot == nil {
			return fmt.Errorf("活跃快照帧缺少 snapshot 字段")
		}
		return s.applyActiveSnapshotLocked(*item.Snapshot)
	case "synchronized":
		// 追平标记：不携带数据，只推进可信标记（不碰游标，规则 2）。
		s.activeSynced = true
		return nil
	case "project.updated":
		if item.Sequence == nil || item.Project == nil || item.Project.ID == "" {
			return fmt.Errorf("project.updated 帧缺少 sequence/project/projectId")
		}
		s.activeProjects.upsert(item.Project, item.Project.ID)
		s.advanceActiveCursorLocked(*item.Sequence)
		return nil
	case "project.removed":
		if item.Sequence == nil || item.ProjectID == "" {
			return fmt.Errorf("project.removed 帧缺少 sequence/projectId")
		}
		s.activeProjects.remove(item.ProjectID)
		s.advanceActiveCursorLocked(*item.Sequence)
		return nil
	case "thread.updated":
		if item.Sequence == nil || item.Thread == nil || item.Thread.ID == "" {
			return fmt.Errorf("thread.updated 帧缺少 sequence/thread/threadId")
		}
		// 活跃流实际只产生 location:"active"（ShellStream.ts:292-320）。旧版服务端
		// 也可能在这条流上发 location:"archive"，上游 reducer 对它的处理是「不进
		// 活跃面」（shellReducer.ts:169-190），归档面归归档订阅维护；当坏帧处理
		// 会把协议演进拖成重连循环，代价远大于收益。
		if item.Location == "active" {
			s.activeThreads.upsert(item.Thread, item.Thread.ID)
		} else {
			s.activeThreads.remove(item.Thread.ID)
		}
		s.advanceActiveCursorLocked(*item.Sequence)
		return nil
	case "thread.removed":
		if item.Sequence == nil || item.ThreadID == "" {
			return fmt.Errorf("thread.removed 帧缺少 sequence/threadId")
		}
		// 只从活跃面删除：归档动作在上游也发 active removal
		// （ShellStream.ts:292-320），不代表线程从归档面消失（规则 3）；
		// removal 的 location 不改变语义（上游对移除不看 location）。
		// state 是纯内存、不持 hub 引用：归档缓存的失效由 hub 层在应用此帧后
		// 触发（applyActiveValues → invalidateArchiveEntry），这里只管活跃面。
		s.activeThreads.remove(item.ThreadID)
		s.advanceActiveCursorLocked(*item.Sequence)
		return nil
	default:
		return fmt.Errorf("活跃流未知帧 kind %q", item.Kind)
	}
}

// advanceActiveCursorLocked 把游标推进为 delta 序号（只前进不回卷；权威快照
// 的重置走 applyActiveSnapshotLocked，不受此限制）。
func (s *t3ShellState) advanceActiveCursorLocked(sequence int) {
	if sequence > s.activeCursor {
		s.activeCursor = sequence
	}
}

// applyArchiveSnapshot 应用归档全量快照：替换归档线程表并清除归档错误——
// 新的全量快照意味着归档面重新可信（G1 的解除路径）。返回 (applied, err)
// 区分两种「未可信落地」的结局（复审遗留：不得把两种 false 合并）：
//   - 坏负载（reset 拒收，如线程缺 ID）：applied=false 且 err 非空。这是
//     「本次读取失败」，不在本函数内 commit，由调用方走失败路径
//     （noteArchiveFetchFailed 写 ArchiveErr 真实原因 + retryAfter 冷却；
//     它的世代逻辑顺带处理写前坏负载——不记冷却、只留 needsRefetch）。
//   - 世代不符（写前快照）：applied=false 且 err=nil——陈旧不是错误。
//
// generation 是发起拉取时刻读到的失效计数：拉取期间归档面被写后失效
// （noteArchiveInvalidated 推进计数）时，本次快照必定反映写前状态——放行它
// 等于把「写前快照」封回可信窗口直到 TTL 到期，wait/详情会在这整段窗口里
// 拿陈旧归档集下「线程不存在」结论。世代变则只应用数据、不提升可信度：
// archivedThreads 换成较新读取（数据并不更旧），但 archiveReady 保持 false、
// ArchiveErr 报告「已失效重拉中」，由发起方负责再发一次新拉取。世代未变
// 才置 ready 并清除 archiveErr/archiveStale（applied=true）。
//
// commit 非空时在同一个临界区内收尾 entry 侧簿记（validUntil/needsRefetch
// 等）：世代校验与簿记提交原子化（Finding R1），「apply 成功后、簿记提交
// 前发生失效」没有可插入的缝隙——失效要么先于本次落地（世代不符，
// commit 收到 false 不封窗），要么在 commit 之后发生（由
// InvalidateArchive 清零簿记），不存在「已失效却被 validUntil 封窗」的
// 交错。坏负载不经本回调（其簿记由失败路径的 commitArchiveFailure 收尾，
// 两处簿记不得双记）。commit 在持锁状态下回调，不得在回调里反向获取
// s.mu；它可以安全地获取 e.arch.mu（锁序 s.mu → arch.mu 全局一致，无
// 反向嵌套）。
//
// 快照里的 Projects 一律忽略（F7）：项目表只有活跃面一份权威来源，归档
// 线程的项目名改从活跃项目表取，项目已删除时显示无名，与「项目已删除」
// 一致。
func (s *t3ShellState) applyArchiveSnapshot(snapshot t3ArchivedShellSnapshot, generation int64, commit func(trusted bool)) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		if commit != nil {
			commit(false)
		}
		return false, nil
	}
	s.archiveReady = true
	s.archiveErr = nil
	s.archiveStale = false
	if commit != nil {
		commit(true)
	}
	return true, nil
}

// noteArchiveInvalidated 记录一次写后失效（InvalidateArchive）：可信窗口
// 作废、世代计数推进（使所有在途拉取的结果失去可信化资格），归档表保留
// 旧行供展示——清表会把「不确定」变成「确定为空」，比保留旧行更糟。
// archiveReady 撤为 false：失效到新快照落地之间 snapshots() 必须把归档面
// 报为不可信（ArchiveErr 非空），否则 wait 会在「写后快照尚未拉回来」的
// 窗口里把线程误判为不存在。
func (s *t3ShellState) noteArchiveInvalidated() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.archiveGeneration++
	s.archiveStale = true
	s.archiveReady = false
	if s.archiveErr == nil {
		s.archiveErr = errT3ShellArchiveStale
	}
}

// noteArchiveExpired 在发起 TTL 过期重拉时把归档面撤为不可信（Finding R2）：
// 过期不等于数据作废，旧行照旧展示，但 validUntil 已过意味着「快照不再被
// 信任为当前完整」——重拉在途期间若不撤 archiveReady，Peek/wait 会拿过期
// 快照判 thread_gone、谎报归档列表完整（与写后失效同型缺口）。落地成功
// （applyArchiveSnapshot 世代相符）恢复可信；落地失败由 noteArchiveFetchFailed
// 记真实失败原因。不推进世代：本次重拉正是为恢复可信而发，落地要按当前
// 世代正常封窗。已不可信的面（未就绪/已失效/已标记）不重复标记——既有
// archiveErr 文案（真实失败/失效）比过期语义更准确。
func (s *t3ShellState) noteArchiveExpired() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noteArchiveExpiredLocked()
}

// noteArchiveExpiredLocked 是 noteArchiveExpired 的无锁变体（调用方须已持有
// s.mu）：kick 的准入临界区经 commit 回调在 arch.mu 持锁内调用它，把「设
// inFlight」与「撤可信」收成同一原子序列（锁序 s.mu → arch.mu，与
// applyArchiveSnapshot 的 commit 同型）。
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

// archiveGenerationNow 返回当前失效计数（发起拉取时取样，落地时回传给
// applyArchiveSnapshot 做世代护栏判定）。
func (s *t3ShellState) archiveGenerationNow() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.archiveGeneration
}

// noteArchiveFetchFailed 记一次归档快照读取失败（INT-173）：不清数据、不改
// ArchiveReady。失败不推翻既有快照——归档面没有增量通道，保留的旧快照与
// 「清空后谎报归档列表为空」相比更不坏；ArchiveErr 单独承载失败，调用方
// 据此决定能不能把「归档面没有」当结论用（G1）。archiveReady 为假时清空
// 归档表——没有快照可留时半张表只会误导（同旧 markArchiveFailed 的清表
// 语义）。entry 的 TTL/失效簿记在 t3ArchiveFetchState，这里只认快照本身。
//
// generation 与 applyArchiveSnapshot 同一护栏：拉取期间归档面被失效标记
// 作废时，「这次读失败」只说明写前那次读取失败——用它把归档面盖成「读
// 失败」会抹掉「已失效重拉中」的语义并把失败冷却封到窗口上，拦不住「在
// 途写前失败盖掉失效」的交错。世代变则不覆盖 archiveErr/表：归档面已由
// noteArchiveInvalidated 标为不可信且保留旧行；返回 false 让发起方不记
// 冷却（世代变的失败不封窗，下次 kick 立刻重拉）。
//
// 清表条件收窄为「从未有过成功快照」（Finding R3）：!archiveReady 不够——
// 失效后留行的面（archiveStale）在当前世代重拉失败时仍须保留旧行，本次
// 失败只说明这次读没成，旧行并不因此更不可信；清表会把「不可信但留行」
// 错变成「确定为空」。无论哪条路径 ArchiveErr 都记本次真实失败原因。
// commit 与 applyArchiveSnapshot 同约束：在同一临界区内收尾冷却簿记
// （retryAfter/needsRefetch），锁序 s.mu → arch.mu。
// applyArchiveItem 应用一个旧归档订阅流项（无 location 字段）。旧面是全
// 量快照 + 增量的常驻流：snapshot 整体重置归档表，updated/removed 按项
// 维护。坏帧返回错误，由订阅路径整条流失败重连（不得静默跳过然后继续
// 宣称可信）。connectionStart 是本连接发 subscribe 之前读到的 reconnect
// 请求世代（出发邮戳，发前取值见 connectArchiveLegacyOnce）——快照项据
// 此判定能否清守旧备忘（见 applyArchiveSnapshotLocked）。
func (s *t3ShellState) applyArchiveItem(item t3ArchiveStreamItem, connectionStart int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch item.Kind {
	case "snapshot":
		if item.Snapshot == nil {
			return fmt.Errorf("归档快照帧缺少 snapshot 字段")
		}
		return s.applyArchiveSnapshotLocked(*item.Snapshot, connectionStart)
	case "thread.updated":
		if item.Sequence == nil || item.Thread == nil || item.Thread.ID == "" {
			return fmt.Errorf("归档 thread.updated 帧缺少 sequence/thread/threadId")
		}
		s.archivedThreads.upsert(item.Thread, item.Thread.ID)
		return nil
	case "thread.removed":
		if item.Sequence == nil || item.ThreadID == "" {
			return fmt.Errorf("归档 thread.removed 帧缺少 sequence/threadId")
		}
		s.archivedThreads.remove(item.ThreadID)
		return nil
	default:
		return fmt.Errorf("归档流未知帧 kind %q", item.Kind)
	}
}

// applyArchiveSnapshotLocked 是订阅模式下的快照整体替换（调用方须已持有
// 锁）：与按需路径的 applyArchiveSnapshot 同体但不带失效世代/commit——常
// 驻流的数据来自权威推送，没有「拉取途中被失效」的交错，可信化无条件
// 成立。快照里的 Projects 一律忽略（F7）：项目表只有活跃面一份权威来
// 源。
//
// reconnect 世代护栏（R4 P2）：connectionStart 是本连接的出发邮戳
// （发 subscribe 之前读到的请求世代）——本快照必在该连接之后发出，故它
// 一定晚于所有世代 ≤ 邮戳的请求。仅当邮戳不小于当前备忘世代时本快照
// 才算「该请求之后的权威状态」、才清备忘；备忘更新（请求记在快照发出
// 之后）时本快照是变更前状态，保留备忘——兑现交给读循环的信号分支：
// 信号必随该请求送达本连接的读循环（chan 缓冲 1 且每连接只有一个读循
// 环消费者，不会被快照吞掉），消费即断开本连接，下一条连接的 subscribe
// 必在请求之后发出，其快照必为写后状态。
func (s *t3ShellState) applyArchiveSnapshotLocked(snapshot t3ArchivedShellSnapshot, connectionStart int64) error {
	if err := s.archivedThreads.reset(snapshot.Threads, func(t *t3ThreadShell) string { return t.ID }); err != nil {
		return fmt.Errorf("归档快照线程缺少 ID: %w", err)
	}
	s.archivedSchemaVersion = snapshot.SchemaVersion
	s.archivedSeq = snapshot.SnapshotSequence
	s.archiveReady = true
	s.archiveErr = nil
	s.archiveStale = false
	if connectionStart >= s.archiveReconnectPendingGen {
		// 权威全量快照即是重连取写后状态的完成：撤销尚存的重建备忘，
		// 避免晚到的信号再把刚恢复的面拆掉一次。
		s.archiveReconnectPendingGen = 0
	}
	return nil
}

// markArchiveFailed 是订阅模式归档面的整体弃用（G1）：清空归档线程表并
// 记录错误，活跃面照常可用。归档订阅的任何终止路径都经 hub 的
// endFaceConnection 调到这里——订阅面数据只有伴随健康连接才可信；下
// 一次权威快照到达即恢复。与按需路径的 noteArchiveFetchFailed 区分：常
// 驻流失败后没有可保留的「TTL 窗口」，清空比留陈旧行更如实。
func (s *t3ShellState) markArchiveFailed(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// noteArchiveReconnectRequested 记订阅模式的「请重建连接」备忘（守旧
// 游标）：RefreshArchive/失效在订阅路径上无独立拉取可发，归档面先标为
// 「已失效重拉中」（不得把备忘期间的面报为完整）；读循环经
// legacyReconnect 信号唤醒后经 consumeArchiveReconnect 消费并断开，
// 监督循环重连取回权威全量。每次请求推进世代（即使备忘本就置位——
// 后到的请求必须能从更早连接发出的快照里区分出来，世代护栏依据），
// 返回本次请求的世代。
func (s *t3ShellState) noteArchiveReconnectRequested() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.archiveReconnectIssued++
	s.archiveReconnectPendingGen = s.archiveReconnectIssued
	s.archiveReady = false
	s.archiveStale = true
	if s.archiveErr == nil {
		s.archiveErr = errT3ShellArchiveStale
	}
	return s.archiveReconnectPendingGen
}

// consumeArchiveReconnect 原子读取并清除重建备忘：返回 true 表示有未消费
// 的请求，调用方（旧面读循环）应断开本连接交监督循环重连。不做邮戳门
// 控——备忘世代比本连接邮戳新正是「须断开本连接」的情形（本连接的订
// 阅取的是变更前状态）；兑现不靠本连接等，靠断开后下一条连接的写后订
// 阅。信号先到、备忘已被权威快照（邮戳 ≥ 备忘世代的那张）或连接终止
// 清掉时返回 false，信号忽略。
func (s *t3ShellState) consumeArchiveReconnect() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := s.archiveReconnectPendingGen != 0
	s.archiveReconnectPendingGen = 0
	return pending
}

// archiveReconnectTarget 返回已发出的 reconnect 请求总数（最新请求的世
// 代；从未请求过为 0）。旧面连接在发 subscribe 之前取它作出发邮戳
// （connectionStart）：发送前取值保证「订阅发出之后记录的新请求世代必
// 然大于邮戳」，本连接送达的快照才不能误清后到的备忘（R4 P2 + 审修
// R6-A 邮戳时机）。只增不减——备忘被消费/作废不回卷它，否则新请求的
// 世代会与旧请求撞号。
func (s *t3ShellState) archiveReconnectTarget() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.archiveReconnectIssued
}

// noteArchiveFetchFailed 记一次归档快照读取失败（INT-173）：不清数据、不改
// ArchiveReady。失败不推翻既有快照——归档面没有增量通道，保留的旧快照与
// 「清空后谎报归档列表为空」相比更不坏；ArchiveErr 单独承载失败，调用方
// 据此决定能不能把「归档面没有」当结论用（G1）。archiveReady 为假时清空
// 归档表——没有快照可留时半张表只会误导（同旧 markArchiveFailed 的清表
// 语义）。entry 的 TTL/失效簿记在 t3ArchiveFetchState，这里只认快照本身。
//
// generation 与 applyArchiveSnapshot 同一护栏：拉取期间归档面被失效标记
// 作废时，「这次读失败」只说明写前那次读取失败——用它把归档面盖成「读
// 失败」会抹掉「已失效重拉中」的语义并把失败冷却封到窗口上，拦不住「在
// 途写前失败盖掉失效」的交错。世代变则不覆盖 archiveErr/表：归档面已由
// noteArchiveInvalidated 标为不可信且保留旧行；返回 false 让发起方不记
// 冷却（世代变的失败不封窗，下次 kick 立刻重拉）。
//
// 清表条件收窄为「从未有过成功快照」（Finding R3）：!archiveReady 不够——
// 失效后留行的面（archiveStale）在当前世代重拉失败时仍须保留旧行，本次
// 失败只说明这次读没成，旧行并不因此更不可信；清表会把「不可信但留行」
// 错变成「确定为空」。无论哪条路径 ArchiveErr 都记本次真实失败原因。
// commit 与 applyArchiveSnapshot 同约束：在同一临界区内收尾冷却簿记
// （retryAfter/needsRefetch），锁序 s.mu → arch.mu。
func (s *t3ShellState) noteArchiveFetchFailed(err error, generation int64, commit func(applied bool)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != s.archiveGeneration {
		if commit != nil {
			commit(false)
		}
		return false
	}
	if !s.archiveReady && !(s.archiveStale && s.archivedThreads.len() != 0) {
		s.archivedThreads = newT3ShellFace[t3ThreadShell]()
	}
	s.archiveErr = err
	if commit != nil {
		commit(true)
	}
	return true
}

// markActiveSynced 由 hub 控制追平标记：resume 时在重发订阅前清 false、
// 等 synchronized（或能力不支持/超时兜底）后置 true。不碰数据与游标。
func (s *t3ShellState) markActiveSynced(synced bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeSynced = synced
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
