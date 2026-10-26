package service

import (
	"strings"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// INT-158 提示词凭证节：两份系统提示词都必须指导 agent 把运行令牌放进 curl
// 配置文件（argv 只剩文件路径）、不进命令行参数/进程列表；渲染后凭证节里仍
// 含真实 fsr_ 令牌（首条消息传输方式不变）。
// INT-169：端点全部 run 作用域（/runs/<run id>/...）；凭证节要求建
// fast-ship-review-<run_id>-<随机后缀> 独立目录、禁共享固定路径。
func TestRenderReviewPromptCredentials(t *testing.T) {
	run := &model.ReviewRun{
		ID:            "run-x",
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		RepoFullName:  "o/r", PRNumber: 7, PRTitle: "t", PRURL: "https://x/pr/7",
		HeadSHA: "sha", BaseRef: "main", HeadRef: "feat",
	}
	for _, kind := range []model.ReviewKind{model.ReviewKindCode, model.ReviewKindVisual} {
		run.Kind = kind
		prompt := renderReviewPrompt(run, "https://fs.example/", "fsr_testtoken123")
		if !strings.Contains(prompt, "## 临时目录与凭证") {
			t.Fatalf("%s 提示词缺临时目录与凭证节", kind)
		}
		if !strings.Contains(prompt, "fast-ship-review-run-x-") {
			t.Fatalf("%s 提示词缺按运行隔离的临时目录名", kind)
		}
		if !strings.Contains(prompt, "/api/review-agent/runs/run-x/") {
			t.Fatalf("%s 提示词的接口未按 run 作用域渲染", kind)
		}
		if strings.Contains(prompt, "{{run_id}}") || strings.Contains(prompt, "{{run_token}}") {
			t.Fatalf("%s 提示词有未渲染的占位符", kind)
		}
		if !strings.Contains(prompt, "curl -K <绝对路径>") {
			t.Fatalf("%s 提示词缺 curl -K 配置文件用法", kind)
		}
		if strings.Contains(prompt, "$(cat") {
			t.Fatalf("%s 提示词的命令替换会让令牌泄进进程参数", kind)
		}
		if !strings.Contains(prompt, `header = "Authorization: Bearer fsr_testtoken123"`) {
			t.Fatalf("%s 渲染后凭证节的配置文件示例应含真实令牌", kind)
		}
	}
}

// TestRenderReviewPromptAgentCleanup INT-170 收尾节：代码审查模板要指导代理
// 在提交结论后删除本轮专属的工作树与临时分支（远程 T3 无删分支 RPC，代理
// 收尾是分支回收的主路径）；核验顺序、保护名单与可核验结果行都要钉住。
// 视觉审查不开工作区：无收尾节，「提交成功即结束」原文保留。
func TestRenderReviewPromptAgentCleanup(t *testing.T) {
	run := &model.ReviewRun{
		ID:            "run-170",
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		RepoFullName:  "o/r", PRNumber: 7, PRTitle: "t", PRURL: "https://x/pr/7",
		HeadSHA: "sha170", BaseRef: "main", HeadRef: "feat-1",
	}
	run.Kind = model.ReviewKindCode
	code := renderReviewPrompt(run, "https://fs.example.com", "fsr_tok")

	// 只读条款的狭窄例外：收尾是唯一允许的改动。
	if !strings.Contains(code, "唯一例外：提交结论成功后按「收尾」节删除本轮专属的工作树与临时分支") {
		t.Fatal("代码模板缺只读条款的收尾例外")
	}
	// 四项事实记录与删除命令。
	for _, want := range []string{
		"git rev-parse --show-toplevel",
		"git branch --show-current",
		"git rev-parse --path-format=absolute --git-common-dir",
		"git worktree remove --force <本轮工作树绝对路径>",
		"git branch -D <本轮分支>",
		"cd <主检出根目录> && ...",
	} {
		if !strings.Contains(code, want) {
			t.Fatalf("代码模板收尾节缺关键指令: %q", want)
		}
	}
	// F1：分支核验必须用 refs/heads/ 完整引用名并说明 tag 遮蔽风险；裸名字
	// 的旧写法不得残留。
	if !strings.Contains(code, "git rev-parse refs/heads/<本轮分支>") {
		t.Fatal("代码模板分支核验应改用 refs/heads/ 完整引用名（防同名 tag 遮蔽）")
	}
	if !strings.Contains(code, "refs/tags/") || !strings.Contains(code, "refs/heads/` 完整引用名") {
		t.Fatal("代码模板应说明裸名字会被 refs/tags/ 先解析、必须用 refs/heads/ 全名")
	}
	if strings.Contains(code, "git rev-parse <本轮分支>") {
		t.Fatal("代码模板不应保留裸名字的分支核验写法")
	}
	// F10：detached HEAD 时分支名的兜底取法与跳过分支步的出口。
	for _, want := range []string{
		"git worktree list --porcelain",
		"branch refs/heads/<名>",
		"branch=kept reason=detached-or-unknown",
	} {
		if !strings.Contains(code, want) {
			t.Fatalf("代码模板收尾节缺 detached HEAD 兜底: %q", want)
		}
	}
	// 保护名单：PR 源分支、主干分支不得删，且渲染后必须是真实 ref 名。
	if !strings.Contains(code, "分支名不等于 feat-1、main 或仓库主分支") {
		t.Fatal("代码模板收尾节应列出渲染后的保护分支（head_ref/base_ref）")
	}
	if !strings.Contains(code, "不能仅凭") || !strings.Contains(code, "`t3/` 前缀判定可删") {
		t.Fatal("代码模板应禁止只凭 t3/ 前缀判定可删")
	}
	// 提交成功不再是「即结束」：按收尾节执行。
	if !strings.Contains(code, "提交成功后按「收尾」节执行，不再需要调用审查接口") {
		t.Fatal("代码模板结尾应指向收尾节")
	}
	if strings.Contains(code, "提交成功即结束") {
		t.Fatal("代码模板不应保留「提交成功即结束」旧文案")
	}
	// 可核验结果行。
	if !strings.Contains(code, "cleanup: worktree=<removed|kept> branch=<removed|kept> reason=<保留原因或 ->") {
		t.Fatal("代码模板缺 cleanup 结果行格式")
	}

	// 视觉模板：无收尾节、无 cleanup 行；结束文案原文保留。
	run.Kind = model.ReviewKindVisual
	visual := renderReviewPrompt(run, "https://fs.example.com", "fsr_tok")
	if strings.Contains(visual, "收尾") || strings.Contains(visual, "cleanup: worktree=") {
		t.Fatal("视觉模板不应含收尾节（视觉不开工作区）")
	}
	if !strings.Contains(visual, "提交成功即结束") {
		t.Fatal("视觉模板应保留「提交成功即结束」原文")
	}
}
