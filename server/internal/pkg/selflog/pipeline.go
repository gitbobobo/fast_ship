package selflog

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ownerResolveTimeout 是刷盘前归属解析的上限；超时则本批按无主写入（API 不可见）。
const ownerResolveTimeout = 2 * time.Second

// Pipeline 把 slog 记录异步批量写入日志库，同时把可读文本写到 stdout。
// 采集侧的任何故障（缓冲满、写库失败）都只影响日志自身，绝不反压业务。
type Pipeline struct {
	sink    Sink
	stdout  slog.Handler
	queue   chan Entry
	flushCh chan struct{}

	bufferSize    int
	batchSize     int
	flushInterval time.Duration
	minLevel      slog.Level

	dropped  atomic.Int64
	failed   atomic.Int64
	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}

	mu       sync.RWMutex
	resolver OwnerResolver
}

// Options 是 Pipeline 的可调参数；零值字段取默认值。
type Options struct {
	// Sink 接收批量条目；为 nil 时用 OpenStore(path) 打开独立日志库。
	Sink Sink
	// Path 是独立日志库文件路径（Sink 为 nil 时必填）。
	Path string
	// Stdout 覆盖默认的 stderr 文本输出（测试注入用）。
	Stdout slog.Handler
	// BufferSize 是入队缓冲条数；默认 4096。
	BufferSize int
	// BatchSize 是单次写库批量；默认 200。
	BatchSize int
	// FlushInterval 是不足一批时的落盘间隔；默认 500ms。
	FlushInterval time.Duration
	// MinLevel 是入库最低级别；stdout 输出不受影响。默认 INFO。
	MinLevel slog.Level
	// Retention 非零值时启动定时清理。
	Retention RetentionOptions
}

// Start 打开存储并启动 batcher 与保留清理协程。
func Start(opts Options) (*Pipeline, error) {
	sink := opts.Sink
	if sink == nil {
		store, err := OpenStore(opts.Path)
		if err != nil {
			return nil, err
		}
		sink = store
	}

	stdoutHandler := opts.Stdout
	if stdoutHandler == nil {
		stdoutHandler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})
	}

	p := &Pipeline{
		sink:          sink,
		stdout:        stdoutHandler,
		queue:         make(chan Entry, orDefault(opts.BufferSize, 4096)),
		flushCh:       make(chan struct{}, 1),
		bufferSize:    orDefault(opts.BufferSize, 4096),
		batchSize:     orDefault(opts.BatchSize, 200),
		flushInterval: orDuration(opts.FlushInterval, 500*time.Millisecond),
		minLevel:      opts.MinLevel,
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
	}
	go p.batchLoop()

	if opts.Retention.Enabled() {
		go p.retentionLoop(opts.Retention)
	}
	return p, nil
}

// Handler 返回接入 slog 的入口 handler：镜像 stdout，同时按 MinLevel 入库。
func (p *Pipeline) Handler() slog.Handler {
	return &pipelineHandler{p: p, mirrorStdout: true, stdout: p.stdout}
}

// IngestHandler 只入库、不镜像 stdout。zap 已经通过 Tee 写过控制台，再镜像会重复一行。
func (p *Pipeline) IngestHandler() slog.Handler {
	return &pipelineHandler{p: p, mirrorStdout: false}
}

// Close 停止 batcher 并冲刷残留条目；重复调用安全。
func (p *Pipeline) Close() {
	p.stopOnce.Do(func() {
		close(p.stopCh)
		<-p.doneCh
	})
}

// SetOwnerResolver 注入归属解析器。main 在业务库 repository 就绪后调用；
// 注入前的启动类日志只能靠显式 system_scope。传 nil 清除。
func (p *Pipeline) SetOwnerResolver(r OwnerResolver) {
	p.mu.Lock()
	p.resolver = r
	p.mu.Unlock()
}

// Dropped 返回因缓冲满被丢弃的条数（诊断用）。
func (p *Pipeline) Dropped() int64 { return p.dropped.Load() }

