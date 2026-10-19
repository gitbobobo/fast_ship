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
// 读取路径（INT-116 起，2026-10 核对自 T3 源码）：
//   - 线程列表/详情/Stop/收尾判定的运行态：来自按「用户 × 环境」常驻的 shell
//     订阅 hub（t3_shell_hub.go，活跃/归档各一条长驻 WS + 增量推送）。列表
//     请求从内存组装回答，不再每请求拉全量快照、不再做每请求会话探测。
//     归档面不可信（未同步完成或读取失败）只作部分失败处理：活跃线程照常
//     返回，环境标降级（G1）。列表排除 lineage.relationshipToParent=subagent
//     （与 T3 自身列表一致）。
//   - 线程详情：HTTP `GET /api/orchestration/threads/:threadId` 完整投影供
//     时间线（visibleTurnItems 含 fork 继承前缀，且已按上游规则排除回滚/取消/
//     被取代的条目）；线程运行态与元数据取 hub 的**可信活跃面**（State==Ready）
//     ——投影的 thread 是 AppThread，不带 status/activityRunStatus/
//     pendingRuntimeRequest。shell 在活跃+归档两份快照中按线程 ID 找出
//     （不过滤子代理，详情可进入子线程），找不到时明确失败，不假 idle；
//     归档面不可信时不得断言线程不存在（G1）。陈旧/同步中的快照一律不当
//     可信运行态（50202），陈旧 idle 会让 Stop 无事可做。
//     条目 ID 由 sourceThreadId + 原始 id 合成。
//   - 条目正文：命令输出/工具输出/subagent 结果在 wire 投影中被上游裁剪，
//     展开时经 WS RPC `orchestration.getTurnItem` 补读，参数从可见行本身派生。
//   - 启动器/工作区选项与项目发现：HTTP `GET /api/projects` 项目表，零 shell
//     订阅（归档与项目 HTTP 无关）。
//
// 单环境失败不阻塞其它环境：错误记进返回的 environments 数组；多环境读取
// 有界并行，整轮与单环境独立预算，坏环境不拖垮健康环境的已得结果。
type T3ThreadsService struct {
	envRepo  *repository.T3EnvironmentRepository
	cfg      *config.Config
	logger   *zap.Logger
	http     *t3HTTPClient
	shellHub *t3ShellHub
	budgets  t3RemoteBudgets
}

// t3RemoteBudgets T3 远程调用预算。整轮预算必须小于前端 API 超时（35s），
// 单环境/单调用预算独立计时；字段可注入以便测试用小预算验证隔离行为。
type t3RemoteBudgets struct {
	Round          time.Duration // 多环境聚合整轮
	PerEnvironment time.Duration // 单环境 HTTP 探测/项目表读取（探测+一次请求）
	ThreadDetail   time.Duration // 线程详情整读（hub 视图 + HTTP 投影）
	ItemDetail     time.Duration // 条目正文补读（HTTP 快照 + WS getTurnItem）
	// ShellFirstSync 只用于「等 hub 首次同步」：千级线程环境的全量快照首字节
	// 可达 13s+，沿用 6s 的 PerEnvironment 会让第一次列表必然超时。它不是
	// 每请求预算的放大——entry 建立后 View 从内存回答，近似零成本。
	ShellFirstSync time.Duration
}

func defaultT3RemoteBudgets() t3RemoteBudgets {
	return t3RemoteBudgets{
		Round:          20 * time.Second,
		PerEnvironment: 6 * time.Second,
		ThreadDetail:   25 * time.Second,
		ItemDetail:     25 * time.Second,
		ShellFirstSync: 15 * time.Second,
	}
}

// t3ListConcurrency 多环境读取的有界并行度。
const t3ListConcurrency = 4

func NewT3ThreadsService(envRepo *repository.T3EnvironmentRepository, cfg *config.Config, logger *zap.Logger, shellHub *t3ShellHub) *T3ThreadsService {
	return &T3ThreadsService{
		envRepo:  envRepo,
		cfg:      cfg,
		logger:   logger,
		http:     newT3HTTPClient(),
		shellHub: shellHub,
		budgets:  defaultT3RemoteBudgets(),
	}
}

