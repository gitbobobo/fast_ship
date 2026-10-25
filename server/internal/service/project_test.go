package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/forges"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func TestParseRepositoryURL(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantHost    string
		wantOwner   string
		wantRepo    string
		wantErr     bool
		errContains string
	}{
		{
			name:      "owner/repo format",
			input:     "godbobo/fast_ship",
			wantHost:  "github.com",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			name:      "https full url",
			input:     "https://github.com/godbobo/fast_ship",
			wantHost:  "github.com",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			name:      "https with .git suffix",
			input:     "https://github.com/godbobo/fast_ship.git",
			wantHost:  "github.com",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			name:      "http url",
			input:     "http://github.com/godbobo/fast_ship",
			wantHost:  "github.com",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			name:      "github.com prefix without protocol",
			input:     "github.com/godbobo/fast_ship",
			wantHost:  "github.com",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			name:      "github.com host upper case",
			input:     "GitHub.COM/godbobo/fast_ship",
			wantHost:  "github.com",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			name:      "https github host upper case",
			input:     "https://GitHub.com/godbobo/fast_ship",
			wantHost:  "github.com",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			name:      "with trailing slash",
			input:     "https://github.com/godbobo/fast_ship/",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantHost:  "github.com",
			wantErr:   false,
		},
		{
			name:      "with leading/trailing spaces",
			input:     "  godbobo/fast_ship  ",
			wantHost:  "github.com",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			// 页面路径不再静默丢弃第三段（审查修复：丢掉后拼出的 owner/repo
			// 不是用户指的仓库），直接拒绝。
			name:        "extra path segment rejected",
			input:       "https://github.com/godbobo/fast_ship/issues",
			wantErr:     true,
			errContains: "多余路径段",
		},
		// 裸 host/o/r：自建 Forgejo 等非 github 主机（INT-166）。无显式端口
		// 时按 scheme 补默认端口（无 scheme / https → 443，http → 80）。
		{
			name:      "bare non-github host",
			input:     "git.example.com/godbobo/fast_ship",
			wantHost:  "git.example.com:443",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			name:      "https non-github host",
			input:     "https://git.example.com/godbobo/fast_ship",
			wantHost:  "git.example.com:443",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			name:      "http non-github host",
			input:     "http://git.example.com/godbobo/fast_ship",
			wantHost:  "git.example.com:80",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			// 「.git + 尾斜杠」组合剥得干净（审查修复：先剥尾斜杠再剥 .git）。
			name:      "https non-github host with port",
			input:     "https://git.example.com:8443/godbobo/fast_ship.git/",
			wantHost:  "git.example.com:8443",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			// 正常 .git 后缀不受影响。
			name:      "bare owner/repo with .git suffix",
			input:     "godbobo/fast_ship.git",
			wantHost:  "github.com",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			// 裸形态三段拒绝（同 URL 形态口径，第三段不再静默丢弃）；
			// 消息同时点出「首段可能是内网主机名」的另一种修法。
			name:        "bare three segments rejected",
			input:       "a/b/c",
			wantErr:     true,
			errContains: "多余路径段",
		},
		{
			// 单标签内网主机裸写 myhost/o/r 落入同一拒绝，提示写全 https。
			name:        "bare single-label host three segments",
			input:       "myhost/o/r",
			wantErr:     true,
			errContains: "内网主机名",
		},
		{
			// SCP 风格 git@host:o/r：首段含 @ 单独识别，SSH 本期不支持——
			// 不再被首段含 : 判成主机后报「主机未匹配」。
			name:        "scp-style ssh rejected",
			input:       "git@git.example.com:godbobo/fast_ship",
			wantErr:     true,
			errContains: "SSH 地址",
		},
		{
			// https URL 带 userinfo 同拒：凭证不该拼进仓库链接落库回显。
			name:        "https url with userinfo rejected",
			input:       "https://u:p@git.example.com/godbobo/fast_ship",
			wantErr:     true,
			errContains: "SSH 地址",
		},
		{
			// PR 页面链接拒绝：它不是仓库根。
			name:        "pr page url rejected",
			input:       "https://forge.example.com/acme/app/pulls/1",
			wantErr:     true,
			errContains: "多余路径段",
		},
		{
			// 非数字端口按输入错误直接报，不再落到集成匹配报「主机未匹配」
			// 误导管理员去配一条不可能的集成。
			name:        "non-numeric port rejected",
			input:       "https://git.example.com:abc/godbobo/fast_ship",
			wantErr:     true,
			errContains: "端口无效",
		},
		{
			// 越界端口同口径拒收（与 base_url 保存侧 parseBaseURL 一致）。
			name:        "out-of-range port rejected",
			input:       "https://git.example.com:99999/godbobo/fast_ship",
			wantErr:     true,
			errContains: "端口无效",
		},
		{
			// 空端口走既有的端口无效分支（SplitHostPort 解析出空 port）。
			name:        "empty port rejected",
			input:       "forge:/o/r",
			wantErr:     true,
			errContains: "端口无效",
		},
		{
			// 无 scheme 且首段含冒号的畸形主机按格式错误直接报：补默认
			// 端口只会归一成永不命中的键，误报成「主机未匹配」。
			name:        "unbracketed ipv6 rejected",
			input:       "::1/o/r",
			wantErr:     true,
			errContains: "主机格式无效",
		},
		{
			name:        "unbracketed ipv6 with scheme rejected",
			input:       "https://::1/o/r",
			wantErr:     true,
			errContains: "主机格式无效",
		},
		{
			name:        "broken ipv6 bracket rejected",
			input:       "https://[::1/o/r",
			wantErr:     true,
			errContains: "主机格式无效",
		},
		{
			// 完整括号的 IPv6 无端口仍是合法形态，按缺端口补默认。
			name:      "bracketed ipv6 default port",
			input:     "https://[::1]/o/r",
			wantHost:  "[::1]:443",
			wantOwner: "o",
			wantRepo:  "r",
			wantErr:   false,
		},
		{
			name:      "non-github host upper case",
			input:     "https://Git.Example.com/godbobo/fast_ship",
			wantHost:  "git.example.com:443",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			// github.com 带 DNS 根点尾缀 + 非默认口径：与 github.com 同主机，
			// 归内置 github（与前端归一、repoHostKey 同口径）。
			name:      "github.com trailing dot http",
			input:     "http://github.com./godbobo/fast_ship",
			wantHost:  "github.com",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			name:      "github.com trailing dot https",
			input:     "https://github.com./godbobo/fast_ship",
			wantHost:  "github.com",
			wantOwner: "godbobo",
			wantRepo:  "fast_ship",
			wantErr:   false,
		},
		{
			// 非 HTTP(S) scheme 拒绝（审查修复）：ssh:// 若静默当 https 归一，
			// 会命中 https 集成的 host_key 把仓库绑到协议不符的集成。
			name:        "ssh scheme rejected",
			input:       "ssh://git.example.com/godbobo/fast_ship",
			wantErr:     true,
			errContains: "协议无效",
		},
		{
			name:        "ftp scheme rejected",
			input:       "ftp://github.com/godbobo/fast_ship",
			wantErr:     true,
			errContains: "协议无效",
		},
		{
			name:        "empty string",
			input:       "",
			wantErr:     true,
			errContains: "不能为空",
		},
		{
			name:        "only owner",
			input:       "godbobo",
			wantErr:     true,
			errContains: "格式无效",
		},
		{
			name:        "missing owner",
			input:       "/fast_ship",
			wantErr:     true,
			errContains: "不能为空",
		},
		{
			name:        "missing repo",
			input:       "godbobo/",
			wantErr:     true,
			errContains: "格式无效",
		},
		{
			name:        "empty repo segment",
			input:       "godbobo//",
			wantErr:     true,
			errContains: "不能为空",
		},
		{
			name:        "invalid characters in owner",
			input:       "god bobo/fast_ship",
			wantErr:     true,
			errContains: "非法字符",
		},
		{
			name:        "invalid characters in repo",
			input:       "godbobo/fast ship",
			wantErr:     true,
			errContains: "非法字符",
		},
		{
			name:        "dot-segment owner rejected",
			input:       "https://git.example.com/../app",
			wantErr:     true,
			errContains: "不能是 . 或 ..",
		},
		{
			name:        "dot-segment repo rejected",
			input:       "godbobo/..",
			wantErr:     true,
			errContains: "不能是 . 或 ..",
		},
		{
			name:        "single-dot owner rejected",
			input:       "https://github.com/./app",
			wantErr:     true,
			errContains: "不能是 . 或 ..",
		},
		{
			name:      "repo with dot and hyphen",
			input:     "godbobo/fast-ship.v2",
			wantHost:  "github.com",
			wantOwner: "godbobo",
			wantRepo:  "fast-ship.v2",
			wantErr:   false,
		},
		{
			name:      "owner with underscore",
			input:     "god_bobo/fast-ship",
			wantHost:  "github.com",
			wantOwner: "god_bobo",
			wantRepo:  "fast-ship",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotHost, gotOwner, gotRepo, _, err := parseRepositoryURL(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errContains)
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("expected error containing %q, got %q", tt.errContains, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotHost != tt.wantHost {
				t.Errorf("host = %q, want %q", gotHost, tt.wantHost)
			}
			if gotOwner != tt.wantOwner {
				t.Errorf("owner = %q, want %q", gotOwner, tt.wantOwner)
			}
			if gotRepo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", gotRepo, tt.wantRepo)
			}
		})
	}
}

