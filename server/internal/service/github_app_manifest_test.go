package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// GitHubAppManifestService（INT-129 B 轮）定向测试：manifest 组装、state 一次性
// /过期/换管理员、conversions 换凭证的成败映射、落库与紧接的检测。

func manifestTestService(t *testing.T) (*GitHubAppManifestService, *IntegrationService, *repository.DeploymentSettingsRepository, *config.Config, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.DeploymentSettings{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := &config.Config{
		Encryption: config.EncryptionConfig{Key: "12345678901234567890123456789012"},
		Server:     config.ServerConfig{PublicURL: "https://fs.example.com"},
	}
	repo := repository.NewDeploymentSettingsRepository(db)
	settings := NewDeploymentSettingsService(repo, cfg)
	integrationSvc := NewIntegrationService(repo, cfg, zap.NewNop())
	manifestSvc := NewGitHubAppManifestService(repo, settings, cfg, zap.NewNop())
	return manifestSvc, integrationSvc, repo, cfg, db
}

// stubManifestVerify 给 manifest 服务注入固定检测（记录收到的 app_id/私钥）。
func stubManifestVerify(svc *GitHubAppManifestService, detection githubapp.Detection) *[]string {
	appIDs := &[]string{}
	svc.SetVerify(func(ctx context.Context, appID string, keyPEM []byte, apiBaseURL string) githubapp.Detection {
		*appIDs = append(*appIDs, appID)
		return detection
	})
	return appIDs
}

// startConvertServer 模拟 GitHub 的 app-manifests conversions 端点。
func startConvertServer(t *testing.T, status int, body string, hit *[]string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hit != nil {
			*hit = append(*hit, r.URL.Path)
		}
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/conversions") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Errorf("missing api version header")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts
}

// jsq 把字符串编码成可嵌入 JSON 字面量的内容（PEM 的换行/引号需要转义）。
func jsq(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(b[1 : len(b)-1])
}

// TestManifest_Create 组装内容：name 带 host、hook/redirect/setup 指向
// public_url、权限集、个人与组织两种 action_url。
func TestManifest_Create(t *testing.T) {
	svc, _, _, _, _ := manifestTestService(t)
	ctx := context.Background()

	// 个人 App：action_url 官方创建页。
	out, err := svc.Create(ctx, "admin-1", &api.GitHubAppManifestRequest{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if out.ActionUrl != "https://github.com/settings/apps/new" {
		t.Fatalf("action_url = %s", out.ActionUrl)
	}
	if out.Manifest.Name != "Fast Ship (fs.example.com)" || out.Manifest.Url != "https://fs.example.com" {
		t.Fatalf("name/url = %q %q", out.Manifest.Name, out.Manifest.Url)
	}
	if out.Manifest.HookAttributes.Url != "https://fs.example.com/api/github/webhooks" || !out.Manifest.HookAttributes.Active {
		t.Fatalf("hook = %+v", out.Manifest.HookAttributes)
	}
	if out.Manifest.RedirectUrl != "https://fs.example.com/settings/integrations/github/callback" ||
		out.Manifest.SetupUrl != "https://fs.example.com/settings/integrations" {
		t.Fatalf("redirect/setup = %q %q", out.Manifest.RedirectUrl, out.Manifest.SetupUrl)
	}
	if out.Manifest.Public {
		t.Fatal("public must be false")
	}
	events := out.Manifest.DefaultEvents
	if len(events) != 2 || string(events[0]) != "pull_request" || string(events[1]) != "issue_comment" {
		t.Fatalf("events = %v", events)
	}
	perms := out.Manifest.DefaultPermissions
	if string(perms.PullRequests) != "write" || string(perms.Issues) != "write" || string(perms.Statuses) != "write" ||
		string(perms.Contents) != "read" || string(perms.Metadata) != "read" {
		t.Fatalf("permissions = %+v", perms)
	}
	if out.State == "" {
		t.Fatal("state must not be empty")
	}

	// 组织 App：action_url 指向组织的 Apps 设置。
	org := "my-org"
	out, err = svc.Create(ctx, "admin-1", &api.GitHubAppManifestRequest{Org: &org})
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	if out.ActionUrl != "https://github.com/organizations/my-org/settings/apps/new" {
		t.Fatalf("org action_url = %s", out.ActionUrl)
	}
}

// TestManifest_CreateRequiresPublicURL public_url 未配置 → 41202。
func TestManifest_CreateRequiresPublicURL(t *testing.T) {
	svc, _, _, cfg, _ := manifestTestService(t)
	cfg.Server.PublicURL = ""

	_, err := svc.Create(context.Background(), "admin-1", &api.GitHubAppManifestRequest{})
	if got := appErr(t, err); got.Code != errs.ErrPublicURLMissing.Code {
		t.Fatalf("err = [%d %s], want [%d]", got.Code, got.Message, errs.ErrPublicURLMissing.Code)
	}
}

// TestManifest_StateLifecycle state 一次性：重放、过期、换管理员统一 40009；
// 正常单次使用放行。
func TestManifest_StateLifecycle(t *testing.T) {
	svc, _, _, cfg, db := manifestTestService(t)
	ctx := context.Background()
	key := keyPEMString(t)
	cfg.GitHubApp.APIBaseURL = startConvertServer(t, http.StatusOK,
		`{"id":123,"slug":"fast-ship-bot","pem":"`+jsq(key)+`","webhook_secret":"sec"}`, nil).URL
	stubManifestVerify(svc, githubapp.Detection{Status: githubapp.DetectionOK, Detail: "ok", Slug: "fast-ship-bot"})

	// 正常使用：state 校验通过、凭证落库。
	out, err := svc.Create(ctx, "admin-1", &api.GitHubAppManifestRequest{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.Complete(ctx, "admin-1", &api.CompleteGitHubAppManifestRequest{Code: "code-1", State: out.State}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// 重放同一 state → 40009。
	err = svc.Complete(ctx, "admin-1", &api.CompleteGitHubAppManifestRequest{Code: "code-2", State: out.State})
	if got := appErr(t, err); got.Code != errs.ErrGitHubManifestStateInvalid.Code {
		t.Fatalf("replay = [%d %s], want [%d]", got.Code, got.Message, errs.ErrGitHubManifestStateInvalid.Code)
	}

	// 过期 → 40009。
	out, err = svc.Create(ctx, "admin-1", &api.GitHubAppManifestRequest{})
	if err != nil {
		t.Fatalf("create 2: %v", err)
	}
	svc.now = func() time.Time { return time.Now().Add(11 * time.Minute) }
	err = svc.Complete(ctx, "admin-1", &api.CompleteGitHubAppManifestRequest{Code: "code-3", State: out.State})
	if got := appErr(t, err); got.Code != errs.ErrGitHubManifestStateInvalid.Code {
		t.Fatalf("expired = [%d %s], want [%d]", got.Code, got.Message, errs.ErrGitHubManifestStateInvalid.Code)
	}
	svc.now = time.Now

	// 换管理员 → 40009。
	out, err = svc.Create(ctx, "admin-1", &api.GitHubAppManifestRequest{})
	if err != nil {
		t.Fatalf("create 3: %v", err)
	}
	err = svc.Complete(ctx, "admin-2", &api.CompleteGitHubAppManifestRequest{Code: "code-4", State: out.State})
	if got := appErr(t, err); got.Code != errs.ErrGitHubManifestStateInvalid.Code {
		t.Fatalf("other admin = [%d %s], want [%d]", got.Code, got.Message, errs.ErrGitHubManifestStateInvalid.Code)
	}

	// 首次 complete 的结果确实落库（后面的失败尝试没有污染它）。
	var settings model.DeploymentSettings
	if err := db.First(&settings, "id = ?", model.DeploymentSettingsID).Error; err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if settings.AppID != "123" || settings.AppSlug != "fast-ship-bot" {
		t.Fatalf("app columns = %s/%s", settings.AppID, settings.AppSlug)
	}
	plain, err := crypto.Decrypt(settings.ConfigEncrypted, []byte(cfg.Encryption.Key))
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	var payload githubAppSecretPayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.PrivateKeyPEM != key || payload.WebhookSecret != "sec" || payload.AppID != "123" {
		t.Fatalf("payload = pem(%v) secret(%q) id(%q)", payload.PrivateKeyPEM == key, payload.WebhookSecret, payload.AppID)
	}
	if settings.DetectionStatus != model.DeploymentDetectionOK || settings.DetectionDetail != "ok" {
		t.Fatalf("detection = %s %s", settings.DetectionStatus, settings.DetectionDetail)
	}
}

// TestManifest_CompleteConvertErrors conversions 404/422 → 40010；500 → 50205；
// 网络错误 → 50205。
func TestManifest_CompleteConvertErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"404", http.StatusNotFound, `{"message":"Not Found"}`, errs.ErrGitHubManifestCodeInvalid.Code},
		{"422", http.StatusUnprocessableEntity, `{"message":"Unprocessable"}`, errs.ErrGitHubManifestCodeInvalid.Code},
		{"500", http.StatusInternalServerError, `{}`, errs.ErrGitHubManifestFailed.Code},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _, cfg, _ := manifestTestService(t)
			ctx := context.Background()
			cfg.GitHubApp.APIBaseURL = startConvertServer(t, tc.status, tc.body, nil).URL

			out, err := svc.Create(ctx, "admin-1", &api.GitHubAppManifestRequest{})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			err = svc.Complete(ctx, "admin-1", &api.CompleteGitHubAppManifestRequest{Code: "code-x", State: out.State})
			if got := appErr(t, err); got.Code != tc.want {
				t.Fatalf("err = [%d %s], want [%d]", got.Code, got.Message, tc.want)
			}
		})
	}

	// 网络错误：服务已关闭。
	svc, _, _, cfg, _ := manifestTestService(t)
	ctx := context.Background()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	cfg.GitHubApp.APIBaseURL = dead.URL
	dead.Close()
	out, err := svc.Create(ctx, "admin-1", &api.GitHubAppManifestRequest{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	err = svc.Complete(ctx, "admin-1", &api.CompleteGitHubAppManifestRequest{Code: "c", State: out.State})
	if got := appErr(t, err); got.Code != errs.ErrGitHubManifestFailed.Code {
		t.Fatalf("network err = [%d %s], want [%d]", got.Code, got.Message, errs.ErrGitHubManifestFailed.Code)
	}
}

// TestManifest_CompleteWritesSlugAndDetection 成功路径：conversions 只取
// id/slug/pem/webhook_secret，检测紧接一次并把状态与 slug 写回。
func TestManifest_CompleteWritesSlugAndDetection(t *testing.T) {
	svc, integrationSvc, _, cfg, _ := manifestTestService(t)
	ctx := context.Background()

	var convertPaths []string
	convert := startConvertServer(t, http.StatusOK,
		`{"id":987,"slug":"bot-slug","pem":"`+jsq(keyPEMString(t))+`","webhook_secret":"wh-sec","client_id":"cid","client_secret":"csecret"}`,
		&convertPaths)
	cfg.GitHubApp.APIBaseURL = convert.URL
	appIDs := stubManifestVerify(svc, githubapp.Detection{Status: githubapp.DetectionOK, Detail: "已连接 GitHub App：bot-slug", Slug: "bot-slug"})

	out, err := svc.Create(ctx, "admin-1", &api.GitHubAppManifestRequest{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.Complete(ctx, "admin-1", &api.CompleteGitHubAppManifestRequest{Code: "the-code", State: out.State}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	// 打到的端点带 code（PathEscape 后原样）。
	if len(convertPaths) != 1 || !strings.HasSuffix(convertPaths[0], "/app-manifests/the-code/conversions") {
		t.Fatalf("convert paths = %v", convertPaths)
	}
	// 检测用刚换到的身份。
	if len(*appIDs) != 1 || (*appIDs)[0] != "987" {
		t.Fatalf("verify app ids = %v", *appIDs)
	}

	// 投影：slug、安装链接、检测状态、webhook 链接齐全。
	proj, err := integrationSvc.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if proj.Source != api.GitHubAppDeploymentSourceDatabase || proj.AppId != "987" || proj.AppSlug != "bot-slug" {
		t.Fatalf("projection = %s %s/%s", proj.Source, proj.AppId, proj.AppSlug)
	}
	if proj.InstallUrl != "https://github.com/apps/bot-slug/installations/new" {
		t.Fatalf("install_url = %s", proj.InstallUrl)
	}
	if proj.WebhookUrl != "https://fs.example.com/api/github/webhooks" {
		t.Fatalf("webhook_url = %s", proj.WebhookUrl)
	}
	if proj.DetectionStatus != api.GitHubAppDeploymentDetectionStatusOk || !proj.PrivateKeySet || !proj.WebhookSecretSet {
		t.Fatalf("detection/sets = %s %v %v", proj.DetectionStatus, proj.PrivateKeySet, proj.WebhookSecretSet)
	}
}

// TestManifest_CompleteRejectsIncompleteConversion conversions 响应缺 id/slug/
// webhook_secret、或私钥 PEM 无法解析 → 50205，且既有部署身份不被覆盖（异常
// 响应不得改写库里的 App 身份——密钥为空会让此后所有 webhook 503）。
func TestManifest_CompleteRejectsIncompleteConversion(t *testing.T) {
	key := keyPEMString(t)
	cases := []struct {
		name string
		body string
	}{
		{"id 为 0", `{"id":0,"slug":"bot","pem":"` + jsq(key) + `","webhook_secret":"sec"}`},
		{"slug 为空", `{"id":123,"slug":"  ","pem":"` + jsq(key) + `","webhook_secret":"sec"}`},
		{"缺 webhook_secret", `{"id":123,"slug":"bot","pem":"` + jsq(key) + `","webhook_secret":"  "}`},
		{"PEM 非法", `{"id":123,"slug":"bot","pem":"not-a-pem","webhook_secret":"sec"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, repo, cfg, _ := manifestTestService(t)
			ctx := context.Background()
			stubManifestVerify(svc, githubapp.Detection{Status: githubapp.DetectionOK, Detail: "ok", Slug: "old-bot"})

			// 先完整走一次成功 complete，落一份既有身份。
			good := startConvertServer(t, http.StatusOK,
				`{"id":111,"slug":"old-bot","pem":"`+jsq(key)+`","webhook_secret":"old-sec"}`, nil)
			cfg.GitHubApp.APIBaseURL = good.URL
			out, err := svc.Create(ctx, "admin-1", &api.GitHubAppManifestRequest{})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if err := svc.Complete(ctx, "admin-1", &api.CompleteGitHubAppManifestRequest{Code: "c1", State: out.State}); err != nil {
				t.Fatalf("seed complete: %v", err)
			}
			before, err := repo.Get()
			if err != nil {
				t.Fatalf("load before: %v", err)
			}

			// 换成坏响应的 conversions 服务再 complete：50205，行保持原样。
			cfg.GitHubApp.APIBaseURL = startConvertServer(t, http.StatusOK, tc.body, nil).URL
			out, err = svc.Create(ctx, "admin-1", &api.GitHubAppManifestRequest{})
			if err != nil {
				t.Fatalf("create 2: %v", err)
			}
			err = svc.Complete(ctx, "admin-1", &api.CompleteGitHubAppManifestRequest{Code: "c2", State: out.State})
			if got := appErr(t, err); got.Code != errs.ErrGitHubManifestFailed.Code {
				t.Fatalf("err = [%d %s], want [%d]", got.Code, got.Message, errs.ErrGitHubManifestFailed.Code)
			}

			after, err := repo.Get()
			if err != nil {
				t.Fatalf("load after: %v", err)
			}
			if after.AppID != before.AppID || after.AppSlug != before.AppSlug ||
				!bytes.Equal(after.ConfigEncrypted, before.ConfigEncrypted) {
				t.Fatalf("identity overwritten: %s/%s -> %s/%s",
					before.AppID, before.AppSlug, after.AppID, after.AppSlug)
			}
			payload, err := decryptAppPayload(after.ConfigEncrypted, []byte(cfg.Encryption.Key))
			if err != nil {
				t.Fatalf("decrypt: %v", err)
			}
			if payload.AppID != "111" || payload.WebhookSecret != "old-sec" {
				t.Fatalf("payload changed: id(%q) secret(%q)", payload.AppID, payload.WebhookSecret)
			}
		})
	}
}

// GitHub 对 App 名称有 34 字符上限：超长会让预填表单不合规，一键创建变成手动改名。
func TestManifestAppNameFitsGitHubLimit(t *testing.T) {
	cases := map[string]string{
		"https://fs.example.com":                                         "Fast Ship (fs.example.com)",
		"http://127.0.0.1:5088":                                          "Fast Ship (127.0.0.1:5088)",
		"https://fast-ship.example.company.com":                          "Fast Ship (fast-ship.example.com~)",
		"https://a-very-long-subdomain-name.example-corp.internal.tools": "Fast Ship (a-very-long-subdomain~)",
		"": "Fast Ship",
	}
	for in, want := range cases {
		got := manifestAppName(in)
		if n := utf8.RuneCountInString(got); n > githubAppNameMaxRunes {
			t.Fatalf("%q -> %q is %d runes, want <= %d", in, got, n, githubAppNameMaxRunes)
		}
		if got != want {
			t.Fatalf("manifestAppName(%q) = %q, want %q", in, got, want)
		}
	}
}

// 非 ASCII host（url.Parse 不做 punycode）也按字符数截断，不切坏多字节字符。
func TestManifestAppNameTruncatesByRune(t *testing.T) {
	got := manifestAppName("https://例子.测试.一个很长的域名.工具.本地")
	if n := utf8.RuneCountInString(got); n > githubAppNameMaxRunes {
		t.Fatalf("%q is %d runes, want <= %d", got, n, githubAppNameMaxRunes)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncated name is not valid UTF-8: %q", got)
	}
}
