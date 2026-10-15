package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// T3ThreadsService 聚合用户各 T3 环境的线程，并承载 T3 客户端读写操作
// （INT-82 只读集成 + INT-85 客户端化：写入协议见 docs/internal/threads.md）。
//
// 读取路径（2026-10 核对自 T3 源码）：
//   - 线程列表：WS RPC `orchestration.subscribeShell` 取活跃快照，再以一次性
//     RPC `orchestration.getArchivedShellSnapshot` 取归档线程（真实 T3 的活跃
//     快照 archivedThreads 恒为空，归档数据只有独立 RPC 提供）。随后立即断开。
//     归档 RPC 失败只作部分失败处理：活跃线程照常返回，环境标降级（G1）。
//     不做实时订阅、不落库。列表排除 lineage.relationshipToParent=subagent
//     （与 T3 自身列表一致）。
//   - 线程详情：HTTP `GET /api/orchestration/threads/:threadId` 完整投影供
//     时间线（visibleTurnItems 含 fork 继承前缀，且已按上游规则排除回滚/取消/
//     被取代的条目）；线程运行态与元数据取同一 WS 快照面的可信 ThreadShell
//     ——投影的 thread 是 AppThread，不带 status/activityRunStatus/
//     pendingRuntimeRequest。shell 在活跃+归档两份快照中按线程 ID 找出
//     （不过滤子代理，详情可进入子线程），找不到时明确失败，不假 idle；
//     归档面不可信时不得断言线程不存在（G1）。
//     条目 ID 由 sourceThreadId + 原始 id 合成。
//   - 条目正文：命令输出/工具输出/subagent 结果在 wire 投影中被上游裁剪，
//     展开时经 WS RPC `orchestration.getTurnItem` 补读，参数从可见行本身派生。
//
// 单环境失败不阻塞其它环境：错误记进返回的 environments 数组；多环境读取
// 有界并行，整轮与单环境独立预算，坏环境不拖垮健康环境的已得结果。
type T3ThreadsService struct {
	envRepo *repository.T3EnvironmentRepository
	cfg     *config.Config
	logger  *zap.Logger
	http    *t3HTTPClient
	budgets t3RemoteBudgets
}

// t3RemoteBudgets T3 远程调用预算。整轮预算必须小于前端 API 超时（35s），
// 单环境/单调用预算独立计时；字段可注入以便测试用小预算验证隔离行为。
type t3RemoteBudgets struct {
	Round          time.Duration // 多环境聚合整轮
	PerEnvironment time.Duration // 单环境列表读取（探测+WS 握手+RPC）
	ThreadDetail   time.Duration // 线程详情整读（HTTP 投影 + WS shell 快照）
	ItemDetail     time.Duration // 条目正文补读（HTTP 快照 + WS getTurnItem）
}

func defaultT3RemoteBudgets() t3RemoteBudgets {
	return t3RemoteBudgets{
		Round:          20 * time.Second,
		PerEnvironment: 6 * time.Second,
		ThreadDetail:   25 * time.Second,
		ItemDetail:     25 * time.Second,
	}
}

// t3ListConcurrency 多环境读取的有界并行度。
const t3ListConcurrency = 4

func NewT3ThreadsService(envRepo *repository.T3EnvironmentRepository, cfg *config.Config, logger *zap.Logger) *T3ThreadsService {
	return &T3ThreadsService{
		envRepo: envRepo,
		cfg:     cfg,
		logger:  logger,
		http:    newT3HTTPClient(),
		budgets: defaultT3RemoteBudgets(),
	}
}

