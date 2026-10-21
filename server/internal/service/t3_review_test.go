package service

import (
	"errors"
	"strings"
	"testing"
)

// 登记收集与线程投影真值提取（t3_review.go）的纯函数测试：分页上限、
// 去重排序、cursor 透传，以及 AppThread 投影的 branch/worktreePath 字段层级。

// page 是测试里构造 t3WorktreesPage 的便捷写法。
func page(cursor *int, refs ...[2]string) t3WorktreesPage {
	p := t3WorktreesPage{NextCursor: cursor}
	for _, r := range refs {
		path := r[1]
		p.Refs = append(p.Refs, struct {
			Name         string  `json:"name"`
			WorktreePath *string `json:"worktreePath"`
		}{Name: r[0], WorktreePath: &path})
	}
	return p
}

// TestCollectWorktreesPaginationLimit 达到页数上限仍有下一页：返回上游错误，
// 绝不把截断列表当完整结果交给核验——截断列表会把已登记误判成「未登记」。
func TestCollectWorktreesPaginationLimit(t *testing.T) {
	next := 999
	calls := 0
	_, err := collectWorktrees(func(cursor *int) (t3WorktreesPage, error) {
		calls++
		return page(&next, [2]string{"ref-" + string(rune('a'+calls)), "/wt/x"}), nil
	})
	if err == nil || !strings.Contains(err.Error(), "分页超过上限") {
		t.Fatalf("err = %v, want 分页超过上限", err)
	}
	if calls != reviewWorktreeMaxRefPages {
		t.Fatalf("calls = %d, want %d（正好到上限）", calls, reviewWorktreeMaxRefPages)
	}
}

// TestCollectWorktreesTwoPages 两页正常收集：cursor 逐页透传、同一路径多个
// ref 名聚合去重、结果按路径排序、worktreePath 为空的 ref 跳过。
func TestCollectWorktreesTwoPages(t *testing.T) {
	c1 := 1
	pages := []t3WorktreesPage{
		page(&c1, [2]string{"br-a", "/wt/a"}, [2]string{"br-b", "/wt/b"}, [2]string{"br-dup", "/wt/a"}),
		page(nil, [2]string{"br-a2", "/wt/a"}, [2]string{"br-c", "/wt/c"}, [2]string{"main", ""}),
		page(nil, [2]string{"unreachable", "/wt/never"}),
	}
	var cursors []*int
	out, err := collectWorktrees(func(cursor *int) (t3WorktreesPage, error) {
		cursors = append(cursors, cursor)
		p := pages[0]
		pages = pages[1:]
		return p, nil
	})
	if err != nil {
		t.Fatalf("collectWorktrees: %v", err)
	}
	if len(cursors) != 2 || cursors[0] != nil || cursors[1] == nil || *cursors[1] != 1 {
		t.Fatalf("cursor 透传错误: %v", cursors)
	}
	if len(pages) != 1 {
		t.Fatalf("第三页不应被取（第二页已无 nextCursor）")
	}
	// 按路径排序；/wt/a 聚合两个 ref 名并去重（br-a 出现两次只留一个）。
	want := []T3Worktree{
		{Path: "/wt/a", RefNames: []string{"br-a", "br-dup", "br-a2"}},
		{Path: "/wt/b", RefNames: []string{"br-b"}},
		{Path: "/wt/c", RefNames: []string{"br-c"}},
	}
	if len(out) != len(want) {
		t.Fatalf("out = %+v, want %+v", out, want)
	}
	for i := range want {
		if out[i].Path != want[i].Path || strings.Join(out[i].RefNames, ",") != strings.Join(want[i].RefNames, ",") {
			t.Fatalf("out[%d] = %+v, want %+v", i, out[i], want[i])
		}
	}
}

// TestCollectWorktreesFetchError 单页读取失败：错误原样上抛，不返回部分列表。
func TestCollectWorktreesFetchError(t *testing.T) {
	wantErr := errors.New("listRefs down")
	_, err := collectWorktrees(func(*int) (t3WorktreesPage, error) {
		return t3WorktreesPage{}, wantErr
	})
	if err != wantErr {
		t.Fatalf("err = %v, want 原样上抛", err)
	}
}

// TestThreadWorktreeFromProjection 用真实形状的 AppThread 投影 fixture 钉住
// 字段层级契约：branch/worktreePath 挂在 projection.thread 下；两者为 null
// 时解析为空串（可空字段，视觉式 root 工作区 / 主检出线程）。
func TestThreadWorktreeFromProjection(t *testing.T) {
	withValues := `{"thread":{"id":"t","projectId":"p","title":"x","branch":"t3/b","worktreePath":"/w/wt"},
		"runs":[],"messages":[],"providerSessions":[],"providerThreads":[],"providerTurns":[],
		"runtimeRequests":[],"turnItems":[],"visibleTurnItems":[]}`
	parsed, err := decodeT3Projection([]byte(withValues))
	if err != nil {
		t.Fatalf("decodeT3Projection: %v", err)
	}
	wt := threadWorktreeFromProjection(parsed)
	if wt.Path != "/w/wt" || wt.Branch != "t3/b" {
		t.Fatalf("worktree = %+v, want {/w/wt t3/b}", wt)
	}

	nulls := `{"thread":{"id":"t","projectId":"p","title":"x","branch":null,"worktreePath":null},
		"runs":[],"messages":[],"providerSessions":[],"providerThreads":[],"providerTurns":[],
		"runtimeRequests":[],"turnItems":[],"visibleTurnItems":[]}`
	parsed, err = decodeT3Projection([]byte(nulls))
	if err != nil {
		t.Fatalf("decodeT3Projection nulls: %v", err)
	}
	wt = threadWorktreeFromProjection(parsed)
	if wt.Path != "" || wt.Branch != "" {
		t.Fatalf("null 字段应解析为空串: %+v", wt)
	}
}