// SubscribeShellEvents 注册当前用户的 shell 变更订阅（INT-132，SSE 推送用）。
// handler 不直接持有 hub；订阅关闭（hub 停机或消费方主动 Close）以信号
// channel 关闭表达，Drain 返回 nil 即退出。
func (s *T3ThreadsService) SubscribeShellEvents(userID string) *T3ShellEventSubscription {
	return s.shellHub.Subscribe(userID)
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
	// 单环境预算用 ShellFirstSync 而非 PerEnvironment：等待的是 hub 首次同步
	// （千级线程环境首字节 13s+），不是每请求的探测+两次 RPC。entry 建立后
	// 这里近似零成本返回；整轮 Round 仍是 20s 上限。
	runT3Bounded(roundCtx, len(envs), t3ListConcurrency, s.budgets.ShellFirstSync, func(envCtx context.Context, i int) {
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
// 运行态取 hub 可信活跃面的 ThreadShell（投影 thread 是 AppThread，无运行态
// 字段）；HTTP 投影供时间线。先读运行态、后读 HTTP 投影：两读之间运行
// 结束时拼出的是「idle 运行态 + 更新的时间线」，不会漏掉最终消息——反过来
// 先读投影则会留下「旧时间线 + idle」的错位。两段读取共享一个
// 整体 deadline（< 前端 35s 超时），ctx 取消即刻中止。hub 视图未就绪（首次
// 同步中）或只剩陈旧快照（断连重连中）时一律 50202——陈旧快照里的 idle 会让
// Stop 无事可做，不得当可信运行态回答。shell 读不到时以
// HTTP 投影归类：投影 404 → 40418（线程真的不存在），投影在、shell 无 →
// 50202（上游快照面不一致，明确失败）。归档面不可信时只信活跃面（G1）：
// 活跃命中照常返回详情；活跃没有则回上游错误重试——归档不可信，不能借它把
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

	view, err := s.viewShell(ctx, env, token)
	if err != nil {
		return nil, s.upstreamError(err)
	}
	// 凭证被拒不限 Failed：陈旧命中（Stale）里的凭证错误同样是「请重新配对」，
	// 不能降级成一句笼统的「正在同步」。
	if errors.Is(view.Err, errT3CredentialRejected) {
		return nil, errs.New(errs.ErrT3Upstream.Code, "T3 环境拒绝凭证（可能已过期或被吊销），请重新配对")
	}
	if view.State != t3ShellReady || view.Snaps == nil {
		// 未就绪/陈旧一律不当可信运行态：让前端重试。归档不可信的分类在下面
		// findThread 的 G1 分支处理，这里只挡「活跃面不可信」。
		return nil, errs.New(errs.ErrT3Upstream.Code, "线程运行状态暂时不可读（正在同步），请重试")
	}
	shell, projectNames, ok := view.Snaps.findThread(threadID)
	if !ok {
		// 活跃面没有：归档不可信（读取失败或尚未同步完成）时归档面等价于
		// 「没读过」，不能据此下「线程不存在」的结论（归档线程会在这里被
		// 谎报 404），回上游错误让调用方重试（G1）。
		if view.Snaps.ArchiveErr != nil {
			return nil, s.upstreamError(fmt.Errorf("归档快照读取失败: %w", view.Snaps.ArchiveErr))
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

// viewShell 读取环境的 shell 视图（hub）。版本守卫拒绝（errT3ShellConfigStale）
// 说明请求读出的环境配置已被替换/删除（迟到请求）：统一翻译成「环境配置已
// 更新，请重试」的可重试上游错误返回，**不做内部重读重试**——重读会让同一
// 操作的前半程用旧配置、后半程用新配置（新配置的 shell 命中 + 旧配置的
// 地址/凭证去读投影/写命令，地址切换且线程 ID 重合时存在跨环境混读/误操作
// 风险，F5）。调用方把可重试错误交还上层；用户/前端重试会重新加载环境，
// 整个操作从头到尾用同一份配置。
func (s *T3ThreadsService) viewShell(ctx context.Context, env *model.T3Environment, token string) (*t3ShellView, error) {
	view, err := s.shellHub.View(ctx, env, token)
	if err != nil {
		if errors.Is(err, errT3ShellConfigStale) {
			return nil, fmt.Errorf("环境配置已更新，请重试")
		}
		return nil, err
	}
	return view, nil
}

// t3ShellStateMessage 把 hub 视图状态翻译成环境状态与说明文案（列表用）。
// 陈旧命中（Stale）不当可信运行态，但内存里的线程表仍如实展示并标注断连
// 时间；归档面不可信（读取失败或尚未同步完成）是部分失败：只在活跃面 Ready
// 时改写文案，其它状态优先自己的原因说明（G1）。
func t3ShellStateMessage(view *t3ShellView) (T3EnvironmentStatus, string) {
	// 凭证被拒优先于新鲜度文案：已拿到过快照的环境在重连探测里被拒时视图是
	// Stale（ActiveReady 仍为真），若只按 Stale 报 degraded，被吊销的凭证会
	// 一直停在「正在重连」被反复重试，用户永远看不到「请重新配对」。
	if errors.Is(view.Err, errT3CredentialRejected) {
		return T3EnvironmentStatusRepair, "凭证已失效，请重新配对"
	}
	switch view.State {
	case t3ShellFailed:
		return t3StatusFromError(view.Err)
	case t3ShellSyncing:
		return T3EnvironmentStatusDegraded, "正在同步 T3 线程快照，请稍后刷新"
	case t3ShellStale:
		return T3EnvironmentStatusDegraded, fmt.Sprintf("与 T3 的连接已断开（最近确认于 %s 前），正在重连", view.SyncedAgo.Round(time.Second))
	default:
		if view.Snaps != nil && view.Snaps.ArchiveErr != nil {
			return T3EnvironmentStatusDegraded, "归档线程暂不可读"
		}
		return T3EnvironmentStatusOK, ""
	}
}

// fetchEnvironmentThreads 从 shell hub 读取一个环境的全部线程（活跃+归档）。
// 返回 (线程, 状态, 错误说明)。线程表来自按环境常驻的订阅（内存组装），
// 列表请求不再各自拉全量快照、不做每请求探测；hub 未就绪（首次同步中）或
// 断连重连中按新鲜度降级标注，陈旧命中不当可信运行态。归档面不可信属于
// 部分失败：活跃线程照常返回，状态降级并附错误说明（G1）。
func (s *T3ThreadsService) fetchEnvironmentThreads(ctx context.Context, env *model.T3Environment) ([]T3ThreadSummary, T3EnvironmentStatus, string) {
	token, err := s.bearerToken(env)
	if err != nil {
		return nil, T3EnvironmentStatusRepair, "环境凭证不可用，请重新配对"
	}

	view, err := s.viewShell(ctx, env, token)
	if err != nil {
		status, msg := t3StatusFromError(err)
		return nil, status, msg
	}

	threads := []T3ThreadSummary{}
	if view.Snaps != nil {
		projectNames := view.Snaps.projectNames()
		active, archived := &view.Snaps.Active, &view.Snaps.Archived
		threads = make([]T3ThreadSummary, 0, len(active.Threads)+len(archived.Threads))
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
	}
	status, msg := t3ShellStateMessage(view)
	return threads, status, msg
}

// trustedShell 取得线程的可信 shell（Stop / 收尾判定的运行态来源）：活跃面
// 必须 Ready——陈旧/同步中的快照不得当可信运行态，陈旧 idle 会让 Stop 无事
// 可做、让收尾误判安全。凭证被拒与未就绪分别给出明确文案；活跃面没有该
// 线程且归档面不可信时回 G1 上游错误，不断言线程不存在。
func (s *T3ThreadsService) trustedShell(ctx context.Context, env *model.T3Environment, token, threadID string) (*t3ThreadShell, map[string]string, error) {
	view, err := s.viewShell(ctx, env, token)
	if err != nil {
		return nil, nil, s.upstreamError(err)
	}
	if errors.Is(view.Err, errT3CredentialRejected) {
		return nil, nil, errs.New(errs.ErrT3Upstream.Code, "T3 环境拒绝凭证（可能已过期或被吊销），请重新配对")
	}
	if view.State != t3ShellReady || view.Snaps == nil {
		return nil, nil, errs.New(errs.ErrT3Upstream.Code, "线程运行状态暂时不可读（正在同步），请重试")
	}
	shell, projectNames, ok := view.Snaps.findThread(threadID)
	if !ok {
		if view.Snaps.ArchiveErr != nil {
			return nil, nil, s.upstreamError(fmt.Errorf("归档快照读取失败: %w", view.Snaps.ArchiveErr))
		}
		return nil, nil, errs.New(errs.ErrT3Upstream.Code, "线程运行状态不可读：shell 快照中未找到该线程")
	}
	return shell, projectNames, nil
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
