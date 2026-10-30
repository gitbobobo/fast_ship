package service

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// 以下为 t3ShellState 纯 reducer 单测（无 I/O），逐条覆盖 INT-116 设计 B1 规则：
// 权威 vs 元数据帧、游标只由活跃面推进、两面独立、合并视图 active 优先、
// 归档失败清空归档面、order 无 tombstone、发布对象不被原地修改、坏帧报错。

func shellTestThread(id, title string) *t3ThreadShell {
	return &t3ThreadShell{ID: id, ProjectID: "p-1", Title: title, Status: "idle"}
}

func shellTestThreads(items ...*t3ThreadShell) []t3ThreadShell {
	threads := make([]t3ThreadShell, 0, len(items))
	for _, item := range items {
		threads = append(threads, *item)
	}
	return threads
}

// shellTestAuthoritative 构造权威活跃快照流项（无 resolvedRepositoryIdentityRoots 字段）。
func shellTestAuthoritative(sequence int, projects []t3ProjectShell, threads []t3ThreadShell) t3ShellStreamItem {
	return t3ShellStreamItem{
		Kind:     "snapshot",
		Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: sequence, Projects: projects, Threads: threads},
	}
}

// shellTestEnrichment 构造元数据帧（带 resolvedRepositoryIdentityRoots 字段，
// threads 恒空、projects 只含已解析项——形状对照 ShellStream.ts:186-222）。
func shellTestEnrichment(sequence int, projects []t3ProjectShell) t3ShellStreamItem {
	return t3ShellStreamItem{
		Kind:     "snapshot",
		Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: sequence, Projects: projects, Threads: []t3ThreadShell{}},
		// RawMessage 非空即代表字段存在（哪怕空数组）。
		ResolvedRepositoryIdentityRoots: []byte("[]"),
	}
}

func TestShellState_AuthoritativeSnapshotReplacesAndReadies(t *testing.T) {
	state := newT3ShellState()
	projects := []t3ProjectShell{{ID: "p-1", Title: "Fast Ship"}}
	if err := state.applyActiveItem(shellTestAuthoritative(7, projects, shellTestThreads(shellTestThread("th-1", "一"), shellTestThread("th-2", "二")))); err != nil {
		t.Fatalf("apply authoritative: %v", err)
	}
	st := state.status()
	if !st.ActiveReady || !st.ActiveSynced {
		t.Fatalf("authoritative snapshot must set ready+synced, got %+v", st)
	}
	if st.ActiveCursor != 7 {
		t.Fatalf("cursor = %d, want 7", st.ActiveCursor)
	}
	snaps := state.snapshots()
	if len(snaps.Active.Threads) != 2 || snaps.Active.Threads[0].ID != "th-1" {
		t.Fatalf("threads = %+v", snaps.Active.Threads)
	}
	if len(snaps.Active.Projects) != 1 || snaps.Active.Projects[0].Title != "Fast Ship" {
		t.Fatalf("projects = %+v", snaps.Active.Projects)
	}
}

// 规则 1：元数据帧整体忽略——不改数据、不动游标、不动可信标记。
func TestShellState_MetadataFrameIgnored(t *testing.T) {
	state := newT3ShellState()
	// 权威帧先建立数据（游标 7）。
	if err := state.applyActiveItem(shellTestAuthoritative(7, []t3ProjectShell{{ID: "p-1", Title: "A"}}, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
		t.Fatalf("apply authoritative: %v", err)
	}
	// 元数据帧：threads 空、projects 只有已解析项、字段存在。
	if err := state.applyActiveItem(shellTestEnrichment(99, []t3ProjectShell{{ID: "p-9", Title: "Enriched"}})); err != nil {
		t.Fatalf("apply enrichment: %v", err)
	}
	st := state.status()
	if st.ActiveCursor != 7 {
		t.Fatalf("metadata frame must not move cursor, cursor = %d", st.ActiveCursor)
	}
	snaps := state.snapshots()
	if len(snaps.Active.Projects) != 1 || snaps.Active.Projects[0].ID != "p-1" {
		t.Fatalf("metadata frame must not replace projects: %+v", snaps.Active.Projects)
	}
	if len(snaps.Active.Threads) != 1 || snaps.Active.Threads[0].ID != "th-1" {
		t.Fatalf("metadata frame must not touch threads: %+v", snaps.Active.Threads)
	}
	// 元数据帧也不能让未就绪的状态宣称 ready。
	fresh := newT3ShellState()
	if err := fresh.applyActiveItem(shellTestEnrichment(1, nil)); err != nil {
		t.Fatalf("apply enrichment to fresh state: %v", err)
	}
	if st := fresh.status(); st.ActiveReady || st.ActiveSynced {
		t.Fatalf("metadata frame must not set ready flags: %+v", st)
	}
}

// 规则 2：较低权威快照把游标重置为 snapshotSequence（可以变小，不是 max）。
func TestShellState_LowerAuthoritativeSnapshotResetsCursor(t *testing.T) {
	state := newT3ShellState()
	if err := state.applyActiveItem(shellTestAuthoritative(50, nil, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	seq := 51
	if err := state.applyActiveItem(t3ShellStreamItem{Kind: "thread.updated", Sequence: &seq, Location: "active", Thread: shellTestThread("th-1", "一改")}); err != nil {
		t.Fatalf("apply delta: %v", err)
	}
	if st := state.status(); st.ActiveCursor != 51 {
		t.Fatalf("delta must advance cursor to 51, got %d", st.ActiveCursor)
	}
	// 上游 gap 超限时的回退：改发权威全量，序号比游标小。
	if err := state.applyActiveItem(shellTestAuthoritative(10, nil, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
		t.Fatalf("apply fallback: %v", err)
	}
	if st := state.status(); st.ActiveCursor != 10 {
		t.Fatalf("authoritative snapshot must reset cursor to 10, got %d", st.ActiveCursor)
	}
}

// 规则 2：游标只由活跃面推进——归档快照/归档增量/元数据帧/标记都不碰它。
func TestShellState_CursorOnlyAdvancedByActiveFace(t *testing.T) {
	state := newT3ShellState()
	if err := state.applyActiveItem(shellTestAuthoritative(7, nil, nil)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// 归档全量快照（INT-173 起按需快照是唯一来源，无增量流）。
	if applied, applyErr := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 40, Threads: shellTestThreads(shellTestThread("th-a", "归档"), shellTestThread("th-b", "归档二"))}, state.archiveGenerationNow(), nil); !applied || applyErr != nil {
		t.Fatalf("apply archive snapshot must be trusted")
	}
	// 元数据帧与 synchronized 标记。
	if err := state.applyActiveItem(shellTestEnrichment(42, nil)); err != nil {
		t.Fatalf("apply enrichment: %v", err)
	}
	if err := state.applyActiveItem(t3ShellStreamItem{Kind: "synchronized"}); err != nil {
		t.Fatalf("apply synchronized: %v", err)
	}
	if st := state.status(); st.ActiveCursor != 7 {
		t.Fatalf("non-active-face frames must not move cursor, cursor = %d", st.ActiveCursor)
	}
	// 活跃增量才推进。
	seq := 8
	if err := state.applyActiveItem(t3ShellStreamItem{Kind: "thread.updated", Sequence: &seq, Location: "active", Thread: shellTestThread("th-2", "二")}); err != nil {
		t.Fatalf("apply active delta: %v", err)
	}
	if st := state.status(); st.ActiveCursor != 8 {
		t.Fatalf("active delta must advance cursor to 8, got %d", st.ActiveCursor)
	}
}

// 规则 3：两面独立——thread.removed{location:"active"} 只删活跃面，不删归档面。
func TestShellState_FacesIndependent(t *testing.T) {
	state := newT3ShellState()
	if err := state.applyActiveItem(shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "活跃")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied, applyErr := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2, Threads: shellTestThreads(shellTestThread("th-1", "归档残留"), shellTestThread("th-a", "归档"))}, state.archiveGenerationNow(), nil); !applied || applyErr != nil {
		t.Fatalf("apply archive must be trusted")
	}
	// 上游归档动作在活跃流上表现为 active removal（ShellStream.ts:292-320）。
	seq := 3
	if err := state.applyActiveItem(t3ShellStreamItem{Kind: "thread.removed", Sequence: &seq, Location: "active", ThreadID: "th-1"}); err != nil {
		t.Fatalf("apply removal: %v", err)
	}
	snaps := state.snapshots()
	if len(snaps.Active.Threads) != 0 {
		t.Fatalf("active removal must empty active face, got %+v", snaps.Active.Threads)
	}
	if len(snaps.Archived.Threads) != 2 {
		t.Fatalf("active removal must not touch archived face, got %+v", snaps.Archived.Threads)
	}
}

// 规则 4：合并视图 active 优先——归档面跳过活跃面已有的 ID。
func TestShellState_MergedViewActiveWins(t *testing.T) {
	state := newT3ShellState()
	if err := state.applyActiveItem(shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "活跃版")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied, applyErr := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2, Threads: shellTestThreads(shellTestThread("th-1", "unarchive 残留"), shellTestThread("th-a", "真归档"))}, state.archiveGenerationNow(), nil); !applied || applyErr != nil {
		t.Fatalf("apply archive must be trusted")
	}
	snaps := state.snapshots()
	if len(snaps.Active.Threads) != 1 || snaps.Active.Threads[0].Title != "活跃版" {
		t.Fatalf("active view = %+v", snaps.Active.Threads)
	}
	if len(snaps.Archived.Threads) != 1 || snaps.Archived.Threads[0].ID != "th-a" {
		t.Fatalf("archived view must skip IDs held by active face, got %+v", snaps.Archived.Threads)
	}
	// findThread 语义不受影响：活跃优先命中。
	shell, _, ok := snaps.findThread("th-1")
	if !ok || shell.Title != "活跃版" {
		t.Fatalf("findThread = %+v %v", shell, ok)
	}
}

