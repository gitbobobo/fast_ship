package forge

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
)

// sourceStub 恒返回同一 githubapi 访问面的解析器桩，并记录调用次数（断言
// 非 GitHub 集成不得回落到 GitHub 侧）。
type sourceStub struct {
	calls int
	api   githubapi.GitHubClient
	err   error
}

func (s *sourceStub) ClientFor(context.Context) (githubapi.GitHubClient, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.api, nil
}

// forgejoSourceStub 恒返回同一组 Forgejo 访问三件套，并记录调用参数。
type forgejoSourceStub struct {
	calls   []string
	http    *http.Client
	apiBase string
	token   string
	err     error
}

func (s *forgejoSourceStub) ForgejoClient(_ context.Context, integrationID string) (*http.Client, string, string, error) {
	s.calls = append(s.calls, integrationID)
	if s.err != nil {
		return nil, "", "", s.err
	}
	return s.http, s.apiBase, s.token, nil
}

func TestResolverBuiltinGitHub(t *testing.T) {
	api := &recordingAPI{resolveInst: 42}
	source := &sourceStub{api: api}
	resolver := NewReviewClients(source, nil)
	ctx := context.Background()

	// 空串与内置 github-default 同走 GitHub 分支，安装 id 由 resolver 按仓库
	// 现解析（INT-230）后收编进适配器。
	for _, integrationID := range []string{"", model.ForgeIntegrationBuiltinGitHubID} {
		client, err := resolver.ClientFor(ctx, integrationID, "o/r")
		if err != nil {
			t.Fatalf("integrationID=%q: %v", integrationID, err)
		}
		if client.Kind() != KindGitHub {
			t.Fatalf("integrationID=%q: Kind = %q", integrationID, client.Kind())
		}
		if api.resolveRepo != "o/r" {
			t.Fatalf("integrationID=%q: ResolveInstallation repo = %q, want o/r", integrationID, api.resolveRepo)
		}
		// 透传断言：调用经适配器落到同一访问面，安装 id 为解析值。
		if _, err := client.GetPullRequest(ctx, "o/r", 1); err != nil || api.last() != 42 {
			t.Fatalf("integrationID=%q: installationID = %d, want 42 (err=%v)", integrationID, api.last(), err)
		}
	}
}

// TestResolverGitHubResolveFailure 安装解析失败包装成 ErrGitHubAPI AppError：
// 消息带原因文本、错误链保留底层错误（确定性失败归类依赖 %w 链）。
func TestResolverGitHubResolveFailure(t *testing.T) {
	cause := &githubapi.StatusError{StatusCode: 404, Method: "GET", URL: "/repos/o/r/installation"}
	resolver := NewReviewClients(&sourceStub{api: &recordingAPI{resolveErr: cause}}, nil)
	_, err := resolver.ClientFor(context.Background(), model.ForgeIntegrationBuiltinGitHubID, "o/r")
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || appErr.Code != errs.ErrGitHubAPI.Code {
		t.Fatalf("err = %v, want ErrGitHubAPI AppError", err)
	}
	if !strings.Contains(appErr.Message, "解析仓库安装失败") {
		t.Fatalf("message = %q, want 前缀 解析仓库安装失败", appErr.Message)
	}
	if !errors.Is(err, cause) {
		t.Fatal("底层 StatusError 不得脱离错误链")
	}
}