// 主机归一键：仓库 URL 与集成 base_url 双方都归一成「小写 host + 端口
// （空端口按 scheme 缺省 443/80）」后比较（INT-166）。
func TestRepoHostKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{"bare host assumes https default", repoHostKey("git.example.com"), "git.example.com:443"},
		{"explicit port kept", repoHostKey("git.example.com:8443"), "git.example.com:8443"},
		{"numeric port spelling normalized", repoHostKey("git.example.com:0443"), "git.example.com:443"},
		{"host lower cased", repoHostKey("Git.Example.com"), "git.example.com:443"},
		{"ipv6 brackets stripped", repoHostKey("[::1]:3000"), "::1:3000"},
		{"dns root dot stripped", repoHostKey("git.example.com."), "git.example.com:443"},
		{"dns root dot with port", repoHostKey("git.example.com.:8443"), "git.example.com:8443"},
		{"base root dot", forgeBaseHostKey("https://git.example.com."), "git.example.com:443"},
		{"https base default port", forgeBaseHostKey("https://git.example.com"), "git.example.com:443"},
		{"http base default port", forgeBaseHostKey("http://git.lan"), "git.lan:80"},
		{"base explicit port", forgeBaseHostKey("https://git.example.com:8443"), "git.example.com:8443"},
		{"base trailing slash", forgeBaseHostKey("https://git.example.com/"), "git.example.com:443"},
		{"base upper host", forgeBaseHostKey("HTTPS://GIT.EXAMPLE.COM"), "git.example.com:443"},
		{"base invalid", forgeBaseHostKey("not a url"), ""},
		{"scheme default differs", forgeBaseHostKey("http://git.lan:80") + "|" + repoHostKey("git.lan"), "git.lan:80|git.lan:443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
}

// GHE 部署下显式 github.com 地址指向本部署 App 够不到的公共实例：拒收并
// 点明实例主机；裸 owner/repo 与实例主机地址才归内置（INT-166 R9）。
func TestParseRepoBindingGHERejectsPublicGitHubURL(t *testing.T) {
	svc := setupTestServices(t)
	svc.cfg.GitHubApp.APIBaseURL = "https://ghe.corp.example/api/v3"
	ps := newTestProjectService(svc)

	_, _, _, _, _, appErr := ps.parseRepoBinding("https://github.com/o/r", false)
	if appErr == nil || !strings.Contains(appErr.Error(), "ghe.corp.example") {
		t.Fatalf("explicit github.com under GHE: err = %v", appErr)
	}
	// 公开仓是存量数据字段：显式 github.com 在 GHE 下放行（基线语义，
	// 前端回填恒为 github.com 形态，拒掉会把普通编辑挡住）；publicHost
	// 记 github.com 供 code_repo_url 回落渲染选主机。
	owner, repo, id, _, publicHost, appErr := ps.parseRepoBinding("https://github.com/o/r", true)
	if appErr != nil || id != "" || owner != "o" || repo != "r" || publicHost != "github.com" {
		t.Fatalf("public github.com under GHE → owner=%q repo=%q id=%q host=%q err=%v", owner, repo, id, publicHost, appErr)
	}
	for _, in := range []string{"o/r", "ghe.corp.example/o/r", "https://ghe.corp.example/o/r"} {
		owner, repo, id, _, publicHost, appErr := ps.parseRepoBinding(in, false)
		if appErr != nil || id != "" || owner != "o" || repo != "r" || publicHost != "" {
			t.Fatalf("%q → owner=%q repo=%q id=%q host=%q err=%v", in, owner, repo, id, publicHost, appErr)
		}
	}
}

// http 部署（本地 GHE / 调试）：githubWebBase 恒拼 https 会让 http 仓库
// URL 的归一键（host:80）与部署键（host:443）错配成「主机未匹配」（R42
// Note）——保留 http scheme 才归内置 github-default。
func TestParseRepoBindingHTTPDeployment(t *testing.T) {
	svc := setupTestServices(t)
	svc.cfg.GitHubApp.APIBaseURL = "http://ghe.lan:8080/api/v3"
	ps := newTestProjectService(svc)

	for _, in := range []string{"o/r", "http://ghe.lan:8080/o/r", "ghe.lan:8080/o/r"} {
		owner, repo, id, _, publicHost, appErr := ps.parseRepoBinding(in, false)
		if appErr != nil || id != "" || owner != "o" || repo != "r" || publicHost != "" {
			t.Fatalf("%q → owner=%q repo=%q id=%q host=%q err=%v", in, owner, repo, id, publicHost, appErr)
		}
	}
	// 仓库链接渲染同样按 http 基址。
	p := &model.Project{GithubOwner: "o", GithubRepo: "r"}
	got, err := ps.codeRepoUrl(p, nil)
	if err != nil || got == nil || *got != "http://ghe.lan:8080/o/r" {
		t.Fatalf("http deploy code_repo_url = %v, %v", got, err)
	}
}

