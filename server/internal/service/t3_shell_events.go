package service

import (
	"slices"
	"sync"
)

// T3 shell 变更通知（INT-132）：hub 在数据路径上发布轻量事件，SSE 端点
// （/api/threads/stream）推给浏览器，浏览器据此重拉列表/详情。设计要点：
//   - 发布只做内存合并与非阻塞信号，绝不写网络、绝不持 entry/hub.mu——
//     发布点位于流帧数据路径上，任何阻塞或重锁都会拖慢订阅本身。
//   - 同一环境的多次发布在订阅者侧合并成一条：full 逻辑或、ThreadIDs 并集
//     去重、State 取最新非空。消费者醒来一次 Drain 全部，重拉的粒度是
//     「环境」，不是单个事件。
//   - 跨用户隔离靠按用户分组的注册表：发布只进事件源所属用户的订阅。

// t3ShellEventMergeLimit 是合并后 ThreadIDs 的条数上限：超过即升级为 Full
// 并清空列表，避免单帧 JSON 随线程数无界膨胀（全量刷新语义完全覆盖它）。
const t3ShellEventMergeLimit = 256

// T3ShellEvent 是一条「某环境的数据可能变了」的合并事件。
//   - ThreadIDs 非空：这些线程可能有增量（thread.updated/removed）。
//   - Full：该环境内所有线程都可能变化（权威快照、项目变化、归档面失效、
//     合并超限），浏览器应整环境重拉。
//   - State 非空：连接状态翻转（syncing/ready/stale/failed），浏览器可据此
//     调整「正在同步/已断连」的提示。
type T3ShellEvent struct {
	EnvironmentID string   `json:"environment_id,omitempty"`
	ThreadIDs     []string `json:"thread_ids,omitempty"`
	Full          bool     `json:"full,omitempty"`
	State         string   `json:"state,omitempty"`
}

// T3ShellEventSubscription 是一个用户的事件订阅。C() 返回容量 1 的信号
// channel：发布方非阻塞发送，已有未消费信号时跳过（Drain 会取走全部合并
// 事件，单信号即足够）。订阅 Close 后信号 channel 关闭，消费者立即醒来并
// 以 Drain 返回 nil 识别退出。
type T3ShellEventSubscription struct {
	hub  *t3ShellHub
	user string

	mu      sync.Mutex
	pending map[string]*T3ShellEvent // envID → 合并中的事件
	signal  chan struct{}
	closed  bool
}

func newT3ShellEventSubscription() *T3ShellEventSubscription {
	return &T3ShellEventSubscription{
		pending: map[string]*T3ShellEvent{},
		signal:  make(chan struct{}, 1),
	}
}

// C 返回信号 channel。
func (s *T3ShellEventSubscription) C() <-chan struct{} {
	return s.signal
}

// Drain 原子摘取全部待发事件（同一 envID 的多次发布已合并为一条）。订阅已
// 关闭时返回 nil——区别于「无事件」的空切片，消费方以此识别退出。
func (s *T3ShellEventSubscription) Drain() []T3ShellEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	events := make([]T3ShellEvent, 0, len(s.pending))
	for _, ev := range s.pending {
		events = append(events, *ev)
	}
	s.pending = map[string]*T3ShellEvent{}
	return events
}

// Close 注销订阅并关闭信号（幂等）。调用方（SSE handler 的 defer 或 hub 停机）
// 之后发布一律被丢弃。
func (s *T3ShellEventSubscription) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	// 先摘注册表再关信号：保证 Close 返回后不会再有发布者碰本订阅。
	if s.hub != nil {
		s.hub.unsubscribe(s.user, s)
	}
	close(s.signal)
}

// merge 合并一个事件进 pending 并发信号。锁序 subsMu → s.mu（发布方持
// subsMu 进来）；非阻塞发送——信号已挂起时跳过，Drain 反正会取走全部。
func (s *T3ShellEventSubscription) merge(ev T3ShellEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	merged, ok := s.pending[ev.EnvironmentID]
	if !ok {
		merged = &T3ShellEvent{EnvironmentID: ev.EnvironmentID}
		s.pending[ev.EnvironmentID] = merged
	}
	if ev.Full {
		merged.Full = true
	}
	for _, id := range ev.ThreadIDs {
		dup := false
		for _, existing := range merged.ThreadIDs {
			if existing == id {
				dup = true
				break
			}
		}
		if !dup {
			merged.ThreadIDs = append(merged.ThreadIDs, id)
		}
	}
	if ev.State != "" {
		merged.State = ev.State
	}
	if len(merged.ThreadIDs) > t3ShellEventMergeLimit {
		merged.Full = true
		merged.ThreadIDs = nil
	}
	select {
	case s.signal <- struct{}{}:
	default:
	}
}

