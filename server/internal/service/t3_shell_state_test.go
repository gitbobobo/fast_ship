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
	if err := stApplyActive(state, shellTestAuthoritative(7, projects, shellTestThreads(shellTestThread("th-1", "一"), shellTestThread("th-2", "二")))); err != nil {
		t.Fatalf("apply authoritative: %v", err)
	}
	st := state.Status()
	if !st.ActiveReady || !st.ActiveSynced {
		t.Fatalf("authoritative snapshot must set ready+synced, got %+v", st)
	}
	if st.ActiveCursor != 7 {
		t.Fatalf("cursor = %d, want 7", st.ActiveCursor)
	}
	snaps := state.Snapshots()
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
	if err := stApplyActive(state, shellTestAuthoritative(7, []t3ProjectShell{{ID: "p-1", Title: "A"}}, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
		t.Fatalf("apply authoritative: %v", err)
	}
	// 元数据帧：threads 空、projects 只有已解析项、字段存在。
	if err := stApplyActive(state, shellTestEnrichment(99, []t3ProjectShell{{ID: "p-9", Title: "Enriched"}})); err != nil {
		t.Fatalf("apply enrichment: %v", err)
	}
	st := state.Status()
	if st.ActiveCursor != 7 {
		t.Fatalf("metadata frame must not move cursor, cursor = %d", st.ActiveCursor)
	}
	snaps := state.Snapshots()
	if len(snaps.Active.Projects) != 1 || snaps.Active.Projects[0].ID != "p-1" {
		t.Fatalf("metadata frame must not replace projects: %+v", snaps.Active.Projects)
	}
	if len(snaps.Active.Threads) != 1 || snaps.Active.Threads[0].ID != "th-1" {
		t.Fatalf("metadata frame must not touch threads: %+v", snaps.Active.Threads)
	}
	// 元数据帧也不能让未就绪的状态宣称 ready。
	fresh := newT3ShellState()
	if err := stApplyActive(fresh, shellTestEnrichment(1, nil)); err != nil {
		t.Fatalf("apply enrichment to fresh state: %v", err)
	}
	if st := fresh.Status(); st.ActiveReady || st.ActiveSynced {
		t.Fatalf("metadata frame must not set ready flags: %+v", st)
	}
}

// 规则 2：较低权威快照把游标重置为 snapshotSequence（可以变小，不是 max）。
func TestShellState_LowerAuthoritativeSnapshotResetsCursor(t *testing.T) {
	state := newT3ShellState()
	if err := stApplyActive(state, shellTestAuthoritative(50, nil, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	seq := 51
	if err := stApplyActive(state, t3ShellStreamItem{Kind: "thread.updated", Sequence: &seq, Location: "active", Thread: shellTestThread("th-1", "一改")}); err != nil {
		t.Fatalf("apply delta: %v", err)
	}
	if st := state.Status(); st.ActiveCursor != 51 {
		t.Fatalf("delta must advance cursor to 51, got %d", st.ActiveCursor)
	}
	// 上游 gap 超限时的回退：改发权威全量，序号比游标小。
	if err := stApplyActive(state, shellTestAuthoritative(10, nil, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
		t.Fatalf("apply fallback: %v", err)
	}
	if st := state.Status(); st.ActiveCursor != 10 {
		t.Fatalf("authoritative snapshot must reset cursor to 10, got %d", st.ActiveCursor)
	}
}

// 规则 2：游标只由活跃面推进——归档快照/归档增量/元数据帧/标记都不碰它。
func TestShellState_CursorOnlyAdvancedByActiveFace(t *testing.T) {
	state := newT3ShellState()
	if err := stApplyActive(state, shellTestAuthoritative(7, nil, nil)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// 归档全量快照（INT-173 起按需快照是唯一来源，无增量流）。
	if applied, applyErr := stApplySnap(state, t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 40, Threads: shellTestThreads(shellTestThread("th-a", "归档"), shellTestThread("th-b", "归档二"))}, stGen(state)); !applied || applyErr != nil {
		t.Fatalf("apply archive snapshot must be trusted")
	}
	// 元数据帧与 synchronized 标记。
	if err := stApplyActive(state, shellTestEnrichment(42, nil)); err != nil {
		t.Fatalf("apply enrichment: %v", err)
	}
	if err := stApplyActive(state, t3ShellStreamItem{Kind: "synchronized"}); err != nil {
		t.Fatalf("apply synchronized: %v", err)
	}
	if st := state.Status(); st.ActiveCursor != 7 {
		t.Fatalf("non-active-face frames must not move cursor, cursor = %d", st.ActiveCursor)
	}
	// 活跃增量才推进。
	seq := 8
	if err := stApplyActive(state, t3ShellStreamItem{Kind: "thread.updated", Sequence: &seq, Location: "active", Thread: shellTestThread("th-2", "二")}); err != nil {
		t.Fatalf("apply active delta: %v", err)
	}
	if st := state.Status(); st.ActiveCursor != 8 {
		t.Fatalf("active delta must advance cursor to 8, got %d", st.ActiveCursor)
	}
}

// 规则 3：两面独立——thread.removed{location:"active"} 只删活跃面，不删归档面。
func TestShellState_FacesIndependent(t *testing.T) {
	state := newT3ShellState()
	if err := stApplyActive(state, shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "活跃")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied, applyErr := stApplySnap(state, t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2, Threads: shellTestThreads(shellTestThread("th-1", "归档残留"), shellTestThread("th-a", "归档"))}, stGen(state)); !applied || applyErr != nil {
		t.Fatalf("apply archive must be trusted")
	}
	// 上游归档动作在活跃流上表现为 active removal（ShellStream.ts:292-320）。
	seq := 3
	if err := stApplyActive(state, t3ShellStreamItem{Kind: "thread.removed", Sequence: &seq, Location: "active", ThreadID: "th-1"}); err != nil {
		t.Fatalf("apply removal: %v", err)
	}
	snaps := state.Snapshots()
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
	if err := stApplyActive(state, shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "活跃版")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied, applyErr := stApplySnap(state, t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2, Threads: shellTestThreads(shellTestThread("th-1", "unarchive 残留"), shellTestThread("th-a", "真归档"))}, stGen(state)); !applied || applyErr != nil {
		t.Fatalf("apply archive must be trusted")
	}
	snaps := state.Snapshots()
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
	st := state.Status()
	if st.ArchiveReady {
		t.Fatalf("fresh state must not be archiveReady")
	}
	if applied, applyErr := stApplySnap(state, t3ArchivedShellSnapshot{SchemaVersion: 2, SnapshotSequence: 9, Projects: []t3ProjectShell{{ID: "p-1", Title: "A"}}, Threads: shellTestThreads(shellTestThread("th-a", "一"), shellTestThread("th-b", "二"))}, stGen(state)); !applied || applyErr != nil {
		t.Fatalf("apply archive snapshot must be trusted")
	}
	if st := state.Status(); !st.ArchiveReady {
		t.Fatalf("archive snapshot must set archiveReady")
	}
	// 新一次快照整体替换：值更新、集合变化都按新快照落地。
	if applied, applyErr := stApplySnap(state, t3ArchivedShellSnapshot{SchemaVersion: 2, SnapshotSequence: 10, Threads: shellTestThreads(shellTestThread("th-a", "一改"), shellTestThread("th-c", "三"))}, stGen(state)); !applied || applyErr != nil {
		t.Fatalf("re-apply archive snapshot must be trusted")
	}
	snaps := state.Snapshots()
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
	if err := stApplyActive(state, shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "活跃")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied, applyErr := stApplySnap(state, t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}, stGen(state)); !applied || applyErr != nil {
		t.Fatalf("apply archive must be trusted")
	}
	archiveErr := errors.New("归档快照读取失败")
	if eff := stRecordFailure(state, archiveErr, stGen(state)); !errors.Is(eff.Err, archiveErr) || eff.Refetch {
		t.Fatalf("current-generation failure must be recorded without a refetch, got %+v", eff)
	}

	st := state.Status()
	if !st.ArchiveReady {
		t.Fatalf("fetch failure must not revoke a ready archive face")
	}
	if !errors.Is(st.ArchiveErr, archiveErr) {
		t.Fatalf("archiveErr = %v, want wrapped input", st.ArchiveErr)
	}
	snaps := state.Snapshots()
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
	if applied, applyErr := stApplySnap(state, t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 3, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}, stGen(state)); !applied || applyErr != nil {
		t.Fatalf("re-apply archive must be trusted")
	}
	if st := state.Status(); st.ArchiveErr != nil || !st.ArchiveReady {
		t.Fatalf("fresh archive snapshot must restore trust, got %+v", st)
	}
	if snaps := state.Snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("snaps.ArchiveErr = %v, want nil", snaps.ArchiveErr)
	}

	// 未就绪状态下失败：无快照可留，表保持空、ArchiveErr 承载原因。
	fresh := newT3ShellState()
	if eff := stRecordFailure(fresh, archiveErr, stGen(fresh)); !errors.Is(eff.Err, archiveErr) || eff.Refetch {
		t.Fatalf("cold-start failure must be recorded without a refetch, got %+v", eff)
	}
	if snaps := fresh.Snapshots(); len(snaps.Archived.Threads) != 0 || !errors.Is(snaps.ArchiveErr, archiveErr) {
		t.Fatalf("failed fetch on a never-ready face = %+v", snaps)
	}
}

// 写后失效的视图语义（Finding A）：noteArchiveInvalidated 撤销 archiveReady
// 并让 snapshots() 报不可信（errT3ShellArchiveStale），归档行保留；拉取途中
// 发生的失效使同代落地的快照只换数据、不封可信窗口（世代护栏）；落地后
// 再用当前世代应用新快照才恢复可信。
func TestShellState_ArchiveInvalidationGenerationGuard(t *testing.T) {
	state := newT3ShellState()
	if err := stApplyActive(state, shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "活跃")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied, applyErr := stApplySnap(state, t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}, stGen(state)); !applied || applyErr != nil {
		t.Fatalf("apply archive must be trusted")
	}

	// 拉取发起前取样世代，随后发生写后失效。
	gen := stGen(state)
	state.Invalidate()

	snaps := state.Snapshots()
	if !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("invalidated archive must report stale, got %v", snaps.ArchiveErr)
	}
	if len(snaps.Archived.Threads) != 1 || snaps.Archived.Threads[0].ID != "th-a" {
		t.Fatalf("invalidation must keep rows, got %+v", snaps.Archived.Threads)
	}
	if st := state.Status(); st.ArchiveReady {
		t.Fatalf("invalidation must revoke archiveReady")
	}

	// 写前快照落地（世代不符）：数据照换但不封窗，且是「陈旧非错误」——
	// applied=false、err=nil，调用方据此与坏负载（err 非空）分派。
	if applied, applyErr := stApplySnap(state, t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 3, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}, gen); applied || applyErr != nil {
		t.Fatalf("pre-write snapshot must be stale not error, applied=%v err=%v", applied, applyErr)
	}
	snaps = state.Snapshots()
	if snaps.ArchiveErr == nil {
		t.Fatalf("pre-write snapshot must not reseal the window")
	}

	// 世代不符的失败同样不覆盖失效语义（表达补拉且不记 ArchiveErr 覆盖）。
	preWriteErr := errors.New("写前读取失败")
	if eff := stRecordFailure(state, preWriteErr, gen); !eff.Refetch || !errors.Is(eff.Err, preWriteErr) {
		t.Fatalf("pre-write failure must demand a refetch and carry the error, got %+v", eff)
	}
	if !errors.Is(state.Snapshots().ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("pre-write failure must not overwrite stale semantics, got %v", state.Snapshots().ArchiveErr)
	}

	// 写后快照（当前世代）落地才恢复可信。
	if applied, applyErr := stApplySnap(state, t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 4, Threads: shellTestThreads(shellTestThread("th-a", "归档"), shellTestThread("th-b", "写后"))}, stGen(state)); !applied || applyErr != nil {
		t.Fatalf("post-write snapshot must be trusted, applied=%v err=%v", applied, applyErr)
	}
	if snaps := state.Snapshots(); snaps.ArchiveErr != nil || len(snaps.Archived.Threads) != 2 {
		t.Fatalf("post-write snapshot must restore trust, err=%v threads=%+v", snaps.ArchiveErr, snaps.Archived.Threads)
	}
}