// GHE 部署下公开仓显式 github.com：未单独配代码仓时 code_repo_url 必须
// 渲染公共实例主机，而不是错拼到本部署 GHE（R31 P2）。
func TestCodeRepoUrlGHEExplicitPublicGitHub(t *testing.T) {
	svc := setupTestServices(t)
	svc.cfg.GitHubApp.APIBaseURL = "https://ghe.corp.example/api/v3"
	ps := newTestProjectService(svc)

	p := &model.Project{
		GithubOwner: "o", GithubRepo: "r",
		GithubHost: "github.com",
	}
	got, err := ps.codeRepoUrl(p, nil)
	if err != nil {
		t.Fatalf("codeRepoUrl: %v", err)
	}
	if got == nil || *got != "https://github.com/o/r" {
		t.Fatalf("code_repo_url = %v, want https://github.com/o/r", got)
	}

	// 同部署下存量行（GithubHost 空）与显式 code 字段仍按本部署 GHE 拼。
	p.GithubHost = ""
	got, _ = ps.codeRepoUrl(p, nil)
	if got == nil || *got != "https://ghe.corp.example/o/r" {
		t.Fatalf("legacy row code_repo_url = %v, want ghe host", got)
	}
	p.CodeGithubOwner, p.CodeGithubRepo = "co", "cr"
	got, _ = ps.codeRepoUrl(p, nil)
	if got == nil || *got != "https://ghe.corp.example/co/cr" {
		t.Fatalf("code-field code_repo_url = %v, want ghe host", got)
	}

	// 写路径端到端：显式 github.com 公开仓落库 github_host（R32 P1 回归——
	// 仓储层 INSERT/Updates 显式列清单曾把该列静默丢掉）。
	user := createTestUser(t, svc.db, "ghe-user")
	proj, err := ps.Create(user.ID, &CreateProjectRequest{
		Name:          "ghe-public-repo",
		RepositoryUrl: api.Ptr("https://github.com/o/r"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var stored model.Project
	if err := svc.db.First(&stored, "id = ?", proj.Id).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.GithubHost != "github.com" {
		t.Fatalf("persisted github_host = %q, want github.com", stored.GithubHost)
	}
	got, _ = ps.codeRepoUrl(&stored, nil)
	if got == nil || *got != "https://github.com/o/r" {
		t.Fatalf("persisted code_repo_url = %v, want https://github.com/o/r", got)
	}
}

// repository_url 投影透出公开仓主机（R41 P2）：前端不再自拼 github.com——
// GHE 部署下 github_host 空的行要回本部署地址，显式 github.com 回公共实例。
func TestRepositoryUrlProjection(t *testing.T) {
	svc := setupTestServices(t)
	svc.cfg.GitHubApp.APIBaseURL = "https://ghe.corp.example/api/v3"
	ps := newTestProjectService(svc)

	for _, tc := range []struct {
		name, host, want string
	}{
		{"显式公共 github.com", "github.com", "https://github.com/o/r"},
		{"本部署 GHE", "", "https://ghe.corp.example/o/r"},
	} {
		p := &model.Project{
			ID: uuid.NewString(), Name: tc.name,
			GithubOwner: "o", GithubRepo: "r", GithubHost: tc.host,
		}
		resp, err := ps.toResponse(p, nil)
		if err != nil {
			t.Fatalf("%s toResponse: %v", tc.name, err)
		}
		if resp.RepositoryUrl == nil || *resp.RepositoryUrl != tc.want {
			t.Fatalf("%s repository_url = %v, want %s", tc.name, resp.RepositoryUrl, tc.want)
		}
	}
	// 无公开仓：null。
	resp, err := ps.toResponse(&model.Project{ID: uuid.NewString(), Name: "empty"}, nil)
	if err != nil || resp.RepositoryUrl != nil {
		t.Fatalf("empty repo repository_url = %v, want nil", resp.RepositoryUrl)
	}
}

// GHE 部署下公开仓显式 github.com、代码仓写本部署主机（裸写或 GHE 地址）：
// 两边同 owner/repo 也是不同实例，normalizeCodeRepo 不得吞并 code 字段——
// 吞并会让 code_repo_url 回落渲染到公共 github.com 同名仓（R38 P1 回归）。
func TestNormalizeCodeRepoGHECrossHost(t *testing.T) {
	svc := setupTestServices(t)
	svc.cfg.GitHubApp.APIBaseURL = "https://ghe.corp.example/api/v3"
	ps := newTestProjectService(svc)
	user := createTestUser(t, svc.db, "ghe-cross")

	loadCode := func(id string) model.Project {
		var stored model.Project
		if err := svc.db.First(&stored, "id = ?", id).Error; err != nil {
			t.Fatalf("reload %s: %v", id, err)
		}
		return stored
	}

	// Create：公开仓显式 github.com + 代码仓裸写同字面（=本部署 GHE）。
	proj, err := ps.Create(user.ID, &CreateProjectRequest{
		Name:              "cross-host",
		RepositoryUrl:     api.Ptr("https://github.com/o/r"),
		CodeRepositoryUrl: api.Ptr("o/r"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	stored := loadCode(proj.Id)
	if stored.CodeGithubOwner != "o" || stored.CodeGithubRepo != "r" {
		t.Fatalf("code fields collapsed: %q/%q", stored.CodeGithubOwner, stored.CodeGithubRepo)
	}
	got, _ := ps.codeRepoUrl(&stored, nil)
	if got == nil || *got != "https://ghe.corp.example/o/r" {
		t.Fatalf("code_repo_url = %v, want ghe host", got)
	}

	// Update：只把公开仓改成显式 github.com/o/r，存量 GHE code o/r 不吞并。
	proj2, err := ps.Create(user.ID, &CreateProjectRequest{
		Name:              "cross-host-2",
		RepositoryUrl:     api.Ptr("https://ghe.corp.example/a/b"),
		CodeRepositoryUrl: api.Ptr("o/r"),
	})
	if err != nil {
		t.Fatalf("create2: %v", err)
	}
	if _, err := ps.Update(proj2.Id, user.ID, &UpdateProjectRequest{
		RepositoryUrl: api.Ptr("https://github.com/o/r"),
	}); err != nil {
		t.Fatalf("update public only: %v", err)
	}
	stored = loadCode(proj2.Id)
	if stored.CodeGithubOwner != "o" || stored.CodeGithubRepo != "r" {
		t.Fatalf("update collapsed code fields: %q/%q", stored.CodeGithubOwner, stored.CodeGithubRepo)
	}

	// 同主机仍吞并：公开仓裸写（=本部署）+ 代码仓同字面 → code 字段清空。
	proj3, err := ps.Create(user.ID, &CreateProjectRequest{
		Name:              "same-host",
		RepositoryUrl:     api.Ptr("o/r"),
		CodeRepositoryUrl: api.Ptr("https://ghe.corp.example/o/r"),
	})
	if err != nil {
		t.Fatalf("create3: %v", err)
	}
	stored = loadCode(proj3.Id)
	if stored.CodeGithubOwner != "" || stored.CodeGithubRepo != "" {
		t.Fatalf("same-host code fields = %q/%q, want normalized empty", stored.CodeGithubOwner, stored.CodeGithubRepo)
	}
}

// 非 GHE 部署（实例即 github.com）：公开仓显式 github.com + 代码仓裸写同
// 字面，两边都指公共实例，吞并仍成立。
func TestNormalizeCodeRepoPublicGitHubCollapse(t *testing.T) {
	svc := setupTestServices(t)
	ps := newTestProjectService(svc)
	user := createTestUser(t, svc.db, "gh-collapse")

	proj, err := ps.Create(user.ID, &CreateProjectRequest{
		Name:              "public-collapse",
		RepositoryUrl:     api.Ptr("https://github.com/o/r"),
		CodeRepositoryUrl: api.Ptr("o/r"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var stored model.Project
	if err := svc.db.First(&stored, "id = ?", proj.Id).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.CodeGithubOwner != "" || stored.CodeGithubRepo != "" {
		t.Fatalf("code fields = %q/%q, want normalized empty", stored.CodeGithubOwner, stored.CodeGithubRepo)
	}
}

func newTestProjectService(svc *testServices) *ProjectService {
	return NewProjectService(svc.projectRepo, svc.versionRepo, svc.syncStateRepo, svc.tokenRepo, svc.storage, svc.cfg, zap.NewNop(),
		repository.NewForgeIntegrationRepository(svc.db))
}

func TestProjectServiceCreate_WithoutGitHub(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-no-github")
	projectSvc := newTestProjectService(svc)

	// 创建不带 GitHub 仓库的项目应该成功
	project, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:        "no-github-project",
		Description: api.Ptr("A project without GitHub"),
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if project.GithubOwner != "" || project.GithubRepo != "" {
		t.Fatalf("expected empty github owner/repo, got %q/%q", project.GithubOwner, project.GithubRepo)
	}
	if project.PublicGithubTokenId != "" || project.CodeGithubTokenId != "" {
		t.Fatalf("expected empty token references, got %q/%q", project.PublicGithubTokenId, project.CodeGithubTokenId)
	}
}

// 创建时可以只填仓库不选 Token：仓库与 Token 相互独立，公开仓库 Token
// 之后在设置页或项目表单里补配。
func TestProjectServiceCreate_WithRepoURLButNoToken(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-no-token")
	projectSvc := newTestProjectService(svc)

	project, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:          "no-token-project",
		RepositoryUrl: api.Ptr("https://github.com/owner/repo"),
	})
	if err != nil {
		t.Fatalf("expected repo URL without token to succeed, got %v", err)
	}
	if project.GithubOwner != "owner" || project.GithubRepo != "repo" {
		t.Fatalf("expected owner/repo, got %q/%q", project.GithubOwner, project.GithubRepo)
	}
	if project.PublicGithubTokenId != "" || project.CodeGithubTokenId != "" {
		t.Fatalf("expected empty token references, got %q/%q", project.PublicGithubTokenId, project.CodeGithubTokenId)
	}
}

// 创建时引用本人 Token 库记录：两个用途可同时引用同一条记录。
func TestProjectServiceCreate_WithTokenReferences(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-create-refs")
	projectSvc := newTestProjectService(svc)
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "ghp_create")

	project, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:                "refs-project",
		RepositoryUrl:       api.Ptr("https://github.com/owner/repo"),
		PublicGithubTokenId: api.Ptr(token.ID),
		CodeGithubTokenId:   api.Ptr(token.ID),
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if project.PublicGithubTokenId != token.ID || project.CodeGithubTokenId != token.ID {
		t.Fatalf("expected both references = %s, got %q/%q", token.ID, project.PublicGithubTokenId, project.CodeGithubTokenId)
	}
}

// 引用必须属于当前用户：不存在或他人 Token 统一 40420（跨用户不可见）。
func TestProjectServiceCreate_TokenReferenceValidation(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-create-ref-err")
	other := createTestUser(t, svc.db, "user-create-ref-other")
	foreign := createTestToken(t, svc.db, svc.cfg.Encryption.Key, other.ID, "ghp_foreign")
	projectSvc := newTestProjectService(svc)

	for _, tc := range []struct {
		name     string
		publicID string
		codeID   string
	}{
		{"missing public token", "nonexistent-token-id", ""},
		{"foreign public token", foreign.ID, ""},
		{"missing code token", "", "nonexistent-token-id"},
		{"foreign code token", "", foreign.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := projectSvc.Create(user.ID, &CreateProjectRequest{
				Name:                "ref-err-project",
				PublicGithubTokenId: api.NonEmpty(tc.publicID),
				CodeGithubTokenId:   api.NonEmpty(tc.codeID),
			})
			appErr, ok := err.(*errs.AppError)
			if !ok || !errors.Is(appErr, errs.ErrGitHubTokenNotFound) {
				t.Fatalf("expected ErrGitHubTokenNotFound, got %v", err)
			}
		})
	}
}

func TestProjectRepoCreate_RejectsMissingToken(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-repo-missing-token")
	err := svc.projectRepo.Create(&model.Project{
		ID:                  uuid.NewString(),
		UserID:              user.ID,
		Name:                "dangling-ref",
		PublicGitHubTokenID: "gone-token-id",
	})
	if !errors.Is(err, repository.ErrGitHubTokenMissing) {
		t.Fatalf("expected ErrGitHubTokenMissing, got %v", err)
	}
	err = svc.projectRepo.Create(&model.Project{
		ID:     uuid.NewString(),
		UserID: user.ID,
		Name:   "no-tokens",
	})
	if err != nil {
		t.Fatalf("empty token refs must be allowed, got %v", err)
	}
}

func TestProjectRepoUpdate_RejectsDeletedToken(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-repo-update-gone")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "ghp_live")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = ""
	})
	if err := svc.db.Where("id = ?", token.ID).Delete(&model.UserGitHubToken{}).Error; err != nil {
		t.Fatalf("delete token: %v", err)
	}
	project.Name = "renamed"
	if err := svc.projectRepo.Update(project); !errors.Is(err, repository.ErrGitHubTokenMissing) {
		t.Fatalf("expected ErrGitHubTokenMissing, got %v", err)
	}
}

// 项目写路径的集成存在性条件（INT-166 审查修复）：绑定的集成行在匹配之后、
// 落库之前被并发删除时，Create/Update 都落不下行，repository 返回可区分的
// ErrForgeIntegrationMissing（与删除侧 DeleteIfUnreferenced 的原子条件双向
// 关闭悬空引用窗口）。
func TestProjectRepoWrite_GoneIntegrationRejected(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-gone-integration")

	// Create：绑定非空但集成行不在 → ErrForgeIntegrationMissing（Token 条件
	// 全空放行，排除误报为 Token 缺失）。
	err := svc.projectRepo.Create(&model.Project{
		ID:                uuid.NewString(),
		UserID:            user.ID,
		Name:              "gone-integration",
		GithubOwner:       "acme",
		GithubRepo:        "app",
		CodeIntegrationID: "missing-integration",
		CodeGithubOwner:   "acme",
		CodeGithubRepo:    "app",
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	})
	if !errors.Is(err, repository.ErrForgeIntegrationMissing) {
		t.Fatalf("create with gone integration err = %v, want ErrForgeIntegrationMissing", err)
	}
	var rows int64
	svc.db.Model(&model.Project{}).Where("user_id = ?", user.ID).Count(&rows)
	if rows != 0 {
		t.Fatalf("rows = %d, want 0（失效绑定未落库）", rows)
	}

	// Update：已有绑定在落库前失效同样拦下。
	forge := createTestForgeIntegration(t, svc.db, "https://git.example.com")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.CodeIntegrationID = forge.ID
	})
	if err := svc.db.Where("id = ?", forge.ID).Delete(&model.ForgeIntegration{}).Error; err != nil {
		t.Fatalf("delete forge: %v", err)
	}
	project.Name = "renamed"
	if err := svc.projectRepo.Update(project); !errors.Is(err, repository.ErrForgeIntegrationMissing) {
		t.Fatalf("update with gone integration err = %v, want ErrForgeIntegrationMissing", err)
	}

	// 对照：项目本身不存在仍优先报 NotFound（项目存在性先于集成条件）。
	ghost := *project
	ghost.ID = "no-such-project"
	if err := svc.projectRepo.Update(&ghost); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("update missing project err = %v, want ErrRecordNotFound", err)
	}

	// service 层把集成失效折成 40001「代码仓集成不存在或已被删除，请重试」。
	psvc := newTestProjectService(svc)
	_, err = psvc.Update(project.ID, user.ID, &UpdateProjectRequest{Name: api.Ptr("renamed-2")})
	gone := appErr(t, err)
	if gone.Code != errs.ErrInvalidParams.Code || !strings.Contains(gone.Message, "代码仓集成不存在或已被删除") {
		t.Fatalf("service update = %d %q, want 40001 gone-integration", gone.Code, gone.Message)
	}

	// 逃生口：显式清空 code_repository_url 的更新照常落库（cid='' 放行），
	// 用户可以解除失效绑定而不必先恢复集成。
	if _, err := psvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		CodeRepositoryUrl: OptionalStringUpdate{Set: true},
	}); err != nil {
		t.Fatalf("clear binding update: %v", err)
	}
	var got model.Project
	if err := svc.db.Where("id = ?", project.ID).First(&got).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.CodeIntegrationID != "" || got.CodeGithubOwner != "" || got.CodeGithubRepo != "" {
		t.Fatalf("binding clear failed: cid=%q owner=%q repo=%q", got.CodeIntegrationID, got.CodeGithubOwner, got.CodeGithubRepo)
	}
}