func (s *T3ThreadsService) List(ctx context.Context, userID string) (*T3ThreadListResponse, error) {
	envs, err := s.envRepo.ListByUserID(ctx, userID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	resp := &T3ThreadListResponse{
		Items:        []T3ThreadSummary{},
		Environments: []T3EnvironmentResponse{},
	}
	if len(envs) == 0 {
		return resp, nil
	}

	roundCtx, cancelRound := context.WithTimeout(ctx, s.budgets.Round)
	defer cancelRound()

	type envResult struct {
		threads []T3ThreadSummary
		status  T3EnvironmentStatus
		errMsg  string
	}
	results := make([]envResult, len(envs))
	runT3Bounded(roundCtx, len(envs), t3ListConcurrency, s.budgets.PerEnvironment, func(envCtx context.Context, i int) {
		threads, status, errMsg := s.fetchEnvironmentThreads(envCtx, &envs[i])
		results[i] = envResult{threads: threads, status: status, errMsg: errMsg}
	})

	for i := range envs {
		resp.Environments = append(resp.Environments, *s.envResponse(&envs[i], results[i].status, results[i].errMsg))
		resp.Items = append(resp.Items, results[i].threads...)
	}
	return resp, nil
}

// Detail 读取单个线程的完整详情。
//
// 运行态取 WS 快照面的可信 ThreadShell（投影 thread 是 AppThread，无运行态
// 字段）；HTTP 投影供时间线。先读 shell 快照、后读 HTTP 投影：两读之间运行
// 结束时拼出的是「idle 运行态 + 更新的时间线」，前端停止轮询也不会漏掉最终
// 消息——反过来先读投影则会留下「旧时间线 + idle」的错位。两段读取共享一个
// 整体 deadline（< 前端 35s 超时），ctx 取消即刻中止。shell 读不到时以 HTTP
// 投影归类：投影 404 → 40418（线程真的不存在），投影在、shell 无 → 50202
// （上游快照面不一致，明确失败）。归档面读取失败时只信活跃面（G1）：活跃
// 命中照常返回详情；活跃没有则回上游错误重试——归档不可信，不能借它把
// 归档线程谎报成 404。
func (s *T3ThreadsService) Detail(ctx context.Context, userID, envID, threadID string) (*T3ThreadDetail, error) {
	env, err := s.envRepo.FindByIDAndUserID(ctx, envID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrT3EnvironmentNotFound
		}
		return nil, errs.ErrInternal
	}

	token, err := s.bearerToken(env)
	if err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}

	ctx, cancel := context.WithTimeout(ctx, s.budgets.ThreadDetail)
	defer cancel()

	// 与列表同一探测：Bearer 被吊销/过期时 WS 握手失败不区分状态码（见
	// dialT3RPC 注释），先用 HTTP 会话探明，凭证失效直接回「请重新配对」，
	// 而不是落到 WS 握手失败的一句笼统 50202。
	if status, probeErr := s.http.probeSession(ctx, env.Address, token); status != T3EnvironmentStatusOK {
		return nil, errs.New(errs.ErrT3Upstream.Code, probeErr)
	}

	snaps, err := s.fetchShellSnapshots(ctx, env, token)
	if err != nil {
		return nil, s.upstreamError(err)
	}
	shell, projectNames, ok := snaps.findThread(threadID)
	if !ok {
		// 活跃面没有：归档读取失败时归档面等价于「没读过」，不能据此下
		// 「线程不存在」的结论（归档线程会在这里被谎报 404），回上游错误
		// 让调用方重试（G1）。
		if snaps.ArchiveErr != nil {
			return nil, s.upstreamError(fmt.Errorf("归档快照读取失败: %w", snaps.ArchiveErr))
		}
		// shell 快照面无此线程：读一次 HTTP 投影区分「线程真的不存在」（404 →
		// 40418）与「投影在、shell 无」的上游不一致（50202 明确失败）。两段
		// 读取顺序不变（运行态在先），这里只是错误路径上的归类。
		if _, err := s.http.fetchThreadDetail(ctx, env.Address, token, threadID); err != nil {
			return nil, s.upstreamError(err)
		}
		return nil, errs.New(errs.ErrT3Upstream.Code, "线程运行状态不可读：shell 快照中未找到该线程")
	}

	projection, err := s.http.fetchThreadDetail(ctx, env.Address, token, threadID)
	if err != nil {
		return nil, s.upstreamError(err)
	}

	detail, err := normalizeT3ThreadDetail(env, projection, shell, projectNames)
	if err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "线程投影解析失败: "+shortenT3Error(err))
	}
	return detail, nil
}

