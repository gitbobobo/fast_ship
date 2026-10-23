package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// 审查执行引擎（INT-135）对 T3 的四个窄操作：线程工作区真值读取、工作区登记
// 核验与回收、线程归档。除线程投影走 HTTP 外都是一次性 WS 会话（探测 → 拨号
// → RPC → 断开），与写路径同一口径。

// T3Worktree 是 vcs.listRefs 登记的一条工作区项：路径 + 该路径下聚合的全部
// ref 名。同一路径可能登记在多个 ref 下（分支/标签各自指过来），只取首个
// ref 名会把合法属主误判成分支不一致，所以按路径聚合去重。
type T3Worktree struct {
	Path     string
	RefNames []string
}

// T3ThreadWorktree 是线程投影里的工作区真值：worktreePath 与 branch。
// 两者在 AppThread 投影上都是可空字段（视觉式 root 工作区、主检出线程），
// 缺省即空串。
type T3ThreadWorktree struct {
	Path   string
	Branch string
}

// ThreadWorktree 读线程投影记录的工作区与分支：回收阶段的工作区归属只认这份
// 真值（createThread 确认时 worktree 往往还没建好，launch 窗口内做差集必然
// 歧义）。线程不存在原样透传 errT3ThreadNotFound——调用方按「无线程即无
// 工作区可清」处理；其余错误走 upstreamError。
func (s *T3ThreadsService) ThreadWorktree(ctx context.Context, userID, envID, threadID string) (*T3ThreadWorktree, error) {
	env, err := s.loadEnvForWrite(ctx, userID, envID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	token, err := s.bearerToken(env)
	if err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}
	raw, err := s.http.fetchThreadDetail(ctx, env.Address, token, threadID)
	if err != nil {
		if errors.Is(err, errT3ThreadNotFound) {
			return nil, err
		}
		return nil, s.upstreamError(err)
	}
	parsed, err := decodeT3Projection(raw)
	if err != nil {
		return nil, s.upstreamError(fmt.Errorf("线程投影解析失败: %w", err))
	}
	out := threadWorktreeFromProjection(parsed)
	return &out, nil
}

// threadWorktreeFromProjection 从线程投影提取工作区真值（纯函数，便于用真实
// 形状的 projection fixture 钉住字段层级契约）：branch/worktreePath 挂在
// projection.thread（AppThread）下，可空，缺省解析为空串。
func threadWorktreeFromProjection(p *t3ThreadProjection) T3ThreadWorktree {
	var out T3ThreadWorktree
	if p.Thread.WorktreePath != nil {
		out.Path = *p.Thread.WorktreePath
	}
	if p.Thread.Branch != nil {
		out.Branch = *p.Thread.Branch
	}
	return out
}

// t3WorktreesPage 是 vcs.listRefs 单页响应里登记核验用到的字段。
type t3WorktreesPage struct {
	Refs []struct {
		Name         string  `json:"name"`
		WorktreePath *string `json:"worktreePath"`
	} `json:"refs"`
	NextCursor *int `json:"nextCursor"`
}

// reviewWorktreeMaxRefPages 登记收集的分页上限。
const reviewWorktreeMaxRefPages = 8

// collectWorktrees 分页收集 vcs.listRefs 的 worktree 项（纯函数，fetch 取一页
// 并透传 cursor）：同一路径聚合全部 ref 名去重，结果按路径排序。到达页数上限
// 仍有 NextCursor 时返回上游错误、绝不返回不完整列表——核验把「不在列表」当
// 「已无此登记」，截断列表会造成漏删或归属误判。
func collectWorktrees(fetch func(cursor *int) (t3WorktreesPage, error)) ([]T3Worktree, error) {
	byPath := map[string][]string{}
	var nextCursor *int
	for page := 0; page < reviewWorktreeMaxRefPages; page++ {
		p, err := fetch(nextCursor)
		if err != nil {
			return nil, err
		}
		for _, ref := range p.Refs {
			if ref.WorktreePath == nil || *ref.WorktreePath == "" {
				continue
			}
			if !slices.Contains(byPath[*ref.WorktreePath], ref.Name) {
				byPath[*ref.WorktreePath] = append(byPath[*ref.WorktreePath], ref.Name)
			}
		}
		if p.NextCursor == nil {
			nextCursor = nil
			break
		}
		nextCursor = p.NextCursor
	}
	if nextCursor != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code,
			fmt.Sprintf("引用列表分页超过上限（%d 页），拒绝在不完整列表上核验工作区归属", reviewWorktreeMaxRefPages))
	}
	paths := make([]string, 0, len(byPath))
	for path := range byPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	out := make([]T3Worktree, 0, len(paths))
	for _, path := range paths {
		out = append(out, T3Worktree{Path: path, RefNames: byPath[path]})
	}
	return out, nil
}