// Subscribe 注册当前用户的事件订阅。hub 已关闭时返回一个立即关闭的订阅，
// 消费方首轮即退出。
func (h *t3ShellHub) Subscribe(userID string) *T3ShellEventSubscription {
	sub := newT3ShellEventSubscription()
	sub.hub = h
	sub.user = userID
	h.subsMu.Lock()
	if h.subsStopped {
		h.subsMu.Unlock()
		sub.Close()
		return sub
	}
	set, ok := h.subs[userID]
	if !ok {
		set = map[*T3ShellEventSubscription]struct{}{}
		h.subs[userID] = set
	}
	set[sub] = struct{}{}
	h.subsMu.Unlock()
	return sub
}

// unsubscribe 摘除订阅；集合空了连用户分组一起删，注册表不随历史增长。
func (h *t3ShellHub) unsubscribe(userID string, sub *T3ShellEventSubscription) {
	h.subsMu.Lock()
	defer h.subsMu.Unlock()
	if set, ok := h.subs[userID]; ok {
		delete(set, sub)
		if len(set) == 0 {
			delete(h.subs, userID)
		}
	}
}

// publishToUser 把事件合并进该用户全部订阅并各发一次信号。只做内存操作。
func (h *t3ShellHub) publishToUser(userID string, ev T3ShellEvent) {
	h.subsMu.Lock()
	defer h.subsMu.Unlock()
	for sub := range h.subs[userID] {
		sub.merge(ev)
	}
}

// t3ArchiveAnnounced 是归档面已对外发布过的可见状态指纹
// （publishArchiveFaceEvent 的去重基准）。守 entry 锁，与 publishedState
// 同型——都是「对外承诺过什么」的发布侧缓存。announced 区分「从未发布
// 过」与「发布过空归档可信态」：两者指纹相同（ids 空、err 空），零值若
// 不加区分，新环境的首份快照会被误判「与基准相同」而整场静默（R11
// P2-3）——归档落地晚于活跃面 Ready 的页面收不到任何事件，停在
// 「归档线程暂不可读」降级。首次发布无条件放行。
type t3ArchiveAnnounced struct {
	announced bool
	ids       []string
	err       string
}

// publishArchiveFaceEvent 是归档面事件的统一发布口（审修 R9 P2-2）：按
// 「对外可见状态是否真的变了」去重，替代旧实现「凡落地必 Full」的无条件
// 广播——TTL 例行重拉同一份快照每次都发 Full，前端把 Full 当整环境失效，
// 每环境每 TTL 一次全量重取，推送退化成定时轮询。
//
// 判定基准是状态指纹（archiveFaceFingerprint，与 snapshots() 给浏览器的
// 视图同源）：
//   - 线程集合/顺序变了，或本 entry 还从未发布过（announced=false，含
//     空归档——空表也是要交给浏览器的结论，不能因零值基准撞车而静默）
//     → Full；
//   - 集合没变、只有可信度文案变了（失败首次出现/失败原因变化/恢复可信/
//     TTL 过期撤可信与随后的同数据落地恢复）→ 轻量 State 事件（载荷取
//     当前视图状态，前端只按「有 state 即失效该环境详情」处理）；
//   - 两者都没变（同一错误反复失败、已不可信面的重复撤可信）→ 不发，
//     至多静默。TTL 过期重拉同数据因此不再是全程静默（P1，R12）：若撤
//     可信不发事件，宣布基准停在可信指纹，落地恢复与基准一致会被判
//     「无新话可说」——降级的那次响应之后没有任何事件，前端降级横幅
//     挂到用户下次手动读。
//
// forceFull 用于写后失效（invalidateArchiveEntry）：归档集「可能」变了但
// 去向不可辨，必须整环境失效一次，不做去重；指纹仍同步为发布后状态，让
// 随后的重拉落地能与它比较。
//
// 指纹比较与缓存更新在 e.mu 内完成，发布在锁外（与 noteConnState 同一锁
// 边界：发布路径不持 entry 锁）。锁序 e.mu → s.mu 与全局一致。
func (h *t3ShellHub) publishArchiveFaceEvent(e *t3ShellEntry, forceFull bool) {
	ids, errText := e.state.archiveFaceFingerprint()
	e.mu.Lock()
	full := forceFull || !e.archAnnounced.announced || !slices.Equal(ids, e.archAnnounced.ids)
	stateHint := !full && errText != e.archAnnounced.err
	e.archAnnounced.announced = true
	e.archAnnounced.ids = ids
	e.archAnnounced.err = errText
	statePayload := ""
	if stateHint {
		statePayload = e.stateLocked().String()
	}
	e.mu.Unlock()
	switch {
	case full:
		h.publishThreadEvent(e, nil, true)
	case stateHint:
		h.publishState(e, statePayload)
	}
}