// 项目仓库地址变更时，审查设置必须停用并刷新缓存的 repo_full_name：
// 否则旧仓库占着唯一索引，而新仓库可能与另一个已启用项目重复。
func TestProjectRepoUpdate_RepoSyncsReviewSettings(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-repo-review-sync")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = "oldowner"
		p.GithubRepo = "oldrepo"
	})
	now := time.Now().UTC()
	row := &model.ProjectReviewSettings{
		ProjectID:              project.ID,
		UserID:                 user.ID,
		RepoFullName:           "oldowner/oldrepo",
		Enabled:                true,
		CodeCombinationsJSON:   "[]",
		VisualCombinationsJSON: "[]",
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	if err := svc.db.Create(row).Error; err != nil {
		t.Fatalf("create review settings: %v", err)
	}
	project.GithubOwner = "NewOwner"
	project.GithubRepo = "NewRepo"
	if err := svc.projectRepo.Update(project); err != nil {
		t.Fatalf("update project: %v", err)
	}
	var got model.ProjectReviewSettings
	if err := svc.db.Where("project_id = ?", project.ID).First(&got).Error; err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if got.Enabled {
		t.Fatal("repo 变更后审查设置应停用")
	}
	if got.RepoFullName != "newowner/newrepo" {
		t.Fatalf("repo_full_name = %q, want newowner/newrepo", got.RepoFullName)
	}
	// 换仓库要推进 epoch 并清 pending：在途同步不能再向旧仓库发布。
	var ledger model.ReviewPullRequest
	if err := svc.db.Create(&model.ReviewPullRequest{
		ProjectID: project.ID, RepoFullName: "oldowner/oldrepo", PRNumber: 1,
		UserID: user.ID, HeadSHA: "sha", SyncPending: true, SyncEpoch: 4, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	project.GithubOwner, project.GithubRepo = "Another", "Repo"
	if err := svc.projectRepo.Update(project); err != nil {
		t.Fatalf("update project 2: %v", err)
	}
	svc.db.First(&ledger, "project_id = ? AND pr_number = 1", project.ID)
	if ledger.SyncPending || ledger.SyncEpoch != 5 {
		t.Fatalf("ledger pending/epoch = %v/%d, want false/5", ledger.SyncPending, ledger.SyncEpoch)
	}
}

func TestProjectRepo_NullTokenRefsScanAsEmpty(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-null-token-scan")
	project := createTestProject(t, svc.db, user.ID)
	if err := svc.db.Exec("UPDATE projects SET public_github_token_id = NULL, code_github_token_id = NULL WHERE id = ?", project.ID).Error; err != nil {
		t.Fatalf("null refs: %v", err)
	}
	var got model.Project
	if err := svc.db.First(&got, "id = ?", project.ID).Error; err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got.PublicGitHubTokenID != "" || got.CodeGitHubTokenID != "" {
		t.Fatalf("NULL refs must scan as empty strings, got public=%q code=%q", got.PublicGitHubTokenID, got.CodeGitHubTokenID)
	}
}

func TestProjectServiceGetBranches_NotGitHubConfigured(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-branches")
	projectSvc := newTestProjectService(svc)
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = ""
		p.GithubRepo = ""
	})

	_, _, err := projectSvc.GetBranches(t.Context(), project.ID, user.ID)
	if err == nil {
		t.Fatal("expected error for project without GitHub config, got nil")
	}
}

// 更新三态：key 缺省保留现值、Set+空值清空、Set+非空替换（须属于当前用户）。
func TestProjectServiceUpdate_TokenReferenceThreeStates(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-ref-three")
	projectSvc := newTestProjectService(svc)
	tokenA := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "ghp_token_a")
	tokenB := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "ghp_token_b")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = tokenA.ID
		p.CodeGitHubTokenID = tokenA.ID
	})

	storedRefs := func() (string, string) {
		var stored model.Project
		if err := svc.db.First(&stored, "id = ?", project.ID).Error; err != nil {
			t.Fatalf("reload project: %v", err)
		}
		return stored.PublicGitHubTokenID, stored.CodeGitHubTokenID
	}

	// key 缺省：保留现值
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		Description: api.Ptr("untouched refs"),
	}); err != nil {
		t.Fatalf("update without refs: %v", err)
	}
	publicRef, codeRef := storedRefs()
	if publicRef != tokenA.ID || codeRef != tokenA.ID {
		t.Fatalf("expected refs preserved, got %q/%q", publicRef, codeRef)
	}

	// 非空替换：公开仓库换到 tokenB，代码仓库保持
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		PublicGithubTokenId: TokenRefUpdate{Set: true, Value: tokenB.ID},
	}); err != nil {
		t.Fatalf("update replace public ref: %v", err)
	}
	publicRef, codeRef = storedRefs()
	if publicRef != tokenB.ID || codeRef != tokenA.ID {
		t.Fatalf("expected public=%s code=%s, got %q/%q", tokenB.ID, tokenA.ID, publicRef, codeRef)
	}

	// 显式清空：Set+空值清空公开仓库引用
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		PublicGithubTokenId: TokenRefUpdate{Set: true},
	}); err != nil {
		t.Fatalf("update clear public ref: %v", err)
	}
	publicRef, codeRef = storedRefs()
	if publicRef != "" || codeRef != tokenA.ID {
		t.Fatalf("expected public cleared, got %q/%q", publicRef, codeRef)
	}
}

// 更新时引用不属于当前用户返回 40420，且失败请求不改动现值。
func TestProjectServiceUpdate_TokenReferenceValidation(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-upd-ref-err")
	other := createTestUser(t, svc.db, "user-upd-ref-other")
	foreign := createTestToken(t, svc.db, svc.cfg.Encryption.Key, other.ID, "ghp_foreign_upd")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "ghp_keep")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = token.ID
	})
	projectSvc := newTestProjectService(svc)

	for _, tc := range []struct {
		name     string
		publicID string
	}{
		{"missing", "nonexistent-token-id"},
		{"foreign", foreign.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
				PublicGithubTokenId: TokenRefUpdate{Set: true, Value: tc.publicID},
			})
			appErr, ok := err.(*errs.AppError)
			if !ok || !errors.Is(appErr, errs.ErrGitHubTokenNotFound) {
				t.Fatalf("expected ErrGitHubTokenNotFound, got %v", err)
			}
		})
	}

	var stored model.Project
	if err := svc.db.First(&stored, "id = ?", project.ID).Error; err != nil {
		t.Fatalf("reload project: %v", err)
	}
	if stored.PublicGitHubTokenID != token.ID {
		t.Fatalf("expected failed updates to leave reference unchanged, got %q", stored.PublicGitHubTokenID)
	}
}