// ListWorktrees 列出一个 T3 工作区根目录（cwd）当前登记的全部 worktree
// （vcs.listRefs 的 worktreePath 非空项），每条带聚合后的 ref 名集合，供回收
// 前的登记核验。cwd 由调用方给定且必须与随后的删除同源——从 T3 项目表现查
// workspace_root 会在绑定变更后对着另一个 repo 核验。不做服务端 query 过滤：
// 核验需要完整集合判定「未登记」。
func (s *T3ThreadsService) ListWorktrees(ctx context.Context, userID, envID, cwd string) ([]T3Worktree, error) {
	env, err := s.loadEnvForWrite(ctx, userID, envID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	token, err := s.bearerToken(env)
	if err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}
	session, err := dialT3RPC(ctx, wsBaseOf(env.Address), token)
	if err != nil {
		return nil, s.upstreamError(err)
	}
	defer session.Close()

	listInput := map[string]any{"cwd": cwd, "refKind": "all"}
	return collectWorktrees(func(cursor *int) (t3WorktreesPage, error) {
		if cursor != nil {
			listInput["cursor"] = *cursor
		}
		value, err := session.call(ctx, "vcs.listRefs", listInput)
		if err != nil {
			return t3WorktreesPage{}, s.upstreamError(err)
		}
		var p t3WorktreesPage
		if err := json.Unmarshal(value, &p); err != nil {
			return t3WorktreesPage{}, errs.New(errs.ErrT3Upstream.Code, "引用列表解析失败: "+shortenT3Error(err))
		}
		return p, nil
	})
}

// RemoveWorktree 删除一个已登记的 worktree（vcs.removeWorktree，force 由调用方给）。
// cwd 由调用方给定（launch 快照优先），核验与删除必须同源。
func (s *T3ThreadsService) RemoveWorktree(ctx context.Context, userID, envID, cwd, path string, force bool) error {
	env, err := s.loadEnvForWrite(ctx, userID, envID)
	if err != nil {
		return err
	}
	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	session, err := s.dialAfterProbe(ctx, env)
	if err != nil {
		return err
	}
	defer session.Close()

	_, err = session.call(ctx, "vcs.removeWorktree", map[string]any{
		"cwd": cwd, "path": path, "force": force,
	})
	if err != nil {
		return s.upstreamError(err)
	}
	return nil
}

// ArchiveThread 归档线程（orchestration.dispatchCommand 的 thread.archive 命令）。
// 归档不清 worktree——回收顺序是先归档再 removeWorktree。
func (s *T3ThreadsService) ArchiveThread(ctx context.Context, userID, envID, threadID string) error {
	env, err := s.loadEnvForWrite(ctx, userID, envID)
	if err != nil {
		return err
	}
	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	session, err := s.dialAfterProbe(ctx, env)
	if err != nil {
		return err
	}
	defer session.Close()

	_, err = s.dispatchT3Command(ctx, session, threadID, map[string]any{
		"type":           "thread.archive",
		"createdBy":      "user",
		"creationSource": "web",
		// commandId 按线程派生：同一归档重试带同一 ID。
		"commandId": "fs-review-archive-" + threadID,
		"threadId":  threadID,
	})
	return err
}

// ThreadTurnSettled 读线程投影，判定该运行绑定的 T3 run 是否已收尾（INT-158）：
// 归档前的收尾宽限等待用——结果落库后 agent 的回合往往还在写收尾回复，立刻
// 归档会切断 provider 事件流，T3 把 run 误记为 failed。已归档/已删除线程或
// run 落已知终态集返回 true；活动态、queued、未识别状态与投影里找不到该消息
// 的 run 都返回 false——证明不了已收尾，等宽限兜底。线程不存在原样透传
// errT3ThreadNotFound；其余错误走 upstreamError。messageID 与
// FailedPreparation 同义：Fast Ship 运行行预分配的 initial_message_id。
func (s *T3ThreadsService) ThreadTurnSettled(ctx context.Context, userID, envID, threadID, messageID string) (bool, error) {
	env, err := s.loadEnvForWrite(ctx, userID, envID)
	if err != nil {
		return false, err
	}
	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	token, err := s.bearerToken(env)
	if err != nil {
		return false, errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}
	raw, err := s.http.fetchThreadDetail(ctx, env.Address, token, threadID)
	if err != nil {
		if errors.Is(err, errT3ThreadNotFound) {
			return false, err
		}
		return false, s.upstreamError(err)
	}
	parsed, err := decodeT3Projection(raw)
	if err != nil {
		return false, s.upstreamError(fmt.Errorf("线程投影解析失败: %w", err))
	}
	return threadTurnSettledFromProjection(parsed, messageID), nil
}