// Failed 返回写库失败被丢弃的条数（诊断用）。
func (p *Pipeline) Failed() int64 { return p.failed.Load() }

func (p *Pipeline) enqueue(e Entry) {
	select {
	case p.queue <- e:
	default:
		// 缓冲满：丢弃并计数。日志积压说明存储端瓶颈，丢日志好过拖垮业务。
		total := p.dropped.Add(1)
		if total == 1 || total%1000 == 0 {
			p.stdout.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelWarn,
				fmt.Sprintf("selflog 缓冲已满，已累计丢弃 %d 条", total), 0))
		}
	}
}

func (p *Pipeline) batchLoop() {
	defer close(p.doneCh)
	ctx := context.Background()

	buffer := make([]Entry, 0, p.batchSize)
	flush := func() {
		if len(buffer) == 0 {
			return
		}
		p.applyOwners(ctx, buffer)
		if err := p.sink.Add(ctx, buffer); err != nil {
			// 写失败只计数并回显 stdout，绝不重试拖积队列，更不反压业务。
			p.failed.Add(int64(len(buffer)))
			p.stdout.Handle(ctx, slog.NewRecord(time.Now(), slog.LevelError,
				fmt.Sprintf("selflog 写库失败，丢弃 %d 条: %v", len(buffer), err), 0))
		}
		buffer = buffer[:0]
	}

	ticker := time.NewTicker(p.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case e := <-p.queue:
			buffer = append(buffer, e)
			if len(buffer) >= p.batchSize {
				flush()
			}
		case <-p.flushCh:
			flush()
		case <-ticker.C:
			flush()
		case <-p.stopCh:
			// 排干残留后做最后一次冲刷。
			for {
				select {
				case e := <-p.queue:
					buffer = append(buffer, e)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (p *Pipeline) retentionLoop(opts RetentionOptions) {
	interval := opts.Interval
	if interval <= 0 {
		interval = time.Hour
	}
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if st, ok := p.sink.(*Store); ok {
			if _, err := st.Cleanup(ctx, opts); err != nil {
				p.stdout.Handle(ctx, slog.NewRecord(time.Now(), slog.LevelWarn,
					fmt.Sprintf("selflog 清理失败: %v", err), 0))
			}
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			run()
		case <-p.stopCh:
			return
		}
	}
}

// pipelineHandler 实现 slog.Handler：已知关联字段进固定列，其余进 Extra JSON。
// groups 是当前 WithGroup 路径，只作用于之后绑定的属性，不改写已绑定的 attrs
// （标准 slog 语义：With("module","http").WithGroup("req") 仍把 module 放进固定列）。
type pipelineHandler struct {
	p            *Pipeline
	attrs        []slog.Attr
	groups       []string
	stdout       slog.Handler
	mirrorStdout bool
}

func (h *pipelineHandler) Enabled(_ context.Context, _ slog.Level) bool {
	// stdout 镜像不受入库最低级别限制；入库过滤在 Handle 内做。
	return true
}

func (h *pipelineHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.mirrorStdout {
		stdout := h.stdout
		if stdout == nil {
			stdout = h.p.stdout
		}
		if stdout.Enabled(ctx, r.Level) {
			_ = stdout.Handle(ctx, r.Clone())
		}
	}
	if r.Level < h.p.minLevel {
		return nil
	}

	entry := Entry{
		Time:    r.Time.UTC(),
		Level:   NormalizeLevel(r.Level),
		Message: sanitizeString(r.Message),
	}

	extra := make(map[string]interface{})
	prefix := strings.Join(h.groups, ".")
	// 嵌套组展平为点分 key（group.sub）；空 key 的内联组直接并入前缀。
	var applyAttr func(attrPrefix string, a slog.Attr)
	applyAttr = func(attrPrefix string, a slog.Attr) {
		if a.Value.Kind() == slog.KindGroup {
			p := a.Key
			if a.Key == "" {
				p = attrPrefix
			} else if attrPrefix != "" {
				p = attrPrefix + "." + a.Key
			}
			for _, sub := range a.Value.Group() {
				applyAttr(p, sub)
			}
			return
		}
		key := a.Key
		if attrPrefix != "" {
			key = attrPrefix + "." + a.Key
		}
		value := a.Value.Any()
		switch key {
		case AttrModule:
			entry.Module = sanitizeString(asString(value))
		case AttrRequestID:
			entry.RequestID = sanitizeString(asString(value))
		case AttrUserID:
			entry.UserID = sanitizeString(asString(value))
		case AttrProjectID:
			entry.ProjectID = sanitizeString(asString(value))
		case AttrIssueID:
			entry.IssueID = sanitizeString(asString(value))
		case AttrRunID:
			entry.RunID = sanitizeString(asString(value))
		case AttrThreadID:
			entry.ThreadID = sanitizeString(asString(value))
		case AttrSystemScope:
			if b, ok := value.(bool); ok {
				entry.SystemScope = b
			}
		default:
			extra[key] = sanitizeAttrValue(key, value)
		}
	}
	for _, a := range h.attrs {
		applyAttr("", a)
	}
	r.Attrs(func(a slog.Attr) bool {
		applyAttr(prefix, a)
		return true
	})

	if len(extra) > 0 {
		raw, err := json.Marshal(extra)
		if err == nil {
			entry.Extra = string(raw)
		} else {
			// 附加字段序列化失败退化为占位，消息本体不丢。
			entry.Extra = `{"extra_error":"marshal failed"}`
		}
	}

	h.p.enqueue(entry)
	return nil
}

// WithAttrs 返回携带固定属性的 handler 克隆；当前组路径在绑定时确定。
func (h *pipelineHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	if h.mirrorStdout && h.stdout != nil {
		clone.stdout = h.stdout.WithAttrs(attrs)
	}
	prefixed := prefixAttrs(h.groups, attrs)
	merged := make([]slog.Attr, 0, len(h.attrs)+len(prefixed))
	merged = append(merged, h.attrs...)
	merged = append(merged, prefixed...)
	clone.attrs = merged
	return &clone
}

// WithGroup 只影响之后的属性，已绑定的 attrs 保持原 key（关联列不掉出固定列）。
func (h *pipelineHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	if h.mirrorStdout && h.stdout != nil {
		clone.stdout = h.stdout.WithGroup(name)
	}
	clone.groups = append(append([]string{}, h.groups...), name)
	return &clone
}

func prefixAttrs(groups []string, attrs []slog.Attr) []slog.Attr {
	if len(groups) == 0 || len(attrs) == 0 {
		return attrs
	}
	prefix := strings.Join(groups, ".")
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		key := a.Key
		if key == "" {
			out[i] = a
			continue
		}
		out[i] = slog.Attr{Key: prefix + "." + key, Value: a.Value}
	}
	return out
}

func asString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case error:
		return t.Error()
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

// applyOwners 在刷盘前清除误标的 system_scope，并按关联列补全空 user_id。
// Handle 只入队，解析放在 batcher 以免与业务库 SetMaxOpenConns(1) 自等待。
func (p *Pipeline) applyOwners(ctx context.Context, rows []Entry) {
	for i := range rows {
		if !rows[i].allowsSystemScope() {
			rows[i].SystemScope = false
		}
	}
	p.mu.RLock()
	resolver := p.resolver
	p.mu.RUnlock()
	if resolver == nil {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, ownerResolveTimeout)
	defer cancel()
	owners := resolver.ResolveOwners(rctx, rows)
	if len(owners) != len(rows) {
		return
	}
	for i := range rows {
		if rows[i].UserID == "" && owners[i] != "" {
			rows[i].UserID = owners[i]
		}
	}
}

func orDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

func orDuration(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}