// 响应投影：两个引用字段原样返回（未配置为空串），不再有 has_github_* 布尔标记。
func TestProjectServiceResponse_TokenReferences(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-resp-refs")
	projectSvc := newTestProjectService(svc)

	bare := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = ""
		p.CodeGitHubTokenID = ""
	})
	resp, err := projectSvc.Get(bare.ID, user.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if resp.PublicGithubTokenId != "" || resp.CodeGithubTokenId != "" {
		t.Fatalf("expected empty references, got %q/%q", resp.PublicGithubTokenId, resp.CodeGithubTokenId)
	}

	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "ghp_linked")
	linked := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = token.ID
	})
	resp, err = projectSvc.Get(linked.ID, user.ID)
	if err != nil {
		t.Fatalf("get linked: %v", err)
	}
	if resp.PublicGithubTokenId != token.ID || resp.CodeGithubTokenId != token.ID {
		t.Fatalf("expected references=%s, got %q/%q", token.ID, resp.PublicGithubTokenId, resp.CodeGithubTokenId)
	}
}

// List 的 issue_count 统计项目 Issue 总数（open、closed 都计入），
// issue_workflow_counts 只计 state=open 的 Issue 按内部状态分组（无 internal
// meta 计入 unset，open+done 仍算入 done，closed 不进任何分段）；无 Issue 的
// 项目两字段都缺省（nil）；Get 等详情响应不携带这些字段。
func TestProjectServiceList_IssueCount(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-issue-count")
	projectSvc := newTestProjectService(svc)

	busy := createTestProject(t, svc.db, user.ID)
	quiet := createTestProject(t, svc.db, user.ID)
	empty := createTestProject(t, svc.db, user.ID)

	// 后续 Issue 共用项目时避开 (project_id, github_issue_id) 唯一索引；
	// 只改刚写入的 1001 行，已 bump 过的旧行保持原值
	bumpGitHubIssueID := func(projectID string, id int64) {
		if err := svc.db.Model(&model.IssueGitHubMeta{}).
			Where("project_id = ? AND github_issue_id = 1001", projectID).
			Update("github_issue_id", id).Error; err != nil {
			t.Fatalf("bump github_issue_id: %v", err)
		}
	}

	createTestIssue(t, svc.db, busy.ID, func(i *model.Issue) { i.SequenceNumber = 1 })
	bumpGitHubIssueID(busy.ID, 1002)
	second := createTestIssue(t, svc.db, busy.ID, func(i *model.Issue) {
		i.SequenceNumber = 2
		i.State = model.IssueStateClosed
	})
	bumpGitHubIssueID(busy.ID, 1003)
	third := createTestIssue(t, svc.db, busy.ID, func(i *model.Issue) { i.SequenceNumber = 3 })
	fourth := createTestIssue(t, svc.db, quiet.ID, func(i *model.Issue) { i.SequenceNumber = 1 })
	bumpGitHubIssueID(quiet.ID, 1002)
	fifth := createTestIssue(t, svc.db, quiet.ID, func(i *model.Issue) {
		i.SequenceNumber = 2
		i.State = model.IssueStateClosed
	})

	// busy：open 无 meta（unset）+ closed done + open done；
	// quiet：open in_progress + closed todo
	for issueID, status := range map[string]model.IssueWorkflowStatus{
		second.ID: model.IssueWorkflowStatusDone,
		third.ID:  model.IssueWorkflowStatusDone,
		fourth.ID: model.IssueWorkflowStatusInProgress,
		fifth.ID:  model.IssueWorkflowStatusTodo,
	} {
		if err := svc.db.Create(&model.IssueInternalMeta{
			IssueID:        issueID,
			WorkflowStatus: status,
			CreatedAt:      time.Now(),
			UpdatedAt:      time.Now(),
		}).Error; err != nil {
			t.Fatalf("create internal meta: %v", err)
		}
	}

	resp, total, err := projectSvc.List(user.ID, 1, 10, true)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if total != 3 || len(resp) != 3 {
		t.Fatalf("expected 3 projects, got total=%d len=%d", total, len(resp))
	}

	counts := make(map[string]*int, len(resp))
	workflowCounts := make(map[string]*api.IssueWorkflowCounts, len(resp))
	for _, p := range resp {
		counts[p.Id] = p.IssueCount
		workflowCounts[p.Id] = p.IssueWorkflowCounts
	}
	if got := counts[busy.ID]; got == nil || *got != 3 {
		t.Fatalf("busy issue_count = %v, want 3（closed 也计入）", got)
	}
	if got := counts[quiet.ID]; got == nil || *got != 2 {
		t.Fatalf("quiet issue_count = %v, want 2（closed 也计入）", got)
	}
	if got := counts[empty.ID]; got != nil {
		t.Fatalf("empty issue_count = %v, want nil", *got)
	}

	if got := workflowCounts[busy.ID]; got == nil ||
		got.Unset != 1 || got.Done != 1 || got.Todo != 0 || got.InProgress != 0 {
		t.Fatalf("busy issue_workflow_counts = %+v, want {unset:1 done:1}（closed done 不计）", got)
	}
	if got := workflowCounts[quiet.ID]; got == nil ||
		got.InProgress != 1 || got.Unset != 0 || got.Todo != 0 || got.Done != 0 {
		t.Fatalf("quiet issue_workflow_counts = %+v, want {in_progress:1}（closed todo 不计）", got)
	}
	if got := workflowCounts[empty.ID]; got != nil {
		t.Fatalf("empty issue_workflow_counts = %+v, want nil", *got)
	}

	detail, err := projectSvc.Get(busy.ID, user.ID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if detail.IssueCount != nil || detail.IssueWorkflowCounts != nil {
		t.Fatalf("get 详情不应携带计数: issue_count=%v issue_workflow_counts=%v",
			detail.IssueCount, detail.IssueWorkflowCounts)
	}
}

// List 的 latest_version 取各项目 created_at 最大的版本（无版本则缺省），
// issue_sync 由批量预取填充；版本与同步状态取数必须是常数条批量查询
// （INT-154：此前逐项目各一次版本查询 + 两次同步状态往返，共 3P 条）。
func TestProjectServiceList_LatestVersion(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-latest-version")
	projectSvc := newTestProjectService(svc)

	withVersions := createTestProject(t, svc.db, user.ID)
	failedSync := createTestProject(t, svc.db, user.ID)
	noVersions := createTestProject(t, svc.db, user.ID)

	older := time.Now().Add(-2 * time.Hour)
	newer := time.Now().Add(-1 * time.Hour)
	createTestVersion(t, svc.db, withVersions.ID, func(v *model.Version) {
		v.VersionNumber = "v1.0.0"
		v.CreatedAt = older
	})
	latestW := createTestVersion(t, svc.db, withVersions.ID, func(v *model.Version) {
		v.VersionNumber = "v1.1.0"
		v.Status = model.VersionStatusShipped
		v.CreatedAt = newer
	})
	// failedSync 两条版本 created_at 并列：按 id 升序应稳定取小者
	// （旧实现 Order("created_at DESC").First 会隐式追加主键升序）
	tie := time.Now().Add(-30 * time.Minute)
	createTestVersion(t, svc.db, failedSync.ID, func(v *model.Version) {
		v.ID = "zzzzzzzz-0000-4000-8000-000000000000"
		v.VersionNumber = "v2.0.1"
		v.CreatedAt = tie
	})
	latestF := createTestVersion(t, svc.db, failedSync.ID, func(v *model.Version) {
		v.ID = "aaaaaaaa-0000-4000-8000-000000000000"
		v.VersionNumber = "v2.0.0"
		v.CreatedAt = tie
	})
	if err := svc.syncStateRepo.Save(&model.IssueSyncState{
		ProjectID: failedSync.ID,
		Status:    model.IssueSyncStatusFailed,
		LastError: "boom",
	}); err != nil {
		t.Fatalf("seed sync state: %v", err)
	}

	// 记录 List 期间执行的 SQL：断言两张表各只命中固定条数
	var stmts []string
	record := func(d *gorm.DB) {
		if d.Statement != nil {
			stmts = append(stmts, d.Statement.SQL.String())
		}
	}
	if err := svc.db.Callback().Query().After("gorm:query").Register("int154:record-query", record); err != nil {
		t.Fatalf("register query callback: %v", err)
	}
	// Raw().Scan() 的执行走 row 处理器（Rows→RowQuery）
	if err := svc.db.Callback().Row().After("gorm:row").Register("int154:record-row", record); err != nil {
		t.Fatalf("register row callback: %v", err)
	}
	if err := svc.db.Callback().Create().After("gorm:create").Register("int154:record-create", record); err != nil {
		t.Fatalf("register create callback: %v", err)
	}

	resp, total, err := projectSvc.List(user.ID, 1, 10, true)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if total != 3 || len(resp) != 3 {
		t.Fatalf("expected 3 projects, got total=%d len=%d", total, len(resp))
	}

	latest := make(map[string]*api.LatestVersion, len(resp))
	syncs := make(map[string]*api.IssueSyncState, len(resp))
	for _, p := range resp {
		latest[p.Id] = p.LatestVersion
		syncs[p.Id] = p.IssueSync
	}

	got := latest[withVersions.ID]
	if got == nil || got.Id != latestW.ID || got.VersionNumber != "v1.1.0" ||
		got.Status != model.VersionStatusShipped ||
		got.CreatedAt != latestW.CreatedAt.Format("2006-01-02T15:04:05Z") {
		t.Fatalf("withVersions latest_version = %+v, want %s/shipped", got, latestW.ID)
	}
	if got := latest[failedSync.ID]; got == nil || got.Id != latestF.ID {
		t.Fatalf("failedSync latest_version = %+v, want %s", got, latestF.ID)
	}
	if got := latest[noVersions.ID]; got != nil {
		t.Fatalf("noVersions latest_version = %+v, want nil", got)
	}

	if got := syncs[withVersions.ID]; got == nil || got.Status != model.IssueSyncStatusIdle {
		t.Fatalf("withVersions issue_sync = %+v, want idle", got)
	}
	if got := syncs[failedSync.ID]; got == nil ||
		got.Status != model.IssueSyncStatusFailed || got.LastError != "boom" {
		t.Fatalf("failedSync issue_sync = %+v, want failed/boom", got)
	}

	versionQueries, syncStatements := 0, 0
	for _, sql := range stmts {
		if strings.Contains(sql, "versions") {
			versionQueries++
		}
		if strings.Contains(sql, "issue_sync_states") {
			syncStatements++
		}
	}
	if versionQueries != 1 {
		t.Fatalf("versions 命中 %d 条查询，want 1（批量）：%v", versionQueries, stmts)
	}
	if syncStatements != 2 {
		t.Fatalf("issue_sync_states 命中 %d 条语句，want 2（批量补行+取回）：%v", syncStatements, stmts)
	}

	// 详情不携带 latest_version（omitempty 仅列表项）
	detail, err := projectSvc.Get(withVersions.ID, user.ID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if detail.LatestVersion != nil {
		t.Fatalf("get 详情不应携带 latest_version: %+v", detail.LatestVersion)
	}
}

// description 按指针三态更新：nil 保留现值、指向空串清空、指向非空替换
// （INT-67 回归：Update 曾从未给 project.Description 赋值，任何取值都静默无效）。
func TestProjectServiceUpdate_Description(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-desc")
	projectSvc := newTestProjectService(svc)
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.Description = "old desc"
	})

	storedDesc := func() string {
		var stored model.Project
		if err := svc.db.First(&stored, "id = ?", project.ID).Error; err != nil {
			t.Fatalf("reload project: %v", err)
		}
		return stored.Description
	}

	resp, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{})
	if err != nil {
		t.Fatalf("update nil description: %v", err)
	}
	if resp.Description != "old desc" || storedDesc() != "old desc" {
		t.Fatalf("expected old desc preserved, got resp=%q stored=%q", resp.Description, storedDesc())
	}

	resp, err = projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		Description: api.Ptr("new desc"),
	})
	if err != nil {
		t.Fatalf("update new desc: %v", err)
	}
	if resp.Description != "new desc" || storedDesc() != "new desc" {
		t.Fatalf("expected new desc, got resp=%q stored=%q", resp.Description, storedDesc())
	}

	resp, err = projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		Description: api.Ptr(""),
	})
	if err != nil {
		t.Fatalf("update empty desc: %v", err)
	}
	if resp.Description != "" || storedDesc() != "" {
		t.Fatalf("expected cleared description, got resp=%q stored=%q", resp.Description, storedDesc())
	}
}