// 规则 5/6：归档快照整体替换与 order 维护（INT-173 起归档没有增量流，
// 快照是唯一数据来源）；归档快照可信标记。
func TestShellState_ArchiveLifecycle(t *testing.T) {
	state := newT3ShellState()
	st := state.status()
	if st.ArchiveReady {
		t.Fatalf("fresh state must not be archiveReady")
	}
	if applied, applyErr := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 2, SnapshotSequence: 9, Projects: []t3ProjectShell{{ID: "p-1", Title: "A"}}, Threads: shellTestThreads(shellTestThread("th-a", "一"), shellTestThread("th-b", "二"))}, state.archiveGenerationNow(), nil); !applied || applyErr != nil {
		t.Fatalf("apply archive snapshot must be trusted")
	}
	if st := state.status(); !st.ArchiveReady {
		t.Fatalf("archive snapshot must set archiveReady")
	}
	// 新一次快照整体替换：值更新、集合变化都按新快照落地。
	if applied, applyErr := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 2, SnapshotSequence: 10, Threads: shellTestThreads(shellTestThread("th-a", "一改"), shellTestThread("th-c", "三"))}, state.archiveGenerationNow(), nil); !applied || applyErr != nil {
		t.Fatalf("re-apply archive snapshot must be trusted")
	}
	snaps := state.snapshots()
	if got := orderOfArchive(snaps); len(got) != 2 || got[0] != "th-a" || got[1] != "th-c" {
		t.Fatalf("archive snapshot must replace the face, got %v", got)
	}
	if snaps.Archived.Threads[0].Title != "一改" {
		t.Fatalf("snapshot must replace value, got %+v", snaps.Archived.Threads[0])
	}
}

func orderOfArchive(snaps *t3ShellSnapshots) []string {
	ids := make([]string, 0, len(snaps.Archived.Threads))
	for _, thread := range snaps.Archived.Threads {
		ids = append(ids, thread.ID)
	}
	return ids
}

// G1（INT-173 修订）：归档快照读取失败只记 ArchiveErr，不推翻已有快照——
// 归档面没有增量通道，失败时保留的旧快照比「清空并谎报归档列表为空」
// 更不坏；ArchiveErr 让调用方决定能不能把「归档面没有」当结论。未就绪
// 状态下失败同样清表（没有快照可留时半张表只会误导）。随后快照成功到达
// 即恢复可信。
func TestShellState_ArchiveFetchFailedKeepsSnapshot(t *testing.T) {
	state := newT3ShellState()
	if err := state.applyActiveItem(shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "活跃")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied, applyErr := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}, state.archiveGenerationNow(), nil); !applied || applyErr != nil {
		t.Fatalf("apply archive must be trusted")
	}
	archiveErr := errors.New("归档快照读取失败")
	state.noteArchiveFetchFailed(archiveErr, state.archiveGenerationNow(), nil)

	st := state.status()
	if !st.ArchiveReady {
		t.Fatalf("fetch failure must not revoke a ready archive face")
	}
	if !errors.Is(st.ArchiveErr, archiveErr) {
		t.Fatalf("archiveErr = %v, want wrapped input", st.ArchiveErr)
	}
	snaps := state.snapshots()
	if len(snaps.Archived.Threads) != 1 || snaps.Archived.Threads[0].ID != "th-a" {
		t.Fatalf("fetch failure must keep the last snapshot, got %+v", snaps.Archived)
	}
	if !errors.Is(snaps.ArchiveErr, archiveErr) {
		t.Fatalf("snaps.ArchiveErr = %v", snaps.ArchiveErr)
	}
	if len(snaps.Active.Threads) != 1 || snaps.Active.Threads[0].ID != "th-1" {
		t.Fatalf("active face must survive archive failure, got %+v", snaps.Active.Threads)
	}

	// 恢复：新的归档全量快照清除 ArchiveErr。
	if applied, applyErr := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 3, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}, state.archiveGenerationNow(), nil); !applied || applyErr != nil {
		t.Fatalf("re-apply archive must be trusted")
	}
	if st := state.status(); st.ArchiveErr != nil || !st.ArchiveReady {
		t.Fatalf("fresh archive snapshot must restore trust, got %+v", st)
	}
	if snaps := state.snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("snaps.ArchiveErr = %v, want nil", snaps.ArchiveErr)
	}

	// 未就绪状态下失败：无快照可留，表保持空、ArchiveErr 承载原因。
	fresh := newT3ShellState()
	fresh.noteArchiveFetchFailed(archiveErr, fresh.archiveGenerationNow(), nil)
	if snaps := fresh.snapshots(); len(snaps.Archived.Threads) != 0 || !errors.Is(snaps.ArchiveErr, archiveErr) {
		t.Fatalf("failed fetch on a never-ready face = %+v", snaps)
	}
}

// 写后失效的视图语义（Finding A）：noteArchiveInvalidated 撤销 archiveReady
// 并让 snapshots() 报不可信（errT3ShellArchiveStale），归档行保留；拉取途中
// 发生的失效使同代落地的快照只换数据、不封可信窗口（世代护栏）；落地后
// 再用当前世代应用新快照才恢复可信。
func TestShellState_ArchiveInvalidationGenerationGuard(t *testing.T) {
	state := newT3ShellState()
	if err := state.applyActiveItem(shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "活跃")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied, applyErr := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}, state.archiveGenerationNow(), nil); !applied || applyErr != nil {
		t.Fatalf("apply archive must be trusted")
	}

	// 拉取发起前取样世代，随后发生写后失效。
	gen := state.archiveGenerationNow()
	state.noteArchiveInvalidated()

	snaps := state.snapshots()
	if !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("invalidated archive must report stale, got %v", snaps.ArchiveErr)
	}
	if len(snaps.Archived.Threads) != 1 || snaps.Archived.Threads[0].ID != "th-a" {
		t.Fatalf("invalidation must keep rows, got %+v", snaps.Archived.Threads)
	}
	if st := state.status(); st.ArchiveReady {
		t.Fatalf("invalidation must revoke archiveReady")
	}

	// 写前快照落地（世代不符）：数据照换但不封窗，且是「陈旧非错误」——
	// applied=false、err=nil，调用方据此与坏负载（err 非空）分派。
	if applied, applyErr := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 3, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}, gen, nil); applied || applyErr != nil {
		t.Fatalf("pre-write snapshot must be stale not error, applied=%v err=%v", applied, applyErr)
	}
	snaps = state.snapshots()
	if snaps.ArchiveErr == nil {
		t.Fatalf("pre-write snapshot must not reseal the window")
	}

	// 世代不符的失败同样不覆盖失效语义（返回 false 且不记 ArchiveErr 覆盖）。
	if state.noteArchiveFetchFailed(errors.New("写前读取失败"), gen, nil) {
		t.Fatalf("pre-write failure must not be applied")
	}
	if !errors.Is(state.snapshots().ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("pre-write failure must not overwrite stale semantics, got %v", state.snapshots().ArchiveErr)
	}

	// 写后快照（当前世代）落地才恢复可信。
	if applied, applyErr := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 4, Threads: shellTestThreads(shellTestThread("th-a", "归档"), shellTestThread("th-b", "写后"))}, state.archiveGenerationNow(), nil); !applied || applyErr != nil {
		t.Fatalf("post-write snapshot must be trusted, applied=%v err=%v", applied, applyErr)
	}
	if snaps := state.snapshots(); snaps.ArchiveErr != nil || len(snaps.Archived.Threads) != 2 {
		t.Fatalf("post-write snapshot must restore trust, err=%v threads=%+v", snaps.ArchiveErr, snaps.Archived.Threads)
	}
}

// 坏负载与世代不符是两种不同的「未可信」（复审遗留：不得把两种 false
// 合并）：reset 拒收（如线程缺 ID）返回真实错误，世代不符返回陈旧而非
// 错误。坏负载不得触发 commit——它的簿记由调用方的失败路径收尾；世代
// 不符 commit(false)、世代相符 commit(true)。
func TestShellState_ArchiveBadPayloadDistinctFromStale(t *testing.T) {
	state := newT3ShellState()
	var commits []bool
	commit := func(trusted bool) { commits = append(commits, trusted) }

	// 坏负载：缺 ID 线程拒收，err 点名原因，不触发 commit。
	bad := t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1,
		Threads: []t3ThreadShell{*shellTestThread("th-a", "一"), *shellTestThread("", "无 ID")}}
	if applied, applyErr := state.applyArchiveSnapshot(bad, state.archiveGenerationNow(), commit); applied || applyErr == nil {
		t.Fatalf("bad payload must return an error, applied=%v err=%v", applied, applyErr)
	} else if !strings.Contains(applyErr.Error(), "缺少 ID") {
		t.Fatalf("bad payload error must name the cause, got %v", applyErr)
	}
	if len(commits) != 0 {
		t.Fatalf("bad payload must not commit; the failure path owns its bookkeeping, got %v", commits)
	}

	// 世代不符：写前快照是陈旧而非错误——applied=false、err=nil、commit(false)。
	gen := state.archiveGenerationNow()
	state.noteArchiveInvalidated()
	good := t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}
	if applied, applyErr := state.applyArchiveSnapshot(good, gen, commit); applied || applyErr != nil {
		t.Fatalf("pre-write snapshot must be stale not error, applied=%v err=%v", applied, applyErr)
	}
	if len(commits) != 1 || commits[0] {
		t.Fatalf("pre-write apply must commit(false), got %v", commits)
	}

	// 世代相符：可信落地 commit(true)。
	if applied, applyErr := state.applyArchiveSnapshot(good, state.archiveGenerationNow(), commit); !applied || applyErr != nil {
		t.Fatalf("current-generation apply must be trusted, applied=%v err=%v", applied, applyErr)
	}
	if len(commits) != 2 || !commits[1] {
		t.Fatalf("trusted apply must commit(true), got %v", commits)
	}
}