// 坏负载与世代不符是两种不同的「未可信」（复审遗留：不得把两种 false
// 合并）：结构性坏快照是「本次读取失败」——EndFetch 记 ArchiveErr 真实
// 原因 + retryAfter 冷却，簿记由失败路径收尾；世代不符的写前快照是陈旧
// 而非错误（不记冷却，Effects.Refetch 表达补拉）；世代相符落地封窗。
func TestShellState_ArchiveBadPayloadDistinctFromStale(t *testing.T) {
	bad := t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1,
		Threads: []t3ThreadShell{*shellTestThread("th-a", "一"), *shellTestThread("", "无 ID")}}
	good := t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}

	// 坏负载：EndFetch 按本次读取失败记账——Err 点名原因、记冷却、不补拉。
	badState := newT3ShellState()
	badState.archiveTTL = time.Minute
	ticket, admitted, _ := badState.BeginFetch()
	if !admitted {
		t.Fatalf("must admit")
	}
	eff := badState.EndFetch(ticket, t3FetchOutcome{kind: t3FetchSnapshot, snapshot: bad})
	if eff.Err == nil || !strings.Contains(eff.Err.Error(), "缺少 ID") {
		t.Fatalf("bad payload must surface the cause in Effects.Err, got %+v", eff)
	}
	if eff.Refetch {
		t.Fatalf("bad payload must record the cooldown, not a refetch memo")
	}
	if _, admitted, _ := badState.BeginFetch(); admitted {
		t.Fatalf("the recorded cooldown must gate the next admission")
	}

	// 世代不符：拉取在途时发生失效——写前快照是陈旧而非错误：Err 为空、
	// Refetch 表达补拉诉求、簿记不封窗。
	staleState := newT3ShellState()
	staleState.archiveTTL = time.Minute
	ticket, admitted, _ = staleState.BeginFetch()
	if !admitted {
		t.Fatalf("must admit")
	}
	staleState.Invalidate()
	eff = staleState.EndFetch(ticket, t3FetchOutcome{kind: t3FetchSnapshot, snapshot: good})
	if eff.Err != nil || !eff.Refetch {
		t.Fatalf("pre-write snapshot must be stale-not-error and demand a refetch, got %+v", eff)
	}
	staleState.mu.Lock()
	if !staleState.validUntil.IsZero() || !staleState.retryAfter.IsZero() {
		t.Fatalf("pre-write landing must not seal bookkeeping, validUntil=%v retryAfter=%v", staleState.validUntil, staleState.retryAfter)
	}
	staleState.mu.Unlock()

	// 世代相符：可信落地封窗（Refresh+Publish），不补拉不报错。
	okState := newT3ShellState()
	okState.archiveTTL = time.Minute
	ticket, admitted, _ = okState.BeginFetch()
	if !admitted {
		t.Fatalf("must admit")
	}
	eff = okState.EndFetch(ticket, t3FetchOutcome{kind: t3FetchSnapshot, snapshot: good})
	if eff.Err != nil || eff.Refetch || !eff.Refresh {
		t.Fatalf("trusted landing must refresh without error or refetch, got %+v", eff)
	}
	if st := okState.Status(); !st.ArchiveReady || st.ArchiveErr != nil {
		t.Fatalf("trusted landing must seal the window, got %+v", st)
	}
}

