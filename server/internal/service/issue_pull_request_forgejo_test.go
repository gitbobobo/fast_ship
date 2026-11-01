package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/forge"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/forges"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Issue 关联 Forgejo PR（INT-181）：URL 分流、attach 集成解析与快照落库、
// sync 按快照重建客户端与单行失败域。Forgejo 适配器本身的请求形状与响应映射
// 由 forge 包的 httptest 测试覆盖；这里用假读取端钉住 service 侧编排。

// fakeForgejoPRReader 关联 PR 链路的 Forgejo 假读取端：按 repo+number 返回登记
// 的 PR 快照，按编号注入失败。calls/built 记录取数与组装次数——sync 按
// ForgeIntegrationID 缓存客户端，同集成的一轮 sync 只组装一次。
type fakeForgejoPRReader struct {
	prs       map[string]map[int]*forge.PullRequest
	errs      map[int]error
	calls     int
	built     int
	lastBase  string
	lastToken string
}

func (f *fakeForgejoPRReader) GetPullRequest(_ context.Context, repoFullName string, number int) (*forge.PullRequest, error) {
	f.calls++
	if err, ok := f.errs[number]; ok {
		return nil, err
	}
	if m, ok := f.prs[repoFullName]; ok {
		if pr, ok := m[number]; ok {
			return pr, nil
		}
	}
	return nil, &forge.StatusError{StatusCode: http.StatusNotFound, Method: http.MethodGet, URL: repoFullName, Body: "Not Found"}
}

// stubForgejoPRReader 注入假读取端并记录组装参数：钉住「集成展开的 API 基址
// 与解密后的机器人 PAT 进适配器」。
func stubForgejoPRReader(t *testing.T, svc *testServices, wantBase, wantToken string) *fakeForgejoPRReader {
	t.Helper()
	reader := &fakeForgejoPRReader{}
	svc.issueService.newForgejoPRReader = func(_ *http.Client, apiBase, token string) forgePRReader {
		reader.built++
		reader.lastBase, reader.lastToken = apiBase, token
		if wantBase != "" && apiBase != wantBase {
			t.Fatalf("unexpected api base: %q", apiBase)
		}
		if wantToken != "" && token != wantToken {
			t.Fatalf("unexpected forge token: %q", token)
		}
		return reader
	}
	return reader
}

// createForgejoIntegrationRow 落一条 forgejo 集成行：id 由调用方指定（快照
// 断言用），PAT 以测试密钥加密（ForgeClientSource 要真解密），host_key 按
// forges.HostKey 现算（与生产保存侧同口径），API 基址留空走展开推导。
// host_key 按 forges.HostKey 现算（与生产保存侧同口径）。
func createForgejoIntegrationRow(t *testing.T, db *gorm.DB, id, baseURL, pat string) *model.ForgeIntegration {
	t.Helper()
	enc, err := crypto.Encrypt([]byte(pat), []byte(testEncryptionKey))
	if err != nil {
		t.Fatalf("encrypt forge pat: %v", err)
	}
	row := &model.ForgeIntegration{
		ID:             id,
		Kind:           model.ForgeIntegrationKindForgejo,
		Name:           "forge-" + id,
		BaseURL:        baseURL,
		HostKey:        forges.HostKey(baseURL),
		TokenEncrypted: enc,
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("create forge integration: %v", err)
	}
	return row
}

// testForgePullRequest 组装 forge 中性类型的 PR 快照（对应 testPullRequest）。
func testForgePullRequest(repoFullName string, number int, state string, merged bool, mergedAt *time.Time) *forge.PullRequest {
	pr := &forge.PullRequest{
		RepoFullName: repoFullName,
		Number:       number,
		State:        state,
		Merged:       merged,
		Title:        fmt.Sprintf("Forge PR #%d", number),
		HTMLURL:      fmt.Sprintf("https://forge.example.com/%s/pulls/%d", repoFullName, number),
		AuthorLogin:  "fastship-bot",
		HeadRef:      "feat",
		BaseRef:      "main",
	}
	if mergedAt != nil {
		pr.MergedAt = mergedAt
		pr.ClosedAt = mergedAt
	}
	return pr
}

// bindProjectForgejo 把项目绑到指定代码集成（模拟项目保存时 parseRepoBinding
// 派生的 code_integration_id）：部署级 PAT 的作用面校验要求项目绑定与 PR 主
// 机一致。
func bindProjectForgejo(t *testing.T, svc *testServices, projectID, integrationID string) {
	t.Helper()
	if err := svc.db.Model(&model.Project{}).Where("id = ?", projectID).
		Update("code_integration_id", integrationID).Error; err != nil {
		t.Fatalf("bind project integration: %v", err)
	}
}

func forgePRSet(prs ...*forge.PullRequest) map[string]map[int]*forge.PullRequest {
	out := map[string]map[int]*forge.PullRequest{}
	for _, pr := range prs {
		if out[pr.RepoFullName] == nil {
			out[pr.RepoFullName] = map[int]*forge.PullRequest{}
		}
		out[pr.RepoFullName][pr.Number] = pr
	}
	return out
}

