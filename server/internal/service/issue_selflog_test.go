package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	gh "github.com/google/go-github/v62/github"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// INT-94 复审（Codex P1）：请求发起的服务层日志必须带 user_id，
// 否则以空归属行入库，经 /api/self-logs 的可见性规则泄露给其他用户。
func TestIssueServiceCreateGitHubIssue_LogsCarryUserID(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {})

	core, observed := observer.New(zapcore.InfoLevel)
	svc.issueService.logger = zap.New(core)

	now := time.Now().UTC()
	fake := &fakeIssueGitHubClient{
		createdIssue: &ghclient.Issue{
			Issue: gh.Issue{
				ID:                int64Ptr(201),
				Number:            intPtr(88),
				State:             stringPtr("open"),
				Title:             stringPtr("GitHub 新建问题"),
				Body:              stringPtr("GitHub 问题描述"),
				HTMLURL:           stringPtr("https://github.com/owner/repo/issues/88"),
				User:              &gh.User{Login: stringPtr("alice")},
				AuthorAssociation: stringPtr("MEMBER"),
				Labels:            []*gh.Label{},
				Comments:          intPtr(0),
				CreatedAt:         &gh.Timestamp{Time: now},
				UpdatedAt:         &gh.Timestamp{Time: now},
			},
			BodyHTML: stringPtr("<p>GitHub 问题描述</p>"),
		},
		// 注入时间线拉取失败，触发 sync timeline 降级日志。
		timelineErrs: map[int]map[int]error{88: {1: errors.New("timeline boom")}},
	}
	svc.issueService.newClient = func(token, owner, repo string) ghclient.Client {
		return fake
	}

	if _, err := svc.issueService.CreateGitHubIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "GitHub 新建问题",
		Body:  api.Ptr("GitHub 问题描述"),
	}); err != nil {
		t.Fatalf("create github issue: %v", err)
	}

	found := false
	for _, e := range observed.TakeAll() {
		if !strings.Contains(e.Message, "sync timeline after creating github issue failed") {
			continue
		}
		found = true
		attributed := false
		for _, f := range e.Context {
			if f.Key == "user_id" && f.String == user.ID {
				attributed = true
			}
		}
		if !attributed {
			t.Fatalf("降级日志未携带 user_id=%s: %v", user.ID, e.Context)
		}
	}
	if !found {
		t.Fatalf("未捕获到 sync timeline 降级日志，注入失败未生效")
	}
}
