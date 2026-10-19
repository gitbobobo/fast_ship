package service

import "sync"

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

// publishState 发布连接状态翻转事件（不带线程列表：状态变化本身就是要呈现
// 的信息）。
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

// observeArchive 记录一个归档流项的变化。归档流没有 enrichment 帧，快照
// 一律按权威全量处理。
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