// TestParsePullRequestLink_DualFormats URL 分流：github.com 域 + /pull/{n} 走
// GitHub；其他主机 + /pulls/{n} 走 Forgejo 并带出归一主机键；github 域内形状
// 不符按无效，不误判成 Forgejo。
func TestParsePullRequestLink_DualFormats(t *testing.T) {
	cases := []struct {
		name   string
		url    string
		want   pullRequestLink
		wantOK bool
	}{
		{name: "github 标准链接", url: "https://github.com/owner/repo/pull/7",
			want: pullRequestLink{Provider: model.IssuePullRequestProviderGitHub, Owner: "owner", Repo: "repo", Number: 7}, wantOK: true},
		{name: "github 尾部与 www", url: "http://www.github.com/owner/repo/pull/7/files#x",
			want: pullRequestLink{Provider: model.IssuePullRequestProviderGitHub, Owner: "owner", Repo: "repo", Number: 7}, wantOK: true},
		{name: "forgejo https", url: "https://fj.frp.musiver.cn/fjbobo/fast_ship/pulls/158",
			want: pullRequestLink{Provider: model.IssuePullRequestProviderForgejo, Owner: "fjbobo", Repo: "fast_ship", Number: 158, HostKey: "fj.frp.musiver.cn:443"}, wantOK: true},
		{name: "forgejo http 显式端口", url: "http://nas.local:3000/o/r/pulls/5",
			want: pullRequestLink{Provider: model.IssuePullRequestProviderForgejo, Owner: "o", Repo: "r", Number: 5, HostKey: "nas.local:3000"}, wantOK: true},
		{name: "forgejo /files 尾部", url: "https://forge.example.com/o/r/pulls/12/files?q=1#f",
			want: pullRequestLink{Provider: model.IssuePullRequestProviderForgejo, Owner: "o", Repo: "r", Number: 12, HostKey: "forge.example.com:443"}, wantOK: true},
		{name: "forgejo 大小写混合", url: "HTTPS://Forge.Example.COM/O/R/PULLS/3",
			want: pullRequestLink{Provider: model.IssuePullRequestProviderForgejo, Owner: "O", Repo: "R", Number: 3, HostKey: "forge.example.com:443"}, wantOK: true},
		{name: "github 域 pulls 形状无效", url: "https://github.com/owner/repo/pulls/7", wantOK: false},
		{name: "github www 域 pulls 形状无效", url: "https://www.github.com/owner/repo/pulls/7", wantOK: false},
		{name: "github 尾点域 pulls 仍无效", url: "https://github.com./owner/repo/pulls/7", wantOK: false},
		{name: "github www 尾点域 pulls 仍无效", url: "https://www.github.com./owner/repo/pulls/7", wantOK: false},
		{name: "github 显式非标端口同域拒绝", url: "https://github.com:8443/owner/repo/pulls/7", wantOK: false},
		{name: "forgejo 尾点主机归一同键", url: "https://forge.example.com./o/r/pulls/3",
			want: pullRequestLink{Provider: model.IssuePullRequestProviderForgejo, Owner: "o", Repo: "r", Number: 3, HostKey: "forge.example.com:443"}, wantOK: true},
		{name: "forgejo 端口前导零归一", url: "https://forge.example.com:0443/o/r/pulls/3",
			want: pullRequestLink{Provider: model.IssuePullRequestProviderForgejo, Owner: "o", Repo: "r", Number: 3, HostKey: "forge.example.com:443"}, wantOK: true},
		{name: "forgejo userinfo 不影响主机键", url: "https://bot@forge.example.com/o/r/pulls/3",
			want: pullRequestLink{Provider: model.IssuePullRequestProviderForgejo, Owner: "o", Repo: "r", Number: 3, HostKey: "forge.example.com:443"}, wantOK: true},
		{name: "空主机无效", url: "https://./o/r/pulls/7", wantOK: false},
		{name: "forgejo 编号为零无效", url: "https://forge.example.com/o/r/pulls/0", wantOK: false},
		{name: "gitlab merge request 无效", url: "https://gitlab.com/owner/repo/merge_requests/7", wantOK: false},
		{name: "forgejo 点段 owner 无效", url: "https://forge.example.com/../repo/pulls/7", wantOK: false},
		{name: "forgejo 缺仓库段无效", url: "https://forge.example.com/o/pulls/7", wantOK: false},
		{name: "非 http scheme 无效", url: "ftp://forge.example.com/o/r/pulls/7", wantOK: false},
	}
	for _, tc := range cases {
		got, ok := parsePullRequestLink(tc.url)
		if ok != tc.wantOK {
			t.Fatalf("%s: parsePullRequestLink(%q) ok = %v, want %v", tc.name, tc.url, ok, tc.wantOK)
		}
		if !tc.wantOK {
			continue
		}
		if got != tc.want {
			t.Fatalf("%s: parsePullRequestLink(%q) = %+v, want %+v", tc.name, tc.url, got, tc.want)
		}
	}
}