// threadTurnSettledFromProjection 从线程投影判定运行绑定的 run 是否已收尾
// （纯函数，用真实形状的 projection fixture 钉住契约）。匹配口径与
// failedPreparationFromProjection 相同：按 userMessageId 匹配，同一消息多条
// run 取 ordinal 最大（只有最新那条代表当前状态）。线程已归档/已删除即已
// 收尾——没有可切断的事件流。run 只有落在已知终态集才算收尾：queued 或
// 其它未识别状态、messageID 为空与匹配不到一样按未收尾处理——证明不了
// 已结束，静默放行的代价是退回切断收尾回复的老 bug，等满宽限的代价只是
// 归档晚十分钟。
func threadTurnSettledFromProjection(p *t3ThreadProjection, messageID string) bool {
	if p.Thread.ArchivedAt != nil && *p.Thread.ArchivedAt != "" {
		return true
	}
	if p.Thread.DeletedAt != nil && *p.Thread.DeletedAt != "" {
		return true
	}
	// messageID 为空（存量行没有 initial_message_id）匹配不了任何具体 run：
	// 空串会撞上投影里缺 userMessageId 的 run（反序列化零值同为空串），
	// 按未收尾处理，等宽限兜底。
	if messageID == "" {
		return false
	}
	var run *t3Run
	for i := range p.Runs {
		if p.Runs[i].UserMessageID != messageID {
			continue
		}
		if run == nil || p.Runs[i].Ordinal >= run.Ordinal {
			run = &p.Runs[i]
		}
	}
	if run == nil {
		return false
	}
	return t3SettledRunStatuses[run.Status]
}

// T3FailedPreparation 是线程投影判读出的「工作区准备失败」事实：T3 侧 run
// 行 ID（重试命令的 runId 入参）、失败发生时刻（失败条目 updatedAt，缺失回
// 退 completedAt）与失败原文（failure.message）。
type T3FailedPreparation struct {
	RunID    string
	FailedAt string
	Message  string
}

// FailedPreparation 读线程投影，判读该线程是否处于可代发重试的工作区准备
// 失败态。可重试返回失败事实；不满足条件返回 (nil, nil)——调用方保持现状等
// 下个周期。线程不存在原样透传 errT3ThreadNotFound；其余错误走 upstreamError。
// messageID 是 Fast Ship 运行行预分配的 initial_message_id：投影 run 行以
// userMessageId 承载它，靠它对应「我们的运行 → T3 的 run」。
func (s *T3ThreadsService) FailedPreparation(ctx context.Context, userID, envID, threadID, messageID string) (*T3FailedPreparation, error) {
	env, err := s.loadEnvForWrite(ctx, userID, envID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	token, err := s.bearerToken(env)
	if err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}
	raw, err := s.http.fetchThreadDetail(ctx, env.Address, token, threadID)
	if err != nil {
		if errors.Is(err, errT3ThreadNotFound) {
			return nil, err
		}
		return nil, s.upstreamError(err)
	}
	parsed, err := decodeT3Projection(raw)
	if err != nil {
		return nil, s.upstreamError(fmt.Errorf("线程投影解析失败: %w", err))
	}
	return failedPreparationFromProjection(parsed, messageID), nil
}

// RetryPreparation 代发 T3 的 prepared-run.retry（orchestration.dispatchCommand，
// 与用户手点 Retry 同一 RPC）：T3 复用失败前已建成的 worktree 并重放原用户
// 消息。T3 只要求 commandId/threadId/runId 三个字段，不臆造其余。
func (s *T3ThreadsService) RetryPreparation(ctx context.Context, userID, envID, threadID, t3RunID, commandID string) error {
	env, err := s.loadEnvForWrite(ctx, userID, envID)
	if err != nil {
		return err
	}
	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	session, err := s.dialAfterProbe(ctx, env)
	if err != nil {
		return err
	}
	defer session.Close()

	_, err = s.dispatchT3Command(ctx, session, threadID, map[string]any{
		"type":      "prepared-run.retry",
		"commandId": commandID,
		"threadId":  threadID,
		"runId":     t3RunID,
	})
	return err
}

