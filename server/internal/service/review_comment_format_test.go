package service

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// 顶层评论格式（INT-142 第二部分）：标题（短 SHA + 轮次）、一句话结论、总览表、
// 分节（模型 · 实例 + 选项小字）、发现链接与「未行内标注」注记、Note 折叠。

func fmtTestRun(status model.ReviewRunStatus, result string) model.ReviewRun {
	return model.ReviewRun{
		Kind: model.ReviewKindCode, Status: status, ResultJSON: result,
		Model: "glm-5.3", InstanceID: "claude-code", EnvironmentID: "env",
	}
}

// TestBuildCodeCommentFormatFull 完整渲染：一个 completed（含 P0/P1/P2/Note 与
// 选项）+ 一个 running。
func TestBuildCodeCommentFormatFull(t *testing.T) {
	result := `{"findings":[` +
		`{"level":"P1","file":"server/x.go","line":42,"body":"空指针：map 未初始化就写入"},` +
		`{"level":"P0","file":"server/y.go","line":7,"body":"SQL 拼接注入"},` +
		`{"level":"P2","file":"server/z.go","line":208,"body":"错误被吞掉"},` +
		`{"level":"Note","file":"server/w.go","line":9,"body":"命名可再斟酌"},` +
		`{"level":"Note","file":"server/v.go","body":"日志可加字段"}` +
		`],"prior":[],"summary":"本轮发现 1 个 P0：SQL 拼接注入，必须先修。\n其余为并发与错误处理隐患。",` +
		`"ui_changed":false,"ui_changed_since_last_visual":false}`
	done := fmtTestRun(model.ReviewRunCompleted, result)
	done.ModelOptionsJSON = `[{"id":"effort","value":"high"},{"id":"mode","value":"accept-edits"}]`
	running := fmtTestRun(model.ReviewRunRunning, "")
	running.InstanceID = "codex"

	meta := reviewCommentMeta{RepoFullName: "o/r", HeadSHA: "abcdef1234567890", RoundNumber: 3}
	// 行内判定只认 x.go:42：y.go:7 与 z.go:208 都要加「未行内标注」。
	body := buildCodeComment(meta, []model.ReviewRun{done, running},
		map[string]map[int]bool{"server/x.go": {42: true}}, false, false)

	if !strings.Contains(body, "## 代码审查 · abcdef1 · 第 3 轮") {
		t.Fatalf("missing title: %s", body)
	}
	if !strings.Contains(body, "审查进行中：1/2 个组合已完成。") {
		t.Fatalf("missing running verdict: %s", body)
	}
	if !strings.Contains(body, "| glm-5.3 · claude-code | 完成 | 1 | 1 | 1 | 2 |") {
		t.Fatalf("missing completed table row: %s", body)
	}
	if !strings.Contains(body, "| glm-5.3 · codex | 审查中 | – | – | – | – |") {
		t.Fatalf("missing running table row: %s", body)
	}
	if !strings.Contains(body, "### glm-5.3 · claude-code") ||
		!strings.Contains(body, "<sub>effort=high · mode=accept-edits</sub>") {
		t.Fatalf("missing section heading/options: %s", body)
	}
	if !strings.Contains(body, "> 本轮发现 1 个 P0：SQL 拼接注入，必须先修。\n> 其余为并发与错误处理隐患。") {
		t.Fatalf("missing quoted summary: %s", body)
	}
	if !strings.Contains(body, "**P0** [server/y.go:7](https://github.com/o/r/blob/abcdef1234567890/server/y.go#L7)（未行内标注）") {
		t.Fatalf("missing P0 with not-inlined mark: %s", body)
	}
	if !strings.Contains(body, "**P1** [server/x.go:42](https://github.com/o/r/blob/abcdef1234567890/server/x.go#L42)\n空指针：map 未初始化就写入") {
		t.Fatalf("missing P1 link + body: %s", body)
	}
	if strings.Contains(body, "server/x.go#L42)（未行内标注") {
		t.Fatal("x.go:42 在有效行内，不应加注")
	}
	// 级别稳定排序：P0 → P1 → P2 → Note（折叠块）。
	if p0, p1, p2, det := strings.Index(body, "**P0**"), strings.Index(body, "**P1**"),
		strings.Index(body, "**P2**"), strings.Index(body, "<details>"); !(p0 < p1 && p1 < p2 && p2 < det) {
		t.Fatalf("finding order wrong: p0=%d p1=%d p2=%d details=%d", p0, p1, p2, det)
	}
	if !strings.Contains(body, "<details><summary>Note（2 条）</summary>") ||
		!strings.Contains(body, "- [server/w.go:9](https://github.com/o/r/blob/abcdef1234567890/server/w.go#L9) 命名可再斟酌") ||
		!strings.Contains(body, "- server/v.go 日志可加字段") {
		t.Fatalf("missing folded notes: %s", body)
	}
	if !strings.Contains(body, "### glm-5.3 · codex\n\n审查中…") {
		t.Fatalf("missing running section: %s", body)
	}
	if strings.Contains(body, "视觉审查尚未结束") {
		t.Fatal("visualPending=false 不应出现视觉提示")
	}
	if !strings.HasSuffix(body, "复审说明：推送新提交后会自动重新审查。") {
		t.Fatalf("missing closing line: %s", body)
	}

	// 视觉待办提示仍在表格之后、复审说明之前。
	pending := buildCodeComment(meta, []model.ReviewRun{done, running},
		map[string]map[int]bool{"server/x.go": {42: true}}, true, false)
	if !strings.Contains(pending, "视觉审查尚未结束，将复核截图。") {
		t.Fatalf("missing visual pending hint: %s", pending)
	}
	if i, j, k := strings.Index(pending, "| glm-5.3 · codex"),
		strings.Index(pending, "视觉审查尚未结束"), strings.Index(pending, "### glm-5.3 · claude-code"); !(i < j && j < k) {
		t.Fatalf("visual hint not between table and sections: %d %d %d", i, j, k)
	}
}