// attach 成功：按主机键命中集成，用解密后的机器人 PAT 拉取详情，行落
// provider=forgejo 与集成快照，状态映射 merged，详情投影可见。
func TestIssueServiceAttachPullRequest_ForgejoSucceeds(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)
	// 另一个主机的集成不该被命中。
	createForgejoIntegrationRow(t, svc.db, "fj-other", "https://other.example.com", "pat-other")
	createForgejoIntegrationRow(t, svc.db, "fj-1", "https://forge.example.com", "fj-pat")
	bindProjectForgejo(t, svc, project.ID, "fj-1")

	mergedAt := time.Now().Add(-time.Hour).UTC()
	reader := stubForgejoPRReader(t, svc, "https://forge.example.com/api/v1", "fj-pat")
	reader.prs = forgePRSet(testForgePullRequest("fjbobo/fast_ship", 158, "closed", true, &mergedAt))

	resp, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://forge.example.com/fjbobo/fast_ship/pulls/158",
	})
	if err != nil {
		t.Fatalf("attach forgejo pull request: %v", err)
	}
	if reader.calls != 1 || reader.built != 1 {
		t.Fatalf("expected one build+fetch, got built=%d calls=%d", reader.built, reader.calls)
	}
	if resp.Provider != model.IssuePullRequestProviderForgejo || resp.RepoFullName != "fjbobo/fast_ship" || resp.Number != 158 {
		t.Fatalf("unexpected link: %+v", resp)
	}
	if resp.State != model.IssuePullRequestStateMerged || resp.MergedAt == nil {
		t.Fatalf("expected merged state mapping, got %+v", resp)
	}
	if resp.AuthorLogin != "fastship-bot" || resp.Title != "Forge PR #158" {
		t.Fatalf("expected server-fetched fields, got %+v", resp)
	}

	var row model.IssuePullRequest
	if err := svc.db.Where("issue_id = ?", issue.ID).First(&row).Error; err != nil {
		t.Fatalf("load link row: %v", err)
	}
	if row.ForgeIntegrationID != "fj-1" {
		t.Fatalf("expected forge integration snapshot fj-1, got %q", row.ForgeIntegrationID)
	}
	if row.LinkOrigin != model.IssuePullRequestLinkOriginManual || row.State != model.IssuePullRequestStateMerged {
		t.Fatalf("unexpected row: %+v", row)
	}

	links, err := svc.issueService.ListIssuePullRequests(issue.ID)
	if err != nil {
		t.Fatalf("list pull requests: %v", err)
	}
	if len(links) != 1 || links[0].Provider != model.IssuePullRequestProviderForgejo {
		t.Fatalf("expected forgejo link in issue projection, got %+v", links)
	}
}

// 未配置可处理该主机的 Forgejo 集成：40007 明确指向集成管理，不落记录。
func TestIssueServiceAttachPullRequest_ForgejoIntegrationMissing(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)

	svc.issueService.newForgejoPRReader = func(*http.Client, string, string) forgePRReader {
		t.Fatalf("forgejo reader must not be built without integration")
		return nil
	}

	_, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://forge.example.com/o/r/pulls/9",
	})
	appErr, ok := err.(*errs.AppError)
	if !ok || appErr.Code != errs.ErrForgeIntegrationNotConfigured.Code {
		t.Fatalf("expected %d for missing integration, got %v", errs.ErrForgeIntegrationNotConfigured.Code, err)
	}
	if count := countPullRequestRows(t, svc, issue.ID); count != 0 {
		t.Fatalf("expected no link rows, got %d", count)
	}
}

// 主机上有集成但项目未绑定代码集成：40007，部署级机器人 PAT 不服务未接入
// 该 Forgejo 的项目——防止任意项目成员借集成 PAT 读项目外仓库的 PR 元数据。
// 文案按事实分态：代码仓在内置 GitHub 的项目得到「仅支持 GitHub PR」的指
// 引，不是让用户去改绑的死路。
func TestIssueServiceAttachPullRequest_ForgejoProjectNotBound(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)
	createForgejoIntegrationRow(t, svc.db, "fj-1", "https://forge.example.com", "fj-pat")

	svc.issueService.newForgejoPRReader = func(*http.Client, string, string) forgePRReader {
		t.Fatalf("forgejo reader must not be built for unbound project")
		return nil
	}

	_, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://forge.example.com/o/r/pulls/9",
	})
	appErr, ok := err.(*errs.AppError)
	if !ok || appErr.Code != errs.ErrForgeIntegrationNotConfigured.Code {
		t.Fatalf("expected %d for unbound project, got %v", errs.ErrForgeIntegrationNotConfigured.Code, err)
	}
	if !strings.Contains(appErr.Message, "未配置代码仓库") {
		t.Fatalf("expected binding-oriented message, got %q", appErr.Message)
	}
	if count := countPullRequestRows(t, svc, issue.ID); count != 0 {
		t.Fatalf("expected no link rows, got %d", count)
	}

	// 代码仓在 GitHub（CodeGithubOwner 有值、code_integration_id 空）的项目：
	// 文案点明「仅支持 GitHub PR」，不指引改绑。
	if err := svc.db.Model(&model.Project{}).Where("id = ?", project.ID).
		Update("code_github_owner", "owner").Error; err != nil {
		t.Fatalf("set code github owner: %v", err)
	}
	_, err = svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://forge.example.com/o/r/pulls/9",
	})
	appErr, ok = err.(*errs.AppError)
	if !ok || appErr.Code != errs.ErrForgeIntegrationNotConfigured.Code {
		t.Fatalf("expected %d for github-bound project, got %v", errs.ErrForgeIntegrationNotConfigured.Code, err)
	}
	if !strings.Contains(appErr.Message, "仅支持关联 GitHub PR") {
		t.Fatalf("expected github-bound message, got %q", appErr.Message)
	}
}

