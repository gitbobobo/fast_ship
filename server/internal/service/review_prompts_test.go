package service

import (
	"strings"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// INT-158 提示词凭证节：两份系统提示词都必须指导 agent 把运行令牌放进 curl
// 配置文件（argv 只剩文件路径）、不进命令行参数/进程列表；渲染后凭证节里仍
// 含真实 fsr_ 令牌（首条消息传输方式不变）。
func TestRenderReviewPromptCredentials(t *testing.T) {
	run := &model.ReviewRun{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		RepoFullName:  "o/r", PRNumber: 7, PRTitle: "t", PRURL: "https://x/pr/7",
		HeadSHA: "sha", BaseRef: "main", HeadRef: "feat",
	}
	for _, kind := range []model.ReviewKind{model.ReviewKindCode, model.ReviewKindVisual} {
		run.Kind = kind
		prompt := renderReviewPrompt(run, "https://fs.example/", "fsr_testtoken123")
		if !strings.Contains(prompt, "## 凭证") {
			t.Fatalf("%s 提示词缺凭证节", kind)
		}
		if !strings.Contains(prompt, "curl -K <文件>") {
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
