package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// 登记收集与线程投影真值提取（t3_review.go）的纯函数测试：翻页耗尽、游标
// 推进、预算约束、去重排序、cursor 透传，以及 AppThread 投影的 branch/
// worktreePath 字段层级。

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

// pageCursor 便捷指针（intPtr 已被 issue_test.go 占用）。
func pageCursor(v int) *int { return &v }

// TestCollectRegistryManyPages >800 引用（INT-170 前固定 8 页会截断的量级）
// 跨 5 页全部收集成功：游标逐页透传，登记与 ref 全集完整。
func TestCollectRegistryManyPages(t *testing.T) {
	const total = 850
	pages := make([]t3WorktreesPage, 0, 5)
	for start := 0; start < total; start += t3ListRefsPageSize {
		end := start + t3ListRefsPageSize
		if end > total {
			end = total
		}
		var refs [][2]string
		for i := start; i < end; i++ {
			refs = append(refs, [2]string{fmt.Sprintf("br-%03d", i), fmt.Sprintf("/wt/%03d", i)})
		}
		var next *int
		if end < total {
			next = pageCursor(end)
		}
		pages = append(pages, page(next, refs...))
	}
	var cursors []*int
	reg, err := collectT3Registry(context.Background(), func(cursor *int) (t3WorktreesPage, error) {
		cursors = append(cursors, cursor)
		p := pages[0]
		pages = pages[1:]
		return p, nil
	})
	if err != nil {
		t.Fatalf("collectT3Registry: %v", err)
	}
	if len(cursors) != 5 || len(pages) != 0 {
		t.Fatalf("翻页次数 = %d，剩余页 = %d，want 5 次全部取完", len(cursors), len(pages))
	}
	if len(reg.Worktrees) != total {
		t.Fatalf("worktrees = %d, want %d", len(reg.Worktrees), total)
	}
	if !reg.ContainsWorktree("/wt/000") || !reg.ContainsWorktree(fmt.Sprintf("/wt/%03d", total-1)) {
		t.Fatal("首尾路径都应登记在册")
	}
	if !reg.HasRef("br-000") || !reg.HasRef(fmt.Sprintf("br-%03d", total-1)) {
		t.Fatal("ref 全集应覆盖所有页的 ref 名")
	}
}

