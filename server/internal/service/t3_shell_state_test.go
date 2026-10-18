package service

import (
	"encoding/json"
	"errors"
	"testing"
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
	// 归档全量 + 归档增量。
	if err := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 40, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}); err != nil {
		t.Fatalf("apply archive snapshot: %v", err)
	}
	archiveSeq := 41
	if err := state.applyArchiveItem(t3ArchiveStreamItem{Kind: "thread.updated", Sequence: &archiveSeq, Thread: shellTestThread("th-b", "归档二")}); err != nil {
		t.Fatalf("apply archive delta: %v", err)
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
	if err := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2, Threads: shellTestThreads(shellTestThread("th-1", "归档残留"), shellTestThread("th-a", "归档"))}); err != nil {
		t.Fatalf("apply archive: %v", err)
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
	if err := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2, Threads: shellTestThreads(shellTestThread("th-1", "unarchive 残留"), shellTestThread("th-a", "真归档"))}); err != nil {
		t.Fatalf("apply archive: %v", err)
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

// 规则 5/6：归档增量 upsert/删除与 order 维护；归档快照可信标记。
func TestShellState_ArchiveLifecycle(t *testing.T) {
	state := newT3ShellState()
	st := state.status()
	if st.ArchiveReady {
		t.Fatalf("fresh state must not be archiveReady")
	}
	if err := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 2, SnapshotSequence: 9, Projects: []t3ProjectShell{{ID: "p-1", Title: "A"}}, Threads: shellTestThreads(shellTestThread("th-a", "一"), shellTestThread("th-b", "二"))}); err != nil {
		t.Fatalf("apply archive snapshot: %v", err)
	}
	seq := 10
	// upsert 已有 ID：换值不换位。
	if err := state.applyArchiveItem(t3ArchiveStreamItem{Kind: "thread.updated", Sequence: &seq, Thread: shellTestThread("th-a", "一改")}); err != nil {
		t.Fatalf("apply archive update: %v", err)
	}
	// 新 ID 追加。
	seq = 11
	if err := state.applyArchiveItem(t3ArchiveStreamItem{Kind: "thread.updated", Sequence: &seq, Thread: shellTestThread("th-c", "三")}); err != nil {
		t.Fatalf("apply archive append: %v", err)
	}
	snaps := state.snapshots()
	if got := orderOfArchive(snaps); len(got) != 3 || got[0] != "th-a" || got[1] != "th-b" || got[2] != "th-c" {
		t.Fatalf("archive order = %v, want [th-a th-b th-c]（upsert 不得换位/重复）", got)
	}
	if snaps.Archived.Threads[0].Title != "一改" {
		t.Fatalf("upsert must replace value, got %+v", snaps.Archived.Threads[0])
	}
	// 删除。
	seq = 12
	if err := state.applyArchiveItem(t3ArchiveStreamItem{Kind: "thread.removed", Sequence: &seq, ThreadID: "th-b"}); err != nil {
		t.Fatalf("apply archive removal: %v", err)
	}
	snaps = state.snapshots()
	if got := orderOfArchive(snaps); len(got) != 2 || got[0] != "th-a" || got[1] != "th-c" {
		t.Fatalf("archive order after removal = %v, want [th-a th-c]（不得留 tombstone）", got)
	}
	if st := state.status(); !st.ArchiveReady {
		t.Fatalf("archive snapshot must set archiveReady")
	}
	// 删除后再新增：append 到末尾。
	seq = 13
	if err := state.applyArchiveItem(t3ArchiveStreamItem{Kind: "thread.updated", Sequence: &seq, Thread: shellTestThread("th-b", "二回归档")}); err != nil {
		t.Fatalf("apply archive re-add: %v", err)
	}
	if got := orderOfArchive(state.snapshots()); len(got) != 3 || got[2] != "th-b" {
		t.Fatalf("re-added thread must append, got %v", got)
	}
}

func orderOfArchive(snaps *t3ShellSnapshots) []string {
	ids := make([]string, 0, len(snaps.Archived.Threads))
	for _, thread := range snaps.Archived.Threads {
		ids = append(ids, thread.ID)
	}
	return ids
}