// TestResolverHookDeliveryAccess 投递对账的 GitHub 专属访问面：实现有投递
// 记录面时透出（连同安装令牌客户端）；没有时 nil,nil,nil；访问面缺失报哨兵。
func TestResolverHookDeliveryAccess(t *testing.T) {
	ctx := context.Background()

	// 实现没有投递记录面：静默无能力。
	hooks, api, err := NewReviewClients(&sourceStub{api: &recordingAPI{}}, nil).HookDeliveryAccess(ctx)
	if err != nil || hooks != nil || api != nil {
		t.Fatalf("无投递记录面 = (%v,%v,%v), want (nil,nil,nil)", hooks, api, err)
	}

	// 实现有投递记录面：透出 hooks 与同一客户端。
	withHooks := &hookAPIStub{recordingAPI: &recordingAPI{resolveInst: 7}}
	resolver := NewReviewClients(&sourceStub{api: withHooks}, nil)
	hooks, api, err = resolver.HookDeliveryAccess(ctx)
	if err != nil || hooks == nil || api == nil {
		t.Fatalf("有投递记录面 = (%v,%v,%v), want 非空 hooks/api", hooks, api, err)
	}
	if _, ok := interface{}(api).(githubapp.HookDeliveries); !ok {
		t.Fatal("api 应为 githubapp.HookDeliveries 实现")
	}

	// GitHub 访问面未配置：哨兵错误。
	_, _, err = NewReviewClients(nil, nil).HookDeliveryAccess(ctx)
	if !errors.Is(err, ErrGitHubAccessUnconfigured) {
		t.Fatalf("err = %v, want ErrGitHubAccessUnconfigured", err)
	}
}

// hookAPIStub recordingAPI + 投递记录面，模拟 githubapp.Client 形态。
type hookAPIStub struct {
	*recordingAPI
}

func (h *hookAPIStub) ListHookDeliveries(context.Context, time.Time) ([]githubapp.HookDelivery, error) {
	return nil, nil
}

func (h *hookAPIStub) GetHookDelivery(_ context.Context, _ int64) (githubapp.HookDeliveryDetail, error) {
	return githubapp.HookDeliveryDetail{}, nil
}

func (h *hookAPIStub) RedeliverHookDelivery(context.Context, int64) error { return nil }

// TestResolverForgejoBranch 非 GitHub 集成走 Forgejo 凭证来源：按集成 id 现取
// 三件套组装适配器，Kind/Bearer 来自适配器；来源错误原样透出且不碰 GitHub 侧。
func TestResolverForgejoBranch(t *testing.T) {
	gh := &sourceStub{api: &recordingAPI{}}
	fj := &forgejoSourceStub{http: &http.Client{}, apiBase: "https://forge.example.com/api/v1", token: "pat"}
	resolver := NewReviewClients(gh, fj)
	ctx := context.Background()

	client, err := resolver.ClientFor(ctx, "forgejo-abc", "o/r")
	if err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	if client.Kind() != KindForgejo {
		t.Fatalf("Kind = %q, want forgejo", client.Kind())
	}
	if len(fj.calls) != 1 || fj.calls[0] != "forgejo-abc" {
		t.Fatalf("来源被调 = %v, want [forgejo-abc]", fj.calls)
	}
	tok, err := client.(BearerSource).Bearer(ctx)
	if err != nil || tok != "pat" {
		t.Fatalf("Bearer = (%q,%v), want (pat,nil)", tok, err)
	}
	if gh.calls != 0 {
		t.Fatal("Forgejo 分支不得回落 GitHub 侧")
	}

	// 来源错误透出，GitHub 侧依旧零调用。
	fjErr := &forgejoSourceStub{err: errors.New("集成不存在")}
	resolver = NewReviewClients(gh, fjErr)
	if _, err := resolver.ClientFor(ctx, "forgejo-xyz", "o/r"); !errors.Is(err, fjErr.err) {
		t.Fatalf("err = %v, want 来源错误透出", err)
	}

	// 来源未配置（部署没装配）：明确错误，不静默回落 GitHub。
	resolver = NewReviewClients(gh, nil)
	_, err = resolver.ClientFor(ctx, "forgejo-abc", "o/r")
	if err == nil || !strings.Contains(err.Error(), "凭证来源未配置") {
		t.Fatalf("err = %v, want 凭证来源未配置", err)
	}
	if gh.calls != 0 {
		t.Fatal("来源缺失的 Forgejo 请求不得回落 GitHub 侧")
	}
}