// 空权威快照也是权威：整体替换为空 + 置 ready/synced。
func TestShellState_EmptyAuthoritativeSnapshot(t *testing.T) {
	state := newT3ShellState()
	if err := stApplyActive(state, shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := stApplyActive(state, shellTestAuthoritative(2, nil, nil)); err != nil {
		t.Fatalf("apply empty: %v", err)
	}
	st := state.Status()
	if !st.ActiveReady || !st.ActiveSynced || st.ActiveCursor != 2 {
		t.Fatalf("status = %+v", st)
	}
	if snaps := state.Snapshots(); len(snaps.Active.Threads) != 0 || len(snaps.Active.Projects) != 0 {
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
			if err := stApplyActive(state, tc.item); err == nil {
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
	applied, applyErr := stApplySnap(archive, bad, stGen(archive))
	if applied || applyErr == nil {
		t.Fatalf("missing-ID thread must reject the snapshot as a bad payload, applied=%v err=%v", applied, applyErr)
	}
	if !strings.Contains(applyErr.Error(), "缺少 ID") {
		t.Fatalf("bad payload error must name the cause, got %v", applyErr)
	}
	if st := archive.Status(); st.ArchiveReady {
		t.Fatalf("rejected snapshot must not mark archive ready")
	}
}

// 规则 1/规则 3 的补充：归档面还没拿到全量快照时，ArchiveErr 必须非空——
// 「没出错」不等于「已加载完」，两条连接进度独立（活跃面常先就绪）。
func TestShellState_ArchiveNotReadyIsUntrusted(t *testing.T) {
	state := newT3ShellState()
	if err := stApplyActive(state, shellTestAuthoritative(7, nil, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if snaps := state.Snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveNotReady) {
		t.Fatalf("archiveErr = %v, want not-ready", snaps.ArchiveErr)
	}

	if applied, applyErr := stApplySnap(state, t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1}, stGen(state)); !applied || applyErr != nil {
		t.Fatalf("apply archive must be trusted")
	}
	if snaps := state.Snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("archiveErr = %v, want nil after snapshot", snaps.ArchiveErr)
	}
}

// 活跃流上的 location:"archive" 不是坏帧：上游 reducer 对它的处理是「不进
// 活跃面」（shellReducer.ts:169-190），归档面由归档订阅维护。
func TestShellState_ArchiveLocationLeavesActiveFace(t *testing.T) {
	state := newT3ShellState()
	if err := stApplyActive(state, shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	seq := 2
	if err := stApplyActive(state, t3ShellStreamItem{Kind: "thread.updated", Sequence: &seq, Location: "archive", Thread: shellTestThread("th-1", "一")}); err != nil {
		t.Fatalf("archive-located update must not be a bad frame: %v", err)
	}
	if snaps := state.Snapshots(); len(snaps.Active.Threads) != 0 {
		t.Fatalf("archive-located update must leave the active face: %+v", snaps.Active.Threads)
	}
}

// 规则 6：order 维护——upsert 换值不换位、无重复；活跃面同样适用。
func TestShellState_ActiveOrderMaintenance(t *testing.T) {
	state := newT3ShellState()
	if err := stApplyActive(state, shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "一"), shellTestThread("th-2", "二")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	seq := 2
	if err := stApplyActive(state, t3ShellStreamItem{Kind: "thread.updated", Sequence: &seq, Location: "active", Thread: shellTestThread("th-1", "一改")}); err != nil {
		t.Fatalf("apply update: %v", err)
	}
	snaps := state.Snapshots()
	if len(snaps.Active.Threads) != 2 || snaps.Active.Threads[0].ID != "th-1" || snaps.Active.Threads[1].ID != "th-2" {
		t.Fatalf("upsert must keep order without duplicates: %+v", snaps.Active.Threads)
	}
	if snaps.Active.Threads[0].Title != "一改" {
		t.Fatalf("upsert must replace value: %+v", snaps.Active.Threads[0])
	}
	seq = 3
	if err := stApplyActive(state, t3ShellStreamItem{Kind: "thread.removed", Sequence: &seq, Location: "active", ThreadID: "th-1"}); err != nil {
		t.Fatalf("apply removal: %v", err)
	}
	seq = 4
	if err := stApplyActive(state, t3ShellStreamItem{Kind: "thread.updated", Sequence: &seq, Location: "active", Thread: shellTestThread("th-1", "一回归")}); err != nil {
		t.Fatalf("apply re-add: %v", err)
	}
	snaps = state.Snapshots()
	if snaps.Active.Threads[1].ID != "th-1" {
		t.Fatalf("re-added thread must append at end: %+v", snaps.Active.Threads)
	}
}

// 规则 7：发布的快照是拷贝，后续增量不得原地改动已发布对象。
func TestShellState_SnapshotsAreStableCopies(t *testing.T) {
	state := newT3ShellState()
	if err := stApplyActive(state, shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	before := state.Snapshots()
	seq := 2
	if err := stApplyActive(state, t3ShellStreamItem{Kind: "thread.updated", Sequence: &seq, Location: "active", Thread: shellTestThread("th-1", "一改")}); err != nil {
		t.Fatalf("apply delta: %v", err)
	}
	after := state.Snapshots()
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
	if err := stApplyActive(state, shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// resume 开始：hub 清标记。
	state.MarkActiveSynced(false)
	if st := state.Status(); st.ActiveSynced || !st.ActiveReady {
		t.Fatalf("cleared synced must keep ready, got %+v", st)
	}
	// 回放期间收到 synchronized：置位，游标不动。
	if err := stApplyActive(state, t3ShellStreamItem{Kind: "synchronized"}); err != nil {
		t.Fatalf("apply synchronized: %v", err)
	}
	if st := state.Status(); !st.ActiveSynced || st.ActiveCursor != 1 {
		t.Fatalf("status = %+v", st)
	}
	if snaps := state.Snapshots(); len(snaps.Active.Threads) != 1 {
		t.Fatalf("sync flag changes must not touch data: %+v", snaps.Active.Threads)
	}
}

// JSON 解码精度：resolvedRepositoryIdentityRoots 字段必须能区分「存在」与
// 「不存在」（存在但为 null/空数组都算元数据帧）。
func TestShellState_EnrichmentDecodeFromWire(t *testing.T) {
	state := newT3ShellState()
	// 权威帧先建立数据。
	if err := stApplyActive(state, shellTestAuthoritative(7, nil, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// enrichment 字段存在（字符串数组非空，形如上游真实帧）。
	enriched := []byte(`{"kind":"snapshot","snapshot":{"schemaVersion":1,"snapshotSequence":99,"projects":[{"id":"p-9","title":"X"}],"threads":[]},"resolvedRepositoryIdentityRoots":["/repo"]}`)
	var enrichedItem t3ShellStreamItem
	if err := json.Unmarshal(enriched, &enrichedItem); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := stApplyActive(state, enrichedItem); err != nil {
		t.Fatalf("apply enrichment: %v", err)
	}
	if st := state.Status(); st.ActiveCursor != 7 {
		t.Fatalf("enrichment must not move cursor, cursor = %d", st.ActiveCursor)
	}
	// 字段不存在 → 权威：即便与上游惯例不符的空 projects/threads 也整体替换。
	authoritative := []byte(`{"kind":"snapshot","snapshot":{"schemaVersion":1,"snapshotSequence":8,"projects":[],"threads":[]}}`)
	var authoritativeItem t3ShellStreamItem
	if err := json.Unmarshal(authoritative, &authoritativeItem); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := stApplyActive(state, authoritativeItem); err != nil {
		t.Fatalf("apply authoritative: %v", err)
	}
	if st := state.Status(); st.ActiveCursor != 8 || len(state.Snapshots().Active.Threads) != 0 {
		t.Fatalf("authoritative frame must replace state, status = %+v", st)
	}
}

// F7：项目表只有活跃面一份权威来源——归档快照只维护线程，其 Projects 一律
// 忽略：旧名字不得覆盖活跃流的项目更新，也不得复活已删除项目；项目名表
// 只从活跃面组装。
func TestShellState_ArchiveSnapshotIgnoresProjects(t *testing.T) {
	state := newT3ShellState()
	if err := stApplyActive(state, shellTestAuthoritative(1,
		[]t3ProjectShell{{ID: "p-1", Title: "旧名"}, {ID: "p-2", Title: "将删除"}},
		shellTestThreads(shellTestThread("th-1", "一")),
	)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// 活跃流项目改名。
	seq := 2
	if err := stApplyActive(state, t3ShellStreamItem{Kind: "project.updated", Sequence: &seq, Project: &t3ProjectShell{ID: "p-1", Title: "新名"}}); err != nil {
		t.Fatalf("apply project update: %v", err)
	}
	// 活跃流项目删除。
	seq = 3
	if err := stApplyActive(state, t3ShellStreamItem{Kind: "project.removed", Sequence: &seq, ProjectID: "p-2"}); err != nil {
		t.Fatalf("apply project removal: %v", err)
	}
	// 归档快照带着改名前/已删除的项目表到达：不得覆盖、不得复活。
	if applied, applyErr := stApplySnap(state, t3ArchivedShellSnapshot{
		SchemaVersion: 1, SnapshotSequence: 2,
		Projects: []t3ProjectShell{{ID: "p-1", Title: "旧名"}, {ID: "p-2", Title: "已删除"}},
		Threads:  shellTestThreads(shellTestThread("th-a", "归档")),
	}, stGen(state)); !applied || applyErr != nil {
		t.Fatalf("apply archive must be trusted, applied=%v err=%v", applied, applyErr)
	}
	snaps := state.Snapshots()
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
	if err := stApplyActive(state, shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "活跃")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied, applyErr := stApplySnap(state, t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}, stGen(state)); !applied || applyErr != nil {
		t.Fatalf("apply archive must be trusted")
	}

	// 写后失效：留行但不可信。
	state.Invalidate()

	// 当前世代重拉失败：旧行必须保留（清表会把「不可信但留行」谎报为
	// 「确定为空」），ArchiveErr 是真实失败而非 stale。
	fetchErr := errors.New("归档快照读取失败")
	if eff := stRecordFailure(state, fetchErr, stGen(state)); !errors.Is(eff.Err, fetchErr) || eff.Refetch {
		t.Fatalf("current-generation failure must be recorded without a refetch, got %+v", eff)
	}
	snaps := state.Snapshots()
	if len(snaps.Archived.Threads) != 1 || snaps.Archived.Threads[0].ID != "th-a" {
		t.Fatalf("failed refetch after invalidation must keep rows, got %+v", snaps.Archived.Threads)
	}
	if !errors.Is(snaps.ArchiveErr, fetchErr) {
		t.Fatalf("ArchiveErr = %v, want the real failure", snaps.ArchiveErr)
	}

	// TTL 过期标记（noteArchiveExpired）后失败同样留行：先恢复可信再过期。
	if applied, applyErr := stApplySnap(state, t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 3, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}, stGen(state)); !applied || applyErr != nil {
		t.Fatalf("re-apply archive must be trusted")
	}
	state.mu.Lock()
	stExpireReady(state)
	state.mu.Unlock()
	if eff := stRecordFailure(state, fetchErr, stGen(state)); !errors.Is(eff.Err, fetchErr) || eff.Refetch {
		t.Fatalf("current-generation failure must be recorded without a refetch, got %+v", eff)
	}
	snaps = state.Snapshots()
	if len(snaps.Archived.Threads) != 1 || !errors.Is(snaps.ArchiveErr, fetchErr) {
		t.Fatalf("failed refetch after expiry must keep rows and report the real failure, got %+v", snaps)
	}

	// 冷启动（从未成功）失败仍可清表：没有快照可留时半张表只会误导。
	fresh := newT3ShellState()
	if eff := stRecordFailure(fresh, fetchErr, stGen(fresh)); !errors.Is(eff.Err, fetchErr) || eff.Refetch {
		t.Fatalf("cold-start failure must be recorded without a refetch, got %+v", eff)
	}
	if snaps := fresh.Snapshots(); len(snaps.Archived.Threads) != 0 || !errors.Is(snaps.ArchiveErr, fetchErr) {
		t.Fatalf("cold-start failure must clear the face, got %+v", snaps)
	}
}

// 世代校验与簿记提交原子化（Finding R1）：EndFetch 把「世代校验 + 数据
// 落地 + 簿记收尾」收进 s.mu 单个临界区——持锁期间 EndFetch 阻塞，放锁后
// 落地与封窗/补拉诉求同时可观测，不存在「apply 成功了但簿记未收尾」的可
// 读缝隙（旧实现在两步之间放锁，InvalidateArchive 可在此推进世代后仍被
// validUntil 封窗）。失效只能先于落地（世代不符 → 不封窗、Refetch 表达
// 补拉）或在收尾之后（由 Invalidate 清零簿记）。
func TestShellState_ApplyCommitIsAtomicWithGeneration(t *testing.T) {
	state := newT3ShellState()
	state.archiveTTL = time.Minute
	ticket, admitted, _ := state.BeginFetch()
	if !admitted {
		t.Fatalf("fresh state must admit")
	}

	// 持锁期间 EndFetch 阻塞：提交前读者看不到任何变化。
	state.mu.Lock()
	done := make(chan t3ShellEffects, 1)
	go func() {
		done <- state.EndFetch(ticket, t3FetchOutcome{kind: t3FetchSnapshot,
			snapshot: t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}})
	}()
	select {
	case eff := <-done:
		t.Fatalf("EndFetch must block on s.mu while the lock is held, got %+v", eff)
	case <-time.After(20 * time.Millisecond):
	}
	state.mu.Unlock()
	// 落地与封窗在同一临界区提交：EndFetch 返回时窗口已封。
	eff := <-done
	if eff.Err != nil || eff.Refetch || !eff.Refresh {
		t.Fatalf("trusted landing must refresh without error or refetch, got %+v", eff)
	}
	state.mu.Lock()
	sealed := !state.validUntil.IsZero()
	state.mu.Unlock()
	if !sealed || !state.Status().ArchiveReady {
		t.Fatalf("trusted landing must seal validUntil in the same critical section")
	}

	// 写前结果（世代不符）：不封窗，Effects.Refetch 表达补拉。
	state.Invalidate()
	ticket, admitted, _ = state.BeginFetch()
	if !admitted {
		t.Fatalf("invalidated window must admit a refetch")
	}
	state.Invalidate() // 拉取途中再次失效：ticket 世代变旧
	eff = state.EndFetch(ticket, t3FetchOutcome{kind: t3FetchSnapshot,
		snapshot: t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2}})
	if eff.Err != nil || !eff.Refetch {
		t.Fatalf("pre-write landing must demand a refetch without error, got %+v", eff)
	}
	state.mu.Lock()
	if !state.validUntil.IsZero() || !state.retryAfter.IsZero() {
		t.Fatalf("pre-write landing must not seal bookkeeping, validUntil=%v retryAfter=%v", state.validUntil, state.retryAfter)
	}
	state.mu.Unlock()

	// 失败路径对称：当前世代失败记冷却 retryAfter；写前失败只表达补拉。
	state2 := newT3ShellState()
	state2.archiveTTL = time.Minute
	eff = stRecordFailure(state2, errors.New("拉取失败"), stGen(state2))
	if eff.Err == nil || eff.Refetch {
		t.Fatalf("current-generation failure must be recorded without a refetch, got %+v", eff)
	}
	state2.mu.Lock()
	if state2.retryAfter.IsZero() {
		t.Fatalf("applied failure must record retryAfter")
	}
	state2.mu.Unlock()
}

// 撤可信与在途早退的原子性（R2 / R17 P2-1 / R20 P2 合并后的不变量）：
// 「重拉在途超过宽限 → 撤可信」必须与「本次准入被合并（admitted=false）」
// 在同一临界区提交——读者不可能拿到「kick 被拒但视图仍报可信」的组合，
// 否则 wait 会拿过期快照判 thread_gone。判据锚在本次拉取的发起时刻
// （refetchStartedAt），不是 validUntil 的年龄。撤可信经 Effects 表达
// （Publish=dedup + Refresh）。
//
// 确定性复现分两步：
//  1. 锁序证明：先由本 goroutine 持住 s.mu，再启动 admission——它必须阻塞
//     在 s.mu 上（准入序列第一步取 s.mu），提交前读者看不到任何变化。
//  2. 放开 s.mu 后 admission 完成撤可信；此后读者 B 走早退路径，读到的
//     视图必须已不可信（留行、报 stale）。
func TestShellState_AdmitArchiveRefreshIsAtomicWithRevoke(t *testing.T) {
	base := time.Now()
	now := base
	state := newT3ShellState()
	state.now = func() time.Time { return now }
	state.archiveTTL = time.Minute
	state.archiveStaleGrace = 50 * time.Millisecond

	// 可信归档面：真实准入 + 落地封窗（validUntil = base + TTL）。
	ticket, admitted, _ := state.BeginFetch()
	if !admitted {
		t.Fatalf("fresh state must admit")
	}
	state.EndFetch(ticket, t3FetchOutcome{kind: t3FetchSnapshot, snapshot: t3ArchivedShellSnapshot{
		SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}})
	if snaps := state.Snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("seeded archive must be trusted, got %v", snaps.ArchiveErr)
	}

	// 过期且重拉在途超过宽限：下一次准入必须在早退的同时撤可信。
	now = base.Add(2 * time.Minute) // validUntil 已过
	if _, admitted, _ := state.BeginFetch(); !admitted {
		t.Fatalf("expired cache must admit a refetch")
	}
	now = now.Add(100 * time.Millisecond) // 在途超过宽限（50ms）、窗口仍过期

	// 1) 持住 s.mu：admission 必须阻塞在这里，提交前视图仍可信。
	state.mu.Lock()
	type admitResult struct {
		admitted bool
		eff      t3ShellEffects
	}
	admitDone := make(chan admitResult, 1)
	go func() {
		_, admitted, eff := state.BeginFetch()
		admitDone <- admitResult{admitted, eff}
	}()
	select {
	case res := <-admitDone:
		t.Fatalf("admission must block on s.mu, got %+v", res)
	case <-time.After(20 * time.Millisecond):
	}
	if !state.archiveReady || state.archiveErr != nil {
		t.Fatalf("pre-commit view must still be trusted, ready=%v err=%v", state.archiveReady, state.archiveErr)
	}
	state.mu.Unlock()

	// 2) admission 完成：早退 + 撤可信在同一临界区提交（经 Effects 表达）。
	res := <-admitDone
	if res.admitted || res.eff.Publish == t3PublishNone || !res.eff.Refresh {
		t.Fatalf("in-flight beyond grace must merge onto the running fetch and revoke trust via effects, got %+v", res)
	}
	snaps := state.Snapshots()
	if snaps.ArchiveErr == nil || !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("revocation must be applied in the same critical section, got %v", snaps.ArchiveErr)
	}
	if len(snaps.Archived.Threads) != 1 || snaps.Archived.Threads[0].ID != "th-a" {
		t.Fatalf("revoked face must keep the last rows, got %+v", snaps.Archived.Threads)
	}
	if st := state.Status(); st.ArchiveReady {
		t.Fatalf("archiveReady must be revoked together with the in-flight early return")
	}
	if _, admittedAgain, _ := state.BeginFetch(); admittedAgain {
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
	connA := stReconnectTarget(state)
	if connA != 0 {
		t.Fatalf("fresh state must have no issued reconnect requests, got %d", connA)
	}
	if gen := stRequestReconnect(state); gen != 1 {
		t.Fatalf("first reconnect request generation = %d, want 1", gen)
	}
	// gen1 的信号被读循环消费：连接 A 按备忘断开，监督循环重连出连接 B。
	if !state.ConsumeLegacyReconnect() {
		t.Fatalf("connA read loop must consume the pending request")
	}
	connB := stReconnectTarget(state)
	if connB != 1 {
		t.Fatalf("connB postmark = %d, want 1", connB)
	}
	// 连接 B 订阅后即收到权威快照——但快照尚未 apply 时第二次失效记入
	// gen2（write-in-flight）：这份快照是在 gen2 请求之前发出的。
	if gen := stRequestReconnect(state); gen != 2 {
		t.Fatalf("second reconnect request generation = %d, want 2", gen)
	}
	// 变更前快照落地（连接 B 邮戳 1 < 备忘 gen2）：换数据、恢复可信，但
	// 不得吞掉 gen2 的备忘——它是写前状态，还欠一次写后重连。
	if err := stApplyLegacyItem(state, t3ArchiveStreamItem{
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
	if !state.ConsumeLegacyReconnect() {
		t.Fatalf("connB read loop must consume the request recorded during its snapshot window")
	}
	// 监督循环重连出连接 C：subscribe 在 gen2 之后发出（邮戳=2），其快照
	// 必为写后状态——落地清备忘并交付写后全量。
	connC := stReconnectTarget(state)
	if connC != 2 {
		t.Fatalf("connC postmark = %d, want 2", connC)
	}
	if err := stApplyLegacyItem(state, t3ArchiveStreamItem{
		Kind:     "snapshot",
		Snapshot: &t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 10, Threads: shellTestThreads(shellTestThread("th-fresh", "写后"))},
	}, connC); err != nil {
		t.Fatalf("apply post-change snapshot: %v", err)
	}
	if state.archiveReconnectPendingGen != 0 {
		t.Fatalf("post-change snapshot must clear the fulfilled memo, pending = %d", state.archiveReconnectPendingGen)
	}
	snaps := state.Snapshots()
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

	connA := stReconnectTarget(state)
	if gen := stRequestReconnect(state); gen != 1 {
		t.Fatalf("request generation = %d, want 1", gen)
	}
	// 同世代快照清备忘：连接 B 邮戳=1，它拿到的快照「确在请求之后」。
	connB := stReconnectTarget(state)
	if connB != 1 {
		t.Fatalf("connB postmark = %d, want 1", connB)
	}
	if err := stApplyLegacyItem(state, t3ArchiveStreamItem{
		Kind:     "snapshot",
		Snapshot: &t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 3, Threads: shellTestThreads()},
	}, connB); err != nil {
		t.Fatalf("apply snapshot: %v", err)
	}
	if state.archiveReconnectPendingGen != 0 {
		t.Fatalf("snapshot from a connection that postdates the request must clear the memo, pending = %d", state.archiveReconnectPendingGen)
	}
	// 备忘已清时晚到/重复的信号为假（忽略即可）。
	if state.ConsumeLegacyReconnect() {
		t.Fatalf("cleared memo must not consume")
	}
	// 连接终止作废备忘但 issued 不回卷：再记请求世代继续推进。
	if gen := stRequestReconnect(state); gen != 2 {
		t.Fatalf("re-request must issue a fresh generation, got %d", gen)
	}
	stMarkFailed(state, errors.New("conn dead"))
	if state.archiveReconnectPendingGen != 0 {
		t.Fatalf("connection end must void the memo, pending = %d", state.archiveReconnectPendingGen)
	}
	if gen := stRequestReconnect(state); gen != 3 {
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
	if err := stApplyLegacyItem(state, t3ArchiveStreamItem{
		Kind:     "snapshot",
		Snapshot: &t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
	}, stReconnectTarget(state)); err != nil {
		t.Fatalf("apply initial snapshot: %v", err)
	}

	// 写后失效记备忘（gen1）→ conn1 被拆、conn2 重连（邮戳=1）。
	stRequestReconnect(state)
	state.ConsumeLegacyReconnect()
	conn2 := stReconnectTarget(state) // =1

	// conn2 的权威快照落地前先记第二次失效（gen2）：conn2 之后送达的任
	// 何快照都是「先于 gen2 发出」的写前状态。
	if gen := stRequestReconnect(state); gen != 2 {
		t.Fatalf("second reconnect request generation = %d, want 2", gen)
	}

	// 写前快照落地（邮戳1 < 备忘gen2）：换数据、保持不可信、备忘不清。
	if err := stApplyLegacyItem(state, t3ArchiveStreamItem{
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
	snaps := state.Snapshots()
	if len(snaps.Archived.Threads) != 1 || snaps.Archived.Threads[0].ID != "th-stale" {
		t.Fatalf("pre-change snapshot must swap the rows, got %+v", snaps.Archived.Threads)
	}
	if snaps.ArchiveErr == nil {
		t.Fatalf("view must report the archive face as untrusted")
	}

	// 兑现：conn3（邮戳=2）的写后快照落地——恢复可信、清备忘。
	state.ConsumeLegacyReconnect()
	conn3 := stReconnectTarget(state) // =2
	if err := stApplyLegacyItem(state, t3ArchiveStreamItem{
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
	snaps = state.Snapshots()
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
	if err := stApplyActive(state, shellTestAuthoritative(7, projects, shellTestThreads(shellTestThread("th-1", "一"), shellTestThread("th-2", "二")))); err != nil {
		t.Fatalf("apply authoritative: %v", err)
	}

	// 集合未变的快照：无移除。
	removed, _, err := stApplyTracked(state, shellTestAuthoritative(8, projects, shellTestThreads(shellTestThread("th-1", "一"), shellTestThread("th-2", "二"))))
	if err != nil || len(removed) != 0 {
		t.Fatalf("identical snapshot must track no removals, got %v (err %v)", removed, err)
	}

	// 元数据帧：无移除（不是权威数据）。
	removed, _, err = stApplyTracked(state, shellTestEnrichment(9, projects))
	if err != nil || len(removed) != 0 {
		t.Fatalf("enrichment frame must track no removals, got %v (err %v)", removed, err)
	}

	// 全量快照丢掉 th-2（断连窗口里被别的客户端归档）：移除集须含 th-2。
	removed, _, err = stApplyTracked(state, shellTestAuthoritative(10, projects, shellTestThreads(shellTestThread("th-1", "一"))))
	if err != nil {
		t.Fatalf("apply shrinking snapshot: %v", err)
	}
	if len(removed) != 1 || removed[0] != "th-2" {
		t.Fatalf("shrinking snapshot must track th-2 as removed, got %v", removed)
	}
	snaps := state.Snapshots()
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
	if err := stApplyActive(state, shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "活跃")))); err != nil {
		t.Fatalf("apply active: %v", err)
	}
	archivedAt := "2026-10-01T00:00:00.000Z"
	row := func(id, title, status string) *t3ThreadShell {
		return &t3ThreadShell{ID: id, ProjectID: "p-1", Title: title, Status: status, ArchivedAt: &archivedAt, UpdatedAt: "2026-10-01T08:00:00.000Z"}
	}
	if applied, applyErr := stApplySnap(state, t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 5, Threads: shellTestThreads(row("th-a", "归档", "idle"), row("th-b", "归档二", "idle"))}, stGen(state)); !applied || applyErr != nil {
		t.Fatalf("apply archive snapshot must be trusted")
	}

	rows, errText := stFingerprint(state)
	if len(rows) != 2 || rows[0].ID != "th-a" || rows[1].ID != "th-b" {
		t.Fatalf("fingerprint rows = %+v", rows)
	}
	if errText != "" {
		t.Fatalf("trusted face must have empty err text, got %q", errText)
	}

	// 同集合、同内容重拉：指纹不变（例行重拉静默的依据）。
	if applied, applyErr := stApplySnap(state, t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 6, Threads: shellTestThreads(row("th-a", "归档", "idle"), row("th-b", "归档二", "idle"))}, stGen(state)); !applied || applyErr != nil {
		t.Fatalf("re-apply must be trusted")
	}
	rows2, errText2 := stFingerprint(state)
	if !slices.Equal(rows, rows2) || errText2 != errText {
		t.Fatalf("identical re-pull must keep the fingerprint: %+v vs %+v", rows, rows2)
	}

	// 同集合、行内容变（改名 + 状态翻转）：ID 序列不变、摘要变——发布端
	// 据此发轻量 State 而不是静默。
	if applied, applyErr := stApplySnap(state, t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 7, Threads: shellTestThreads(row("th-a", "归档（改名）", "completed"), row("th-b", "归档二", "idle"))}, stGen(state)); !applied || applyErr != nil {
		t.Fatalf("apply renamed snapshot must be trusted")
	}
	rows3, _ := stFingerprint(state)
	if slices.Equal(rows, rows3) {
		t.Fatalf("content change with same collection must change the fingerprint")
	}
	ids3 := archiveFingerprintIDs(rows3)
	if !slices.Equal(ids3, archiveFingerprintIDs(rows)) {
		t.Fatalf("content change must keep the ID sequence, got %v vs %v", ids3, archiveFingerprintIDs(rows))
	}

	// 集合变（新增 th-c）：ID 序列变——发布端据此发 Full。
	if applied, applyErr := stApplySnap(state, t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 8, Threads: shellTestThreads(row("th-a", "归档（改名）", "completed"), row("th-b", "归档二", "idle"), row("th-c", "归档三", "idle"))}, stGen(state)); !applied || applyErr != nil {
		t.Fatalf("apply grown snapshot must be trusted")
	}
	rows4, _ := stFingerprint(state)
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
	if err := stApplyActive(state, shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-live", "活跃")))); err != nil {
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
		if applied, applyErr := stApplySnap(state, t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 5, Threads: shellTestThreads(thread)}, stGen(state)); !applied || applyErr != nil {
			t.Fatalf("apply archive snapshot must be trusted")
		}
	}

	apply(row(onePR, nil))
	rows, _ := stFingerprint(state)
	if len(rows) != 1 || rows[0].PullRequests != 1 {
		t.Fatalf("digest must carry the visible PR count, got %+v", rows)
	}

	// 同集合 PR 数变（外部关联了第二个 PR，updatedAt 不变）：ID 序列不变、
	// 摘要变——发布端据此发轻量 State 而不是静默。
	apply(row(twoPRs, nil))
	rows2, _ := stFingerprint(state)
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
	rows3, _ := stFingerprint(state)
	if slices.Equal(rows2, rows3) || rows3[0].ParentThreadID != "th-parent-2" {
		t.Fatalf("parent thread change must change the fingerprint, got %+v", rows3[0])
	}

	// 非子代理行不携带父线程字段（与 normalizeT3ThreadShell 同一判定）。
	apply(row(twoPRs, nil))
	rows4, _ := stFingerprint(state)
	if rows4[0].ParentThreadID != "" {
		t.Fatalf("non-subagent row must not carry a parent, got %+v", rows4[0])
	}
}

