package github

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRawPreservedOnUnmarshal 钉住 Issue/IssueComment/TimelineEvent 的原始
// 载荷保留：镜像的取证列与前端深链接递归搜 html_url 依赖未映射字段完整
// 写回，Raw 自身不进序列化输出（service 的 rawJSONString 回退路径也依赖
// 这一点，否则 Raw 会被二次嵌进载荷里）。
func TestRawPreservedOnUnmarshal(t *testing.T) {
	t.Run("issue keeps unknown fields and pull_request marker", func(t *testing.T) {
		payload := `{"id":1,"number":2,"title":"t","pull_request":{"url":"u"},"custom_field":"keep-me"}`
		var issue Issue
		if err := json.Unmarshal([]byte(payload), &issue); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !issue.IsPullRequest() {
			t.Fatal("expected IsPullRequest")
		}
		if !strings.Contains(string(issue.Raw), `"custom_field":"keep-me"`) {
			t.Fatalf("raw missing unknown field: %s", issue.Raw)
		}
		out, err := json.Marshal(&issue)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(out), `"custom_field"`) {
			t.Fatalf("marshal should only emit declared fields: %s", out)
		}
	})

	t.Run("timeline nested source issue keeps own raw", func(t *testing.T) {
		payload := `{"id":7,"event":"cross-referenced","source":{"issue":{"number":9,"html_url":"https://example.com/i/9","extra":"x"}},"html_url":"https://example.com/e/7"}`
		var event TimelineEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !strings.Contains(string(event.Raw), `"html_url"`) {
			t.Fatalf("event raw lost html_url: %s", event.Raw)
		}
		if event.GetSource() == nil || event.GetSource().Issue == nil {
			t.Fatal("source issue not decoded")
		}
		if got := event.Source.Issue.GetHTMLURL(); got != "https://example.com/i/9" {
			t.Fatalf("nested html_url = %q", got)
		}
		if !strings.Contains(string(event.Source.Issue.Raw), `"extra":"x"`) {
			t.Fatalf("nested raw missing unknown field: %s", event.Source.Issue.Raw)
		}
	})

	t.Run("comment raw not re-emitted on marshal", func(t *testing.T) {
		var comment IssueComment
		if err := json.Unmarshal([]byte(`{"id":3,"body":"hi","unknown":42}`), &comment); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		out, err := json.Marshal(&comment)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(out), `"unknown"`) || strings.Contains(string(out), `"Raw"`) {
			t.Fatalf("raw leaked into marshal output: %s", out)
		}
	})
}
