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