// 空权威快照也是权威：整体替换为空 + 置 ready/synced。
func TestShellState_EmptyAuthoritativeSnapshot(t *testing.T) {
	state := newT3ShellState()
	if err := state.applyActiveItem(shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := state.applyActiveItem(shellTestAuthoritative(2, nil, nil)); err != nil {
		t.Fatalf("apply empty: %v", err)
	}
	st := state.status()
	if !st.ActiveReady || !st.ActiveSynced || st.ActiveCursor != 2 {
		t.Fatalf("status = %+v", st)
	}
	if snaps := state.snapshots(); len(snaps.Active.Threads) != 0 || len(snaps.Active.Projects) != 0 {
		t.Fatalf("empty authoritative snapshot must clear the faces, got %+v", snaps.Active)
	}
}

// 规则 8：坏帧 / 未知 kind / 违例 location 一律报错。
func TestShellState_BadFramesError(t *testing.T) {
	state := newT3ShellState()
	seq := 1
	cases := []struct {
		name string
		item t3ShellStreamItem
	}{
		{"未知 kind", t3ShellStreamItem{Kind: "mystery"}},
		{"snapshot 无 snapshot 字段", t3ShellStreamItem{Kind: "snapshot"}},
		{"thread.updated 缺 thread", t3ShellStreamItem{Kind: "thread.updated", Sequence: &seq, Location: "active"}},
		{"thread.updated 缺 sequence", t3ShellStreamItem{Kind: "thread.updated", Location: "active", Thread: shellTestThread("th-1", "一")}},
		{"thread.updated 空 ID", t3ShellStreamItem{Kind: "thread.updated", Sequence: &seq, Location: "active", Thread: shellTestThread("", "一")}},
		{"thread.removed 缺 threadId", t3ShellStreamItem{Kind: "thread.removed", Sequence: &seq, Location: "active"}},
		{"project.updated 缺 project", t3ShellStreamItem{Kind: "project.updated", Sequence: &seq}},
		{"project.removed 缺 projectId", t3ShellStreamItem{Kind: "project.removed", Sequence: &seq}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := state.applyActiveItem(tc.item); err == nil {
				t.Fatalf("expected error for %+v", tc.item)
			}
		})
	}

	// 归档面（INT-173：快照整体替换）：缺 ID 的线程行算坏数据，整条快照
	// 被拒——不得静默丢弃一半再宣称就绪；拒收必须以 err 点名原因（坏负
	// 载），让调用方记失败冷却，而不是与世代不符（陈旧非错误）合并成同
	// 一种 false。
	archive := newT3ShellState()
	bad := t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1,
		Threads: []t3ThreadShell{*shellTestThread("th-a", "一"), *shellTestThread("", "无 ID")}}
	applied, applyErr := archive.applyArchiveSnapshot(bad, archive.archiveGenerationNow(), nil)
	if applied || applyErr == nil {
		t.Fatalf("missing-ID thread must reject the snapshot as a bad payload, applied=%v err=%v", applied, applyErr)
	}
	if !strings.Contains(applyErr.Error(), "缺少 ID") {
		t.Fatalf("bad payload error must name the cause, got %v", applyErr)
	}
	if st := archive.status(); st.ArchiveReady {
		t.Fatalf("rejected snapshot must not mark archive ready")
	}
}

// 规则 1/规则 3 的补充：归档面还没拿到全量快照时，ArchiveErr 必须非空——
// 「没出错」不等于「已加载完」，两条连接进度独立（活跃面常先就绪）。
func TestShellState_ArchiveNotReadyIsUntrusted(t *testing.T) {
	state := newT3ShellState()
	if err := state.applyActiveItem(shellTestAuthoritative(7, nil, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if snaps := state.snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveNotReady) {
		t.Fatalf("archiveErr = %v, want not-ready", snaps.ArchiveErr)
	}

	if applied, applyErr := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1}, state.archiveGenerationNow(), nil); !applied || applyErr != nil {
		t.Fatalf("apply archive must be trusted")
	}
	if snaps := state.snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("archiveErr = %v, want nil after snapshot", snaps.ArchiveErr)
	}
}