// ItemDetail 读取单个条目的完整正文（detail_available=true 的条目才走到
// 远程补读）。成员校验在先：先取指定线程的可见时间线确认条目属于它，再从
// 可信行本身派生 getTurnItem 参数（threadId/itemId 取行内 item 的持久身份），
// 不接受前端指定的任何来源或 RPC 方法。
func (s *T3ThreadsService) ItemDetail(ctx context.Context, userID, envID, threadID, itemID string) (*T3ThreadItem, error) {
	env, err := s.envRepo.FindByIDAndUserID(ctx, envID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrT3EnvironmentNotFound
		}
		return nil, errs.ErrInternal
	}

	token, err := s.bearerToken(env)
	if err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}

	ctx, cancel := context.WithTimeout(ctx, s.budgets.ItemDetail)
	defer cancel()

	projection, err := s.http.fetchThreadDetail(ctx, env.Address, token, threadID)
	if err != nil {
		return nil, s.upstreamError(err)
	}

	parsed, err := decodeT3Projection(projection)
	if err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "线程投影解析失败: "+shortenT3Error(err))
	}
	rows := parsed.VisibleTurnItems

	row, ok := findT3VisibleRow(rows, itemID)
	if !ok {
		return nil, errs.ErrT3ThreadItemNotFound
	}
	item, ok := normalizeT3TurnItem(row.SourceThreadID, row.Item)
	if !ok {
		return nil, errs.ErrT3ThreadItemNotFound
	}
	annotateT3ItemOrigin(&item, *row)
	enriched := []T3ThreadItem{item}
	enrichT3RequestItems(enriched, parsed.RuntimeRequests)
	item = enriched[0]
	if !api.Deref(item.DetailAvailable) {
		return &item, nil // 快照已含全部正文（或上游 detail 也不提供），无需补读
	}

	var ref struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal(row.Item, &ref); err != nil || ref.ID == "" {
		return nil, errs.New(errs.ErrT3Upstream.Code, "线程条目缺少来源身份")
	}
	// 宽松解码：真实条目恒带 threadId（继承行指向来源线程）；缺失时回退到
	// 可见行的 sourceThreadId——两者都来自上游同一行，不信任前端输入。
	persistedIn := ref.ThreadID
	if persistedIn == "" {
		persistedIn = row.SourceThreadID
	}

	session, err := dialT3RPC(ctx, wsBaseOf(env.Address), token)
	if err != nil {
		return nil, s.upstreamError(err)
	}
	defer session.Close()

	value, err := session.call(ctx, "orchestration.getTurnItem", map[string]any{
		"threadId": persistedIn,
		"itemId":   ref.ID,
	})
	if err != nil {
		return nil, s.upstreamError(err)
	}
	var result struct {
		Item *json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(value, &result); err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "线程条目响应解析失败: "+shortenT3Error(err))
	}
	if result.Item == nil || len(*result.Item) == 0 || string(*result.Item) == "null" {
		return nil, errs.ErrT3ThreadItemNotFound
	}
	full, ok := normalizeT3TurnItemFull(row.SourceThreadID, *result.Item)
	if !ok {
		return nil, errs.New(errs.ErrT3Upstream.Code, "线程条目解析失败")
	}
	annotateT3ItemOrigin(&full, *row)
	return &full, nil
}

// fetchEnvironmentThreads 拉取一个环境的全部线程（活跃+归档）。
// 返回 (线程, 状态, 错误说明)；整体失败（探测/握手/活跃快照）时线程为空；
// 归档面失败属于部分失败：活跃线程照常返回，状态降级并附错误说明（G1）。
//
// 先用 HTTP 会话端点分类凭证状态（x/net/websocket 的握手失败不带状态码，
// 凭证失效与网络不可达在 WS 侧无法区分），探通后再走 WS RPC 读取。
// ctx 携带单环境预算：探测、WS 握手、两次 RPC 全程受限，取消即刻中止。
func (s *T3ThreadsService) fetchEnvironmentThreads(ctx context.Context, env *model.T3Environment) ([]T3ThreadSummary, T3EnvironmentStatus, string) {
	token, err := s.bearerToken(env)
	if err != nil {
		return nil, T3EnvironmentStatusRepair, "环境凭证不可用，请重新配对"
	}

	status, probeErr := s.http.probeSession(ctx, env.Address, token)
	if status != T3EnvironmentStatusOK {
		return nil, status, probeErr
	}

	snaps, err := s.fetchShellSnapshots(ctx, env, token)
	if err != nil {
		status, msg := t3StatusFromError(err)
		return nil, status, msg
	}

	projectNames := snaps.projectNames()
	active, archived := &snaps.Active, &snaps.Archived
	threads := make([]T3ThreadSummary, 0, len(active.Threads)+len(archived.Threads))
	for i := range active.Threads {
		if t3IsSubagentThread(&active.Threads[i]) {
			continue
		}
		threads = append(threads, normalizeT3ThreadShell(env, &active.Threads[i], projectNames))
	}
	for i := range archived.Threads {
		if t3IsSubagentThread(&archived.Threads[i]) {
			continue
		}
		threads = append(threads, normalizeT3ThreadShell(env, &archived.Threads[i], projectNames))
	}
	if snaps.ArchiveErr != nil {
		// 归档 RPC 失败不推翻活跃结果（G1）：活跃线程照常展示，环境标 degraded
		// （部分可读，凭证与连通性都正常），交既有 problem banner 提示。
		s.logger.With(zap.String("user_id", env.UserID)).Warn("t3 archived shell snapshot read failed, serving active threads only",
			zap.String("environment_id", env.ID), zap.Error(snaps.ArchiveErr))
		return threads, T3EnvironmentStatusDegraded, "归档线程暂不可读"
	}
	return threads, T3EnvironmentStatusOK, ""
}

