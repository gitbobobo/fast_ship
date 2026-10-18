package service

import (
	"encoding/json"
	"fmt"
	"sync"
)

// T3 shell 订阅的纯内存状态（INT-116）：活跃 / 归档两面各自维护 map+order，
// 由 hub 的两条长驻订阅流分别喂入，读时组装成既有消费方使用的 t3ShellSnapshots。
// 无任何 I/O，可独立单测。
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
//   - 归档流（orchestration.subscribeArchivedShell，输入 {} 无 afterSequence）：
//     先全量 snapshot 再接 thread.updated/thread.removed（无 location 字段，
//     contracts orchestrationV2.ts:3264-3281；ShellStream.ts:322-344）。
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

// t3ArchiveStreamItem 是归档流流项的宽松解码（无 location / enrichment 字段）。
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

// t3ShellState 汇聚一个环境的活跃 + 归档两面 shell 数据。
//
// 可信标记分离（设计 B1 规则 5）：
//   - ActiveReady：收到过权威活跃快照；
//   - ActiveSynced：追平。权威帧本身即视为追平（快照是完整的）；resume 回放
//     期间只有元数据帧与增量，须等 synchronized 标记——由 hub 调
//     markActiveSynced 控制；能力不支持时 hub 在发出订阅后立即置位（对齐
//     官方客户端 packages/client-runtime/src/state/shell.ts:386-398）；
//   - ArchiveReady：收到过归档全量快照。
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
}

func newT3ShellState() *t3ShellState {
	return &t3ShellState{
		activeProjects:  newT3ShellFace[t3ProjectShell](),
		activeThreads:   newT3ShellFace[t3ThreadShell](),
		archivedThreads: newT3ShellFace[t3ThreadShell](),
	}
}

// applyActiveSnapshot 应用权威活跃快照：整体替换 projects + active threads，
// 游标重置为 snapshotSequence；archivedThreads 字段（真实服务恒为空）一律
// 丢弃——归档数据只由归档流维护，不能被活跃帧清空（规则 1/3）。
func (s *t3ShellState) applyActiveSnapshot(snapshot t3ShellSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyActiveSnapshotLocked(snapshot)
}

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
		s.activeThreads.remove(item.ThreadID)
		s.advanceActiveCursorLocked(*item.Sequence)
		return nil
	default:
		return fmt.Errorf("活跃流未知帧 kind %q", item.Kind)
	}
}

// advanceActiveCursorLocked 把游标推进为 delta 序号（只前进不回卷；权威快照
// 的重置走 applyActiveSnapshot，不受此限制）。
func (s *t3ShellState) advanceActiveCursorLocked(sequence int) {
	if sequence > s.activeCursor {
		s.activeCursor = sequence
	}
}

// applyArchiveSnapshot 应用归档全量快照：替换归档两表并清除归档错误——
// 新的全量快照意味着归档面重新可信（G1 的解除路径）。
func (s *t3ShellState) applyArchiveSnapshot(snapshot t3ArchivedShellSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyArchiveSnapshotLocked(snapshot)
}

// applyArchiveSnapshotLocked 应用归档全量快照：替换归档线程表并清除归档错误
// ——新的全量快照意味着归档面重新可信（G1 的解除路径）。
//
// 快照里的 Projects 一律忽略（F7）：项目表只有活跃面一份权威来源。归档订阅
// 只在初始快照时携带项目，常驻订阅后旧名字会一直覆盖活跃流的项目更新/删除，
// 永不自愈；归档线程的项目名改从活跃项目表取，项目已删除时显示无名，与
// 「项目已删除」一致。
func (s *t3ShellState) applyArchiveSnapshotLocked(snapshot t3ArchivedShellSnapshot) error {
	if err := s.archivedThreads.reset(snapshot.Threads, func(t *t3ThreadShell) string { return t.ID }); err != nil {
		return fmt.Errorf("归档快照线程缺少 ID: %w", err)
	}
	s.archivedSchemaVersion = snapshot.SchemaVersion
	s.archivedSeq = snapshot.SnapshotSequence
	s.archiveReady = true
	s.archiveErr = nil
	return nil
}

// applyArchiveItem 应用一个归档流流项（无 location 字段）。
func (s *t3ShellState) applyArchiveItem(item t3ArchiveStreamItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch item.Kind {
	case "snapshot":
		if item.Snapshot == nil {
			return fmt.Errorf("归档快照帧缺少 snapshot 字段")
		}
		return s.applyArchiveSnapshotLocked(*item.Snapshot)
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

// markArchiveFailed 归档面整体弃用（G1）：清空归档线程表并记录错误，活跃面
// 照常可用。归档连接的任何终止路径都经 hub 的 endFaceConnection 调到这里
// （F2）——归档数据只有伴随健康连接才可信。之后归档快照成功到达即恢复。
func (s *t3ShellState) markArchiveFailed(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.archivedThreads = newT3ShellFace[t3ThreadShell]()
	s.archiveReady = false
	s.archiveErr = err
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

	// 归档面未就绪也要如实标不可信：ArchiveErr == nil 只说明「读到了且没出错」，
	// 不代表归档已经加载完（两条连接进度独立，活跃面常先就绪）。
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