// 活跃流上的 location:"archive" 不是坏帧：上游 reducer 对它的处理是「不进
// 活跃面」（shellReducer.ts:169-190），归档面由归档订阅维护。
func TestShellState_ArchiveLocationLeavesActiveFace(t *testing.T) {
	state := newT3ShellState()
	if err := state.applyActiveItem(shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	seq := 2
	if err := state.applyActiveItem(t3ShellStreamItem{Kind: "thread.updated", Sequence: &seq, Location: "archive", Thread: shellTestThread("th-1", "一")}); err != nil {
		t.Fatalf("archive-located update must not be a bad frame: %v", err)
	}
	if snaps := state.snapshots(); len(snaps.Active.Threads) != 0 {
		t.Fatalf("archive-located update must leave the active face: %+v", snaps.Active.Threads)
	}
}

// 规则 6：order 维护——upsert 换值不换位、无重复；活跃面同样适用。
func TestShellState_ActiveOrderMaintenance(t *testing.T) {
	state := newT3ShellState()
	if err := state.applyActiveItem(shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "一"), shellTestThread("th-2", "二")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	seq := 2
	if err := state.applyActiveItem(t3ShellStreamItem{Kind: "thread.updated", Sequence: &seq, Location: "active", Thread: shellTestThread("th-1", "一改")}); err != nil {
		t.Fatalf("apply update: %v", err)
	}
	snaps := state.snapshots()
	if len(snaps.Active.Threads) != 2 || snaps.Active.Threads[0].ID != "th-1" || snaps.Active.Threads[1].ID != "th-2" {
		t.Fatalf("upsert must keep order without duplicates: %+v", snaps.Active.Threads)
	}
	if snaps.Active.Threads[0].Title != "一改" {
		t.Fatalf("upsert must replace value: %+v", snaps.Active.Threads[0])
	}
	seq = 3
	if err := state.applyActiveItem(t3ShellStreamItem{Kind: "thread.removed", Sequence: &seq, Location: "active", ThreadID: "th-1"}); err != nil {
		t.Fatalf("apply removal: %v", err)
	}
	seq = 4
	if err := state.applyActiveItem(t3ShellStreamItem{Kind: "thread.updated", Sequence: &seq, Location: "active", Thread: shellTestThread("th-1", "一回归")}); err != nil {
		t.Fatalf("apply re-add: %v", err)
	}
	snaps = state.snapshots()
	if snaps.Active.Threads[1].ID != "th-1" {
		t.Fatalf("re-added thread must append at end: %+v", snaps.Active.Threads)
	}
}

// 规则 7：发布的快照是拷贝，后续增量不得原地改动已发布对象。
func TestShellState_SnapshotsAreStableCopies(t *testing.T) {
	state := newT3ShellState()
	if err := state.applyActiveItem(shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	before := state.snapshots()
	seq := 2
	if err := state.applyActiveItem(t3ShellStreamItem{Kind: "thread.updated", Sequence: &seq, Location: "active", Thread: shellTestThread("th-1", "一改")}); err != nil {
		t.Fatalf("apply delta: %v", err)
	}
	after := state.snapshots()
	if before.Active.Threads[0].Title != "一" {
		t.Fatalf("published snapshot must not be mutated in place: %+v", before.Active.Threads[0])
	}
	if after.Active.Threads[0].Title != "一改" {
		t.Fatalf("new snapshot must reflect delta: %+v", after.Active.Threads[0])
	}
	if &before.Active.Threads[0] == &after.Active.Threads[0] {
		t.Fatalf("snapshots must be fresh copies")
	}
}

// 追平标记：markActiveSynced 由 hub 控制；synchronized 流项置位；两者都不碰数据。
func TestShellState_SyncedFlagControl(t *testing.T) {
	state := newT3ShellState()
	if err := state.applyActiveItem(shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// resume 开始：hub 清标记。
	state.markActiveSynced(false)
	if st := state.status(); st.ActiveSynced || !st.ActiveReady {
		t.Fatalf("cleared synced must keep ready, got %+v", st)
	}
	// 回放期间收到 synchronized：置位，游标不动。
	if err := state.applyActiveItem(t3ShellStreamItem{Kind: "synchronized"}); err != nil {
		t.Fatalf("apply synchronized: %v", err)
	}
	if st := state.status(); !st.ActiveSynced || st.ActiveCursor != 1 {
		t.Fatalf("status = %+v", st)
	}
	if snaps := state.snapshots(); len(snaps.Active.Threads) != 1 {
		t.Fatalf("sync flag changes must not touch data: %+v", snaps.Active.Threads)
	}
}

// JSON 解码精度：resolvedRepositoryIdentityRoots 字段必须能区分「存在」与
// 「不存在」（存在但为 null/空数组都算元数据帧）。
func TestShellState_EnrichmentDecodeFromWire(t *testing.T) {
	state := newT3ShellState()
	// 权威帧先建立数据。
	if err := state.applyActiveItem(shellTestAuthoritative(7, nil, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// enrichment 字段存在（字符串数组非空，形如上游真实帧）。
	enriched := []byte(`{"kind":"snapshot","snapshot":{"schemaVersion":1,"snapshotSequence":99,"projects":[{"id":"p-9","title":"X"}],"threads":[]},"resolvedRepositoryIdentityRoots":["/repo"]}`)
	var enrichedItem t3ShellStreamItem
	if err := json.Unmarshal(enriched, &enrichedItem); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := state.applyActiveItem(enrichedItem); err != nil {
		t.Fatalf("apply enrichment: %v", err)
	}
	if st := state.status(); st.ActiveCursor != 7 {
		t.Fatalf("enrichment must not move cursor, cursor = %d", st.ActiveCursor)
	}
	// 字段不存在 → 权威：即便与上游惯例不符的空 projects/threads 也整体替换。
	authoritative := []byte(`{"kind":"snapshot","snapshot":{"schemaVersion":1,"snapshotSequence":8,"projects":[],"threads":[]}}`)
	var authoritativeItem t3ShellStreamItem
	if err := json.Unmarshal(authoritative, &authoritativeItem); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := state.applyActiveItem(authoritativeItem); err != nil {
		t.Fatalf("apply authoritative: %v", err)
	}
	if st := state.status(); st.ActiveCursor != 8 || len(state.snapshots().Active.Threads) != 0 {
		t.Fatalf("authoritative frame must replace state, status = %+v", st)
	}
}

// F7：项目表只有活跃面一份权威来源——归档快照只维护线程，其 Projects 一律
// 忽略：旧名字不得覆盖活跃流的项目更新，也不得复活已删除项目；项目名表
// 只从活跃面组装。
func TestShellState_ArchiveSnapshotIgnoresProjects(t *testing.T) {
	state := newT3ShellState()
	if err := state.applyActiveItem(shellTestAuthoritative(1,
		[]t3ProjectShell{{ID: "p-1", Title: "旧名"}, {ID: "p-2", Title: "将删除"}},
		shellTestThreads(shellTestThread("th-1", "一")),
	)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// 活跃流项目改名。
	seq := 2
	if err := state.applyActiveItem(t3ShellStreamItem{Kind: "project.updated", Sequence: &seq, Project: &t3ProjectShell{ID: "p-1", Title: "新名"}}); err != nil {
		t.Fatalf("apply project update: %v", err)
	}
	// 活跃流项目删除。
	seq = 3
	if err := state.applyActiveItem(t3ShellStreamItem{Kind: "project.removed", Sequence: &seq, ProjectID: "p-2"}); err != nil {
		t.Fatalf("apply project removal: %v", err)
	}
	// 归档快照带着改名前/已删除的项目表到达：不得覆盖、不得复活。
	if applied, applyErr := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{
		SchemaVersion: 1, SnapshotSequence: 2,
		Projects: []t3ProjectShell{{ID: "p-1", Title: "旧名"}, {ID: "p-2", Title: "已删除"}},
		Threads:  shellTestThreads(shellTestThread("th-a", "归档")),
	}, state.archiveGenerationNow(), nil); !applied || applyErr != nil {
		t.Fatalf("apply archive must be trusted, applied=%v err=%v", applied, applyErr)
	}
	snaps := state.snapshots()
	if len(snaps.Active.Projects) != 1 || snaps.Active.Projects[0].ID != "p-1" || snaps.Active.Projects[0].Title != "新名" {
		t.Fatalf("archive snapshot must not overwrite active projects: %+v", snaps.Active.Projects)
	}
	if len(snaps.Archived.Projects) != 0 {
		t.Fatalf("archive face must not maintain projects: %+v", snaps.Archived.Projects)
	}
	// 项目名只来自活跃面：已删除项目取不到名字（与「项目已删除」一致）。
	names := snaps.projectNames()
	if names["p-1"] != "新名" {
		t.Fatalf("projectNames = %+v", names)
	}
	if _, ok := names["p-2"]; ok {
		t.Fatalf("deleted project must not resurrect via archive snapshot: %+v", names)
	}
}

// 失效后留行的面重拉失败保留旧行（Finding R3）：archiveReady 为假不足以
// 决定清表——archiveStale（失效/过期后留行）时当前世代失败只记真实失败
// 原因，归档行照旧；从未有过成功快照的冷启动失败才可清表。
func TestShellState_ArchiveFetchFailedAfterInvalidationKeepsRows(t *testing.T) {
	state := newT3ShellState()
	if err := state.applyActiveItem(shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "活跃")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied, applyErr := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}, state.archiveGenerationNow(), nil); !applied || applyErr != nil {
		t.Fatalf("apply archive must be trusted")
	}

	// 写后失效：留行但不可信。
	state.noteArchiveInvalidated()

	// 当前世代重拉失败：旧行必须保留（清表会把「不可信但留行」谎报为
	// 「确定为空」），ArchiveErr 是真实失败而非 stale。
	fetchErr := errors.New("归档快照读取失败")
	if !state.noteArchiveFetchFailed(fetchErr, state.archiveGenerationNow(), nil) {
		t.Fatalf("current-generation failure must be applied")
	}
	snaps := state.snapshots()
	if len(snaps.Archived.Threads) != 1 || snaps.Archived.Threads[0].ID != "th-a" {
		t.Fatalf("failed refetch after invalidation must keep rows, got %+v", snaps.Archived.Threads)
	}
	if !errors.Is(snaps.ArchiveErr, fetchErr) {
		t.Fatalf("ArchiveErr = %v, want the real failure", snaps.ArchiveErr)
	}

	// TTL 过期标记（noteArchiveExpired）后失败同样留行：先恢复可信再过期。
	if applied, applyErr := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 3, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}, state.archiveGenerationNow(), nil); !applied || applyErr != nil {
		t.Fatalf("re-apply archive must be trusted")
	}
	state.mu.Lock()
	state.noteArchiveExpiredLocked()
	state.mu.Unlock()
	if !state.noteArchiveFetchFailed(fetchErr, state.archiveGenerationNow(), nil) {
		t.Fatalf("current-generation failure must be applied")
	}
	snaps = state.snapshots()
	if len(snaps.Archived.Threads) != 1 || !errors.Is(snaps.ArchiveErr, fetchErr) {
		t.Fatalf("failed refetch after expiry must keep rows and report the real failure, got %+v", snaps)
	}

	// 冷启动（从未成功）失败仍可清表：没有快照可留时半张表只会误导。
	fresh := newT3ShellState()
	if !fresh.noteArchiveFetchFailed(fetchErr, fresh.archiveGenerationNow(), nil) {
		t.Fatalf("cold-start failure must be applied")
	}
	if snaps := fresh.snapshots(); len(snaps.Archived.Threads) != 0 || !errors.Is(snaps.ArchiveErr, fetchErr) {
		t.Fatalf("cold-start failure must clear the face, got %+v", snaps)
	}
}

// 世代校验与簿记提交原子化（Finding R1 确定性交错）：commit 必须在
// apply 的同一临界区内执行——TryLock 失败证明 s.mu 仍被 apply 持有，
// 「世代校验与簿记提交之间」没有可插入失效的缝隙（旧实现在两步之间放
// 锁，InvalidateArchive 可在此推进世代后仍被 validUntil 封窗）。失效只
// 能先于落地（世代不符 → commit(false)，不封窗、留 needsRefetch）或在
// commit 之后（由 InvalidateArchive 清零簿记）。
func TestShellState_ApplyCommitIsAtomicWithGeneration(t *testing.T) {
	entry := &t3ShellEntry{state: newT3ShellState(), archTTL: time.Minute}
	gen := entry.state.archiveGenerationNow()

	commitInsideLock := false
	var gotTrusted bool
	trusted, applyErr := entry.state.applyArchiveSnapshot(
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		gen, func(trusted bool) {
			gotTrusted = trusted
			if entry.state.mu.TryLock() {
				entry.state.mu.Unlock()
			} else {
				commitInsideLock = true
			}
			entry.commitArchiveApply(trusted)
		})
	if !trusted || applyErr != nil || !gotTrusted {
		t.Fatalf("apply must be trusted, err=%v gotTrusted=%v", applyErr, gotTrusted)
	}
	if !commitInsideLock {
		t.Fatalf("commit must run inside the s.mu critical section; a gap would let invalidation seal-stale")
	}
	entry.arch.mu.Lock()
	sealed := !entry.arch.validUntil.IsZero() && !entry.arch.needsRefetch
	entry.arch.mu.Unlock()
	if !sealed {
		t.Fatalf("trusted apply must seal validUntil")
	}

	// 写前结果（世代不符）：commit(false) 只留 needsRefetch，不封窗。
	entry.state.noteArchiveInvalidated()
	entry.arch.mu.Lock()
	entry.arch.validUntil = time.Time{}
	entry.arch.retryAfter = time.Time{}
	entry.arch.needsRefetch = false
	entry.arch.mu.Unlock()
	if applied, applyErr := entry.state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2}, gen, entry.commitArchiveApply); applied || applyErr != nil {
		t.Fatalf("pre-write apply must be stale not error, applied=%v err=%v", applied, applyErr)
	}
	entry.arch.mu.Lock()
	if !entry.arch.validUntil.IsZero() || !entry.arch.retryAfter.IsZero() {
		t.Fatalf("pre-write apply must not seal bookkeeping, validUntil=%v retryAfter=%v", entry.arch.validUntil, entry.arch.retryAfter)
	}
	if !entry.arch.needsRefetch {
		t.Fatalf("pre-write apply must leave needsRefetch")
	}
	entry.arch.mu.Unlock()

	// 失败路径对称：commit 同样在同一临界区内执行。
	entry2 := &t3ShellEntry{state: newT3ShellState(), archTTL: time.Minute}
	commitInsideLock = false
	var gotApplied bool
	entry2.state.noteArchiveFetchFailed(errors.New("拉取失败"), entry2.state.archiveGenerationNow(), func(applied bool) {
		gotApplied = applied
		if entry2.state.mu.TryLock() {
			entry2.state.mu.Unlock()
		} else {
			commitInsideLock = true
		}
		entry2.commitArchiveFailure(applied)
	})
	if !commitInsideLock || !gotApplied {
		t.Fatalf("failure commit must run inside the critical section and be applied")
	}
	entry2.arch.mu.Lock()
	if entry2.arch.retryAfter.IsZero() {
		t.Fatalf("applied failure must record retryAfter")
	}
	entry2.arch.mu.Unlock()
}

