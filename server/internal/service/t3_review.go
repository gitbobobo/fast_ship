package service

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// 审查执行引擎（INT-135）对 T3 的三个窄操作：工作区路径解析与回收、线程归档。
// 都是一次性 WS 会话（探测 → 拨号 → RPC → 断开），与写路径同一口径。

// ListWorktreePaths 列出一个 T3 项目当前登记的全部 worktree 路径（vcs.listRefs
// 的 worktreePath 非空项），供审查运行「启动前后差集 = 本次工作区」的解析。
// cwd 从上游项目表 workspaceRoot 解析（与 createThread 同口径）。
func (s *T3ThreadsService) ListWorktreePaths(ctx context.Context, userID, envID, projectID string) ([]string, error) {
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
			WorktreePath *string `json:"worktreePath"`
		} `json:"refs"`
		NextCursor *int `json:"nextCursor"`
	}
	listInput := map[string]any{"cwd": workspaceRoot, "refKind": "all"}
	seen := map[string]struct{}{}
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
			if ref.WorktreePath != nil && *ref.WorktreePath != "" {
				seen[*ref.WorktreePath] = struct{}{}
			}
		}
		if p.NextCursor == nil {
			break
		}
		nextCursor = p.NextCursor
	}
	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
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