// Forgejo 对「PR 不存在」与「PAT 无权」都返回 404：50206 文案两种可能都保留，
// 不落记录。
func TestIssueServiceAttachPullRequest_ForgejoPRNotFound(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)
	createForgejoIntegrationRow(t, svc.db, "fj-1", "https://forge.example.com", "fj-pat")
	bindProjectForgejo(t, svc, project.ID, "fj-1")

	reader := stubForgejoPRReader(t, svc, "", "")
	// 错误原文带集成的内网 API 地址与响应体：都不允许外显。
	reader.errs = map[int]error{9: &forge.StatusError{
		StatusCode: http.StatusNotFound, Method: http.MethodGet,
		URL:  "https://internal.forge.local:8443/api/v1/repos/o/r/pulls/9",
		Body: `{"message":"internal placement"}`,
	}}

	_, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://forge.example.com/o/r/pulls/9",
	})
	appErr, ok := err.(*errs.AppError)
	if !ok || appErr.Code != errs.ErrForgeAPI.Code {
		t.Fatalf("expected %d for forgejo fetch failure, got %v", errs.ErrForgeAPI.Code, err)
	}
	for _, want := range []string{"不存在", "无权"} {
		if !strings.Contains(appErr.Message, want) {
			t.Fatalf("expected message to keep both 404 causes, got %q", appErr.Message)
		}
	}
	if !strings.Contains(appErr.Message, "状态码 404") {
		t.Fatalf("expected message to keep status code, got %q", appErr.Message)
	}
	for _, leak := range []string{"internal.forge.local", "internal placement", "8443"} {
		if strings.Contains(appErr.Message, leak) {
			t.Fatalf("message leaks internals %q: %q", leak, appErr.Message)
		}
	}
	if count := countPullRequestRows(t, svc, issue.ID); count != 0 {
		t.Fatalf("expected no link rows, got %d", count)
	}
}

// 幂等重复 attach：同一 Forgejo PR 只有一行；换绑集成后快照跟随本次解析。
func TestIssueServiceAttachPullRequest_ForgejoIdempotentAndSnapshotRefresh(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)
	createForgejoIntegrationRow(t, svc.db, "fj-1", "https://forge.example.com", "fj-pat")
	bindProjectForgejo(t, svc, project.ID, "fj-1")

	reader := stubForgejoPRReader(t, svc, "", "")
	reader.prs = forgePRSet(testForgePullRequest("o/r", 5, "open", false, nil))

	first, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://forge.example.com/o/r/pulls/5",
	})
	if err != nil {
		t.Fatalf("first attach: %v", err)
	}
	if reader.lastToken != "fj-pat" {
		t.Fatalf("expected integration PAT, got %q", reader.lastToken)
	}
	if count := countPullRequestRows(t, svc, issue.ID); count != 1 {
		t.Fatalf("expected exactly one link row, got %d", count)
	}

	// 同 URL 再 attach：仍是一行，返回既有行。
	second, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://forge.example.com/o/r/pulls/5/files",
	})
	if err != nil {
		t.Fatalf("second attach: %v", err)
	}
	if second.Id != first.Id || countPullRequestRows(t, svc, issue.ID) != 1 {
		t.Fatalf("expected idempotent re-attach, got %q vs %q", second.Id, first.Id)
	}

	// 换绑：原集成删除，同主机新建集成。项目绑定仍指向已删的 fj-1 时
	// attach 被作用面校验拒绝；重新绑定到 fj-2 后重复 attach，快照指向新集成。
	if err := svc.db.Delete(&model.ForgeIntegration{}, "id = ?", "fj-1").Error; err != nil {
		t.Fatalf("delete old integration: %v", err)
	}
	createForgejoIntegrationRow(t, svc.db, "fj-2", "https://forge.example.com", "fj-pat-2")
	if _, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://forge.example.com/o/r/pulls/5",
	}); err == nil {
		t.Fatalf("attach with dangling binding must fail")
	} else if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrForgeIntegrationNotConfigured.Code {
		t.Fatalf("expected %d for dangling binding, got %v", errs.ErrForgeIntegrationNotConfigured.Code, err)
	}
	bindProjectForgejo(t, svc, project.ID, "fj-2")
	reader.lastToken = ""
	third, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://forge.example.com/o/r/pulls/5",
	})
	if err != nil {
		t.Fatalf("attach after rebind: %v", err)
	}
	if reader.lastToken != "fj-pat-2" {
		t.Fatalf("expected new integration PAT, got %q", reader.lastToken)
	}
	var row model.IssuePullRequest
	if err := svc.db.Where("issue_id = ?", issue.ID).First(&row).Error; err != nil {
		t.Fatalf("load link row: %v", err)
	}
	if row.ForgeIntegrationID != "fj-2" {
		t.Fatalf("expected snapshot refreshed to fj-2, got %q", row.ForgeIntegrationID)
	}
	if third.Id != first.Id {
		t.Fatalf("re-attach after rebind should keep the same row, got %q vs %q", third.Id, first.Id)
	}
}