// G1：归档失败清空归档面并记录 ArchiveErr，活跃面照常可用；随后归档快照
// 成功到达即恢复可信。
func TestShellState_MarkArchiveFailedClearsArchiveFace(t *testing.T) {
	state := newT3ShellState()
	if err := state.applyActiveItem(shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "活跃")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}); err != nil {
		t.Fatalf("apply archive: %v", err)
	}
	archiveErr := errors.New("归档流读取失败")
	state.markArchiveFailed(archiveErr)

	st := state.status()
	if st.ArchiveReady {
		t.Fatalf("failed archive face must not stay ready")
	}
	if !errors.Is(st.ArchiveErr, archiveErr) {
		t.Fatalf("archiveErr = %v, want wrapped input", st.ArchiveErr)
	}
	snaps := state.snapshots()
	if len(snaps.Archived.Threads) != 0 || len(snaps.Archived.Projects) != 0 {
		t.Fatalf("failed archive face must be cleared, got %+v", snaps.Archived)
	}
	if !errors.Is(snaps.ArchiveErr, archiveErr) {
		t.Fatalf("snaps.ArchiveErr = %v", snaps.ArchiveErr)
	}
	if len(snaps.Active.Threads) != 1 || snaps.Active.Threads[0].ID != "th-1" {
		t.Fatalf("active face must survive archive failure, got %+v", snaps.Active.Threads)
	}
	// 恢复：新的归档全量快照清除 ArchiveErr。
	if err := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 3, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}); err != nil {
		t.Fatalf("re-apply archive: %v", err)
	}
	if st := state.status(); st.ArchiveErr != nil || !st.ArchiveReady {
		t.Fatalf("fresh archive snapshot must restore trust, got %+v", st)
	}
	if snaps := state.snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("snaps.ArchiveErr = %v, want nil", snaps.ArchiveErr)
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

	archive := newT3ShellState()
	archiveSeq := 1
	archiveCases := []struct {
		name string
		item t3ArchiveStreamItem
	}{
		{"未知 kind", t3ArchiveStreamItem{Kind: "mystery"}},
		{"snapshot 无 snapshot 字段", t3ArchiveStreamItem{Kind: "snapshot"}},
		{"thread.updated 缺 thread", t3ArchiveStreamItem{Kind: "thread.updated", Sequence: &archiveSeq}},
		{"thread.removed 缺 threadId", t3ArchiveStreamItem{Kind: "thread.removed", Sequence: &archiveSeq}},
	}
	for _, tc := range archiveCases {
		t.Run("归档/"+tc.name, func(t *testing.T) {
			if err := archive.applyArchiveItem(tc.item); err == nil {
				t.Fatalf("expected error for %+v", tc.item)
			}
		})
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

	if err := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1}); err != nil {
		t.Fatalf("apply archive: %v", err)
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

// 归档快照与活跃快照的项目表独立维护（Archived.Projects 只来自归档流）。
func TestShellState_ProjectsFacesIndependent(t *testing.T) {
	state := newT3ShellState()
	if err := state.applyActiveItem(shellTestAuthoritative(1, []t3ProjectShell{{ID: "p-1", Title: "活跃项目"}}, nil)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := state.applyArchiveSnapshot(t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2, Projects: []t3ProjectShell{{ID: "p-2", Title: "归档项目"}}, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}); err != nil {
		t.Fatalf("apply archive: %v", err)
	}
	snaps := state.snapshots()
	if len(snaps.Active.Projects) != 1 || snaps.Active.Projects[0].ID != "p-1" {
		t.Fatalf("active projects = %+v", snaps.Active.Projects)
	}
	if len(snaps.Archived.Projects) != 1 || snaps.Archived.Projects[0].ID != "p-2" {
		t.Fatalf("archived projects = %+v", snaps.Archived.Projects)
	}
	// 活跃流的 project 增量只影响活跃项目表。
	seq := 3
	if err := state.applyActiveItem(t3ShellStreamItem{Kind: "project.removed", Sequence: &seq, ProjectID: "p-1"}); err != nil {
		t.Fatalf("apply project removal: %v", err)
	}
	snaps = state.snapshots()
	if len(snaps.Active.Projects) != 0 {
		t.Fatalf("active projects after removal = %+v", snaps.Active.Projects)
	}
	if len(snaps.Archived.Projects) != 1 {
		t.Fatalf("archived projects must be untouched: %+v", snaps.Archived.Projects)
	}
}
