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

// T3Registry 是 vcs.listRefs 翻页收集完的整份登记（INT-170）：Worktrees 是
// worktreePath 非空项按路径聚合的 worktree 清单，RefNames 是全部 ref 名的
// 存在集合——分支被删后 ref 整条消失，只看 worktree 项区分不了「分支还在
// 但没有 worktree」与「分支已删」，分支步核验必须查全集。
type T3Registry struct {
	Worktrees []T3Worktree
	RefNames  map[string]bool
}

// ContainsWorktree 报告路径是否登记在册；nil 登记按未登记处理。
func (r *T3Registry) ContainsWorktree(path string) bool {
	if r == nil {
		return false
	}
	return slices.ContainsFunc(r.Worktrees, func(w T3Worktree) bool { return w.Path == path })
}

// HasRef 报告 ref 名是否存在于登记全集；nil 登记按不存在处理。
func (r *T3Registry) HasRef(name string) bool {
	return r != nil && r.RefNames[name]
}

// vcs.listRefs 单页条数（T3 0.0.45 已核实接受该入参，缺省 100）。大仓库
// （远端实测 ~800 本地分支）按默认页宽要翻 8+ 页，显式给大一点的页宽减少
// 往返次数。
const t3ListRefsPageSize = 200

// reviewWorktreeMaxRefPages 登记翻页的兜底上限（异常探测，不是常规截断）：
// 200/页 × 512 页 ≈ 10 万引用，正常仓库远达不到；翻到这个页数仍未耗尽即认定
// 上游分页异常，明确报错而不是继续翻。
const reviewWorktreeMaxRefPages = 512

// errRegistryIncomplete 登记读取未能在预算内翻完（ctx 到点/取消，含页内调用
// 被 ctx 掐断）：不是上游异常、也不是读到了完整列表——调用方按「待确认」
// 处理（保持 none、退避重试、不置 failed），必须能与普通上游错误区分。
var errRegistryIncomplete = errors.New("登记读取未在预算内完成")

// collectT3Registry 分页收集 vcs.listRefs 整份登记（纯函数，fetch 取一页并
// 透传 cursor，ctx 只用于页间预算检查）：worktree 项按路径聚合 ref 名去重、
// 按路径排序，同时收集全部 ref 名集合。三重异常约束：
//   - 预算：每页发出前查 ctx.Err()，页内调用失败且 ctx 已到点同样归入预算
//     耗尽——返回 errRegistryIncomplete 包装，绝不返回不完整列表；
//   - 游标推进：nextCursor 与刚发出的同值或复现已见过的值都是上游分页异常，
//     明确报错（死循环翻页比报错更糟）；
//   - 页数兜底：超过 reviewWorktreeMaxRefPages 页仍未耗尽，同样明确报错。
func collectT3Registry(ctx context.Context, fetch func(cursor *int) (t3WorktreesPage, error)) (*T3Registry, error) {
	byPath := map[string][]string{}
	refNames := map[string]bool{}
	var nextCursor *int
	seenCursors := map[int]bool{}
	for page := 0; ; page++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%w: %s", errRegistryIncomplete, err)
		}
		p, err := fetch(nextCursor)
		if err != nil {
			if ctx.Err() != nil {
				// 页内调用被行预算掐断：与页间检查同一「待确认」语义。
				return nil, fmt.Errorf("%w: %s", errRegistryIncomplete, ctx.Err())
			}
			return nil, err
		}
		for _, ref := range p.Refs {
			refNames[ref.Name] = true
			if ref.WorktreePath == nil || *ref.WorktreePath == "" {
				continue
			}
			if !slices.Contains(byPath[*ref.WorktreePath], ref.Name) {
				byPath[*ref.WorktreePath] = append(byPath[*ref.WorktreePath], ref.Name)
			}
		}
		if p.NextCursor == nil {
			break
		}
		if seenCursors[*p.NextCursor] || (nextCursor != nil && *p.NextCursor == *nextCursor) {
			return nil, errs.New(errs.ErrT3Upstream.Code,
				fmt.Sprintf("引用列表分页游标未推进（回到 %d），拒绝在不完整列表上核验工作区归属", *p.NextCursor))
		}
		seenCursors[*p.NextCursor] = true
		nextCursor = p.NextCursor
		if page+1 >= reviewWorktreeMaxRefPages {
			return nil, errs.New(errs.ErrT3Upstream.Code,
				fmt.Sprintf("引用列表分页超过上限（%d 页），拒绝在不完整列表上核验工作区归属", reviewWorktreeMaxRefPages))
		}
	}
	paths := make([]string, 0, len(byPath))
	for path := range byPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	worktrees := make([]T3Worktree, 0, len(paths))
	for _, path := range paths {
		worktrees = append(worktrees, T3Worktree{Path: path, RefNames: byPath[path]})
	}
	return &T3Registry{Worktrees: worktrees, RefNames: refNames}, nil
}

