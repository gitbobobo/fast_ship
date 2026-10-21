package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// 审查执行引擎（INT-135）对 T3 的四个窄操作：线程工作区真值读取、工作区登记
// 核验与回收、线程归档。除线程投影走 HTTP 外都是一次性 WS 会话（探测 → 拨号
// → RPC → 断开），与写路径同一口径。

// T3Worktree 是 vcs.listRefs 登记的一条工作区项：路径 + ref 名（分支）。
type T3Worktree struct {
	Path    string
	RefName string
}

// T3ThreadWorktree 是线程投影里的工作区真值：worktreePath 与 branch。
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
	out := &T3ThreadWorktree{}
	if parsed.Thread.WorktreePath != nil {
		out.Path = *parsed.Thread.WorktreePath
	}
	if parsed.Thread.Branch != nil {
		out.Branch = *parsed.Thread.Branch
	}
	return out, nil
}

// ListWorktrees 列出一个 T3 项目当前登记的全部工作区（vcs.listRefs 的
// worktreePath 非空项），每条带 ref 名，供回收前的登记核验。同一路径可能在
// 多个 ref 下出现，按路径去重保留首个 ref 名。cwd 从上游项目表
// workspaceRoot 解析（与 createThread 同口径）。
func (s *T3ThreadsService) ListWorktrees(ctx context.Context, userID, envID, projectID string) ([]T3Worktree, error) {
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
	projectShells, err := s.http.fetchProjectSnapshot(ctx, env.Address, token)
	if err != nil {
		return nil, s.upstreamError(err)
	}
	var workspaceRoot string
	for _, project := range projectShells {
		if project.ID == projectID && project.WorkspaceRoot != nil {
			workspaceRoot = *project.WorkspaceRoot
		}
	}
	if workspaceRoot == "" {
		return nil, errs.New(40401, "T3 项目不在该环境（或项目缺少 workspaceRoot）")
	}

	session, err := dialT3RPC(ctx, wsBaseOf(env.Address), token)
	if err != nil {
		return nil, s.upstreamError(err)
	}
	defer session.Close()

	type listRefsPage struct {
		Refs []struct {
			Name         string  `json:"name"`
			WorktreePath *string `json:"worktreePath"`
		} `json:"refs"`
		NextCursor *int `json:"nextCursor"`
	}
	listInput := map[string]any{"cwd": workspaceRoot, "refKind": "all"}
	seen := map[string]string{}
	const maxRefPages = 8
	var nextCursor *int
	for page := 0; page < maxRefPages; page++ {
		if nextCursor != nil {
			listInput["cursor"] = *nextCursor
		}
		value, err := session.call(ctx, "vcs.listRefs", listInput)
		if err != nil {
			return nil, s.upstreamError(err)
		}
		var p listRefsPage
		if err := json.Unmarshal(value, &p); err != nil {
			return nil, errs.New(errs.ErrT3Upstream.Code, "引用列表解析失败: "+shortenT3Error(err))
		}
		for _, ref := range p.Refs {
			if ref.WorktreePath == nil || *ref.WorktreePath == "" {
				continue
			}
			if _, dup := seen[*ref.WorktreePath]; !dup {
				seen[*ref.WorktreePath] = ref.Name
			}
		}
		if p.NextCursor == nil {
			break
		}
		nextCursor = p.NextCursor
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	out := make([]T3Worktree, 0, len(paths))
	for _, path := range paths {
		out = append(out, T3Worktree{Path: path, RefName: seen[path]})
	}
	return out, nil
}

// RemoveWorktree 删除一个已登记的 worktree（vcs.removeWorktree，force 由调用方给）。
// cwd 用项目绑定里快照的 workspace_root。
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