// TestBuildCodeCommentNotInlined 未行内标注注记：行号不在 diff 有效行内、无
// 行号、没读 diff（inlineLines=nil）三种口径。
func TestBuildCodeCommentNotInlined(t *testing.T) {
	result := `{"findings":[` +
		`{"level":"P1","file":"a.go","line":5,"body":"x"},` +
		`{"level":"P2","file":"b.go","body":"y"}` +
		`],"prior":[],"summary":"s","ui_changed":false,"ui_changed_since_last_visual":false}`
	run := fmtTestRun(model.ReviewRunCompleted, result)
	meta := reviewCommentMeta{RepoFullName: "o/r", HeadSHA: "abcdef1234567890", RoundNumber: 1}

	// inlineLines 存在但缺该行 → 链接后加注；无行号 → file 后加注。
	body := buildCodeComment(meta, []model.ReviewRun{run}, map[string]map[int]bool{"a.go": {9: true}}, false, false)
	if !strings.Contains(body, "**P1** [a.go:5](https://github.com/o/r/blob/abcdef1234567890/a.go#L5)（未行内标注）") {
		t.Fatalf("missing not-inlined mark on linked finding: %s", body)
	}
	if !strings.Contains(body, "**P2** b.go（未行内标注）") {
		t.Fatalf("missing not-inlined mark on lineless finding: %s", body)
	}

	// 行在 diff 有效行内 → 有行号的不加注（无行号的仍然加注）。
	body = buildCodeComment(meta, []model.ReviewRun{run}, map[string]map[int]bool{"a.go": {5: true}}, false, false)
	if strings.Contains(body, "a.go#L5)（未行内标注") {
		t.Fatalf("inlined finding should not be marked: %s", body)
	}
	if !strings.Contains(body, "**P2** b.go（未行内标注）") {
		t.Fatalf("lineless finding should stay marked: %s", body)
	}

	// 没读 diff（nil）→ 有行号的不臆断加注，无行号的照加。
	body = buildCodeComment(meta, []model.ReviewRun{run}, nil, false, false)
	if strings.Contains(body, "a.go#L5)（未行内标注") {
		t.Fatalf("nil inlineLines should not mark line findings: %s", body)
	}
	if !strings.Contains(body, "**P2** b.go（未行内标注）") {
		t.Fatalf("lineless finding should stay marked with nil inlineLines: %s", body)
	}
}

// TestBuildCodeCommentAllPass 无 P0–P2 → 结论「全部组合通过」；Note 计数照常。
func TestBuildCodeCommentAllPass(t *testing.T) {
	pass := `{"findings":[{"level":"Note","file":"n.go","body":"nit"}],"prior":[],"summary":"通过",` +
		`"ui_changed":false,"ui_changed_since_last_visual":false}`
	run := fmtTestRun(model.ReviewRunCompleted, pass)
	body := buildCodeComment(reviewCommentMeta{RepoFullName: "o/r", HeadSHA: "abcdef1234567890", RoundNumber: 1},
		[]model.ReviewRun{run}, nil, false, false)
	if !strings.Contains(body, "全部组合通过，未发现 P0–P2 问题。") {
		t.Fatalf("missing all-pass verdict: %s", body)
	}
	if !strings.Contains(body, "| glm-5.3 · claude-code | 完成 | 0 | 0 | 0 | 1 |") {
		t.Fatalf("missing note count in table: %s", body)
	}
}

// TestRunCombinationOptions 选项串：值取原生形态、坏 JSON 给空串、超长只截
// 选项而 label（模型 · 实例）完整。
func TestRunCombinationOptions(t *testing.T) {
	run := &model.ReviewRun{
		Model: "glm-5.3", InstanceID: "claude-code",
		ModelOptionsJSON: `[{"id":"effort","value":"high"},{"id":"mode","value":"accept-edits"},` +
			`{"id":"thinking","value":true},{"id":"max_turns","value":40}]`,
	}
	if got, want := runCombinationOptions(run), "effort=high · mode=accept-edits · thinking=true · max_turns=40"; got != want {
		t.Fatalf("runCombinationOptions = %q, want %q", got, want)
	}
	if got := runCombinationOptions(&model.ReviewRun{Model: "m", InstanceID: "i"}); got != "" {
		t.Fatalf("empty options = %q, want empty", got)
	}
	if got := runCombinationOptions(&model.ReviewRun{Model: "m", InstanceID: "i", ModelOptionsJSON: "{oops"}); got != "" {
		t.Fatalf("broken options JSON = %q, want empty", got)
	}

	long := &model.ReviewRun{
		Model: strings.Repeat("模", 90), InstanceID: strings.Repeat("实", 90),
		ModelOptionsJSON: `[{"id":"k","value":"` + strings.Repeat("值", 100) + `"}]`,
	}
	if want := strings.Repeat("模", 90) + " · " + strings.Repeat("实", 90); runCombinationLabel(long) != want {
		t.Fatalf("label 被截断: %q", runCombinationLabel(long))
	}
	opts := runCombinationOptions(long)
	if n := utf8.RuneCountInString(opts); n > reviewOptionsMaxRunes+1 {
		t.Fatalf("options runes = %d, want ≤%d（含省略号）", n, reviewOptionsMaxRunes+1)
	}
	if !strings.HasSuffix(opts, "…") {
		t.Fatalf("超长选项应以省略号结尾: %q", opts)
	}
}