// 创建带 code_repository_url：单独配置的代码仓库原样落库并回显；
// 与公开仓库大小写不敏感相同 → 归一为空串（空 = 同公开仓库）。
func TestProjectServiceCreate_CodeRepoURL(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-code-repo")
	projectSvc := newTestProjectService(svc)

	created, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:              "code-repo-project",
		RepositoryUrl:     api.Ptr("https://github.com/liuyincs/musiver"),
		CodeRepositoryUrl: api.Ptr("gitbobobo/musiver"),
	})
	if err != nil {
		t.Fatalf("create with code repo: %v", err)
	}
	if created.CodeGithubOwner != "gitbobobo" || created.CodeGithubRepo != "musiver" {
		t.Fatalf("code repo = %q/%q, want gitbobobo/musiver", created.CodeGithubOwner, created.CodeGithubRepo)
	}
	var stored model.Project
	if err := svc.db.First(&stored, "id = ?", created.Id).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.CodeGithubOwner != "gitbobobo" || stored.CodeGithubRepo != "musiver" {
		t.Fatalf("stored code repo = %q/%q", stored.CodeGithubOwner, stored.CodeGithubRepo)
	}

	// code 仓库与公开仓库相同（大小写不同）→ 归一为空串。
	same, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:              "code-repo-same",
		RepositoryUrl:     api.Ptr("liuyincs/Musiver"),
		CodeRepositoryUrl: api.Ptr("https://github.com/LIUYINCS/musiver.git"),
	})
	if err != nil {
		t.Fatalf("create same repo: %v", err)
	}
	if same.CodeGithubOwner != "" || same.CodeGithubRepo != "" {
		t.Fatalf("same repo should normalize to empty, got %q/%q", same.CodeGithubOwner, same.CodeGithubRepo)
	}

	// 非法格式 → 40001。
	if _, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:              "code-repo-bad",
		CodeRepositoryUrl: api.Ptr("git bobo/musiver"),
	}); appErrorCode(t, err) != errs.ErrInvalidParams.Code {
		t.Fatalf("invalid code repo code = %d, want %d", appErrorCode(t, err), errs.ErrInvalidParams.Code)
	}
}