// 成功落地窗口不得再撤可信（跟进 PR①，Codex :214）：重拉可能已经成功落地
// （archiveReady=true、validUntil 在未来），只是 kick 的后台 defer 还没清
// inFlight/refetchStartedAt。此刻按「在途过久」撤可信会把刚恢复的面重新
// 打回不可信；inFlight 清空后，后续准入看到未来的 validUntil 不再重拉，
// 降级就会持续整个 TTL。撤可信经 BeginFetch 的 Effects（Publish+Refresh）
// 表达。
func TestShellState_AdmitDoesNotRevokeAfterSuccessfulLanding(t *testing.T) {
	base := time.Now()
	now := base
	state := newT3ShellState()
	state.now = func() time.Time { return now }
	state.archiveTTL = time.Minute
	state.archiveStaleGrace = 50 * time.Millisecond

	// 可信落地：validUntil 封到未来。
	ticket, admitted, _ := state.BeginFetch()
	if !admitted {
		t.Fatalf("fresh state must admit")
	}
	state.EndFetch(ticket, t3FetchOutcome{kind: t3FetchSnapshot, snapshot: t3ArchivedShellSnapshot{
		SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}})

	// 模拟「落地已提交、收尾 defer 未执行」的窗口：inFlight 仍置位、
	// refetchStartedAt 是很久以前。
	state.mu.Lock()
	state.inFlight = true
	state.refetchStartedAt = now.Add(-time.Minute)
	state.mu.Unlock()

	_, admitted, eff := state.BeginFetch()
	if admitted || eff.Publish != t3PublishNone {
		t.Fatalf("freshly landed snapshot must not be revoked, admitted=%v eff=%+v", admitted, eff)
	}
	if snaps := state.Snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("successful landing window must stay trusted, got %v", snaps.ArchiveErr)
	}

	// 反例（保持原语义）：缓存确实过期时，在途过久仍要撤可信（经 Effects 表达）。
	now = now.Add(2 * time.Minute)
	_, admitted, eff = state.BeginFetch()
	if admitted || eff.Publish == t3PublishNone || !eff.Refresh {
		t.Fatalf("expired cache with overlong refetch must revoke trust, admitted=%v eff=%+v", admitted, eff)
	}
	revokedSnaps := state.Snapshots()
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
	gen := stGen(state)
	if applied, err := stApplySnap(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		gen); !applied || err != nil {
		t.Fatalf("seed archive: applied=%v err=%v", applied, err)
	}

	if registered := stUncertain(state, []string{"th-x"}, 40*time.Millisecond); registered != 1 {
		t.Fatalf("marking a live face uncertain must register, got registered=%d", registered)
	}
	if snaps := state.Snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("uncertain membership must report stale, got %v", snaps.ArchiveErr)
	}
	// 行仍保留、面仍 ready（只是不宣称完整）。
	if st := state.Status(); !st.ArchiveReady {
		t.Fatalf("uncertainty must not drop archiveReady")
	}

	// 到期由定时器驱动（不是读路径懒清，见 #156 R2）：清理要报告「需要发布事件」。
	time.Sleep(80 * time.Millisecond)
	if !stExpire(state) {
		t.Fatalf("expiry must report a trust transition to publish")
	}
	if snaps := state.Snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("uncertainty must self-heal after the settle deadline, got %v", snaps.ArchiveErr)
	}
	if len(state.Snapshots().Archived.Threads) != 1 {
		t.Fatalf("rows must be retained")
	}
}