// publishThreadEvent 发布一批线程级变化。线程列表非空或 full 才有意义；
// 空事件直接丢弃，省掉订阅者的无谓唤醒。
func (h *t3ShellHub) publishThreadEvent(e *t3ShellEntry, threadIDs []string, full bool) {
	if !full && len(threadIDs) == 0 {
		return
	}
	h.publishToUser(e.userID, T3ShellEvent{
		EnvironmentID: e.envID,
		ThreadIDs:     threadIDs,
		Full:          full,
	})
}

// publishState 发布连接状态事件（不带线程列表）。常规用途是连接状态翻转
// （状态变化本身就是要呈现的信息）；归档面的可信度翻转也走这里做轻量提示
// （载荷为当前视图状态）——前端只按「有 state 即失效该环境的全部详情」
// 处理，不依赖具体取值。
func (h *t3ShellHub) publishState(e *t3ShellEntry, state string) {
	h.publishToUser(e.userID, T3ShellEvent{
		EnvironmentID: e.envID,
		State:         state,
	})
}

// t3ShellEventBatch 是单个流帧内收集的变化集合，帧应用成功后一次性发布。
type t3ShellEventBatch struct {
	ids  []string
	full bool
}

// observeActive 记录一个活跃流项对浏览器可见的变化。
// 权威快照（resolvedRepositoryIdentityRoots 不存在）与项目增删都是整环境级
// 变化 → Full；元数据帧不含线程数据，不算变化。
func (b *t3ShellEventBatch) observeActive(item *t3ShellStreamItem) {
	switch item.Kind {
	case "thread.updated":
		if item.Thread != nil && item.Thread.ID != "" {
			b.ids = append(b.ids, item.Thread.ID)
		}
	case "thread.removed":
		if item.ThreadID != "" {
			b.ids = append(b.ids, item.ThreadID)
		}
	case "snapshot":
		if item.Snapshot != nil && len(item.ResolvedRepositoryIdentityRoots) == 0 {
			b.full = true
		}
	case "project.updated", "project.removed":
		b.full = true
	}
}

// observeArchive 记录一个旧归档订阅流项的变化（旧面回退路径）。归档流
// 没有 enrichment 帧，快照一律按权威全量处理。
func (b *t3ShellEventBatch) observeArchive(item *t3ArchiveStreamItem) {
	switch item.Kind {
	case "thread.updated":
		if item.Thread != nil && item.Thread.ID != "" {
			b.ids = append(b.ids, item.Thread.ID)
		}
	case "thread.removed":
		if item.ThreadID != "" {
			b.ids = append(b.ids, item.ThreadID)
		}
	case "snapshot":
		b.full = true
	}
}

// PublishUserRefresh 让该用户的浏览器整棵线程子树失效一次（Full 且不带
// environment_id，前端按「全局刷新」处理）。环境增删改本身不产生线程级
// 增量：删除时 entry 若正处在重连退避里，Drop 不会经 endFaceConnection
// 发出任何事件；其它标签页就会一直显示已删除的环境，直到聚焦/手动刷新。
// 环境 CRUD 成功后由 T3EnvironmentService 调用。
func (h *t3ShellHub) PublishUserRefresh(userID string) {
	h.publishToUser(userID, T3ShellEvent{Full: true})
}