// TestResolverForgejoLoginCacheKeyByPAT 身份共享缓存按 (integrationID, PAT)
// 分槽：同 PAT 的两次 ClientFor 共槽（学到的 login 跨实例存活）；PAT 轮换到
// 不同凭证自动换槽——缓存命中即不再探测 /user，旧账号身份不滞留（fsb 第 6
// 轮 Note）。
func TestResolverForgejoLoginCacheKeyByPAT(t *testing.T) {
	fj := &forgejoSourceStub{http: &http.Client{}, apiBase: "https://forge.example.com/api/v1", token: "pat-a"}
	resolver := NewReviewClients(nil, fj)
	ctx := context.Background()

	c1, err := resolver.ClientFor(ctx, "forgejo-abc", "o/r")
	if err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	c1b, err := resolver.ClientFor(ctx, "forgejo-abc", "o/r")
	if err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	cache1 := c1.(*ForgejoReviewClient).loginCache
	if c1b.(*ForgejoReviewClient).loginCache != cache1 {
		t.Fatal("同集成同 PAT 的两次 ClientFor 应共享 login 缓存槽")
	}

	fj.token = "pat-b" // PAT 轮换
	c2, err := resolver.ClientFor(ctx, "forgejo-abc", "o/r")
	if err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	if c2.(*ForgejoReviewClient).loginCache == cache1 {
		t.Fatal("PAT 轮换后应换新缓存槽，旧身份不得滞留")
	}
	// 换槽后同集成旧 PAT 槽被逐出，不随 resolver 终身滞留。
	n := 0
	resolver.forgejoLogins.Range(func(_, _ any) bool { n++; return true })
	if n != 1 {
		t.Fatalf("同集成应只剩当前 PAT 槽, slots = %d", n)
	}
}

func TestResolverNilGitHubSource(t *testing.T) {
	resolver := NewReviewClients(nil, nil)
	_, err := resolver.ClientFor(context.Background(), "", "o/r")
	if !errors.Is(err, ErrGitHubAccessUnconfigured) {
		t.Fatalf("err = %v, want ErrGitHubAccessUnconfigured", err)
	}
}

func TestResolverPropagatesSourceError(t *testing.T) {
	want := errors.New("GitHub App 未配置")
	resolver := NewReviewClients(&sourceStub{err: want}, nil)
	if _, err := resolver.ClientFor(context.Background(), model.ForgeIntegrationBuiltinGitHubID, "o/r"); !errors.Is(err, want) {
		t.Fatalf("err = %v, want 源错误原样透出", err)
	}
}

func TestResolverClientSatisfiesBearerSource(t *testing.T) {
	// GitHub 路径行为不变：底层有凭证面时，解析出的客户端可取媒体下载
	// bearer；没有时 Bearer 返回空串（匿名下载）。
	bAPI := &withBearer{recordingAPI: &recordingAPI{resolveInst: 5}, token: "tok"}
	resolver := NewReviewClients(&sourceStub{api: bAPI}, nil)
	client, err := resolver.ClientFor(context.Background(), "", "o/r")
	if err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	tok, err := client.(BearerSource).Bearer(context.Background())
	if err != nil || tok != "tok" {
		t.Fatalf("Bearer = (%q,%v), want (tok,nil)", tok, err)
	}
	if scope := client.(BearerSource).BearerScope(); scope != "install:5" {
		t.Fatalf("BearerScope = %q, want install:5", scope)
	}

	plainResolver := NewReviewClients(&sourceStub{api: &recordingAPI{}}, nil)
	client, err = plainResolver.ClientFor(context.Background(), model.ForgeIntegrationBuiltinGitHubID, "o/r")
	if err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	if tok, err = client.(BearerSource).Bearer(context.Background()); err != nil || tok != "" {
		t.Fatalf("无凭证面 Bearer = (%q,%v), want (\"\",nil)", tok, err)
	}
}
