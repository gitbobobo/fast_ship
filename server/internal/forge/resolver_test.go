package forge

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
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
	api := &recordingAPI{}
	source := &sourceStub{api: api}
	resolver := NewReviewClients(source, nil)
	ctx := context.Background()

	// 空串与内置 github-default 同走 GitHub 分支，安装 id 收编进适配器。
	for _, integrationID := range []string{"", model.ForgeIntegrationBuiltinGitHubID} {
		client, err := resolver.ClientFor(ctx, integrationID, "o/r", 42)
		if err != nil {
			t.Fatalf("integrationID=%q: %v", integrationID, err)
		}
		if client.Kind() != KindGitHub {
			t.Fatalf("integrationID=%q: Kind = %q", integrationID, client.Kind())
		}
		// 透传断言：调用经适配器落到同一访问面，安装 id 为构造值。
		if _, err := client.GetPullRequest(ctx, "o/r", 1); err != nil || api.last() != 42 {
			t.Fatalf("integrationID=%q: installationID = %d, want 42 (err=%v)", integrationID, api.last(), err)
		}
	}
}

// TestResolverForgejoBranch 非 GitHub 集成走 Forgejo 凭证来源：按集成 id 现取
// 三件套组装适配器，Kind/Bearer 来自适配器；来源错误原样透出且不碰 GitHub 侧。
func TestResolverForgejoBranch(t *testing.T) {
	gh := &sourceStub{api: &recordingAPI{}}
	fj := &forgejoSourceStub{http: &http.Client{}, apiBase: "https://forge.example.com/api/v1", token: "pat"}
	resolver := NewReviewClients(gh, fj)
	ctx := context.Background()

	client, err := resolver.ClientFor(ctx, "forgejo-abc", "o/r", 0)
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
	if _, err := resolver.ClientFor(ctx, "forgejo-xyz", "o/r", 0); !errors.Is(err, fjErr.err) {
		t.Fatalf("err = %v, want 来源错误透出", err)
	}

	// 来源未配置（部署没装配）：明确错误，不静默回落 GitHub。
	resolver = NewReviewClients(gh, nil)
	_, err = resolver.ClientFor(ctx, "forgejo-abc", "o/r", 0)
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

	c1, err := resolver.ClientFor(ctx, "forgejo-abc", "o/r", 0)
	if err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	c1b, err := resolver.ClientFor(ctx, "forgejo-abc", "o/r", 0)
	if err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	cache1 := c1.(*ForgejoReviewClient).loginCache
	if c1b.(*ForgejoReviewClient).loginCache != cache1 {
		t.Fatal("同集成同 PAT 的两次 ClientFor 应共享 login 缓存槽")
	}

	fj.token = "pat-b" // PAT 轮换
	c2, err := resolver.ClientFor(ctx, "forgejo-abc", "o/r", 0)
	if err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	if c2.(*ForgejoReviewClient).loginCache == cache1 {
		t.Fatal("PAT 轮换后应换新缓存槽，旧身份不得滞留")
	}
}

func TestResolverNilGitHubSource(t *testing.T) {
	resolver := NewReviewClients(nil, nil)
	if _, err := resolver.ClientFor(context.Background(), "", "o/r", 1); err == nil {
		t.Fatal("GitHub 访问面缺失应返回错误")
	}
}

func TestResolverPropagatesSourceError(t *testing.T) {
	want := errors.New("GitHub App 未配置")
	resolver := NewReviewClients(&sourceStub{err: want}, nil)
	if _, err := resolver.ClientFor(context.Background(), model.ForgeIntegrationBuiltinGitHubID, "o/r", 1); !errors.Is(err, want) {
		t.Fatalf("err = %v, want 源错误原样透出", err)
	}
}

func TestResolverClientSatisfiesBearerSource(t *testing.T) {
	// GitHub 路径行为不变：底层有凭证面时，解析出的客户端可取媒体下载
	// bearer；没有时 Bearer 返回空串（匿名下载）。
	bAPI := &withBearer{recordingAPI: &recordingAPI{}, token: "tok"}
	resolver := NewReviewClients(&sourceStub{api: bAPI}, nil)
	client, err := resolver.ClientFor(context.Background(), "", "o/r", 5)
	if err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	tok, err := client.(BearerSource).Bearer(context.Background())
	if err != nil || tok != "tok" {
		t.Fatalf("Bearer = (%q,%v), want (tok,nil)", tok, err)
	}

	plainResolver := NewReviewClients(&sourceStub{api: &recordingAPI{}}, nil)
	client, err = plainResolver.ClientFor(context.Background(), model.ForgeIntegrationBuiltinGitHubID, "o/r", 5)
	if err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	if tok, err = client.(BearerSource).Bearer(context.Background()); err != nil || tok != "" {
		t.Fatalf("无凭证面 Bearer = (%q,%v), want (\"\",nil)", tok, err)
	}
}