// sync 按 provider 分流：forgejo 行按快照重建客户端并刷新状态（merged 与
// closed→open 复活都按平台数据重置）；同集成的一轮 sync 只组装一次客户端。
func TestIssueServiceSyncPullRequests_ForgejoRefreshesStates(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)
	createForgejoIntegrationRow(t, svc.db, "fj-1", "https://forge.example.com", "fj-pat")
	bindProjectForgejo(t, svc, project.ID, "fj-1")

	reader := stubForgejoPRReader(t, svc, "https://forge.example.com/api/v1", "fj-pat")
	reader.prs = forgePRSet(
		testForgePullRequest("o/r", 5, "open", false, nil),
		testForgePullRequest("o/r", 6, "open", false, nil),
	)
	for _, n := range []int{5, 6} {
		if _, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
			Url: fmt.Sprintf("https://forge.example.com/o/r/pulls/%d", n),
		}); err != nil {
			t.Fatalf("attach pr %d: %v", n, err)
		}
	}

	mergedAt := time.Now().Add(-time.Minute).UTC()
	reader.prs = forgePRSet(
		testForgePullRequest("o/r", 5, "closed", true, &mergedAt),
		testForgePullRequest("o/r", 6, "closed", false, nil),
	)
	builtBefore := reader.built
	result, err := svc.issueService.SyncIssuePullRequests(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(result.Items) != 2 || len(result.Failures) != 0 {
		t.Fatalf("expected 2 items 0 failures, got %+v", result)
	}
	if reader.built-builtBefore != 1 {
		t.Fatalf("expected one reader build per sync for the same integration, got %d", reader.built-builtBefore)
	}
	byNumber := map[int]*IssuePullRequestResponse{}
	for i := range result.Items {
		byNumber[result.Items[i].Number] = &result.Items[i]
	}
	if byNumber[5].State != model.IssuePullRequestStateMerged || byNumber[5].MergedAt == nil {
		t.Fatalf("expected pr 5 merged with merged_at, got %+v", byNumber[5])
	}
	if byNumber[6].State != model.IssuePullRequestStateClosed || byNumber[6].MergedAt != nil {
		t.Fatalf("expected pr 6 closed without merged_at, got %+v", byNumber[6])
	}

	// closed→open 复活：合并时间一并清空（apply 重置语义）。
	reader.prs = forgePRSet(
		testForgePullRequest("o/r", 5, "open", false, nil),
		testForgePullRequest("o/r", 6, "closed", false, nil),
	)
	result, err = svc.issueService.SyncIssuePullRequests(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("expected 2 items, got %+v", result)
	}
	for i := range result.Items {
		if result.Items[i].Number == 5 && (result.Items[i].State != model.IssuePullRequestStateOpen || result.Items[i].MergedAt != nil) {
			t.Fatalf("expected pr 5 reopened with cleared merge fields, got %+v", result.Items[i])
		}
	}
}

// sync 单行失败域：集成行被删只让 forgejo 行失败，同 Issue 的 github 行照常
// 刷新。
func TestIssueServiceSyncPullRequests_ForgejoIntegrationMissingSingleFailure(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)
	// 同一主机两个集成行同属一个 Forgejo：删 fj-1 前先建 fj-2 备用行，
	// 断言失败只落在快照指向 fj-1 的行上。
	_ = createForgejoIntegrationRow(t, svc.db, "fj-1", "https://forge.example.com", "fj-pat")
	bindProjectForgejo(t, svc, project.ID, "fj-1")

	reader := stubForgejoPRReader(t, svc, "", "")
	reader.prs = forgePRSet(testForgePullRequest("o/r", 5, "open", false, nil))
	if _, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://forge.example.com/o/r/pulls/5",
	}); err != nil {
		t.Fatalf("attach forgejo pr: %v", err)
	}

	ghFake := &fakeIssueGitHubClient{
		pullRequests: prSet(testPullRequest("owner/repo", 7, "open", nil)),
	}
	stubPullRequestClient(t, svc, ghFake, "gh-token")
	if _, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://github.com/owner/repo/pull/7",
	}); err != nil {
		t.Fatalf("attach github pr: %v", err)
	}

	if err := svc.db.Delete(&model.ForgeIntegration{}, "id = ?", "fj-1").Error; err != nil {
		t.Fatalf("delete integration: %v", err)
	}

	result, err := svc.issueService.SyncIssuePullRequests(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Provider != model.IssuePullRequestProviderGitHub {
		t.Fatalf("expected github row refreshed, got %+v", result.Items)
	}
	if len(result.Failures) != 1 {
		t.Fatalf("expected exactly one failure, got %+v", result.Failures)
	}
	if !strings.Contains(result.Failures[0].Error, "o/r#5") || !strings.Contains(result.Failures[0].Error, "不可用或已被删除") {
		t.Fatalf("expected failure naming the forgejo row and missing integration, got %q", result.Failures[0].Error)
	}
	// 失败行旧数据保留。
	var row model.IssuePullRequest
	if err := svc.db.Where("issue_id = ? AND provider = ?", issue.ID, model.IssuePullRequestProviderForgejo).First(&row).Error; err != nil {
		t.Fatalf("load forgejo row: %v", err)
	}
	if row.State != model.IssuePullRequestStateOpen || row.Title != "Forge PR #5" {
		t.Fatalf("expected stale forgejo data preserved, got %+v", row)
	}
}