// fetchShellSnapshots 在一条 WS 连接上取活跃 + 归档两份 shell 快照（均含
// 子代理线程；列表/详情各自决定怎么筛）。列表与详情共用这条读取路径。
//
// 只有活跃面读取失败才整体失败（没有活跃快照，任何结果都不可信）；归档面
// 失败记进 snaps.ArchiveErr 返回部分结果——归档 RPC 超时/出错曾让已到手的
// 活跃线程整批丢失（G1），半解码的归档数据同样不可信，失败即整体弃用
// 归档面（Archived 归零）。两份是否齐全看 ArchiveErr 是否为 nil。
func (s *T3ThreadsService) fetchShellSnapshots(ctx context.Context, env *model.T3Environment, token string) (*t3ShellSnapshots, error) {
	session, err := dialT3RPC(ctx, wsBaseOf(env.Address), token)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	snaps := &t3ShellSnapshots{}
	gotActive := false
	err = session.callFirstChunkValue(ctx, "orchestration.subscribeShell", map[string]any{}, func(value json.RawMessage) (bool, error) {
		var item struct {
			Kind     string          `json:"kind"`
			Snapshot t3ShellSnapshot `json:"snapshot"`
		}
		if err := json.Unmarshal(value, &item); err != nil {
			return true, fmt.Errorf("shell 流帧解析失败: %w", err)
		}
		if item.Kind != "snapshot" {
			return false, nil // 活动帧：快照前的等待帧，继续读
		}
		snaps.Active = item.Snapshot
		gotActive = true
		return true, nil
	})
	if err != nil && !gotActive {
		return nil, err
	}
	if err != nil {
		// 活跃快照已完整收下，收尾写（Ack/Interrupt）失败不推翻活跃面——
		// 与归档 RPC 失败同一降级：记进 ArchiveErr，活跃线程照常返回。
		snaps.ArchiveErr = err
		return snaps, nil
	}

	// 归档线程来自独立一次性 RPC（真实活跃快照的 archivedThreads 恒为空）。
	archivedValue, archiveErr := session.call(ctx, "orchestration.getArchivedShellSnapshot", map[string]any{})
	if archiveErr == nil {
		archiveErr = json.Unmarshal(archivedValue, &snaps.Archived)
	}
	if archiveErr != nil {
		snaps.Archived = t3ArchivedShellSnapshot{}
		snaps.ArchiveErr = archiveErr
	}
	return snaps, nil
}

func (s *T3ThreadsService) bearerToken(env *model.T3Environment) (string, error) {
	plain, err := crypto.Decrypt(env.BearerTokenEncrypted, []byte(s.cfg.Encryption.Key))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (s *T3ThreadsService) envResponse(env *model.T3Environment, status T3EnvironmentStatus, probeErr string) *T3EnvironmentResponse {
	return newT3EnvironmentResponse(env, status, probeErr)
}

// upstreamError 把适配器错误翻译成 Fast Ship 业务错误。
func (s *T3ThreadsService) upstreamError(err error) error {
	if errors.Is(err, errT3ThreadNotFound) {
		return errs.ErrT3ThreadNotFound
	}
	if errors.Is(err, errT3CredentialRejected) {
		return errs.New(errs.ErrT3Upstream.Code, "T3 环境拒绝凭证（可能已过期或被吊销），请重新配对")
	}
	return errs.New(errs.ErrT3Upstream.Code, shortenT3Error(err))
}

func t3StatusFromError(err error) (T3EnvironmentStatus, string) {
	if errors.Is(err, errT3CredentialRejected) {
		return T3EnvironmentStatusRepair, "凭证已失效，请重新配对"
	}
	return T3EnvironmentStatusUnreachable, shortenT3Error(err)
}

// wsBaseOf 把 HTTP 基址换成 WS 基址。
func wsBaseOf(httpBase string) string {
	switch {
	case strings.HasPrefix(httpBase, "https://"):
		return "wss://" + strings.TrimPrefix(httpBase, "https://")
	case strings.HasPrefix(httpBase, "http://"):
		return "ws://" + strings.TrimPrefix(httpBase, "http://")
	default:
		return httpBase
	}
}

// runT3Bounded 以有界并行对 n 个下标跑 fn。ctx 是整轮预算，fn 每个下标再叠加
// per 预算；结果由 fn 闭包写入调用方容器。预算到点 fn 即随 ctx 中止，不阻塞收敛。
func runT3Bounded(ctx context.Context, n, concurrency int, per time.Duration, fn func(ctx context.Context, i int)) {
	if n <= 0 {
		return
	}
	if concurrency > n {
		concurrency = n
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ctxI, cancel := context.WithTimeout(ctx, per)
			defer cancel()
			fn(ctxI, i)
		}(i)
	}
	wg.Wait()
}