// 撤可信与在途早退的原子性（R2 / R17 P2-1 / R20 P2 合并后的不变量）：
// 「重拉在途超过宽限 → 撤可信」必须与「本次准入被合并（return false）」
// 在同一临界区提交——读者不可能拿到「kick 被拒但视图仍报可信」的组合，
// 否则 wait 会拿过期快照判 thread_gone。判据锚在本次拉取的发起时刻
// （refetchStartedAt），不是 validUntil 的年龄。
//
// 确定性复现分两步：
//  1. 锁序证明：先由本 goroutine 持住 s.mu，再启动 admission——它必须阻塞
//     在 s.mu 上（准入序列第一步取 s.mu），提交前读者看不到任何变化。
//  2. 放开 s.mu 后 admission 完成撤可信；此后读者 B 走早退路径，读到的
//     视图必须已不可信（留行、报 stale）。
func TestShellState_AdmitArchiveRefreshIsAtomicWithRevoke(t *testing.T) {
	entry := &t3ShellEntry{state: newT3ShellState(), archTTL: time.Minute, archiveStaleGrace: 50 * time.Millisecond}
	gen := entry.state.archiveGenerationNow()
	if applied, applyErr := entry.state.applyArchiveSnapshot(
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		gen, entry.commitArchiveApply); !applied || applyErr != nil {
		t.Fatalf("seed trusted archive: applied=%v err=%v", applied, applyErr)
	}
	if snaps := entry.state.snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("seeded archive must be trusted, got %v", snaps.ArchiveErr)
	}

	// 过期且重拉在途超过宽限：下一次准入必须在早退的同时撤可信。
	entry.arch.mu.Lock()
	entry.arch.validUntil = time.Now().Add(-time.Minute)
	entry.arch.inFlight = true
	entry.arch.refetchStartedAt = time.Now().Add(-time.Minute)
	entry.arch.mu.Unlock()

	// 1) 持住 s.mu：admission 必须阻塞在这里，提交前视图仍可信。
	entry.state.mu.Lock()
	admitDone := make(chan struct{})
	var admitted, revoked bool
	go func() {
		defer close(admitDone)
		admitted, revoked = entry.admitArchiveRefresh()
	}()
	select {
	case <-admitDone:
		t.Fatalf("admission must block on s.mu (lock order s.mu → arch.mu)")
	case <-time.After(20 * time.Millisecond):
	}
	if !entry.state.archiveReady || entry.state.archiveErr != nil {
		t.Fatalf("pre-commit view must still be trusted, ready=%v err=%v", entry.state.archiveReady, entry.state.archiveErr)
	}
	entry.state.mu.Unlock()

	// 2) admission 完成：早退 + 撤可信在同一临界区提交。
	<-admitDone
	if admitted || !revoked {
		t.Fatalf("in-flight beyond grace must be merged onto the running fetch and revoke trust, admitted=%v revoked=%v", admitted, revoked)
	}
	snaps := entry.state.snapshots()
	if snaps.ArchiveErr == nil || !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("revocation must be applied in the same critical section, got %v", snaps.ArchiveErr)
	}
	if len(snaps.Archived.Threads) != 1 || snaps.Archived.Threads[0].ID != "th-a" {
		t.Fatalf("revoked face must keep the last rows, got %+v", snaps.Archived.Threads)
	}
	if st := entry.state.status(); st.ArchiveReady {
		t.Fatalf("archiveReady must be revoked together with the in-flight early return")
	}
	if admittedAgain, _ := entry.admitArchiveRefresh(); admittedAgain {
		t.Fatalf("reader B must merge onto the in-flight fetch, not admit a second one")
	}
}

// 旧面 reconnect 世代护栏（R4 P2，Preserve newer legacy reconnect
// requests）：新连接已发出权威快照、但快照尚未 apply 的窗口里又记了一次
// reconnect 请求（写后失效）时，该快照是「变更前」的——落地不
// 得清掉这份更新的备忘（旧实现无条件清，已排队的 reconnect 信号被无视，
// wait 拿写前数据判 thread_gone 直到下一个事件）。世代规则：备忘只能被
// 「出发邮戳（subscribe 发出时读到的请求世代）≥ 备忘世代」的连接的快照
// 清掉；被变更前快照挡下的备忘由读循环的信号分支消费——断开本连接、监
// 督循环重连后新连接的 subscribe 必在请求之后发出，其快照必为写后。
func TestShellState_LegacyReconnectGenerationGuard(t *testing.T) {
	state := newT3ShellState()

	// 连接 A 建立（邮戳=当时已发出的请求数 0），第一次失效记请求 gen1。
	connA := state.archiveReconnectTarget()
	if connA != 0 {
		t.Fatalf("fresh state must have no issued reconnect requests, got %d", connA)
	}
	if gen := state.noteArchiveReconnectRequested(); gen != 1 {
		t.Fatalf("first reconnect request generation = %d, want 1", gen)
	}
	// gen1 的信号被读循环消费：连接 A 按备忘断开，监督循环重连出连接 B。
	if !state.consumeArchiveReconnect() {
		t.Fatalf("connA read loop must consume the pending request")
	}
	connB := state.archiveReconnectTarget()
	if connB != 1 {
		t.Fatalf("connB postmark = %d, want 1", connB)
	}
	// 连接 B 订阅后即收到权威快照——但快照尚未 apply 时第二次失效记入
	// gen2（write-in-flight）：这份快照是在 gen2 请求之前发出的。
	if gen := state.noteArchiveReconnectRequested(); gen != 2 {
		t.Fatalf("second reconnect request generation = %d, want 2", gen)
	}
	// 变更前快照落地（连接 B 邮戳 1 < 备忘 gen2）：换数据、恢复可信，但
	// 不得吞掉 gen2 的备忘——它是写前状态，还欠一次写后重连。
	if err := state.applyArchiveItem(t3ArchiveStreamItem{
		Kind:     "snapshot",
		Snapshot: &t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 9, Threads: shellTestThreads(shellTestThread("th-stale", "变更前"))},
	}, connB); err != nil {
		t.Fatalf("apply stale-window snapshot: %v", err)
	}
	if state.archiveReconnectPendingGen != 2 {
		t.Fatalf("pre-change snapshot must NOT clear the newer pending request, pending = %d", state.archiveReconnectPendingGen)
	}
	// 兑现路径：gen2 的信号送达连接 B 的读循环——备忘在，消费并断开本
	// 连接（不做邮戳门控：备忘比本连接邮戳新正是「须断开」的证据）。
	if !state.consumeArchiveReconnect() {
		t.Fatalf("connB read loop must consume the request recorded during its snapshot window")
	}
	// 监督循环重连出连接 C：subscribe 在 gen2 之后发出（邮戳=2），其快照
	// 必为写后状态——落地清备忘并交付写后全量。
	connC := state.archiveReconnectTarget()
	if connC != 2 {
		t.Fatalf("connC postmark = %d, want 2", connC)
	}
	if err := state.applyArchiveItem(t3ArchiveStreamItem{
		Kind:     "snapshot",
		Snapshot: &t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 10, Threads: shellTestThreads(shellTestThread("th-fresh", "写后"))},
	}, connC); err != nil {
		t.Fatalf("apply post-change snapshot: %v", err)
	}
	if state.archiveReconnectPendingGen != 0 {
		t.Fatalf("post-change snapshot must clear the fulfilled memo, pending = %d", state.archiveReconnectPendingGen)
	}
	snaps := state.snapshots()
	if len(snaps.Archived.Threads) != 1 || snaps.Archived.Threads[0].ID != "th-fresh" {
		t.Fatalf("post-change snapshot must carry the write-behind rows, got %+v", snaps.Archived.Threads)
	}
	if snaps.ArchiveErr != nil {
		t.Fatalf("post-change snapshot must restore trust, ArchiveErr = %v", snaps.ArchiveErr)
	}
}