// 历史脏行：forgejo 行缺集成快照（快照列上线前的数据不该存在，防御）或
// provider 未知，都只记单行失败，不影响其余行。
func TestIssueServiceSyncPullRequests_ForgejoDegenerateRowsRecorded(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)
	createForgejoIntegrationRow(t, svc.db, "fj-1", "https://forge.example.com", "fj-pat")
	bindProjectForgejo(t, svc, project.ID, "fj-1")

	reader := stubForgejoPRReader(t, svc, "", "")
	reader.prs = forgePRSet(testForgePullRequest("o/r", 5, "open", false, nil))
	if _, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://forge.example.com/o/r/pulls/5",
	}); err != nil {
		t.Fatalf("attach forgejo pr: %v", err)
	}

	now := time.Now().UTC()
	degenerates := []model.IssuePullRequest{
		{
			ID: uuid.NewString(), IssueID: issue.ID, ProjectID: project.ID,
			Provider: model.IssuePullRequestProviderForgejo, RepoFullName: "x/y", Number: 8,
			State: model.IssuePullRequestStateOpen, LinkOrigin: model.IssuePullRequestLinkOriginManual,
			ForgeIntegrationID: "", SyncedAt: now, CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: uuid.NewString(), IssueID: issue.ID, ProjectID: project.ID,
			Provider: "gitlab", RepoFullName: "x/z", Number: 9,
			State: model.IssuePullRequestStateOpen, LinkOrigin: model.IssuePullRequestLinkOriginManual,
			SyncedAt: now, CreatedAt: now, UpdatedAt: now,
		},
	}
	for i := range degenerates {
		if err := svc.db.Create(&degenerates[i]).Error; err != nil {
			t.Fatalf("seed degenerate row: %v", err)
		}
	}

	result, err := svc.issueService.SyncIssuePullRequests(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Number != 5 {
		t.Fatalf("expected only the healthy forgejo row refreshed, got %+v", result.Items)
	}
	if len(result.Failures) != 2 {
		t.Fatalf("expected two failures, got %+v", result.Failures)
	}
	failures := map[string]string{}
	for _, f := range result.Failures {
		failures[f.Id] = f.Error
	}
	if !strings.Contains(failures[degenerates[0].ID], "缺少 Forgejo 集成快照") {
		t.Fatalf("expected missing-snapshot failure, got %q", failures[degenerates[0].ID])
	}
	if !strings.Contains(failures[degenerates[1].ID], "不支持的 PR 来源") {
		t.Fatalf("expected unsupported provider failure, got %q", failures[degenerates[1].ID])
	}
}

// TestForgeClientSource_IssueServiceWiring IssueService 的 Forgejo 凭证面接的
// 就是生产 ForgeClientSource：按集成行解密 PAT 并展开 API 基址（防止装配点
// 误接桩或空实现——那会让 attach 恒报「凭证来源未配置」）。
func TestForgeClientSource_IssueServiceWiring(t *testing.T) {
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.ForgeIntegration{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := &config.Config{Encryption: config.EncryptionConfig{Key: testEncryptionKey}}
	svc := &IssueService{
		forgeSource: NewForgeClientSource(repository.NewForgeIntegrationRepository(db), cfg, nil),
	}
	createForgejoIntegrationRow(t, db, "fj-1", "https://forge.example.com", "fj-pat")
	httpClient, apiBase, token, err := svc.forgeSource.ForgejoClient(context.Background(), "fj-1")
	if err != nil {
		t.Fatalf("ForgejoClient: %v", err)
	}
	if httpClient == nil || apiBase != "https://forge.example.com/api/v1" || token != "fj-pat" {
		t.Fatalf("unexpected forgejo client triple: %v %q %q", httpClient != nil, apiBase, token)
	}
}

// 传输层错误（连不上集成指向的服务）：对外只留固定提示，错误原文可能带的
// 内部主机名不外显；不落记录。
func TestIssueServiceAttachPullRequest_ForgejoTransportErrorSanitized(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)
	createForgejoIntegrationRow(t, svc.db, "fj-1", "https://forge.example.com", "fj-pat")
	bindProjectForgejo(t, svc, project.ID, "fj-1")

	reader := stubForgejoPRReader(t, svc, "", "")
	reader.errs = map[int]error{5: fmt.Errorf("dial tcp 10.1.2.3:8443: connect refused")}

	_, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://forge.example.com/o/r/pulls/5",
	})
	appErr, ok := err.(*errs.AppError)
	if !ok || appErr.Code != errs.ErrForgeAPI.Code {
		t.Fatalf("expected %d for forgejo transport failure, got %v", errs.ErrForgeAPI.Code, err)
	}
	if !strings.Contains(appErr.Message, "连接或响应异常") {
		t.Fatalf("expected generic connectivity message, got %q", appErr.Message)
	}
	for _, leak := range []string{"10.1.2.3", "8443", "dial tcp"} {
		if strings.Contains(appErr.Message, leak) {
			t.Fatalf("message leaks transport details %q: %q", leak, appErr.Message)
		}
	}
	if count := countPullRequestRows(t, svc, issue.ID); count != 0 {
		t.Fatalf("expected no link rows, got %d", count)
	}
}

// sync 失败域同样脱敏：状态码错误只带状态码与提示，不带适配器错误原文
// （内网 API 地址/响应体）。
func TestIssueServiceSyncPullRequests_ForgejoFailureSanitized(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)
	createForgejoIntegrationRow(t, svc.db, "fj-1", "https://forge.example.com", "fj-pat")
	bindProjectForgejo(t, svc, project.ID, "fj-1")

	reader := stubForgejoPRReader(t, svc, "", "")
	reader.prs = forgePRSet(testForgePullRequest("o/r", 5, "open", false, nil))
	if _, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://forge.example.com/o/r/pulls/5",
	}); err != nil {
		t.Fatalf("attach forgejo pr: %v", err)
	}

	reader.errs = map[int]error{5: &forge.StatusError{
		StatusCode: http.StatusForbidden, Method: http.MethodGet,
		URL:  "https://internal.forge.local:8443/api/v1/repos/o/r/pulls/5",
		Body: `{"message":"forbidden internals"}`,
	}}
	result, err := svc.issueService.SyncIssuePullRequests(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(result.Failures) != 1 {
		t.Fatalf("expected one failure, got %+v", result.Failures)
	}
	msg := result.Failures[0].Error
	for _, leak := range []string{"internal.forge.local", "forbidden internals", "8443"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("failure leaks internals %q: %q", leak, msg)
		}
	}
	if !strings.Contains(msg, "状态码 403") || !strings.Contains(msg, "o/r#5") {
		t.Fatalf("expected failure with status code and row locator, got %q", msg)
	}
}