// 更新 code_repository_url 三态：key 缺省保留、显式空清空（回落公开仓库）、
// 非空替换；替换成与公开仓库相同的值归一为空串；失败请求不留半改动。
func TestProjectServiceUpdate_CodeRepoURLThreeStates(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-code-repo-upd")
	projectSvc := newTestProjectService(svc)
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = "liuyincs"
		p.GithubRepo = "musiver"
		p.CodeGithubOwner = "gitbobobo"
		p.CodeGithubRepo = "musiver"
	})

	storedCode := func() (string, string) {
		var stored model.Project
		if err := svc.db.First(&stored, "id = ?", project.ID).Error; err != nil {
			t.Fatalf("reload project: %v", err)
		}
		return stored.CodeGithubOwner, stored.CodeGithubRepo
	}

	// key 缺省：保留现值。
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		Description: api.Ptr("code repo untouched"),
	}); err != nil {
		t.Fatalf("update without code repo: %v", err)
	}
	if owner, repo := storedCode(); owner != "gitbobobo" || repo != "musiver" {
		t.Fatalf("expected code repo preserved, got %q/%q", owner, repo)
	}

	// 非空替换。
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		CodeRepositoryUrl: OptionalStringUpdate{Set: true, Value: "https://github.com/other/code.git"},
	}); err != nil {
		t.Fatalf("update replace code repo: %v", err)
	}
	if owner, repo := storedCode(); owner != "other" || repo != "code" {
		t.Fatalf("expected other/code, got %q/%q", owner, repo)
	}

	// 替换成与公开仓库相同的值（大小写不同）→ 归一为空串。
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		CodeRepositoryUrl: OptionalStringUpdate{Set: true, Value: "LIUYINCS/MUSIVER"},
	}); err != nil {
		t.Fatalf("update same-as-public code repo: %v", err)
	}
	if owner, repo := storedCode(); owner != "" || repo != "" {
		t.Fatalf("same-as-public should normalize to empty, got %q/%q", owner, repo)
	}

	// 显式空串清空（代码仓库本就为空时仍是合法三态操作）。
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		CodeRepositoryUrl: OptionalStringUpdate{Set: true, Value: "again/repo"},
	}); err != nil {
		t.Fatalf("set again: %v", err)
	}
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		CodeRepositoryUrl: OptionalStringUpdate{Set: true},
	}); err != nil {
		t.Fatalf("update clear code repo: %v", err)
	}
	if owner, repo := storedCode(); owner != "" || repo != "" {
		t.Fatalf("expected cleared code repo, got %q/%q", owner, repo)
	}

	// 非法格式 → 40001，且不改动现值。
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		CodeRepositoryUrl: OptionalStringUpdate{Set: true, Value: "bad repo"},
	}); appErrorCode(t, err) != errs.ErrInvalidParams.Code {
		t.Fatalf("invalid code repo code = %d, want %d", appErrorCode(t, err), errs.ErrInvalidParams.Code)
	}
	if owner, repo := storedCode(); owner != "" || repo != "" {
		t.Fatalf("failed update must leave value unchanged, got %q/%q", owner, repo)
	}

	// —— 归一收敛为「应用完仓库字段后的最后一步」的回归 ——

	seedEnabled := func(repo string) {
		t.Helper()
		now := time.Now().UTC()
		if err := svc.db.Where("project_id = ?", project.ID).
			Delete(&model.ProjectReviewSettings{}).Error; err != nil {
			t.Fatalf("clear settings: %v", err)
		}
		if err := svc.db.Create(&model.ProjectReviewSettings{
			ProjectID: project.ID, UserID: user.ID, RepoFullName: repo, Enabled: true,
			CodeCombinationsJSON: "[]", VisualCombinationsJSON: "[]",
			CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed settings: %v", err)
		}
	}
	loadSettings := func() model.ProjectReviewSettings {
		t.Helper()
		var row model.ProjectReviewSettings
		if err := svc.db.Where("project_id = ?", project.ID).First(&row).Error; err != nil {
			t.Fatalf("load settings: %v", err)
		}
		return row
	}

	// 同一请求里公开与代码仓库指向同一仓库（大小写不同）→ code 归一为空、
	// 审查停用且快照刷新为新公开仓库。
	seedEnabled("liuyincs/musiver") // 此时代码仓库为空，审查仓库回落公开仓库
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		RepositoryUrl:     api.Ptr("new/pub"),
		CodeRepositoryUrl: OptionalStringUpdate{Set: true, Value: "NEW/Pub"},
	}); err != nil {
		t.Fatalf("update both repos: %v", err)
	}
	if owner, repo := storedCode(); owner != "" || repo != "" {
		t.Fatalf("同一请求 code=公开（大小写不同）应归一为空, got %q/%q", owner, repo)
	}
	if row := loadSettings(); row.Enabled || row.RepoFullName != "new/pub" {
		t.Fatalf("审查应停用且快照刷新为 new/pub: enabled=%v repo=%q", row.Enabled, row.RepoFullName)
	}

	// code 仓库等于旧公开仓库、同时公开仓库改走：code 按请求显式落库，
	// 不被误清空（归一只清「改完之后两者相同」的情形）。
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		RepositoryUrl:     api.Ptr("another/pub2"),
		CodeRepositoryUrl: OptionalStringUpdate{Set: true, Value: "liuyincs/musiver"},
	}); err != nil {
		t.Fatalf("update code to old public: %v", err)
	}
	if owner, repo := storedCode(); owner != "liuyincs" || repo != "musiver" {
		t.Fatalf("code 应按请求显式落库, got %q/%q", owner, repo)
	}
	if row := loadSettings(); row.RepoFullName != "liuyincs/musiver" {
		t.Fatalf("审查仓库已变为 liuyincs/musiver, snapshot = %q", row.RepoFullName)
	}

	// 只改公开仓库使其等于存量显式代码仓库：归一成空；审查仓库没变
	// （liuyincs/musiver → liuyincs/musiver），不停用、快照保持正确。
	seedEnabled("liuyincs/musiver")
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		RepositoryUrl: api.Ptr("LIUYINCS/Musiver"),
	}); err != nil {
		t.Fatalf("update public onto code repo: %v", err)
	}
	if owner, repo := storedCode(); owner != "" || repo != "" {
		t.Fatalf("公开仓库撞存量 code 仓库应归一为空, got %q/%q", owner, repo)
	}
	if row := loadSettings(); !row.Enabled || row.RepoFullName != "liuyincs/musiver" {
		t.Fatalf("审查仓库未变不应停用: enabled=%v repo=%q", row.Enabled, row.RepoFullName)
	}
	var storedPublic model.Project
	if err := svc.db.First(&storedPublic, "id = ?", project.ID).Error; err != nil {
		t.Fatalf("reload project: %v", err)
	}
	if storedPublic.GithubOwner != "LIUYINCS" || storedPublic.GithubRepo != "Musiver" {
		t.Fatalf("公开仓库应原样落库, got %q/%q", storedPublic.GithubOwner, storedPublic.GithubRepo)
	}
}

// 改代码仓库会停用审查并刷新 repo_full_name 快照（新的 CodeRepo 小写）；
// 代码仓库已显式配置时，只改公开仓库不停用审查——审查仓库未变。
func TestProjectRepoUpdate_CodeRepoSyncsReviewSettings(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-code-repo-sync")

	seedEnabled := func(projectID string, repo string) {
		t.Helper()
		now := time.Now().UTC()
		if err := svc.db.Create(&model.ProjectReviewSettings{
			ProjectID: projectID, UserID: user.ID, RepoFullName: repo, Enabled: true,
			CodeCombinationsJSON: "[]", VisualCombinationsJSON: "[]",
			CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed settings: %v", err)
		}
	}

	// 只改代码仓库：停用 + 快照刷新为新代码仓库。
	withCode := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = "pub"
		p.GithubRepo = "proj"
	})
	seedEnabled(withCode.ID, "pub/proj")
	withCode.CodeGithubOwner = "gitbobobo"
	withCode.CodeGithubRepo = "musiver"
	if err := svc.projectRepo.Update(withCode); err != nil {
		t.Fatalf("update code repo: %v", err)
	}
	var gotCode model.ProjectReviewSettings
	if err := svc.db.Where("project_id = ?", withCode.ID).First(&gotCode).Error; err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if gotCode.Enabled {
		t.Fatal("改代码仓库后审查设置应停用")
	}
	if gotCode.RepoFullName != "gitbobobo/musiver" {
		t.Fatalf("repo_full_name = %q, want gitbobobo/musiver", gotCode.RepoFullName)
	}

	// 只改公开仓库（代码仓库显式配置）：审查不停用、快照不变。
	codeOnly := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = "pub"
		p.GithubRepo = "proj"
		p.CodeGithubOwner = "gitbobobo"
		p.CodeGithubRepo = "musiver"
	})
	seedEnabled(codeOnly.ID, "gitbobobo/musiver")
	codeOnly.GithubOwner = "AnotherPub"
	codeOnly.GithubRepo = "AnotherProj"
	if err := svc.projectRepo.Update(codeOnly); err != nil {
		t.Fatalf("update public repo only: %v", err)
	}
	var gotCodeOnly model.ProjectReviewSettings
	if err := svc.db.Where("project_id = ?", codeOnly.ID).First(&gotCodeOnly).Error; err != nil {
		t.Fatalf("load settings 2: %v", err)
	}
	if !gotCodeOnly.Enabled || gotCodeOnly.RepoFullName != "gitbobobo/musiver" {
		t.Fatalf("只改公开仓库不应影响审查: enabled=%v repo=%q", gotCodeOnly.Enabled, gotCodeOnly.RepoFullName)
	}

	// 未配置代码仓库时改公开仓库：行为与现状一致（停用 + 刷新）。
	fallback := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = "old"
		p.GithubRepo = "repo"
	})
	seedEnabled(fallback.ID, "old/repo")
	fallback.GithubOwner = "new"
	fallback.GithubRepo = "Repo"
	if err := svc.projectRepo.Update(fallback); err != nil {
		t.Fatalf("update public repo fallback: %v", err)
	}
	var gotFallback model.ProjectReviewSettings
	if err := svc.db.Where("project_id = ?", fallback.ID).First(&gotFallback).Error; err != nil {
		t.Fatalf("load settings 3: %v", err)
	}
	if gotFallback.Enabled || gotFallback.RepoFullName != "new/repo" {
		t.Fatalf("fallback 项目改公开仓库应停用并刷新: enabled=%v repo=%q", gotFallback.Enabled, gotFallback.RepoFullName)
	}
}

// createTestForgeIntegration 测试用 forgejo 集成行：base_url 原样落库
// （生产保存时已归一，用例里直接写归一后的形态），host_key 与生产一致
// 按 base_url 归一写入——绑定匹配按 host_key 点查。
func createTestForgeIntegration(t *testing.T, db *gorm.DB, baseURL string) *model.ForgeIntegration {
	t.Helper()
	row := &model.ForgeIntegration{
		ID:         uuid.NewString(),
		Kind:       model.ForgeIntegrationKindForgejo,
		Name:       "forge-" + baseURL,
		BaseURL:    baseURL,
		APIBaseURL: baseURL + "/api/v1",
		HostKey:    forges.HostKey(baseURL),
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("create forge integration: %v", err)
	}
	return row
}