// reconnect 世代规则的边角：备忘被清后晚到/重复的信号为假（沿用旧语
// 义）；连接终止作废备忘但 issued 不回卷，新请求世代继续推进。
func TestShellState_LegacyReconnectMemoLifecycle(t *testing.T) {
	state := newT3ShellState()

	connA := state.archiveReconnectTarget()
	if gen := state.noteArchiveReconnectRequested(); gen != 1 {
		t.Fatalf("request generation = %d, want 1", gen)
	}
	// 同世代快照清备忘：连接 B 邮戳=1，它拿到的快照「确在请求之后」。
	connB := state.archiveReconnectTarget()
	if connB != 1 {
		t.Fatalf("connB postmark = %d, want 1", connB)
	}
	if err := state.applyArchiveItem(t3ArchiveStreamItem{
		Kind:     "snapshot",
		Snapshot: &t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 3, Threads: shellTestThreads()},
	}, connB); err != nil {
		t.Fatalf("apply snapshot: %v", err)
	}
	if state.archiveReconnectPendingGen != 0 {
		t.Fatalf("snapshot from a connection that postdates the request must clear the memo, pending = %d", state.archiveReconnectPendingGen)
	}
	// 备忘已清时晚到/重复的信号为假（忽略即可）。
	if state.consumeArchiveReconnect() {
		t.Fatalf("cleared memo must not consume")
	}
	// 连接终止作废备忘但 issued 不回卷：再记请求世代继续推进。
	if gen := state.noteArchiveReconnectRequested(); gen != 2 {
		t.Fatalf("re-request must issue a fresh generation, got %d", gen)
	}
	state.markArchiveFailed(errors.New("conn dead"))
	if state.archiveReconnectPendingGen != 0 {
		t.Fatalf("connection end must void the memo, pending = %d", state.archiveReconnectPendingGen)
	}
	if gen := state.noteArchiveReconnectRequested(); gen != 3 {
		t.Fatalf("issued counter must never rewind, generation = %d, want 3", gen)
	}
	_ = connA
}

// 旧面写前快照不得封可信（审修 R8 P2，deterministic）：订阅连接「邮戳 <
// 备忘世代」的快照是先于最近一次写后失效发出的变更前状态——落地只能
// 换数据，archiveReady 保持 false、ArchiveErr 报「已失效重拉中」，备忘
// 不清。修复前 applyArchiveSnapshotLocked 无条件置 Ready+清 Err 才判邮
// 戳，从消费备忘到连接被拆开的窗口里 View/Peek 会拿写前归档集判
// thread_gone、谎报列表完整。邮戳 ≥ 备忘世代的写后快照才恢复可信并清
// 备忘。
func TestShellState_LegacyPreChangeSnapshotStaysUntrusted(t *testing.T) {
	state := newT3ShellState()

	// conn1（邮戳=0）先落地一张权威快照就绪。
	if err := state.applyArchiveItem(t3ArchiveStreamItem{
		Kind:     "snapshot",
		Snapshot: &t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
	}, state.archiveReconnectTarget()); err != nil {
		t.Fatalf("apply initial snapshot: %v", err)
	}

	// 写后失效记备忘（gen1）→ conn1 被拆、conn2 重连（邮戳=1）。
	state.noteArchiveReconnectRequested()
	state.consumeArchiveReconnect()
	conn2 := state.archiveReconnectTarget() // =1

	// conn2 的权威快照落地前先记第二次失效（gen2）：conn2 之后送达的任
	// 何快照都是「先于 gen2 发出」的写前状态。
	if gen := state.noteArchiveReconnectRequested(); gen != 2 {
		t.Fatalf("second reconnect request generation = %d, want 2", gen)
	}

	// 写前快照落地（邮戳1 < 备忘gen2）：换数据、保持不可信、备忘不清。
	if err := state.applyArchiveItem(t3ArchiveStreamItem{
		Kind:     "snapshot",
		Snapshot: &t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 9, Threads: shellTestThreads(shellTestThread("th-stale", "变更前"))},
	}, conn2); err != nil {
		t.Fatalf("apply pre-change snapshot: %v", err)
	}
	if state.archiveReady {
		t.Fatalf("pre-change snapshot must NOT seal trust")
	}
	if state.archiveErr == nil {
		t.Fatalf("pre-change snapshot must keep the stale error")
	}
	if !errors.Is(state.archiveErr, errT3ShellArchiveStale) {
		t.Fatalf("pre-change snapshot must keep stale semantics, got %v", state.archiveErr)
	}
	if state.archiveReconnectPendingGen != 2 {
		t.Fatalf("pre-change snapshot must NOT clear the newer memo, pending = %d", state.archiveReconnectPendingGen)
	}
	snaps := state.snapshots()
	if len(snaps.Archived.Threads) != 1 || snaps.Archived.Threads[0].ID != "th-stale" {
		t.Fatalf("pre-change snapshot must swap the rows, got %+v", snaps.Archived.Threads)
	}
	if snaps.ArchiveErr == nil {
		t.Fatalf("view must report the archive face as untrusted")
	}

	// 兑现：conn3（邮戳=2）的写后快照落地——恢复可信、清备忘。
	state.consumeArchiveReconnect()
	conn3 := state.archiveReconnectTarget() // =2
	if err := state.applyArchiveItem(t3ArchiveStreamItem{
		Kind:     "snapshot",
		Snapshot: &t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 10, Threads: shellTestThreads(shellTestThread("th-a", "归档"), shellTestThread("th-b", "写后"))},
	}, conn3); err != nil {
		t.Fatalf("apply post-change snapshot: %v", err)
	}
	if !state.archiveReady {
		t.Fatalf("post-change snapshot must restore trust")
	}
	if state.archiveReconnectPendingGen != 0 {
		t.Fatalf("post-change snapshot must clear the memo, pending = %d", state.archiveReconnectPendingGen)
	}
	snaps = state.snapshots()
	if snaps.ArchiveErr != nil {
		t.Fatalf("post-change snapshot must clear the error, got %v", snaps.ArchiveErr)
	}
}

// 权威全量快照整体替换时被移除的旧线程要被追踪为「离开活跃面」（R13
// P2-1）：state 在同一临界区内计算移除集，hub 据此失效归档缓存——否则
// 断连重连/强制重同步走全量快照时，被别的客户端归档的线程既不在新活跃
// 快照、也不在旧归档缓存，ArchiveErr 却仍 nil。集合未变与元数据帧恒无
// 移除；被移除的 ID 与 reset 后的活跃面一致。
func TestShellState_AuthoritativeSnapshotRemovalsTracked(t *testing.T) {
	state := newT3ShellState()
	projects := []t3ProjectShell{{ID: "p-1", Title: "Fast Ship"}}
	if err := state.applyActiveItem(shellTestAuthoritative(7, projects, shellTestThreads(shellTestThread("th-1", "一"), shellTestThread("th-2", "二")))); err != nil {
		t.Fatalf("apply authoritative: %v", err)
	}

	// 集合未变的快照：无移除。
	removed, err := state.applyActiveItemTracked(shellTestAuthoritative(8, projects, shellTestThreads(shellTestThread("th-1", "一"), shellTestThread("th-2", "二"))))
	if err != nil || len(removed) != 0 {
		t.Fatalf("identical snapshot must track no removals, got %v (err %v)", removed, err)
	}

	// 元数据帧：无移除（不是权威数据）。
	removed, err = state.applyActiveItemTracked(shellTestEnrichment(9, projects))
	if err != nil || len(removed) != 0 {
		t.Fatalf("enrichment frame must track no removals, got %v (err %v)", removed, err)
	}

	// 全量快照丢掉 th-2（断连窗口里被别的客户端归档）：移除集须含 th-2。
	removed, err = state.applyActiveItemTracked(shellTestAuthoritative(10, projects, shellTestThreads(shellTestThread("th-1", "一"))))
	if err != nil {
		t.Fatalf("apply shrinking snapshot: %v", err)
	}
	if len(removed) != 1 || removed[0] != "th-2" {
		t.Fatalf("shrinking snapshot must track th-2 as removed, got %v", removed)
	}
	snaps := state.snapshots()
	if len(snaps.Active.Threads) != 1 || snaps.Active.Threads[0].ID != "th-1" {
		t.Fatalf("active face must be replaced by the snapshot, got %+v", snaps.Active.Threads)
	}
}