// failedPreparationFromProjection 从线程投影判读「工作区准备失败可重试」
// （纯函数，用真实形状的 projection fixture 钉住契约）。对齐 T3 侧
// dispatchPreparedRunRetry 的受理条件：目标 run（按 userMessageId 匹配，
// 同一消息出现多条 run 时取 ordinal 最大的一条——只有最新那条代表当前状态）
// status=failed 且带 workspacePreparation 字段；turnItems/visibleTurnItems
// 里有该 run 的 error 条目（status=failed、failure.code=workspace_preparation_failed）；
// 线程未归档未软删；同线程没有其他占用中的 run。任一不满足即 (nil, nil)。
func failedPreparationFromProjection(p *t3ThreadProjection, messageID string) *T3FailedPreparation {
	if p.Thread.ArchivedAt != nil && *p.Thread.ArchivedAt != "" {
		return nil
	}
	if p.Thread.DeletedAt != nil && *p.Thread.DeletedAt != "" {
		return nil
	}
	// messageID 为空（存量行没有 initial_message_id）匹配不了任何具体 run：
	// 空串会撞上投影里缺 userMessageId 的 run（反序列化零值同为空串），
	// 按无失败处理——与 threadTurnSettledFromProjection 同口径。
	if messageID == "" {
		return nil
	}

	var run *t3Run
	for i := range p.Runs {
		if p.Runs[i].UserMessageID != messageID {
			continue
		}
		// T3 的 prepared-run.retry 复用同一 run（恢复成 preparing），不另开
		// 行；这里取 ordinal 最大只是对「同一消息多条 run」的防御：旧 failed
		// 快照不代表当前状态，取到的不是 failed 就整体不可重试。
		if run == nil || p.Runs[i].Ordinal >= run.Ordinal {
			run = &p.Runs[i]
		}
	}
	if run == nil || run.Status != "failed" || run.WorkspacePreparation == nil {
		return nil
	}
	for i := range p.Runs {
		other := &p.Runs[i]
		if other.ID == run.ID {
			continue
		}
		switch other.Status {
		case "preparing", "starting", "running", "waiting":
			return nil
		}
	}
	return scanPrepFailureItem(p, run.ID)
}

// t3PrepFailureItem 是 error 型 turn item 的最小解码：重试判定只看这些字段，
// 未知字段忽略。
type t3PrepFailureItem struct {
	ID          string  `json:"id"`
	Type        string  `json:"type"`
	RunID       string  `json:"runId"`
	Status      string  `json:"status"`
	UpdatedAt   *string `json:"updatedAt"`
	CompletedAt *string `json:"completedAt"`
	Failure     struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"failure"`
}

// scanPrepFailureItem 在 turnItems 与 visibleTurnItems 两处扫目标 run 的工作
// 区准备失败条目（前者是本线程全量，后者是继承/回滚过滤后的可见窗口，同一
// 条目可能两处都出现）：按条目 id 去重，取先扫到的一条。
func scanPrepFailureItem(p *t3ThreadProjection, runID string) *T3FailedPreparation {
	seen := map[string]bool{}
	var found *t3PrepFailureItem
	consider := func(raw json.RawMessage) {
		if raw == nil || found != nil {
			return
		}
		var item t3PrepFailureItem
		if err := json.Unmarshal(raw, &item); err != nil || item.ID == "" || seen[item.ID] {
			return
		}
		seen[item.ID] = true
		if item.Type == "error" && item.RunID == runID && item.Status == "failed" &&
			item.Failure.Code == "workspace_preparation_failed" {
			found = &item
		}
	}
	for _, raw := range p.TurnItems {
		consider(raw)
	}
	for _, row := range p.VisibleTurnItems {
		consider(row.Item)
	}
	if found == nil {
		return nil
	}
	failedAt := api.Deref(found.UpdatedAt)
	if failedAt == "" {
		failedAt = api.Deref(found.CompletedAt)
	}
	return &T3FailedPreparation{RunID: found.RunID, FailedAt: failedAt, Message: found.Failure.Message}
}