// ListRegistry 列出一个 T3 工作区根目录（cwd）的完整登记（vcs.listRefs，
// 按 nextCursor 翻到耗尽）：worktree 项供回收前的归属核验，ref 名全集供分支
// 存在性核验。cwd 由调用方给定且必须与随后的删除同源——从 T3 项目表现查
// workspace_root 会在绑定变更后对着另一个 repo 核验。不做服务端 query 过滤：
// 核验需要完整集合判定「未登记」与「分支已删」。总时间预算沿用 t3WriteBudget
// （整段翻页共享 25s）；预算内没翻完返回 errRegistryIncomplete 包装（调用方
// 按待确认处理），上游异常按普通错误上抛。
func (s *T3ThreadsService) ListRegistry(ctx context.Context, userID, envID, cwd string) (*T3Registry, error) {
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
		// 握手把预算耗到点（对端不回握手包、黑洞地址）：与翻页中途的预算
		// 耗尽同一「待确认」语义——同一根因（预算不够/暂时不可达）不能在
		// 开头记 failed、在翻页中途却是待确认。预算内就明确失败的（凭证被
		// 拒、协议不符、连接被拒）仍按上游错误走有限重试。
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %s", errRegistryIncomplete, ctx.Err())
		}
		return nil, s.upstreamError(err)
	}
	defer session.Close()

	listInput := map[string]any{"cwd": cwd, "refKind": "all", "limit": t3ListRefsPageSize}
	return collectT3Registry(ctx, func(cursor *int) (t3WorktreesPage, error) {
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

// errT3RemoveUncertain 删除请求已发出但结果不可见（INT-170）：读超时、连接
// 被掐断等传输层失败——远端 git worktree remove 可能已经执行完（实测 17 次
// 超时里目录后来都不在了）。调用方不能把它当确定失败置 failed，应先重读登记
// 复查远端实际状态。
var errT3RemoveUncertain = errors.New("删除工作区结果待确认")

// t3RemoveUncertainError 包装不确定的删除失败：errors.Is 命中
// errT3RemoveUncertain，Unwrap 透传底层错误保留诊断链，消息保留原始细节。
// 必须在 upstreamError 之前分类——upstreamError 会丢掉 unwrap 链。
type t3RemoveUncertainError struct{ err error }

func (e *t3RemoveUncertainError) Error() string {
	return errT3RemoveUncertain.Error() + "（" + e.err.Error() + "）"
}
func (e *t3RemoveUncertainError) Is(target error) bool { return target == errT3RemoveUncertain }
func (e *t3RemoveUncertainError) Unwrap() error        { return e.err }

// errT3RemoveNotAttempted 删除请求从未送达远端（INT-170）：环境解析/凭证不可
// 用、会话探测或 WS 拨号失败——请求帧没有发出，远端必然没有执行。这是最安
// 全的重试对象，与 errT3RemoveUncertain（已发出、结果不可见，须先复查远端实
// 际状态）区分开：调用方对未送达直接有限重试即可。
var errT3RemoveNotAttempted = errors.New("删除工作区请求未能送达远端")

// t3RemoveNotAttemptedError 包装删除前的准备失败：errors.Is 命中
// errT3RemoveNotAttempted，Unwrap 透传底层错误保留诊断链。
type t3RemoveNotAttemptedError struct{ err error }

func (e *t3RemoveNotAttemptedError) Error() string {
	return errT3RemoveNotAttempted.Error() + "（" + e.err.Error() + "）"
}
func (e *t3RemoveNotAttemptedError) Is(target error) bool { return target == errT3RemoveNotAttempted }
func (e *t3RemoveNotAttemptedError) Unwrap() error        { return e.err }

// RemoveWorktree 删除一个已登记的 worktree（vcs.removeWorktree，force 由调用方给）。
// cwd 由调用方给定（launch 快照优先），核验与删除必须同源。请求发出前的准备
// 失败（环境/凭证/探测/拨号）返回 errT3RemoveNotAttempted 包装，传输层不确定
// 失败返回 errT3RemoveUncertain 包装（INT-170）；明确拒绝按普通上游错误返回。
func (s *T3ThreadsService) RemoveWorktree(ctx context.Context, userID, envID, cwd, path string, force bool) error {
	env, err := s.loadEnvForWrite(ctx, userID, envID)
	if err != nil {
		// 调用方预算在行内被前面的登记读/复查耗到点：与 ListRegistry 同口径
		// 归「待确认」，不算「未送达」的三次重试对象（复审 Note）。
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %s", errRegistryIncomplete, ctx.Err())
		}
		return &t3RemoveNotAttemptedError{err: err}
	}
	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	session, err := s.dialAfterProbe(ctx, env)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %s", errRegistryIncomplete, ctx.Err())
		}
		return &t3RemoveNotAttemptedError{err: err}
	}
	defer session.Close()

	_, err = session.call(ctx, "vcs.removeWorktree", map[string]any{
		"cwd": cwd, "path": path, "force": force,
	})
	if err != nil {
		// 先分类再翻译：upstreamError 生成新错误会丢 unwrap 链。
		var uncertain *rpcUncertainError
		if errors.As(err, &uncertain) {
			return &t3RemoveUncertainError{err: err}
		}
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

// t3TurnVerdict 绑定回合的收尾判定（INT-158）：归档不只是「等不等」的问题，
// 已归档/已删除的墓碑线程连归档对象都没有，须与「终态可归档」区分开。
type t3TurnVerdict int

const (
	turnUnsettled  t3TurnVerdict = iota // 证明不了已收尾：等宽限兜底
	turnSettled                         // run 落已知终态集：可归档
	turnThreadGone                      // 线程已归档/已删除：目标态已达成，直接落账
)

// ThreadTurnSettled 读线程投影，判定该运行绑定的 T3 run 是否已收尾（INT-158）：
// 归档前的收尾宽限等待用——结果落库后 agent 的回合往往还在写收尾回复，立刻
// 归档会切断 provider 事件流，T3 把 run 误记为 failed。run 落已知终态集返回
// turnSettled；已归档/已删除线程返回 turnThreadGone（没有可归档的对象，更没
// 有可切断的事件流）；活动态、queued、未识别状态与投影里找不到该消息的 run
// 都返回 turnUnsettled——证明不了已收尾，等宽限兜底。线程不存在原样透传
// errT3ThreadNotFound；其余错误走 upstreamError。messageID 与
// FailedPreparation 同义：Fast Ship 运行行预分配的 initial_message_id。
func (s *T3ThreadsService) ThreadTurnSettled(ctx context.Context, userID, envID, threadID, messageID string) (t3TurnVerdict, error) {
	env, err := s.loadEnvForWrite(ctx, userID, envID)
	if err != nil {
		return turnUnsettled, err
	}
	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	token, err := s.bearerToken(env)
	if err != nil {
		return turnUnsettled, errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}
	raw, err := s.http.fetchThreadDetail(ctx, env.Address, token, threadID)
	if err != nil {
		if errors.Is(err, errT3ThreadNotFound) {
			return turnUnsettled, err
		}
		return turnUnsettled, s.upstreamError(err)
	}
	parsed, err := decodeT3Projection(raw)
	if err != nil {
		return turnUnsettled, s.upstreamError(fmt.Errorf("线程投影解析失败: %w", err))
	}
	return threadTurnSettledFromProjection(parsed, messageID), nil
}

// threadTurnSettledFromProjection 从线程投影判定运行绑定的 run 是否已收尾
// （纯函数，用真实形状的 projection fixture 钉住契约）。匹配口径与
// failedPreparationFromProjection 相同：按 userMessageId 匹配，同一消息多条
// run 取 ordinal 最大（只有最新那条代表当前状态）。线程已归档/已删除返回
// turnThreadGone——墓碑投影证明目标态已达成，再走归档 RPC 会把非幂等报错
// 误记成归档失败。run 只有落在已知终态集才算收尾：queued 或其它未识别状
// 态、messageID 为空与匹配不到一样按未收尾处理——证明不了已结束，静默放行
// 的代价是退回切断收尾回复的老 bug，等满宽限的代价只是归档晚十分钟。
func threadTurnSettledFromProjection(p *t3ThreadProjection, messageID string) t3TurnVerdict {
	if p.Thread.ArchivedAt != nil && *p.Thread.ArchivedAt != "" {
		return turnThreadGone
	}
	if p.Thread.DeletedAt != nil && *p.Thread.DeletedAt != "" {
		return turnThreadGone
	}
	// messageID 为空（存量行没有 initial_message_id）匹配不了任何具体 run：
	// 空串会撞上投影里缺 userMessageId 的 run（反序列化零值同为空串），
	// 按未收尾处理，等宽限兜底。
	if messageID == "" {
		return turnUnsettled
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
		return turnUnsettled
	}
	if t3SettledRunStatuses[run.Status] {
		return turnSettled
	}
	return turnUnsettled
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
