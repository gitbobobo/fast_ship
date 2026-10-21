package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"

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
