package service

import (
	"context"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/pkg/githubmedia"
)

// collectScreenshotURLs 公共 GitHub 部署（webHost 为 github.com）下的收集结果：
// 绝大多数用例只验证解析与准入规则，不关心域名推导，这里给一处默认口径。
func collectScreenshotURLs(body string) []screenshotRef {
	return collectScreenshotURLsAt(body, "github.com")
}

// TestScreenshotAttachmentURLsByDeploymentHost GHE 部署的附件直链
// （`https://<ghe-host>/user-attachments/assets/<uuid>`，无扩展名）要按部署
// 的 GitHub Web host 认出来，否则裸链被准入拒掉 → HasScreenshots=false →
// 视觉审查静默不启动；认出来之后，私有附件匿名 404 也要能带安装令牌重试。
func TestScreenshotAttachmentURLsByDeploymentHost(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.svc.cfg.GitHubApp.APIBaseURL = "https://ghe.example.com/api/v3"
	review := e.withT3(nil, nil, nil)
	if got := review.githubWebHost(); got != "ghe.example.com" {
		t.Fatalf("githubWebHost = %q, want ghe.example.com", got)
	}

	body := "附件 https://ghe.example.com/user-attachments/assets/abc-123 如上"
	refs := collectScreenshotURLsAt(body, review.githubWebHost())
	if len(refs) != 1 || refs[0].URL != "https://ghe.example.com/user-attachments/assets/abc-123" {
		t.Fatalf("GHE 附件直链应被收集，refs = %+v", refs)
	}
	// 公共 GitHub 部署口径下同一条链接不是附件形态（无扩展名不收）：对照组
	// 证明上面的收集来自部署 host 推导，而不是放宽了裸链准入。
	if refs := collectScreenshotURLs(body); len(refs) != 0 {
		t.Fatalf("公共 GitHub 部署不应收集 GHE 附件链接，refs = %+v", refs)
	}

	// 私有 GHE 附件：匿名 404 → 带 App 安装令牌重试（凭证闸门也按部署 host）。
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	media := &fakeReviewMedia{
		cached:  &githubmedia.CachedMedia{Path: "/cache/ghe.png"},
		anonErr: &githubmedia.UpstreamStatusError{StatusCode: 404},
	}
	review = e.withT3(nil, media, func(context.Context, int64) (string, error) {
		return "ghe-installation-token", nil
	})
	_, assets, err := review.ensureAssets(context.Background(), repo, 91, "sha-ghe", body, 7)
	if err != nil {
		t.Fatalf("ensureAssets: %v", err)
	}
	if media.anonCalls != 1 || media.bearerCalls != 1 || media.lastBearer != "ghe-installation-token" {
		t.Fatalf("calls = anon:%d bearer:%d(%q), want 1/1 且带安装令牌",
			media.anonCalls, media.bearerCalls, media.lastBearer)
	}
	if len(assets) != 1 || assets[0].StoragePath != "/cache/ghe.png" {
		t.Fatalf("assets = %+v", assets)
	}
}

// TestScreenshotAttachmentURLsGHEAuthority 带非默认端口的 GHE 实例：Web 地址
// 要连端口一起推导（凭证闸门按完整 authority 判定），同 host 其他端口的
// 附件链接不算本实例；GHE 部署下公共 GitHub 附件形态仍然收（GHE 的 PR 引用
// 公共 GitHub 截图很常见，截图下载面本来就允许任意公网图）。
func TestScreenshotAttachmentURLsGHEAuthority(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.svc.cfg.GitHubApp.APIBaseURL = "https://ghe.example.com:8443/api/v3"
	review := e.withT3(nil, nil, nil)
	if got := review.githubWebHost(); got != "ghe.example.com:8443" {
		t.Fatalf("githubWebHost = %q, want ghe.example.com:8443（端口要保留）", got)
	}

	sameInstance := "https://ghe.example.com:8443/user-attachments/assets/abc"
	if refs := collectScreenshotURLsAt(sameInstance, review.githubWebHost()); len(refs) != 1 {
		t.Fatalf("同 authority 的附件应被收集，refs = %+v", refs)
	}
	otherPort := "https://ghe.example.com:4443/user-attachments/assets/abc"
	if refs := collectScreenshotURLsAt(otherPort, review.githubWebHost()); len(refs) != 0 {
		t.Fatalf("同 host 其他端口不是本实例的附件，不应收集，refs = %+v", refs)
	}

	// 公共 GitHub 附件形态（无扩展名）在 GHE 部署下仍按附件形态收。
	public := "https://github.com/user-attachments/assets/abc-123"
	if refs := collectScreenshotURLsAt(public, review.githubWebHost()); len(refs) != 1 {
		t.Fatalf("GHE 部署也应收公共 GitHub 附件直链，refs = %+v", refs)
	}
	// 点段/编码点段不能借附件前缀绕过。
	traversal := "https://ghe.example.com:8443/user-attachments/assets/../../other"
	if refs := collectScreenshotURLsAt(traversal, review.githubWebHost()); len(refs) != 0 {
		t.Fatalf("点段路径不应算附件形态，refs = %+v", refs)
	}
}