// 空 host 键（URL 主机归一不出来）不点查集成：脏行 host_key 就是空串，空键
// 不参与匹配语义。
func TestIssueServiceForgejoIntegrationIDForHost_EmptyKeyRejected(t *testing.T) {
	svc := setupTestServices(t)
	_, err := svc.issueService.forgejoIntegrationIDForProject(&model.Project{}, "")
	appErr, ok := err.(*errs.AppError)
	if !ok || appErr.Code != errs.ErrForgeIntegrationNotConfigured.Code {
		t.Fatalf("expected %d for empty host key, got %v", errs.ErrForgeIntegrationNotConfigured.Code, err)
	}
}

// 状态映射与 GitHub 同口径：closed 且 merged_at 非空才标 merged，
// closed 但缺 merged_at（平台异常快照）按 closed 落，不给 OpenAPI 承诺外的
// 「merged 且 merged_at 为空」组合。
func TestPullRequestStateFromForge(t *testing.T) {
	mergedAt := time.Now().UTC()
	cases := []struct {
		name string
		pr   forge.PullRequest
		want model.IssuePullRequestState
	}{
		{name: "open", pr: forge.PullRequest{State: "open"}, want: model.IssuePullRequestStateOpen},
		{name: "closed merged", pr: forge.PullRequest{State: "closed", Merged: true, MergedAt: &mergedAt}, want: model.IssuePullRequestStateMerged},
		{name: "closed 仅 merged 标记无 merged_at 仍为 merged", pr: forge.PullRequest{State: "closed", Merged: true}, want: model.IssuePullRequestStateMerged},
		{name: "closed 仅 merged_at 无标记仍为 merged", pr: forge.PullRequest{State: "closed", MergedAt: &mergedAt}, want: model.IssuePullRequestStateMerged},
		{name: "closed 未合并", pr: forge.PullRequest{State: "closed", Merged: false, ClosedAt: &mergedAt}, want: model.IssuePullRequestStateClosed},
	}
	for _, tc := range cases {
		if got := pullRequestStateFromForge(&tc.pr); got != tc.want {
			t.Fatalf("%s: state = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Upsert 冲突路径的快照保护：空快照的调用方（如未来的同步投影）不能把
// manual 行的 forge_integration_id 抹掉——否则 sync 永久报缺快照。非空新值
// （重复 attach 换绑）仍正常覆盖。
func TestIssuePullRequestRepoUpsert_EmptySnapshotPreserved(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)
	createForgejoIntegrationRow(t, svc.db, "fj-1", "https://forge.example.com", "fj-pat")
	bindProjectForgejo(t, svc, project.ID, "fj-1")

	reader := stubForgejoPRReader(t, svc, "", "")
	reader.prs = forgePRSet(testForgePullRequest("o/r", 5, "open", false, nil))
	if _, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://forge.example.com/o/r/pulls/5",
	}); err != nil {
		t.Fatalf("attach forgejo pr: %v", err)
	}

	// 空快照撞唯一键：写谓词拦下（既有行绑定活集成且与空新值不同主机不可
	// 判定——空新值不同主机不成立，保守拒绝），快照必须保留原值。
	now := time.Now().UTC()
	blank := &model.IssuePullRequest{
		ID: uuid.NewString(), IssueID: issue.ID, ProjectID: project.ID,
		Provider: model.IssuePullRequestProviderForgejo, RepoFullName: "o/r", Number: 5,
		State: model.IssuePullRequestStateOpen, LinkOrigin: model.IssuePullRequestLinkOriginSynced,
		ForgeIntegrationID: "", SyncedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	applied, err := svc.issueService.pullRequestRepo.Upsert(blank)
	if err != nil {
		t.Fatalf("upsert blank snapshot: %v", err)
	}
	if applied {
		t.Fatalf("blank snapshot upsert must be blocked by host predicate")
	}
	var row model.IssuePullRequest
	if err := svc.db.Where("issue_id = ?", issue.ID).First(&row).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.ForgeIntegrationID != "fj-1" {
		t.Fatalf("empty snapshot must not wipe existing snapshot, got %q", row.ForgeIntegrationID)
	}
	if row.LinkOrigin != model.IssuePullRequestLinkOriginManual {
		t.Fatalf("link_origin must stay manual, got %q", row.LinkOrigin)
	}
}

// 主机冲突守卫：同名 PR 已关联在另一台 Forgejo 实例上时，attach 返回 40917
// 要求先解除原关联，不静默覆盖既有行；同主机重复 attach 不受影响。
func TestIssueServiceAttachPullRequest_ForgejoHostConflict(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)
	createForgejoIntegrationRow(t, svc.db, "fj-a", "https://forge-a.example.com", "pat-a")
	createForgejoIntegrationRow(t, svc.db, "fj-b", "https://forge-b.example.com", "pat-b")
	bindProjectForgejo(t, svc, project.ID, "fj-a")

	reader := stubForgejoPRReader(t, svc, "", "")
	reader.prs = forgePRSet(testForgePullRequest("o/r", 5, "open", false, nil))
	if _, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://forge-a.example.com/o/r/pulls/5",
	}); err != nil {
		t.Fatalf("attach on instance A: %v", err)
	}

	// 项目改绑到实例 B，再 attach 同名 PR：40917，A 的关联行原样保留。
	// URL 大小写/别名写法不同也必须命中——既有行落库的是平台 canonical 名。
	bindProjectForgejo(t, svc, project.ID, "fj-b")
	for _, u := range []string{
		"https://forge-b.example.com/o/r/pulls/5",
		"https://forge-b.example.com/O/R/pulls/5",
	} {
		_, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{Url: u})
		appErr, ok := err.(*errs.AppError)
		if !ok || appErr.Code != errs.ErrPullRequestHostConflict.Code {
			t.Fatalf("expected %d for cross-host same-name PR %s, got %v", errs.ErrPullRequestHostConflict.Code, u, err)
		}
	}
	var row model.IssuePullRequest
	if err := svc.db.Where("issue_id = ?", issue.ID).First(&row).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.ForgeIntegrationID != "fj-a" || row.RepoFullName != "o/r" {
		t.Fatalf("instance A row must stay untouched, got %+v", row)
	}

	// 同主机重复 attach 仍幂等（不走冲突路径）。
	bindProjectForgejo(t, svc, project.ID, "fj-a")
	again, err := svc.issueService.AttachIssuePullRequest(issue.ID, user.ID, AttachIssuePullRequestRequest{
		Url: "https://forge-a.example.com/o/r/pulls/5",
	})
	if err != nil {
		t.Fatalf("idempotent attach after conflict: %v", err)
	}
	if count := countPullRequestRows(t, svc, issue.ID); count != 1 {
		t.Fatalf("expected single row, got %d", count)
	}
	_ = again
}