// 逐个线程对账（#156 R1）：无关线程的增量不得替仍缺增量的线程恢复可信。
func TestShellState_ArchiveMembershipUncertainIsPerThread(t *testing.T) {
	state := newT3ShellState()
	gen := stGen(state)
	if applied, err := stApplySnap(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		gen); !applied || err != nil {
		t.Fatalf("seed archive: applied=%v err=%v", applied, err)
	}
	if registered := stUncertain(state, []string{"th-x", "th-y"}, time.Minute); registered != 2 {
		t.Fatalf("marking must register both ids, got registered=%d", registered)
	}

	// 只有 th-x 的增量到位：仍然不可信（th-y 未对账）。
	seqX := 2
	if err := stApplyLegacyItem(state, t3ArchiveStreamItem{
		Kind: "thread.updated", Sequence: &seqX,
		Thread: shellTestThread("th-x", "归档"),
	}, 0); err != nil {
		t.Fatalf("apply delta: %v", err)
	}
	if snaps := state.Snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("th-y is still unreconciled, must stay stale, got %v", snaps.ArchiveErr)
	}

	// th-y 的增量也不解账（#156 R15）：可能来自更早一轮归档周期，只有
	// 权威快照包含这些 ID 才算对账完成。
	seqY := 3
	if err := stApplyLegacyItem(state, t3ArchiveStreamItem{
		Kind: "thread.updated", Sequence: &seqY,
		Thread: shellTestThread("th-y", "归档"),
	}, 0); err != nil {
		t.Fatalf("apply delta: %v", err)
	}
	if snaps := state.Snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("an archive delta must not settle the uncertainty, got %v", snaps.ArchiveErr)
	}
	if applied, err := stApplySnap(state, t3ArchivedShellSnapshot{
		SchemaVersion: 1, SnapshotSequence: 4,
		Threads: shellTestThreads(shellTestThread("th-x", "归档"), shellTestThread("th-y", "归档")),
	}, stGen(state)); !applied || err != nil {
		t.Fatalf("apply archive snapshot: %v", err)
	}
	if snaps := state.Snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("all reconciled, trust must be restored, got %v", snaps.ArchiveErr)
	}
}

// 归档增量先到、活跃移除后到不得降级（#156 R2-1）：两条流顺序不定，归档面
// 已有该线程时成员关系已确定，不该再登记成「待对账」白降级一个宽限窗口。
func TestShellState_ArchiveMembershipAlreadyArchivedIsNotUncertain(t *testing.T) {
	state := newT3ShellState()
	gen := stGen(state)
	if applied, err := stApplySnap(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		gen); !applied || err != nil {
		t.Fatalf("seed archive: applied=%v err=%v", applied, err)
	}

	// 只有帧自身带着归档行（location!="active" 的 thread.updated）时，才允许
	// 以「归档面已有该行」跳过登记（#156 R13）；无行证明的移除仍须登记——
	// 归档面里的旧行可能早于本次活跃迁移。
	if registered := stUncertainSkip(state, []string{"th-a"}, time.Minute, true); registered != 0 {
		t.Fatalf("an archive row carried by the frame itself needs no reconciliation, got registered=%d", registered)
	}
	if registered := stUncertain(state, []string{"th-a"}, time.Minute); registered != 1 {
		t.Fatalf("a removal without row proof must still be registered, got registered=%d", registered)
	}
	// 反例：归档面没有的线程同样要登记。
	if registered := stUncertain(state, []string{"th-x"}, time.Minute); registered != 1 {
		t.Fatalf("unknown-to-archive thread must be registered, got registered=%d", registered)
	}
}

// 移除与「标不可信」必须原子（#156 R5）：订阅模式下 armed 后，活跃面移除在
// 应用的同一临界区里登记待对账——读者不会观测到「线程已离开活跃面、归档面却
// 仍宣称完整」的中间态（否则并发 wait 会据此把线程判 thread_gone）。
func TestShellState_ArmedRemovalMarksUncertainAtomically(t *testing.T) {
	state := newT3ShellState()
	gen := stGen(state)
	if applied, err := stApplySnap(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		gen); !applied || err != nil {
		t.Fatalf("seed archive: applied=%v err=%v", applied, err)
	}
	// 活跃面先有一行；随后权威快照替换掉它（removed 集）。
	seq1 := 1
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	_ = seq1
	stArm(state, true, time.Minute)

	seq2 := 2
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: seq2, Threads: shellTestThreads()},
	}); err != nil {
		t.Fatalf("apply snapshot removal: %v", err)
	}

	// 应用返回后立刻读：归档面必须已不可信（同一临界区登记，不依赖 hub 回调）。
	if snaps := state.Snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("armed removal must mark uncertainty inside the same critical section, got %v", snaps.ArchiveErr)
	}

	// 未 armed（按需快照模式）时不登记：可信度由失效路径负责。
	state2 := newT3ShellState()
	if applied, err := stApplySnap(state2,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		stGen(state2)); !applied || err != nil {
		t.Fatalf("seed archive2: %v", err)
	}
	if _, _, err := stApplyTracked(state2, t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("seed active2: %v", err)
	}
	if _, _, err := stApplyTracked(state2, t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2, Threads: shellTestThreads()},
	}); err != nil {
		t.Fatalf("apply snapshot removal2: %v", err)
	}
	if snaps := state2.Snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("unarmed state must not self-mark uncertainty, got %v", snaps.ArchiveErr)
	}
}

// 切入订阅模式的窗口内发生移除也必须记账（#156 R6）：此时归档面先是不可信的，
// 若跳过登记，等订阅快照把它恢复可信时，对应归档增量可能仍在队列里——wait
// 会把缺失线程当定论。快照只对账它确实包含的 ID，其余留到到期。
func TestShellState_RemovalDuringLegacySwitchIsRecorded(t *testing.T) {
	state := newT3ShellState()
	gen := stGen(state)
	if applied, err := stApplySnap(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		gen); !applied || err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("seed active: %v", err)
	}

	// 走生产切入订阅路径：EndFetch(method missing) 在同一临界区切
	// archiveLegacy、开启「移除即登记待对账」（armed ≡ legacy）并撤可信。
	state.archiveStaleGrace = 10 * time.Second
	ticket, admitted, _ := state.BeginFetch()
	if !admitted {
		t.Fatalf("must admit the on-demand probe")
	}
	if eff := state.EndFetch(ticket, t3FetchOutcome{kind: t3FetchMethodMissing, err: errors.New("method missing")}); !eff.ActivateLegacy {
		t.Fatalf("method missing must activate legacy mode, got %+v", eff)
	}

	// 面不可信期间发生移除：必须记账（否则快照恢复可信后无人再提它）。
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "thread.removed", Sequence: intPtrTestLocal(2), ThreadID: "th-1",
	}); err != nil {
		t.Fatalf("apply removal: %v", err)
	}

	// 订阅快照落地（权威全量，不含 th-1）：th-1 仍未对账，面不得宣称完整。
	if err := stApplyLegacyItem(state, t3ArchiveStreamItem{
		Kind: "snapshot", Snapshot: &t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
	}, 0); err != nil {
		t.Fatalf("apply legacy snapshot: %v", err)
	}
	if snaps := state.Snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("snapshot without the removed thread must not claim completeness, got %v", snaps.ArchiveErr)
	}

	// th-1 的归档增量不解账（#156 R15，可能来自更早一轮归档周期）；只有权威
	// 快照包含它才算对账完成。
	if err := stApplyLegacyItem(state, t3ArchiveStreamItem{
		Kind: "thread.updated", Sequence: intPtrTestLocal(3), Thread: shellTestThread("th-1", "归档"),
	}, 0); err != nil {
		t.Fatalf("apply delta: %v", err)
	}
	if snaps := state.Snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("an archive delta must not settle the uncertainty, got %v", snaps.ArchiveErr)
	}
	if applied, err := stApplySnap(state, t3ArchivedShellSnapshot{
		SchemaVersion: 1, SnapshotSequence: 4, Threads: shellTestThreads(shellTestThread("th-1", "归档")),
	}, stGen(state)); !applied || err != nil {
		t.Fatalf("apply archive snapshot: %v", err)
	}
	if snaps := state.Snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("after reconciliation the face must be trusted, got %v", snaps.ArchiveErr)
	}
}

// intPtrTestLocal 是测试内的小工具：取整型地址。
func intPtrTestLocal(v int) *int { return &v }