// archiveFingerprintIDs 摘取指纹行序列的 ID 子序列（测试内区分「集合变」
// 与「内容变」用，与发布端 archiveDigestIDs 同一口径）。
func archiveFingerprintIDs(rows []t3ArchiveRowDigest) []string {
	ids := make([]string, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	return ids
}

// 归档面发布指纹必须包含行内容（R13 Note-2）：同集合（ID 序列不变）行
// 内容变化（标题/状态/归档时刻/更新时刻）要让指纹变化——发布端以指纹
// 相等性判「无新话可说」，内容不进指纹的话同集合内容变会被静默，浏览
// 器停在旧行。集合或顺序变化当然也变化；可信度文案独立于行摘要参与指
// 纹（发布端分派 Full/State 用）。
func TestShellState_ArchiveFingerprintIncludesRowContent(t *testing.T) {
	state := newT3ShellState()
	if err := state.applyActiveItem(shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "活跃")))); err != nil {
		t.Fatalf("apply active: %v", err)
	}
	archivedAt := "2026-10-01T00:00:00.000Z"
	row := func(id, title, status string) *t3ThreadShell {
		return &t3ThreadShell{ID: id, ProjectID: "p-1", Title: title, Status: status, ArchivedAt: &archivedAt, UpdatedAt: "2026-10-01T08:00:00.000Z"}
	}
	if applied, applyErr := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 5, Threads: shellTestThreads(row("th-a", "归档", "idle"), row("th-b", "归档二", "idle"))}, state.archiveGenerationNow(), nil); !applied || applyErr != nil {
		t.Fatalf("apply archive snapshot must be trusted")
	}

	rows, errText := state.archiveFaceFingerprint()
	if len(rows) != 2 || rows[0].ID != "th-a" || rows[1].ID != "th-b" {
		t.Fatalf("fingerprint rows = %+v", rows)
	}
	if errText != "" {
		t.Fatalf("trusted face must have empty err text, got %q", errText)
	}

	// 同集合、同内容重拉：指纹不变（例行重拉静默的依据）。
	if applied, applyErr := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 6, Threads: shellTestThreads(row("th-a", "归档", "idle"), row("th-b", "归档二", "idle"))}, state.archiveGenerationNow(), nil); !applied || applyErr != nil {
		t.Fatalf("re-apply must be trusted")
	}
	rows2, errText2 := state.archiveFaceFingerprint()
	if !slices.Equal(rows, rows2) || errText2 != errText {
		t.Fatalf("identical re-pull must keep the fingerprint: %+v vs %+v", rows, rows2)
	}

	// 同集合、行内容变（改名 + 状态翻转）：ID 序列不变、摘要变——发布端
	// 据此发轻量 State 而不是静默。
	if applied, applyErr := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 7, Threads: shellTestThreads(row("th-a", "归档（改名）", "completed"), row("th-b", "归档二", "idle"))}, state.archiveGenerationNow(), nil); !applied || applyErr != nil {
		t.Fatalf("apply renamed snapshot must be trusted")
	}
	rows3, _ := state.archiveFaceFingerprint()
	if slices.Equal(rows, rows3) {
		t.Fatalf("content change with same collection must change the fingerprint")
	}
	ids3 := archiveFingerprintIDs(rows3)
	if !slices.Equal(ids3, archiveFingerprintIDs(rows)) {
		t.Fatalf("content change must keep the ID sequence, got %v vs %v", ids3, archiveFingerprintIDs(rows))
	}

	// 集合变（新增 th-c）：ID 序列变——发布端据此发 Full。
	if applied, applyErr := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 8, Threads: shellTestThreads(row("th-a", "归档（改名）", "completed"), row("th-b", "归档二", "idle"), row("th-c", "归档三", "idle"))}, state.archiveGenerationNow(), nil); !applied || applyErr != nil {
		t.Fatalf("apply grown snapshot must be trusted")
	}
	rows4, _ := state.archiveFaceFingerprint()
	if slices.Equal(archiveFingerprintIDs(rows4), ids3) {
		t.Fatalf("collection growth must change the ID sequence")
	}
}

// 归档行指纹必须包含列表行渲染的关联/运行字段（R14 Note-1）：关联 PR 数
// （thread-row 的 PR 徽标，t3PullRequestCount 同一推导）与子代理父线程
// （rows-compose 按它嵌套行）。外部关联/解关联 PR 通常不推进 updatedAt，
// 指纹不含它的话同集合 PR 数变会被判「无新话可说」不发事件，浏览器停
// 在旧徽标。同集合（ID 序列不变）PR 数变 → 指纹变；父线程关系变同理。
func TestShellState_ArchiveFingerprintIncludesAssociationFields(t *testing.T) {
	state := newT3ShellState()
	if err := state.applyActiveItem(shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-live", "活跃")))); err != nil {
		t.Fatalf("apply active: %v", err)
	}
	archivedAt := "2026-10-01T00:00:00.000Z"
	subagent := "subagent"
	row := func(pullRequests []json.RawMessage, parent *string) *t3ThreadShell {
		t := &t3ThreadShell{ID: "th-a", ProjectID: "p-1", Title: "归档", Status: "idle", ArchivedAt: &archivedAt, UpdatedAt: "2026-10-01T08:00:00.000Z", PullRequests: pullRequests}
		if parent != nil {
			t.Lineage = &t3ThreadLineage{ParentThreadID: *parent, RelationshipToParent: &subagent}
		}
		return t
	}
	onePR := []json.RawMessage{json.RawMessage(`{"host":"github.com","repository":"o/r","number":3}`)}
	twoPRs := append(onePR, json.RawMessage(`{"host":"github.com","repository":"o/r","number":4}`))
	apply := func(thread *t3ThreadShell) {
		t.Helper()
		if applied, applyErr := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 5, Threads: shellTestThreads(thread)}, state.archiveGenerationNow(), nil); !applied || applyErr != nil {
			t.Fatalf("apply archive snapshot must be trusted")
		}
	}

	apply(row(onePR, nil))
	rows, _ := state.archiveFaceFingerprint()
	if len(rows) != 1 || rows[0].PullRequests != 1 {
		t.Fatalf("digest must carry the visible PR count, got %+v", rows)
	}

	// 同集合 PR 数变（外部关联了第二个 PR，updatedAt 不变）：ID 序列不变、
	// 摘要变——发布端据此发轻量 State 而不是静默。
	apply(row(twoPRs, nil))
	rows2, _ := state.archiveFaceFingerprint()
	if slices.Equal(rows, rows2) {
		t.Fatalf("PR count change with same collection must change the fingerprint")
	}
	if !slices.Equal(archiveFingerprintIDs(rows2), archiveFingerprintIDs(rows)) {
		t.Fatalf("PR count change must keep the ID sequence")
	}
	if rows2[0].PullRequests != 2 {
		t.Fatalf("digest must track the new PR count, got %+v", rows2[0])
	}

	// 同集合父线程关系变（子代理挂到新父线程）：摘要变，ID 序列仍不变。
	parent := "th-parent-2"
	apply(row(twoPRs, &parent))
	rows3, _ := state.archiveFaceFingerprint()
	if slices.Equal(rows2, rows3) || rows3[0].ParentThreadID != "th-parent-2" {
		t.Fatalf("parent thread change must change the fingerprint, got %+v", rows3[0])
	}

	// 非子代理行不携带父线程字段（与 normalizeT3ThreadShell 同一判定）。
	apply(row(twoPRs, nil))
	rows4, _ := state.archiveFaceFingerprint()
	if rows4[0].ParentThreadID != "" {
		t.Fatalf("non-subagent row must not carry a parent, got %+v", rows4[0])
	}
}

// 成功落地窗口不得再撤可信（跟进 PR①，Codex :214）：重拉可能已经成功落地
// （archiveReady=true、validUntil 在未来），只是 kick 的后台 defer 还没清
// inFlight/refetchStartedAt。此刻按「在途过久」撤可信会把刚恢复的面重新
// 打回不可信；inFlight 清空后，后续准入看到未来的 validUntil 不再重拉，
// 降级就会持续整个 TTL。
func TestShellState_AdmitDoesNotRevokeAfterSuccessfulLanding(t *testing.T) {
	entry := &t3ShellEntry{state: newT3ShellState(), archTTL: time.Minute, archiveStaleGrace: 50 * time.Millisecond}
	gen := entry.state.archiveGenerationNow()
	if applied, applyErr := entry.state.applyArchiveSnapshot(
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		gen, entry.commitArchiveApply); !applied || applyErr != nil {
		t.Fatalf("seed trusted archive: applied=%v err=%v", applied, applyErr)
	}

	// 模拟「落地已提交、收尾 defer 未执行」的窗口：inFlight 仍置位、
	// refetchStartedAt 是很久以前。
	entry.arch.mu.Lock()
	entry.arch.inFlight = true
	entry.arch.refetchStartedAt = time.Now().Add(-time.Minute)
	entry.arch.mu.Unlock()

	admitted, revoked := entry.admitArchiveRefresh()
	if admitted || revoked {
		t.Fatalf("freshly landed snapshot must not be revoked, admitted=%v revoked=%v", admitted, revoked)
	}
	if snaps := entry.state.snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("successful landing window must stay trusted, got %v", snaps.ArchiveErr)
	}

	// 反例（保持原语义）：缓存确实过期时，在途过久仍要撤可信。
	entry.arch.mu.Lock()
	entry.arch.validUntil = time.Now().Add(-time.Minute)
	entry.arch.mu.Unlock()
	admitted, revoked = entry.admitArchiveRefresh()
	if admitted || !revoked {
		t.Fatalf("expired cache with overlong refetch must revoke trust, admitted=%v revoked=%v", admitted, revoked)
	}
	revokedSnaps := entry.state.snapshots()
	if !errors.Is(revokedSnaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("revoked face must report stale, got %v", revokedSnaps.ArchiveErr)
	}
	if len(revokedSnaps.Archived.Threads) != 1 {
		t.Fatalf("revoked face must keep the last rows, got %+v", revokedSnaps.Archived.Threads)
	}
}

// 归档归属不确定的自愈上界（跟进 PR②/#156 R1）：线程可能是被彻底删除，
// 归档面不会有它的增量——不确定状态不能无限期，超过 settle 仍无对账就按
// 「归档面无该线程」处理（此时若有极晚的增量，流本身也已异常，断连重连会
// 走权威快照）。
func TestShellState_ArchiveMembershipUncertainSettlesOnDeadline(t *testing.T) {
	state := newT3ShellState()
	gen := state.archiveGenerationNow()
	if applied, err := state.applyArchiveSnapshot(
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		gen, nil); !applied || err != nil {
		t.Fatalf("seed archive: applied=%v err=%v", applied, err)
	}

	if registered, flipped := state.noteArchiveMembershipUncertain([]string{"th-x"}, 40*time.Millisecond); registered != 1 || !flipped {
		t.Fatalf("marking a live face uncertain must register and flip, got registered=%d flipped=%v", registered, flipped)
	}
	if snaps := state.snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("uncertain membership must report stale, got %v", snaps.ArchiveErr)
	}
	// 行仍保留、面仍 ready（只是不宣称完整）。
	if st := state.status(); !st.ArchiveReady {
		t.Fatalf("uncertainty must not drop archiveReady")
	}

	// 到期由定时器驱动（不是读路径懒清，见 #156 R2）：清理要报告「需要发布事件」。
	time.Sleep(80 * time.Millisecond)
	if !state.expireArchiveMembershipUncertain() {
		t.Fatalf("expiry must report a trust transition to publish")
	}
	if snaps := state.snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("uncertainty must self-heal after the settle deadline, got %v", snaps.ArchiveErr)
	}
	if len(state.snapshots().Archived.Threads) != 1 {
		t.Fatalf("rows must be retained")
	}
}