// 项目↔集成绑定（INT-166）创建侧：代码仓 URL 命中已配置 forgejo 主机 →
// code_integration_id 落该集成 id；公开仓 URL 非 github 主机 → 40001 明确
// 报错（命中 forgejo 与未匹配两种消息）；public_integration_id 恒为空。
func TestProjectServiceCreate_ForgeIntegrationBinding(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-forge-bind")
	forgeSvc := newTestProjectService(svc)
	forge := createTestForgeIntegration(t, svc.db, "https://git.example.com")
	forgePort := createTestForgeIntegration(t, svc.db, "https://git.local:8443")

	// 命中 forgejo：code_integration_id 落集成 id，公开仓引用列为空。
	created, err := forgeSvc.Create(user.ID, &CreateProjectRequest{
		Name:              "forge-bind",
		RepositoryUrl:     api.Ptr("https://github.com/liuyincs/musiver"),
		CodeRepositoryUrl: api.Ptr("git.example.com/gitbobobo/musiver.git"),
	})
	if err != nil {
		t.Fatalf("create with forgejo code repo: %v", err)
	}
	var stored model.Project
	if err := svc.db.First(&stored, "id = ?", created.Id).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.CodeIntegrationID != forge.ID {
		t.Fatalf("code_integration_id = %q, want %q", stored.CodeIntegrationID, forge.ID)
	}
	if stored.CodeGithubOwner != "gitbobobo" || stored.CodeGithubRepo != "musiver" {
		t.Fatalf("code repo = %q/%q", stored.CodeGithubOwner, stored.CodeGithubRepo)
	}
	if stored.PublicIntegrationID != "" {
		t.Fatalf("public_integration_id 应恒为空, got %q", stored.PublicIntegrationID)
	}
	if created.CodeIntegrationId == nil || *created.CodeIntegrationId != forge.ID {
		t.Fatalf("响应应带出集成 id, got %v", created.CodeIntegrationId)
	}

	// 自定义端口：带端口的 URL 命中带端口的集成。
	withPort, err := forgeSvc.Create(user.ID, &CreateProjectRequest{
		Name:              "forge-bind-port",
		CodeRepositoryUrl: api.Ptr("https://git.local:8443/o/r"),
	})
	if err != nil {
		t.Fatalf("create with ported forgejo: %v", err)
	}
	var storedPort model.Project
	if err := svc.db.First(&storedPort, "id = ?", withPort.Id).Error; err != nil {
		t.Fatalf("reload 2: %v", err)
	}
	if storedPort.CodeIntegrationID != forgePort.ID {
		t.Fatalf("ported code_integration_id = %q, want %q", storedPort.CodeIntegrationID, forgePort.ID)
	}

	// 端口不匹配（缺省 443 vs 配置的 8443）→ 未匹配。
	_, err = forgeSvc.Create(user.ID, &CreateProjectRequest{
		Name:              "forge-bind-port-mismatch",
		CodeRepositoryUrl: api.Ptr("https://git.local/o/r"),
	})
	appErr, ok := err.(*errs.AppError)
	if !ok || appErr.Code != errs.ErrInvalidParams.Code || !strings.Contains(appErr.Message, "未匹配任何已配置集成") {
		t.Fatalf("expected 40001 未匹配, got %v", err)
	}
	// 报错透出用户输入的主机形态——补默认端口是内部归一细节，不回显
	// 「:443」让人误以为自己写了端口。
	if strings.Contains(appErr.Message, "git.local:") || !strings.Contains(appErr.Message, "git.local") {
		t.Fatalf("error should echo user host without normalized default port, got %q", appErr.Message)
	}

	// http 集成（allow_private_network 场景）：URL 也必须显式写 http://
	// 才能命中——http scheme 归一到 :80 与 :443 的 https 集成互不混淆。
	forgeHTTP := createTestForgeIntegration(t, svc.db, "http://git.lan")
	httpBound, err := forgeSvc.Create(user.ID, &CreateProjectRequest{
		Name:              "forge-bind-http",
		CodeRepositoryUrl: api.Ptr("http://git.lan/o/r"),
	})
	if err != nil {
		t.Fatalf("create with http forgejo: %v", err)
	}
	var storedHTTP model.Project
	if err := svc.db.First(&storedHTTP, "id = ?", httpBound.Id).Error; err != nil {
		t.Fatalf("reload http: %v", err)
	}
	if storedHTTP.CodeIntegrationID != forgeHTTP.ID {
		t.Fatalf("http code_integration_id = %q, want %q", storedHTTP.CodeIntegrationID, forgeHTTP.ID)
	}
	// 同一主机 https 写法（git.lan:443）不命中 http 集成（git.lan:80）。
	_, err = forgeSvc.Create(user.ID, &CreateProjectRequest{
		Name:              "forge-bind-http-mismatch",
		CodeRepositoryUrl: api.Ptr("git.lan/o/r"),
	})
	appErr, ok = err.(*errs.AppError)
	if !ok || appErr.Code != errs.ErrInvalidParams.Code || !strings.Contains(appErr.Message, "未匹配任何已配置集成") {
		t.Fatalf("expected 40001 scheme mismatch, got %v", err)
	}

	// 未知主机 → 明确报错，不再静默解析成 owner。
	_, err = forgeSvc.Create(user.ID, &CreateProjectRequest{
		Name:          "unknown-host",
		RepositoryUrl: api.Ptr("https://nowhere.example.org/o/r"),
	})
	appErr, ok = err.(*errs.AppError)
	if !ok || appErr.Code != errs.ErrInvalidParams.Code || !strings.Contains(appErr.Message, "nowhere.example.org") {
		t.Fatalf("expected 40001 unknown host, got %v", err)
	}

	// 公开仓命中 forgejo 集成 → 拒绝且消息点明「公开仓库仅支持 GitHub」。
	_, err = forgeSvc.Create(user.ID, &CreateProjectRequest{
		Name:          "public-forgejo",
		RepositoryUrl: api.Ptr("https://git.example.com/o/r"),
	})
	appErr, ok = err.(*errs.AppError)
	if !ok || appErr.Code != errs.ErrInvalidParams.Code || !strings.Contains(appErr.Message, "公开仓库仅支持 GitHub") {
		t.Fatalf("expected 40001 public GitHub-only, got %v", err)
	}
}

// 项目↔集成绑定（INT-166）更新侧三态与归一：显式空清空代码仓时集成绑定
// 一并清；替换时按新 host 重新派生；forgejo 代码仓与 github 公开仓同
// owner/repo 不归一（归一会丢集成绑定）。
func TestProjectServiceUpdate_ForgeIntegrationBinding(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-forge-bind-upd")
	projectSvc := newTestProjectService(svc)
	forge := createTestForgeIntegration(t, svc.db, "https://git.example.com")

	created, err := projectSvc.Create(user.ID, &CreateProjectRequest{
		Name:              "forge-bind-upd",
		RepositoryUrl:     api.Ptr("liuyincs/musiver"),
		CodeRepositoryUrl: api.Ptr("git.example.com/gitbobobo/musiver"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	project, err := svc.projectRepo.FindByID(created.Id, user.ID)
	if err != nil {
		t.Fatalf("find: %v", err)
	}

	storedBinding := func() (string, string, string) {
		t.Helper()
		var stored model.Project
		if err := svc.db.First(&stored, "id = ?", project.ID).Error; err != nil {
			t.Fatalf("reload project: %v", err)
		}
		return stored.CodeGithubOwner, stored.CodeGithubOwner + "/" + stored.CodeGithubRepo, stored.CodeIntegrationID
	}

	// key 缺省：保留绑定。
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		Description: api.Ptr("binding untouched"),
	}); err != nil {
		t.Fatalf("update without code repo: %v", err)
	}
	if owner, _, id := storedBinding(); id != forge.ID || owner != "gitbobobo" {
		t.Fatalf("expected binding preserved, got owner=%q id=%q", owner, id)
	}

	// 替换为 github 代码仓 → 集成绑定清空。
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		CodeRepositoryUrl: OptionalStringUpdate{Set: true, Value: "https://github.com/other/code"},
	}); err != nil {
		t.Fatalf("update to github code repo: %v", err)
	}
	if owner, _, id := storedBinding(); owner != "other" || id != "" {
		t.Fatalf("expected github binding cleared, got owner=%q id=%q", owner, id)
	}

	// 替换为 forgejo 代码仓 → 绑定重新派生。
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		CodeRepositoryUrl: OptionalStringUpdate{Set: true, Value: "git.example.com/gitbobobo/musiver"},
	}); err != nil {
		t.Fatalf("update to forgejo code repo: %v", err)
	}
	if _, _, id := storedBinding(); id != forge.ID {
		t.Fatalf("expected binding %q, got %q", forge.ID, id)
	}

	// 显式空：代码仓与集成绑定一起清。
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		CodeRepositoryUrl: OptionalStringUpdate{Set: true},
	}); err != nil {
		t.Fatalf("clear code repo: %v", err)
	}
	if owner, full, id := storedBinding(); owner != "" || full != "/" || id != "" {
		t.Fatalf("expected cleared, got owner=%q full=%q id=%q", owner, full, id)
	}

	// 跨 host 同 owner/repo 不归一：forgejo 代码仓 liuyincs/MUSIVER 与 github
	// 公开仓 liuyincs/musiver 大小写不敏感相同，但不是同一仓库。
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		CodeRepositoryUrl: OptionalStringUpdate{Set: true, Value: "git.example.com/LIUYINCS/MUSIVER"},
	}); err != nil {
		t.Fatalf("update cross-host same repo: %v", err)
	}
	if owner, _, id := storedBinding(); owner != "LIUYINCS" || id != forge.ID {
		t.Fatalf("跨 host 同 owner/repo 不得归一, got owner=%q id=%q", owner, id)
	}

	// 替换为未知主机 → 40001 且不改动现值。
	if _, err := projectSvc.Update(project.ID, user.ID, &UpdateProjectRequest{
		CodeRepositoryUrl: OptionalStringUpdate{Set: true, Value: "https://nowhere.example.org/o/r"},
	}); err == nil {
		t.Fatal("expected unknown host error")
	}
	if _, _, id := storedBinding(); id != forge.ID {
		t.Fatalf("failed update must leave binding, got %q", id)
	}
}