// arm 之后补登记：帧应用发生在 arm 之前时，armed 路径没登记过，帧后回调必须
// 补上（#156 R8-1）。按全局 armed 标志一刀切会漏掉这些 ID。
func TestShellState_RegistrationAfterArmCoversEarlierRemoval(t *testing.T) {
	state := newT3ShellState()
	if applied, err := stApplySnap(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		stGen(state)); !applied || err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("seed active: %v", err)
	}

	// 帧应用发生在 arm 之前：armed 路径没记账（未 armed）。
	removed, armedRegistered, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "thread.removed", Sequence: intPtrTestLocal(2), ThreadID: "th-1",
	})
	if err != nil {
		t.Fatalf("apply removal: %v", err)
	}
	// 未 armed：不自行登记，但仍要把「确实移走」的 ID 返回给 hub 补登记（R9：
	// 登记只认活跃面真实移除的 ID）。
	if armedRegistered || len(removed) != 1 || removed[0] != "th-1" {
		t.Fatalf("unarmed apply must report the removed id without self-registering, removed=%v armed=%v", removed, armedRegistered)
	}
	if snaps := state.Snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("unarmed state must not be stale yet, got %v", snaps.ArchiveErr)
	}

	// arm 之后、帧后回调补登记：必须真的登记（不能因 armed 而跳过）。
	stArm(state, true, time.Minute)
	if pending := stUncertain(state, []string{"th-1"}, time.Minute); pending != 1 {
		t.Fatalf("post-callback registration must cover pre-arm removals, got pending=%d", pending)
	}
	if snaps := state.Snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("face must become stale, got %v", snaps.ArchiveErr)
	}
}

// 离开订阅模式必须清空待对账集合（#156 R8-2）：这些条目没有定时器（调度在 hub
// 帧后回调里，可能被模式翻转绕过），留着会让后续订阅快照永远报 stale。
func TestShellState_ResetClearsPendingUncertain(t *testing.T) {
	state := newT3ShellState()
	if applied, err := stApplySnap(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		stGen(state)); !applied || err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	stArm(state, true, time.Minute)
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "thread.removed", Sequence: intPtrTestLocal(2), ThreadID: "th-1",
	}); err != nil {
		t.Fatalf("apply removal: %v", err)
	}
	if snaps := state.Snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("armed removal must mark uncertainty, got %v", snaps.ArchiveErr)
	}

	// 走生产路径 LegacyMethodMissing（切回按需快照）：必须关掉登记并清空
	// 遗留条目——它们没有定时器，留着会让后续订阅快照永远报 stale。
	state.LegacyMethodMissing()

	// 下一次订阅快照（不含 th-1）落地后必须恢复可信，而不是永远 stale。
	if err := stApplyLegacyItem(state, t3ArchiveStreamItem{
		Kind: "snapshot", Snapshot: &t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 2, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
	}, 0); err != nil {
		t.Fatalf("apply snapshot: %v", err)
	}
	if snaps := state.Snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("cleared pending set must not keep the face stale, got %v", snaps.ArchiveErr)
	}
}

// 归档流的 thread.removed 不能算对账完成（#156 R9）：它可能来自更早的一次
// unarchive（两条流顺序无保证），拿来解账会提前恢复可信、误判 thread_gone。
func TestShellState_ArchiveRemovedDoesNotSettle(t *testing.T) {
	state := newT3ShellState()
	if applied, err := stApplySnap(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		stGen(state)); !applied || err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	stArm(state, true, time.Minute)
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "thread.removed", Sequence: intPtrTestLocal(2), ThreadID: "th-1",
	}); err != nil {
		t.Fatalf("apply active removal: %v", err)
	}
	if snaps := state.Snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("armed removal must mark uncertainty, got %v", snaps.ArchiveErr)
	}

	// 归档流投递同名 removed（可能是更早的 unarchive）：不得解账。
	if err := stApplyLegacyItem(state, t3ArchiveStreamItem{
		Kind: "thread.removed", Sequence: intPtrTestLocal(3), ThreadID: "th-1",
	}, 0); err != nil {
		t.Fatalf("apply archive removal: %v", err)
	}
	if snaps := state.Snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("archive thread.removed must not settle the uncertainty, got %v", snaps.ArchiveErr)
	}

	// 增量同样不解账（#156 R15）：只有权威快照包含该 ID 才算对账完成。
	if err := stApplyLegacyItem(state, t3ArchiveStreamItem{
		Kind: "thread.updated", Sequence: intPtrTestLocal(4), Thread: shellTestThread("th-1", "归档"),
	}, 0); err != nil {
		t.Fatalf("apply archive update: %v", err)
	}
	if snaps := state.Snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("an archive delta must not settle the uncertainty, got %v", snaps.ArchiveErr)
	}
	if applied, err := stApplySnap(state, t3ArchivedShellSnapshot{
		SchemaVersion: 1, SnapshotSequence: 5, Threads: shellTestThreads(shellTestThread("th-1", "归档")),
	}, stGen(state)); !applied || err != nil {
		t.Fatalf("apply archive snapshot: %v", err)
	}
	if snaps := state.Snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("an archive snapshot settles the uncertainty, got %v", snaps.ArchiveErr)
	}
}

// 噪声移除不登记（#156 R9）：对不在活跃面的 ID 的移除不该把整个环境降级一个
// 宽限窗口。
func TestShellState_NoiseRemovalDoesNotGoUncertain(t *testing.T) {
	state := newT3ShellState()
	if applied, err := stApplySnap(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		stGen(state)); !applied || err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	stArm(state, true, time.Minute)
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "thread.removed", Sequence: intPtrTestLocal(2), ThreadID: "th-ghost",
	}); err != nil {
		t.Fatalf("apply noise removal: %v", err)
	}
	if snaps := state.Snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("removing an absent thread must not degrade the face, got %v", snaps.ArchiveErr)
	}
}

// 没有宽限就没有定时器：settle<=0 时宁可不登记（#156 R9 Note），否则待对账
// 永远清不掉、归档面一直 stale。
func TestShellState_ZeroSettleDoesNotRegister(t *testing.T) {
	state := newT3ShellState()
	if applied, err := stApplySnap(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		stGen(state)); !applied || err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	stArm(state, true, 0)
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "thread.removed", Sequence: intPtrTestLocal(2), ThreadID: "th-1",
	}); err != nil {
		t.Fatalf("apply removal: %v", err)
	}
	if snaps := state.Snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("zero settle must not register an unrecoverable uncertainty, got %v", snaps.ArchiveErr)
	}
	if pending := stUncertain(state, []string{"th-1"}, 0); pending != 0 {
		t.Fatalf("zero settle must reject registration, got pending=%d", pending)
	}
}

// 线程回到活跃面即解账（#156 R12）：活跃优先——它已确定存在，归档归属不再影响
// 「两面都没有」的判定，不该白降级一个宽限窗口。
func TestShellState_ActiveReappearanceClearsUncertainty(t *testing.T) {
	state := newT3ShellState()
	if applied, err := stApplySnap(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		stGen(state)); !applied || err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	stArm(state, true, time.Minute)
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "thread.removed", Sequence: intPtrTestLocal(2), ThreadID: "th-1",
	}); err != nil {
		t.Fatalf("apply removal: %v", err)
	}
	if snaps := state.Snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("removal must mark uncertainty, got %v", snaps.ArchiveErr)
	}

	// unarchive 回到活跃面（location=active 的 updated）。
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "thread.updated", Sequence: intPtrTestLocal(3), Location: "active",
		Thread: shellTestThread("th-1", "线程"),
	}); err != nil {
		t.Fatalf("apply unarchive: %v", err)
	}
	if snaps := state.Snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("reappearance in the active face must clear uncertainty, got %v", snaps.ArchiveErr)
	}

	// 活跃全量快照带回该线程同样解账。
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "thread.removed", Sequence: intPtrTestLocal(4), ThreadID: "th-1",
	}); err != nil {
		t.Fatalf("apply removal again: %v", err)
	}
	if snaps := state.Snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("second removal must mark uncertainty again, got %v", snaps.ArchiveErr)
	}
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 5, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("apply active snapshot: %v", err)
	}
	if snaps := state.Snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("active snapshot re-including the thread must clear uncertainty, got %v", snaps.ArchiveErr)
	}
}

// 真实失败文案不得被归属不确定覆盖（#156 R12 Note）：否则到期路径会把它清成
// nil，把一次真实拉取失败静默改写成可信。
func TestShellState_RealFailureNotOverwrittenByUncertainty(t *testing.T) {
	state := newT3ShellState()
	if applied, err := stApplySnap(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		stGen(state)); !applied || err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	realErr := errors.New("归档读取失败: 上游 500")
	if eff := stRecordFailure(state, realErr, stGen(state)); !errors.Is(eff.Err, realErr) || eff.Refetch {
		t.Fatalf("current-generation failure must be recorded without a refetch, got %+v", eff)
	}

	if registered := stUncertain(state, []string{"th-x"}, time.Minute); registered != 1 {
		t.Fatalf("uncertainty must still be registered, got %d", registered)
	}
	if snaps := state.Snapshots(); !errors.Is(snaps.ArchiveErr, realErr) {
		t.Fatalf("the real failure must survive uncertainty, got %v", snaps.ArchiveErr)
	}
	// 到期清理不得把真实失败改写成可信。
	if stExpire(state) {
		t.Fatalf("expiry must not report a trust transition over a real failure")
	}
	if snaps := state.Snapshots(); !errors.Is(snaps.ArchiveErr, realErr) {
		t.Fatalf("expiry must keep the real failure, got %v", snaps.ArchiveErr)
	}
}

// 归档面里的旧行不能当作「本次活跃迁移已对账」（#156 R13）：A 被 unarchive
// 后又 archive，旧行仍在归档面时收到活跃 thread.removed——该帧不带行证明，
// 必须照常登记，否则旧行随后被归档流的排队 removed 删掉、而新增量未到时
// wait 会把 A 判成 thread_gone。
func TestShellState_StaleArchiveRowDoesNotSkipRegistration(t *testing.T) {
	state := newT3ShellState()
	if applied, err := stApplySnap(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"), shellTestThread("th-1", "归档"))},
		stGen(state)); !applied || err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	stArm(state, true, time.Minute)

	// thread.removed 不带归档行：即使归档面已有旧行也必须登记。
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "thread.removed", Sequence: intPtrTestLocal(2), ThreadID: "th-1",
	}); err != nil {
		t.Fatalf("apply removal: %v", err)
	}
	if snaps := state.Snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("stale archive row must not skip registration, got %v", snaps.ArchiveErr)
	}
}

// 帧自身带归档行（location!="active" 的 thread.updated）时，归档面已有该行
// 就是「已对账」的证据：不登记、不降级（#156 R2-1 / R13）。
func TestShellState_FrameWithArchiveRowSkipsRegistration(t *testing.T) {
	state := newT3ShellState()
	if applied, err := stApplySnap(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"), shellTestThread("th-1", "归档"))},
		stGen(state)); !applied || err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	stArm(state, true, time.Minute)

	// 归档面已有 th-1：这条 updated 说明归档流已经确认过它，无需降级。
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "thread.updated", Sequence: intPtrTestLocal(2), Location: "archive",
		Thread: shellTestThread("th-1", "归档"),
	}); err != nil {
		t.Fatalf("apply archive-located update: %v", err)
	}
	if snaps := state.Snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("frame carrying an existing archive row needs no reconciliation, got %v", snaps.ArchiveErr)
	}
}

// proven 只能由 state 在「已 arm 且归档面确实已有该行」时给出（#156 R14）：
// 帧在 arm **之前**应用时 armed=false 只是「没登记」，不构成「已证明」——
// hub 若按帧型推断成 proven，排队的归档增量会被跳过，wait 可能判 thread_gone。
func TestShellState_ProvenRequiresArmedJudgement(t *testing.T) {
	state := newT3ShellState()
	if applied, err := stApplySnap(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "归档"))},
		stGen(state)); !applied || err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	if _, _, _, err := stApplyDetailed(state, t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("seed active: %v", err)
	}

	// arm 之前应用：不是 proven。
	removed, armed, proven, err := stApplyDetailed(state, t3ShellStreamItem{
		Kind: "thread.updated", Sequence: intPtrTestLocal(2), Location: "archive",
		Thread: shellTestThread("th-1", "归档"),
	})
	if err != nil {
		t.Fatalf("apply before arm: %v", err)
	}
	if len(removed) != 1 || armed || len(proven) != 0 {
		t.Fatalf("a frame applied before arming proves nothing, got removed=%v armed=%v proven=%v", removed, armed, proven)
	}

	// arm 之后同一帧才算 proven（归档面已有该行）。
	stArm(state, true, time.Minute)
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 3, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("restore active: %v", err)
	}
	removed, armed, proven, err = stApplyDetailed(state, t3ShellStreamItem{
		Kind: "thread.updated", Sequence: intPtrTestLocal(4), Location: "archive",
		Thread: shellTestThread("th-1", "归档"),
	})
	if err != nil {
		t.Fatalf("apply after arm: %v", err)
	}
	if len(removed) != 1 || armed || len(proven) != 1 || proven[0] != "th-1" {
		t.Fatalf("armed frame with an existing archive row is proven, got removed=%v armed=%v proven=%v", removed, armed, proven)
	}
}