// 逐个线程对账（#156 R1）：无关线程的增量不得替仍缺增量的线程恢复可信。
func TestShellState_ArchiveMembershipUncertainIsPerThread(t *testing.T) {
	state := newT3ShellState()
	gen := state.archiveGenerationNow()
	if applied, err := state.applyArchiveSnapshot(
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		gen, nil); !applied || err != nil {
		t.Fatalf("seed archive: applied=%v err=%v", applied, err)
	}
	if registered, flipped := state.noteArchiveMembershipUncertain([]string{"th-x", "th-y"}, time.Minute); registered != 2 || !flipped {
		t.Fatalf("marking must register both ids and flip, got registered=%d flipped=%v", registered, flipped)
	}

	// 只有 th-x 的增量到位：仍然不可信（th-y 未对账）。
	seqX := 2
	if err := state.applyArchiveItem(t3ArchiveStreamItem{
		Kind: "thread.updated", Sequence: &seqX,
		Thread: shellTestThread("th-x", "归档"),
	}, 0); err != nil {
		t.Fatalf("apply delta: %v", err)
	}
	if snaps := state.snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("th-y is still unreconciled, must stay stale, got %v", snaps.ArchiveErr)
	}

	// th-y 的增量到位：全部对账完成，恢复可信。
	seqY := 3
	if err := state.applyArchiveItem(t3ArchiveStreamItem{
		Kind: "thread.updated", Sequence: &seqY,
		Thread: shellTestThread("th-y", "归档"),
	}, 0); err != nil {
		t.Fatalf("apply delta: %v", err)
	}
	if snaps := state.snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("all reconciled, trust must be restored, got %v", snaps.ArchiveErr)
	}
}

// 归档增量先到、活跃移除后到不得降级（#156 R2-1）：两条流顺序不定，归档面
// 已有该线程时成员关系已确定，不该再登记成「待对账」白降级一个宽限窗口。
func TestShellState_ArchiveMembershipAlreadyArchivedIsNotUncertain(t *testing.T) {
	state := newT3ShellState()
	gen := state.archiveGenerationNow()
	if applied, err := state.applyArchiveSnapshot(
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		gen, nil); !applied || err != nil {
		t.Fatalf("seed archive: applied=%v err=%v", applied, err)
	}

	if registered, flipped := state.noteArchiveMembershipUncertain([]string{"th-a"}, time.Minute); registered != 0 || flipped {
		t.Fatalf("an already-archived thread needs no reconciliation, got registered=%d flipped=%v", registered, flipped)
	}
	if snaps := state.snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("face must stay trusted, got %v", snaps.ArchiveErr)
	}

	// 反例：归档面没有的线程仍要登记。
	if registered, flipped := state.noteArchiveMembershipUncertain([]string{"th-x"}, time.Minute); registered != 1 || !flipped {
		t.Fatalf("unknown-to-archive thread must be registered, got registered=%d flipped=%v", registered, flipped)
	}
}

// 移除与「标不可信」必须原子（#156 R5）：订阅模式下 armed 后，活跃面移除在
// 应用的同一临界区里登记待对账——读者不会观测到「线程已离开活跃面、归档面却
// 仍宣称完整」的中间态（否则并发 wait 会据此把线程判 thread_gone）。
func TestShellState_ArmedRemovalMarksUncertainAtomically(t *testing.T) {
	state := newT3ShellState()
	gen := state.archiveGenerationNow()
	if applied, err := state.applyArchiveSnapshot(
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		gen, nil); !applied || err != nil {
		t.Fatalf("seed archive: applied=%v err=%v", applied, err)
	}
	// 活跃面先有一行；随后权威快照替换掉它（removed 集）。
	seq1 := 1
	if _, err := state.applyActiveItemTracked(t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	_ = seq1
	state.armArchiveUncertain(true, time.Minute)

	seq2 := 2
	if _, err := state.applyActiveItemTracked(t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: seq2, Threads: shellTestThreads()},
	}); err != nil {
		t.Fatalf("apply snapshot removal: %v", err)
	}

	// 应用返回后立刻读：归档面必须已不可信（同一临界区登记，不依赖 hub 回调）。
	if snaps := state.snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("armed removal must mark uncertainty inside the same critical section, got %v", snaps.ArchiveErr)
	}

	// 未 armed（按需快照模式）时不登记：可信度由失效路径负责。
	state2 := newT3ShellState()
	if applied, err := state2.applyArchiveSnapshot(
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		state2.archiveGenerationNow(), nil); !applied || err != nil {
		t.Fatalf("seed archive2: %v", err)
	}
	if _, err := state2.applyActiveItemTracked(t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("seed active2: %v", err)
	}
	if _, err := state2.applyActiveItemTracked(t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2, Threads: shellTestThreads()},
	}); err != nil {
		t.Fatalf("apply snapshot removal2: %v", err)
	}
	if snaps := state2.snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("unarmed state must not self-mark uncertainty, got %v", snaps.ArchiveErr)
	}
}

// 切入订阅模式的窗口内发生移除也必须记账（#156 R6）：此时归档面先是不可信的，
// 若跳过登记，等订阅快照把它恢复可信时，对应归档增量可能仍在队列里——wait
// 会把缺失线程当定论。快照只对账它确实包含的 ID，其余留到到期。
func TestShellState_RemovalDuringLegacySwitchIsRecorded(t *testing.T) {
	state := newT3ShellState()
	gen := state.archiveGenerationNow()
	if applied, err := state.applyArchiveSnapshot(
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		gen, nil); !applied || err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	if _, err := state.applyActiveItemTracked(t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("seed active: %v", err)
	}

	// 模拟切入订阅模式：arm + 撤可信（与 switchToArchiveLegacy 同序）。
	state.mu.Lock()
	state.armArchiveUncertainLocked(true, 10*time.Second)
	state.noteArchiveExpiredLocked()
	state.mu.Unlock()

	// 面不可信期间发生移除：必须记账（否则快照恢复可信后无人再提它）。
	if _, err := state.applyActiveItemTracked(t3ShellStreamItem{
		Kind: "thread.removed", Sequence: intPtrTestLocal(2), ThreadID: "th-1",
	}); err != nil {
		t.Fatalf("apply removal: %v", err)
	}

	// 订阅快照落地（权威全量，不含 th-1）：th-1 仍未对账，面不得宣称完整。
	if err := state.applyArchiveItem(t3ArchiveStreamItem{
		Kind: "snapshot", Snapshot: &t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
	}, 0); err != nil {
		t.Fatalf("apply legacy snapshot: %v", err)
	}
	if snaps := state.snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("snapshot without the removed thread must not claim completeness, got %v", snaps.ArchiveErr)
	}

	// th-1 的归档增量到达：对账完成，恢复可信。
	if err := state.applyArchiveItem(t3ArchiveStreamItem{
		Kind: "thread.updated", Sequence: intPtrTestLocal(3), Thread: shellTestThread("th-1", "归档"),
	}, 0); err != nil {
		t.Fatalf("apply delta: %v", err)
	}
	if snaps := state.snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("after reconciliation the face must be trusted, got %v", snaps.ArchiveErr)
	}
}

// intPtrTestLocal 是测试内的小工具：取整型地址。
func intPtrTestLocal(v int) *int { return &v }

// 已被归档流对账的移除不得被重复登记（#156 R7）：armed 模式下登记发生在应用
// 移除的临界区；归档流的 thread.removed 若先到并清掉标记，hub 的帧后回调不得
// 再把它打回待对账，否则归档面白 stale 一个宽限窗口，wait 会误报 watch_degraded。
func TestShellState_ArmedModeDoesNotReregisterReconciledRemoval(t *testing.T) {
	state := newT3ShellState()
	gen := state.archiveGenerationNow()
	if applied, err := state.applyArchiveSnapshot(
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		gen, nil); !applied || err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	if _, err := state.applyActiveItemTracked(t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	state.armArchiveUncertain(true, time.Minute)

	// armed 路径登记（应用移除时）。
	if _, err := state.applyActiveItemTracked(t3ShellStreamItem{
		Kind: "thread.removed", Sequence: intPtrTestLocal(2), ThreadID: "th-1",
	}); err != nil {
		t.Fatalf("apply removal: %v", err)
	}
	if snaps := state.snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("armed removal must mark uncertainty, got %v", snaps.ArchiveErr)
	}

	// 归档流的同名移除先到：对账完成，恢复可信。
	if err := state.applyArchiveItem(t3ArchiveStreamItem{
		Kind: "thread.removed", Sequence: intPtrTestLocal(3), ThreadID: "th-1",
	}, 0); err != nil {
		t.Fatalf("apply archive removal: %v", err)
	}
	if snaps := state.snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("archive delta reconciles the removal, got %v", snaps.ArchiveErr)
	}

	// hub 的帧后回调（armed 模式下对登记是 no-op）：不得再打回待对账。
	if pending, _ := state.noteArchiveMembershipUncertain([]string{"th-1"}, time.Minute); pending != 0 {
		t.Fatalf("reconciled removal must not be re-registered, got pending=%d", pending)
	}
	if snaps := state.snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("face must stay trusted after a no-op registration, got %v", snaps.ArchiveErr)
	}
}