// Upsert 主机写谓词覆盖矩阵（TOCTOU 权威形态）：同快照、同主机、旧集成已删
// 允许覆盖；跨主机活链在写入时原子拦截，不依赖 service 层的先读后写检查。
func TestIssuePullRequestRepoUpsert_HostPredicate(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)
	createForgejoIntegrationRow(t, svc.db, "fj-a", "https://forge-a.example.com", "pat-a")
	createForgejoIntegrationRow(t, svc.db, "fj-b", "https://forge-b.example.com", "pat-b")
	createForgejoIntegrationRow(t, svc.db, "fj-a2", "https://forge-a.example.com", "pat-a2")

	newLink := func(integrationID string) *model.IssuePullRequest {
		now := time.Now().UTC()
		return &model.IssuePullRequest{
			ID: uuid.NewString(), IssueID: issue.ID, ProjectID: project.ID,
			Provider: model.IssuePullRequestProviderForgejo, RepoFullName: "o/r", Number: 5,
			State: model.IssuePullRequestStateOpen, LinkOrigin: model.IssuePullRequestLinkOriginManual,
			ForgeIntegrationID: integrationID, SyncedAt: now, CreatedAt: now, UpdatedAt: now,
		}
	}

	// 初次落行（A 实例）。
	applied, err := svc.issueService.pullRequestRepo.Upsert(newLink("fj-a"))
	if err != nil || !applied {
		t.Fatalf("seed upsert: applied=%v err=%v", applied, err)
	}

	// 跨主机活链（B）：写谓词原子拦截。
	applied, err = svc.issueService.pullRequestRepo.Upsert(newLink("fj-b"))
	if err != nil {
		t.Fatalf("cross-host upsert: %v", err)
	}
	if applied {
		t.Fatalf("cross-host alive overwrite must be blocked")
	}
	var row model.IssuePullRequest
	if err := svc.db.Where("issue_id = ?", issue.ID).First(&row).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.ForgeIntegrationID != "fj-a" {
		t.Fatalf("row must stay on fj-a, got %q", row.ForgeIntegrationID)
	}

	// 同主机换绑（A→A2）：允许，快照跟随。
	applied, err = svc.issueService.pullRequestRepo.Upsert(newLink("fj-a2"))
	if err != nil || !applied {
		t.Fatalf("same-host rebind: applied=%v err=%v", applied, err)
	}
	if err := svc.db.Where("issue_id = ?", issue.ID).First(&row).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.ForgeIntegrationID != "fj-a2" {
		t.Fatalf("row must follow same-host rebind, got %q", row.ForgeIntegrationID)
	}

	// 旧集成行已删（死链）：允许覆盖修复。
	if err := svc.db.Delete(&model.ForgeIntegration{}, "id = ?", "fj-a2").Error; err != nil {
		t.Fatalf("delete integration: %v", err)
	}
	applied, err = svc.issueService.pullRequestRepo.Upsert(newLink("fj-b"))
	if err != nil || !applied {
		t.Fatalf("dead-old repair: applied=%v err=%v", applied, err)
	}
	if err := svc.db.Where("issue_id = ?", issue.ID).First(&row).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.ForgeIntegrationID != "fj-b" {
		t.Fatalf("dead-old row must be repaired to fj-b, got %q", row.ForgeIntegrationID)
	}
}