// 只有「晚于登记时刻」采集的归档快照才算对账证据（#156 R16）：重连快照可能
// 在活跃移除之前就采好、之后才送达，它的包含关系不能清掉本轮待对账。
func TestShellState_PrestaleSnapshotDoesNotSettleUncertainty(t *testing.T) {
	state := newT3ShellState()
	if applied, err := stApplySnap(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		stGen(state)); !applied || err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	stArm(state, true, time.Minute)
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "thread.removed", Sequence: intPtrTestLocal(2), ThreadID: "th-1",
	}); err != nil {
		t.Fatalf("apply removal: %v", err)
	}
	covered := t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 3, Threads: shellTestThreads(shellTestThread("th-1", "归档"))}

	// 采集时刻早于登记：不算证据。
	// 采集早于登记：按变更前状态处理（applied=false，不封窗），且不得解账。
	if _, err := stApplySnapAt(state, covered, stGen(state), time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("apply stale snapshot: %v", err)
	}
	if snaps := state.Snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("a snapshot captured before the removal must not settle it, got %v", snaps.ArchiveErr)
	}

	// 采集时刻晚于登记：算证据，恢复可信。
	if applied, err := stApplySnapAt(state, covered, stGen(state), time.Now().Add(time.Second)); !applied || err != nil {
		t.Fatalf("apply fresh snapshot: %v", err)
	}
	if snaps := state.Snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("a snapshot captured after the removal settles it, got %v", snaps.ArchiveErr)
	}
}

// 待对账集合清空时必须同步清掉登记时刻表（#156 R20）：否则残留的旧时刻会
// 跟着同一 ID 的下一次登记，把之后的权威快照误判成「采集太早」。
func TestShellState_UncertaintySinceFollowsUncertainMap(t *testing.T) {
	state := newT3ShellState()
	if applied, err := stApplySnap(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		stGen(state)); !applied || err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	stArm(state, true, 20*time.Millisecond)
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "thread.removed", Sequence: intPtrTestLocal(2), ThreadID: "th-1",
	}); err != nil {
		t.Fatalf("apply removal: %v", err)
	}

	time.Sleep(40 * time.Millisecond)
	stExpire(state)
	state.mu.Lock()
	pending, since := len(state.archiveUncertain), len(state.archiveUncertainSince)
	state.mu.Unlock()
	if pending != 0 || since != 0 {
		t.Fatalf("expiry must drop both maps, got pending=%d since=%d", pending, since)
	}
}

// 归档流删行同时线程不在活跃面时必须记成待对账（#156 R24）：这条 removed 可能
// 来自更早一轮 unarchive，删完就是「两面都没有」，面还可信 → 会把存在的线程
// 判成 thread_gone；等权威快照/宽限到期再下结论。
func TestShellState_ArchiveRemovalOfActiveAbsentThreadMarksUncertain(t *testing.T) {
	state := newT3ShellState()
	if applied, err := stApplySnap(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "归档"))},
		stGen(state)); !applied || err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	stArm(state, true, time.Minute)
	if err := stApplyLegacyItem(state, t3ArchiveStreamItem{
		Kind: "thread.removed", Sequence: intPtrTestLocal(2), ThreadID: "th-1",
	}, 0); err != nil {
		t.Fatalf("apply archive removal: %v", err)
	}
	if snaps := state.Snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("removing the only trace of a thread must mark uncertainty, got %v", snaps.ArchiveErr)
	}
}

// 待对账到期后，早于那次变更采集的归档快照仍然不算证据（#156 R28）：否则一条
// 移除前采好、迟到超过宽限的快照会把面恢复可信，而线程两面都没有 → thread_gone。
func TestShellState_ExpiryKeepsFreshnessFloor(t *testing.T) {
	state := newT3ShellState()
	if applied, err := stApplySnap(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		stGen(state)); !applied || err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	stArm(state, true, 20*time.Millisecond)
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "thread.removed", Sequence: intPtrTestLocal(2), ThreadID: "th-1",
	}); err != nil {
		t.Fatalf("apply removal: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	stExpire(state)

	// 采集时刻早于那次变更的快照整条丢弃（#156 R39）：既不覆盖行、也不改可信度。
	if _, err := stApplySnapAt(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 3, Threads: nil},
		stGen(state), time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("apply stale snapshot: %v", err)
	}
	if snaps := state.Snapshots(); len(snaps.Archived.Threads) != 1 || snaps.Archived.Threads[0].ID != "th-a" {
		t.Fatalf("a pre-transition snapshot must not replace rows, got %+v", snaps.Archived.Threads)
	}

	// 之后采集的快照可以恢复可信。
	if _, err := stApplySnapAt(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 4, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		stGen(state), time.Now().Add(time.Second)); err != nil {
		t.Fatalf("apply fresh snapshot: %v", err)
	}
	if snaps := state.Snapshots(); snaps.ArchiveErr != nil {
		t.Fatalf("a snapshot captured after the transition restores trust, got %v", snaps.ArchiveErr)
	}
}

// 同一批「先移除再回现」的线程不该被帧后回调重新登记（#156 R36）。
func TestShellState_ExcludeActiveThreads(t *testing.T) {
	state := newT3ShellState()
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "thread.updated", Sequence: intPtrTestLocal(2), Location: "active",
		Thread: shellTestThread("th-1", "线程"),
	}); err != nil {
		t.Fatalf("apply active update: %v", err)
	}
	if got := stExclude(state, []string{"th-1", "th-2"}); len(got) != 1 || got[0] != "th-2" {
		t.Fatalf("only non-active threads may stay, got %v", got)
	}
}

// 订阅路径：变更前采集的快照（observedAt 早于 floor）只换数据、不能封窗，
// 也不再请求重连（#156 R38：批量归档期间重连风暴比多等一个宽限更糟）。
func TestShellState_StreamPrestaleSnapshotDoesNotReconnect(t *testing.T) {
	state := newT3ShellState()
	if applied, err := stApplySnap(state,
		t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))},
		stGen(state)); !applied || err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "snapshot", Snapshot: &t3ShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "线程"))},
	}); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	stArm(state, true, time.Minute)
	if _, _, err := stApplyTracked(state, t3ShellStreamItem{
		Kind: "thread.removed", Sequence: intPtrTestLocal(2), ThreadID: "th-1",
	}); err != nil {
		t.Fatalf("apply removal: %v", err)
	}
	if err := stApplyLegacyItemAt(state, t3ArchiveStreamItem{
		Kind: "snapshot", Sequence: intPtrTestLocal(3),
		Snapshot: &t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 3, Threads: shellTestThreads(shellTestThread("th-1", "归档"))},
	}, 0, time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("apply stream snapshot: %v", err)
	}
	if snaps := state.Snapshots(); !errors.Is(snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("prestale snapshot must not be trusted, got %v", snaps.ArchiveErr)
	}
	if gen := stReconnectTarget(state); gen != 0 {
		t.Fatalf("prestale snapshot must not force a reconnect, got gen=%d", gen)
	}
}

// 首条订阅快照就是变更前状态时必须请求重连（#156 R40）：否则面永远不就绪；
// 但面已就绪时保持安静，避免批量归档期间的连接风暴。
func TestShellState_FirstPrestaleStreamSnapshotRequestsReconnect(t *testing.T) {
	state := newT3ShellState()
	// 先制造一次归属变更（floor 前推），面尚未就绪。
	stArm(state, true, time.Minute)
	if pending := stUncertain(state, []string{"th-1"}, time.Minute); pending != 1 {
		t.Fatalf("seed uncertainty: pending=%d", pending)
	}
	if err := stApplyLegacyItemAt(state, t3ArchiveStreamItem{
		Kind: "snapshot", Sequence: intPtrTestLocal(1),
		Snapshot: &t3ArchivedShellSnapshot{SchemaVersion: 1, SnapshotSequence: 1, Threads: shellTestThreads(shellTestThread("th-1", "归档"))},
	}, 0, time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("apply first prestale snapshot: %v", err)
	}
	if gen := stReconnectTarget(state); gen == 0 {
		t.Fatalf("an unready face must request a reconnect for a pre-transition snapshot")
	}
}

// ==================== INT-216 夹具 ====================
//
// 以下 shim 把既有断言迁到新短接口：写路径一律经 ApplyActive/ApplyLegacy/
// BeginFetch/EndFetch/Invalidate/LegacyEnded/ConsumeLegacyReconnect/
// LegacyMethodMissing/IsLegacy/Settle 与返回的 t3ShellEffects，断言面是
// Snapshots/Status/Fingerprint。少数需要逐帧簿记观测（removed/proven、
// commit 回调）的用例经私有内核驱动——内核是 ApplyActive/EndFetch 的实现
// 细节，测试借它观测中间量，不构成对外接口。

func stApplyActive(s *t3ShellState, it t3ShellStreamItem) error {
	_, err := s.ApplyActive([]t3ShellStreamItem{it})
	return err
}

func stApplyTracked(s *t3ShellState, it t3ShellStreamItem) ([]string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed, armed, _, err := s.applyActiveItemDetailedLocked(&it)
	return removed, armed, err
}

func stApplyDetailed(s *t3ShellState, it t3ShellStreamItem) ([]string, bool, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyActiveItemDetailedLocked(&it)
}

func stApplySnap(s *t3ShellState, snap t3ArchivedShellSnapshot, gen int64) (bool, error) {
	return stApplySnapAt(s, snap, gen, time.Time{})
}

func stApplySnapAt(s *t3ShellState, snap t3ArchivedShellSnapshot, gen int64, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyOnDemandSnapshotCommitted(snap, gen, at)
}

func stApplyLegacyItem(s *t3ShellState, it t3ArchiveStreamItem, connStart int64) error {
	return stApplyLegacyItemAt(s, it, connStart, time.Now())
}

func stApplyLegacyItemAt(s *t3ShellState, it t3ArchiveStreamItem, connStart int64, at time.Time) error {
	_, err := s.ApplyLegacy(t3LegacyStamp{gen: connStart, at: at}, []t3ArchiveStreamItem{it})
	return err
}

func stGen(s *t3ShellState) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.archiveGeneration
}

// stExpireReady 供已持 s.mu 的用例使用（与 stArm 同一约定）。
func stExpireReady(s *t3ShellState) {
	s.noteArchiveExpiredLocked()
}

func stArm(s *t3ShellState, armed bool, settle time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.archiveLegacy = armed
	s.archiveStaleGrace = settle
}

func stUncertain(s *t3ShellState, ids []string, settle time.Duration) int {
	return stUncertainSkip(s, ids, settle, false)
}

func stUncertainSkip(s *t3ShellState, ids []string, settle time.Duration, skipArchived bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, _ := s.noteArchiveMembershipUncertainLocked(ids, settle, skipArchived)
	return pending
}

func stExpire(s *t3ShellState) bool {
	return s.Settle().Refresh
}

func stEarliest(s *t3ShellState) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.earliestUncertainDeadlineLocked()
}