// TestCollectRegistryAggregatesAndRefNames 两页正常收集：同一路径多个 ref 名
// 聚合去重、结果按路径排序、worktreePath 为空的 ref 不进 worktree 清单但
// 进 ref 名全集（分支存在性核验依赖全集——分支删掉后 ref 整条消失，只剩
// worktree 项区分不了「分支还在但没 worktree」与「分支已删」）。
func TestCollectRegistryAggregatesAndRefNames(t *testing.T) {
	c1 := 1
	pages := []t3WorktreesPage{
		page(&c1, [2]string{"br-a", "/wt/a"}, [2]string{"br-b", "/wt/b"}, [2]string{"br-dup", "/wt/a"}),
		page(nil, [2]string{"br-a2", "/wt/a"}, [2]string{"br-c", "/wt/c"}, [2]string{"main", ""}),
		page(nil, [2]string{"unreachable", "/wt/never"}),
	}
	var cursors []*int
	reg, err := collectT3Registry(context.Background(), func(cursor *int) (t3WorktreesPage, error) {
		cursors = append(cursors, cursor)
		p := pages[0]
		pages = pages[1:]
		return p, nil
	})
	if err != nil {
		t.Fatalf("collectT3Registry: %v", err)
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
	if len(reg.Worktrees) != len(want) {
		t.Fatalf("worktrees = %+v, want %+v", reg.Worktrees, want)
	}
	for i := range want {
		if reg.Worktrees[i].Path != want[i].Path ||
			strings.Join(reg.Worktrees[i].RefNames, ",") != strings.Join(want[i].RefNames, ",") {
			t.Fatalf("worktrees[%d] = %+v, want %+v", i, reg.Worktrees[i], want[i])
		}
	}
	// 全集：无 worktree 的 ref（main）与普通分支都在；没出现的名字不在。
	for _, name := range []string{"br-a", "br-b", "br-c", "br-a2", "br-dup", "main"} {
		if !reg.HasRef(name) {
			t.Fatalf("ref 全集缺 %q", name)
		}
	}
	if reg.HasRef("never-existed") {
		t.Fatal("未出现过的 ref 名不应在全集里")
	}
}

// TestCollectRegistryStuckCursor 游标不推进/复现已见过的值：明确的上游分页
// 异常错误，不是「不完整」也不是静默死循环。
func TestCollectRegistryStuckCursor(t *testing.T) {
	stuck := 7
	_, err := collectT3Registry(context.Background(), func(*int) (t3WorktreesPage, error) {
		return page(&stuck, [2]string{"br-a", "/wt/a"}), nil
	})
	if err == nil || !strings.Contains(err.Error(), "游标未推进") {
		t.Fatalf("err = %v, want 游标未推进", err)
	}

	// 复现更早出现过的游标值（A→B→A）同样是死循环。
	first, second := 1, 2
	calls := 0
	_, err = collectT3Registry(context.Background(), func(*int) (t3WorktreesPage, error) {
		calls++
		switch calls {
		case 1:
			return page(&first), nil
		case 2:
			return page(&second), nil
		default:
			return page(&first), nil
		}
	})
	if err == nil || !strings.Contains(err.Error(), "游标未推进") {
		t.Fatalf("循环游标 err = %v, want 游标未推进", err)
	}
}

// TestCollectRegistryPageCap 超过页数兜底上限仍有下一页（游标持续向前推进）：
// 明确的上游异常错误，绝不把截断列表当完整结果交给核验——截断列表会把已
// 登记误判成「未登记」。
func TestCollectRegistryPageCap(t *testing.T) {
	calls := 0
	_, err := collectT3Registry(context.Background(), func(cursor *int) (t3WorktreesPage, error) {
		calls++
		next := calls // 游标必须向前推进，否则先撞上「游标未推进」检测
		return page(&next, [2]string{fmt.Sprintf("br-%d", calls), "/wt/x"}), nil
	})
	if err == nil || !strings.Contains(err.Error(), "分页超过上限") {
		t.Fatalf("err = %v, want 分页超过上限", err)
	}
	if calls != reviewWorktreeMaxRefPages {
		t.Fatalf("calls = %d, want %d（正好到上限）", calls, reviewWorktreeMaxRefPages)
	}
}

// TestCollectRegistryBudgetExhausted 预算耗尽（ctx 取消，含页内调用被 ctx
// 掐断）：返回可类型判定的「登记读取不完整」错误——调用方按待确认处理，
// 必须能与普通上游错误区分。
func TestCollectRegistryBudgetExhausted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pagesRead := 0
	_, err := collectT3Registry(ctx, func(cursor *int) (t3WorktreesPage, error) {
		pagesRead++
		if pagesRead == 2 {
			cancel() // 第 2 页后预算到点
		}
		next := pagesRead
		return page(&next, [2]string{"br-a", "/wt/a"}), nil
	})
	cancel()
	if !errors.Is(err, errRegistryIncomplete) {
		t.Fatalf("err = %v, want errRegistryIncomplete 包装", err)
	}

	// 页内调用失败但 ctx 已到点：同样归入预算耗尽。
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	_, err = collectT3Registry(ctx2, func(*int) (t3WorktreesPage, error) {
		return t3WorktreesPage{}, errors.New("boom")
	})
	cancel2()
	if !errors.Is(err, errRegistryIncomplete) {
		t.Fatalf("页内失败 + ctx 到点 err = %v, want errRegistryIncomplete", err)
	}
}

// TestCollectRegistryFetchError 单页读取失败：错误原样上抛，不返回部分列表，
// 也不归入「不完整」（ctx 未取消）。
func TestCollectRegistryFetchError(t *testing.T) {
	wantErr := errors.New("listRefs down")
	_, err := collectT3Registry(context.Background(), func(*int) (t3WorktreesPage, error) {
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