func stExclude(s *t3ShellState, ids []string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.excludeActiveThreadsLocked(ids)
}

func stPending(s *t3ShellState) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.archiveUncertain)
}

func stReconnectTarget(s *t3ShellState) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.archiveReconnectIssued
}

func stRequestReconnect(s *t3ShellState) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.noteArchiveReconnectRequestedLocked()
}

func stMarkFailed(s *t3ShellState, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markArchiveFailedLocked(err)
}

// stRecordFailure 经生产内核 recordFetchFailureLocked 记一次拉取失败
// （持 s.mu 直调），返回它产出的 Effects：Refetch 表达「写前失败须补拉」，
// Err 承载交给调用方的失败原因。
func stRecordFailure(s *t3ShellState, err error, generation int64) t3ShellEffects {
	s.mu.Lock()
	defer s.mu.Unlock()
	var eff t3ShellEffects
	s.recordFetchFailureLocked(generation, err, &eff)
	return eff
}

func stFingerprint(s *t3ShellState) ([]t3ArchiveRowDigest, string) {
	rows, errText, _ := s.Fingerprint()
	return rows, errText
}

// ==================== Active→Archive 合法/非法转移夹具 ====================
//
// 夹具驱动：给定观察序列（操作 → 断言 Status/Snapshots/Effects），每条
// case 独立起一个 state，逐条对应转移规则，不合并。

// 合法转移 1：按需 → legacy 仅经「方法不存在」。同一临界区内先记失败再切
// 模式（R5 P2）：EndFetch 返回后 IsLegacy 为真、ArchiveErr 承载真实失败、
// ActivateLegacy 要求放行旧面订阅。
func TestShellState_MethodMissingSwitchesToLegacy(t *testing.T) {
	state := newT3ShellState()
	ticket, admitted, _ := state.BeginFetch()
	if !admitted {
		t.Fatalf("fresh state must admit on-demand fetch")
	}
	eff := state.EndFetch(ticket, t3FetchOutcome{kind: t3FetchMethodMissing, err: errors.New("method not found: getArchivedShellSnapshot")})
	if !state.IsLegacy() {
		t.Fatalf("method missing must switch to legacy")
	}
	if !eff.ActivateLegacy {
		t.Fatalf("switch must activate the legacy subscription loop")
	}
	st := state.Status()
	if st.ArchiveReady || st.ArchiveErr == nil || !errors.Is(st.ArchiveErr, errT3ShellArchiveStale) && st.ArchiveErr == nil {
		t.Fatalf("switch must record the fetch failure, got %+v", st)
	}
	// legacy 下 BeginFetch 拒准入（常驻流供数，按需拨号是空转）。
	if _, admitted, _ := state.BeginFetch(); admitted {
		t.Fatalf("legacy mode must not admit on-demand fetch")
	}
}

// 非法转移 1：非 legacy 下 LegacyMethodMissing 必须 no-op——残余连接的
// 终止不得推翻已由按需面接管的归档数据（审修 R9 P2-1）。
func TestShellState_LegacyMethodMissingNoopWhenNotLegacy(t *testing.T) {
	state := newT3ShellState()
	if applied, err := stApplySnap(state, t3ArchivedShellSnapshot{SchemaVersion: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}, stGen(state)); !applied || err != nil {
		t.Fatalf("apply archive snapshot must land")
	}
	eff := state.LegacyMethodMissing()
	if eff != (t3ShellEffects{}) {
		t.Fatalf("non-legacy LegacyMethodMissing must be a no-op, got %+v", eff)
	}
	if !state.Status().ArchiveReady {
		t.Fatalf("on-demand archive data must not be revoked by a stray legacy signal")
	}
}

// 合法转移 2：双方法不存在回按需探测——legacy 面 subscribe 也被判方法不
// 存在（升级/误判证据）时 LegacyMethodMissing 退出 legacy，下一次读
// BeginFetch 重探测；retryAfter 冷却保留（R14 P2-2），同冷却窗内不重探测。
func TestShellState_DoubleMethodMissingResetsToOnDemand(t *testing.T) {
	state := newT3ShellState()
	state.archiveTTL = time.Minute
	ticket, admitted, _ := state.BeginFetch()
	if !admitted {
		t.Fatalf("must admit")
	}
	state.EndFetch(ticket, t3FetchOutcome{kind: t3FetchMethodMissing, err: errors.New("method not found")})
	if !state.IsLegacy() {
		t.Fatalf("must be legacy")
	}
	state.LegacyMethodMissing()
	if state.IsLegacy() {
		t.Fatalf("double method-missing must reset to on-demand probe")
	}
	// 探测冷却在窗内：BeginFetch 不准入，不产生 ping-pong。
	if _, admitted, _ := state.BeginFetch(); admitted {
		t.Fatalf("probe cooldown must block immediate re-probe")
	}
}

// 合法转移 3：待对账三条解除路径——权威归档快照包含线程、活跃面回现、
// 宽限期到期。
func TestShellState_UncertaintyResolvesBySnapshotReappearanceAndExpiry(t *testing.T) {
	// (a) 权威归档快照包含线程：legacy 订阅快照经 ApplyLegacy 落地解账。
	t.Run("snapshot", func(t *testing.T) {
		state := newT3ShellState()
		stArm(state, true, time.Minute)
		if err := stApplyActive(state, shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
			t.Fatalf("apply: %v", err)
		}
		seq := 2
		if _, err := state.ApplyActive([]t3ShellStreamItem{{Kind: "thread.removed", Sequence: &seq, Location: "active", ThreadID: "th-1"}}); err != nil {
			t.Fatalf("removal: %v", err)
		}
		if stPending(state) != 1 {
			t.Fatalf("legacy removal must register membership uncertainty")
		}
		stamp := state.LegacyStamp()
		if _, err := state.ApplyLegacy(stamp, []t3ArchiveStreamItem{{Kind: "snapshot", Snapshot: &t3ArchivedShellSnapshot{SchemaVersion: 1, Threads: shellTestThreads(shellTestThread("th-1", "归档"))}}}); err != nil {
			t.Fatalf("legacy snapshot: %v", err)
		}
		if stPending(state) != 0 {
			t.Fatalf("authoritative snapshot containing the thread must settle uncertainty")
		}
	})
	// (b) 活跃面回现（unarchive / 快照带回）解账（#156 R32）。
	t.Run("reappearance", func(t *testing.T) {
		state := newT3ShellState()
		stArm(state, true, time.Minute)
		if err := stApplyActive(state, shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
			t.Fatalf("apply: %v", err)
		}
		seq := 2
		if _, err := state.ApplyActive([]t3ShellStreamItem{{Kind: "thread.removed", Sequence: &seq, Location: "active", ThreadID: "th-1"}}); err != nil {
			t.Fatalf("removal: %v", err)
		}
		if stPending(state) != 1 {
			t.Fatalf("must register uncertainty")
		}
		seq = 3
		if _, err := state.ApplyActive([]t3ShellStreamItem{{Kind: "thread.updated", Sequence: &seq, Location: "active", Thread: shellTestThread("th-1", "一")}}); err != nil {
			t.Fatalf("reappearance: %v", err)
		}
		if stPending(state) != 0 {
			t.Fatalf("active reappearance must settle uncertainty")
		}
	})
	// (c) 宽限期到期：注入时钟推到截止之后，Settle 清账并恢复可信。
	t.Run("expiry", func(t *testing.T) {
		base := time.Now()
		now := base
		state := newT3ShellState()
		state.now = func() time.Time { return now }
		stArm(state, true, time.Minute)
		if err := stApplyActive(state, shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
			t.Fatalf("apply: %v", err)
		}
		// 先让归档面可信（订阅快照落地），移除才有可恢复的降级对象。
		if _, err := state.ApplyLegacy(state.LegacyStamp(), []t3ArchiveStreamItem{{Kind: "snapshot", Snapshot: &t3ArchivedShellSnapshot{SchemaVersion: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}}}); err != nil {
			t.Fatalf("legacy snapshot: %v", err)
		}
		seq := 2
		if _, err := state.ApplyActive([]t3ShellStreamItem{{Kind: "thread.removed", Sequence: &seq, Location: "active", ThreadID: "th-1"}}); err != nil {
			t.Fatalf("removal: %v", err)
		}
		if stPending(state) != 1 {
			t.Fatalf("must register uncertainty")
		}
		now = base.Add(2 * time.Minute)
		eff := state.Settle()
		if stPending(state) != 0 {
			t.Fatalf("expired entries must settle")
		}
		if !eff.Refresh {
			t.Fatalf("trust restoration must be published via Effects")
		}
	})
}

// 合法转移 4：按需模式移除与失效在同一临界区——ApplyActive 返回时归档面
// 已不可信，「活跃已删、归档未失效」的可读窗口不存在（INT-216 修的按需
// 模式移除窗口）。原实现里失效要等整个 Chunk 跑完在 defer 里执行。
func TestShellState_OnDemandRemovalInvalidatesAtomically(t *testing.T) {
	state := newT3ShellState()
	if err := stApplyActive(state, shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied, err := stApplySnap(state, t3ArchivedShellSnapshot{SchemaVersion: 1, Threads: shellTestThreads(shellTestThread("th-a", "归档"))}, stGen(state)); !applied || err != nil {
		t.Fatalf("archive snapshot must land")
	}
	seq := 2
	eff, err := state.ApplyActive([]t3ShellStreamItem{{Kind: "thread.removed", Sequence: &seq, Location: "active", ThreadID: "th-1"}})
	if err != nil {
		t.Fatalf("removal: %v", err)
	}
	st := state.Status()
	if st.ArchiveReady || st.ArchiveErr == nil {
		t.Fatalf("on-demand removal must revoke archive trust in the same critical section, got %+v", st)
	}
	if eff.Publish != t3PublishForceFull || !eff.Refresh {
		t.Fatalf("invalidation must request forceFull publish + refresh, got %+v", eff)
	}
}

// 非法转移 2：按需模式移除 → 切 legacy → 迟到的旧订阅快照不得封可信
// （原 R7/R8 竞态：帧后补登记与模式翻转的缝隙）。失效推进
// archiveFreshnessFloor，早于它采集的快照被整条丢弃。
func TestShellState_PrestaleSnapshotCannotSealTrustAfterRemoval(t *testing.T) {
	base := time.Now()
	now := base
	state := newT3ShellState()
	state.now = func() time.Time { return now }
	if err := stApplyActive(state, shellTestAuthoritative(1, nil, shellTestThreads(shellTestThread("th-1", "一")))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// 移除在按需模式发生：失效推进世代与 freshnessFloor。
	seq := 2
	if _, err := state.ApplyActive([]t3ShellStreamItem{{Kind: "thread.removed", Sequence: &seq, Location: "active", ThreadID: "th-1"}}); err != nil {
		t.Fatalf("removal: %v", err)
	}
	// 随后按需拉取判方法不存在 → 切 legacy。
	ticket, admitted, _ := state.BeginFetch()
	if !admitted {
		t.Fatalf("must admit")
	}
	state.EndFetch(ticket, t3FetchOutcome{kind: t3FetchMethodMissing, err: errors.New("method not found")})
	if !state.IsLegacy() {
		t.Fatalf("must be legacy")
	}
	// 迟到的订阅快照采集时刻早于移除：整条丢弃、不封窗。
	stamp := t3LegacyStamp{gen: 0, at: base.Add(-time.Hour)}
	if _, err := state.ApplyLegacy(stamp, []t3ArchiveStreamItem{{Kind: "snapshot", Snapshot: &t3ArchivedShellSnapshot{SchemaVersion: 1, Threads: shellTestThreads()}}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if state.Status().ArchiveReady {
		t.Fatalf("pre-change snapshot must not seal trust")
	}
}
