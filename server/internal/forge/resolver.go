package forge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
)

// ReviewClientResolver 按集成身份解析审查链路的访问面。审查侧的
// integrationID 是归一快照（空串 = 内置 GitHub，其余为 forge_integrations.id）。
type ReviewClientResolver interface {
	// ClientFor 内置 GitHub 返回包好安装 id 的适配器——安装 id 由本侧按仓
	// 库现解析（INT-230：installationID 不再经调用方透传，webhook 快照只
	// 剩落库数据语义）；Forgejo 集成经凭证来源现取 SSRF 安全 client + PAT
	// 组装适配器（INT-167 任务 3）。
	ClientFor(ctx context.Context, integrationID, repoFullName string) (ReviewClient, error)
}

// GitHubApps 解析器取得部署级 GitHub 访问面的最小接口，与 service.PRGitHubClients
// 结构同形（两包互不引用，靠接口满足）。
type GitHubApps interface {
	ClientFor(ctx context.Context) (githubapi.GitHubClient, error)
}

// ForgejoSource 解析器取得 Forgejo 集成访问三件套的最小接口（service 侧实现，
// INT-167 任务 3）：按集成行现取 SSRF 安全 client、展开后的 API 基址与解密后
// 的机器人 PAT。集成不存在/kind 不符/解密失败都在实现里报明确错误。
type ForgejoSource interface {
	ForgejoClient(ctx context.Context, integrationID string) (httpClient *http.Client, apiBaseURL, token string, err error)
}

// ReviewClients ReviewClientResolver 的实现：内置 GitHub 分支 + Forgejo 分支。
// repoFullName 只进错误文案；Forgejo 适配器按集成凭证构造时也不按仓库分支。
type ReviewClients struct {
	gh      GitHubApps
	forgejo ForgejoSource
	// forgejoLogins 各凭证的 PAT 账号共享缓存槽
	// （key=integrationID+"\x00"+sha256(PAT)）：
	// Forgejo 适配器每次现取现建，写响应学到的 login 不跨实例保留的话，仓库级
	// PAT（/user 恒 403）下 Self 标记永不生效、自己的行内评论回流下一轮历史。
	// 键里带凭证摘要：轮换到不同账号自动换槽，旧身份连同旧凭证一并隔离——
	// 缓存命中即不再探测，按键分槽才不会有陈旧 login 滞留窗口；裸 PAT 不进
	// 常驻 map key（明文凭证不该常驻内存），同集成旧槽在新槽建立时逐出。
	forgejoLogins sync.Map
	// installations GitHub 安装解析短 TTL 缓存（key=repoFullName）：安装解析
	// 每次都要签 App JWT 打 /repos/{o}/{r}/installation，而安装关系极少变
	// 化——命中即不再打 JWT，防抖 tick/同步账本/开轮每轮一次的构造不再放大
	// App 级配额。确定性失败（仓库未装 App 等 401/403/404 非限流）按更短
	// 负 TTL 缓存：最常见滞留支路不再每轮打 JWT，装完负 TTL 内即生效。
	// TTL 内安装变更拿到旧 id：适配器经 reResolve 钩子作废条目重解析并重
	// 试一次（见 GitHubReviewClient），不自愈才按确定性失败归类。
	installations sync.Map
}

const (
	// installationCacheTTL 安装 id 成功结果的缓存时长。
	installationCacheTTL = 5 * time.Minute
	// installationNegCacheTTL 确定性失败（未安装/凭证被拒）的负缓存时长：
	// 比成功 TTL 短——装完 App 很快生效，又压住未安装支路的 JWT 放大。
	installationNegCacheTTL = time.Minute
)

// installationCacheEntry installations 的值：解析结果与过期时刻。err 非空
// 时是确定性失败的原始 cause（AppError 包装在取用处按当前语义重建）。
type installationCacheEntry struct {
	id  int64
	err error
	exp time.Time
}

// 编译期钉住实现满足 resolver 契约。
var _ ReviewClientResolver = (*ReviewClients)(nil)

// ErrGitHubAccessUnconfigured 部署没有 GitHub App 访问面的哨兵：确定性配
// 置态（重试只得到同一结果），调用方按确定性失败归类。
var ErrGitHubAccessUnconfigured = errors.New("GitHub 访问面未配置")

// NewReviewClients 用部署级 GitHub 访问面与 Forgejo 凭证来源构造 resolver；
// 两者均可为 nil（对应「访问面未配置」的既有口径，ClientFor 报明确错误）。
func NewReviewClients(gh GitHubApps, forgejo ForgejoSource) *ReviewClients {
	return &ReviewClients{gh: gh, forgejo: forgejo}
}

func (r *ReviewClients) ClientFor(ctx context.Context, integrationID, repoFullName string) (ReviewClient, error) {
	if integrationID != "" && integrationID != model.ForgeIntegrationBuiltinGitHubID {
		if r.forgejo == nil {
			return nil, fmt.Errorf("forge: 集成 %s（仓库 %s）的 Forgejo 凭证来源未配置", integrationID, repoFullName)
		}
		httpClient, apiBase, token, err := r.forgejo.ForgejoClient(ctx, integrationID)
		if err != nil {
			return nil, err
		}
		tokenDigest := sha256.Sum256([]byte(token))
		key := integrationID + "\x00" + hex.EncodeToString(tokenDigest[:])
		// 换槽逐出同集成旧 PAT 槽：每次解析都清扫而不只新建槽时——并发
		// ClientFor 在轮换窗口竞争时拿到旧槽的一方跳过逐出会让旧身份残留到
		// 下一次轮换。槽随 resolver 终身存活，集成数量级小，Range 前缀过滤
		// 代价可忽略。
		prefix := integrationID + "\x00"
		r.forgejoLogins.Range(func(k, _ any) bool {
			if ks, ok := k.(string); ok && ks != key && strings.HasPrefix(ks, prefix) {
				r.forgejoLogins.Delete(k)
			}
			return true
		})
		slot, _ := r.forgejoLogins.LoadOrStore(key, &ForgejoLoginCache{})
		return NewForgejoReviewClient(httpClient, apiBase, token).
			withLoginCache(slot.(*ForgejoLoginCache)), nil
	}
	if r.gh == nil {
		return nil, ErrGitHubAccessUnconfigured
	}
	api, err := r.gh.ClientFor(ctx)
	if err != nil {
		return nil, err
	}
	// 安装 id 按仓库现解析（INT-230）：调用方不再透传 webhook 快照。%v 文案
	// 进 AppError 消息（对外形态与 service 侧解析时代一致），cause 保留链条
	// 供确定性失败归类（401/403/404 且非限流）。成功结果走 installations
	// 短 TTL 缓存，命中不打 JWT（见字段注释）。
	installationID, err := r.resolveInstallation(ctx, api, repoFullName)
	if err != nil {
		return nil, err
	}
	client := NewGitHubReviewClient(api, installationID)
	// reResolve：调用命中陈旧安装（401/404、非限流 403）时作废本仓库缓存
	// 条目重解析——TTL 窗口内的 App 卸装重装/仓库转组不再把确定性失败误判
	// 成丢投递（INT-230 复审 P2）。
	client.reResolve = func(ctx context.Context) (int64, error) {
		r.installations.Delete(repoFullName)
		return r.resolveInstallation(ctx, api, repoFullName)
	}
	return client, nil
}

// resolveInstallation 按仓库解析 GitHub App 安装 id：成功结果按 repo 短
// TTL 缓存、确定性失败按更短负 TTL 缓存（INT-230 复审：无缓存时防抖/同
// 步/开轮与未安装支路的重试每轮一次 JWT 解析会放大 App 级配额）。瞬时
// 失败不缓存，错误照旧包装。
func (r *ReviewClients) resolveInstallation(ctx context.Context, api githubapi.GitHubClient, repoFullName string) (int64, error) {
	if v, ok := r.installations.Load(repoFullName); ok {
		if e, ok := v.(installationCacheEntry); ok && time.Now().Before(e.exp) {
			if e.err != nil {
				return 0, errs.Wrap(errs.ErrGitHubAPI.Code, fmt.Sprintf("解析仓库安装失败: %v", e.err), e.err)
			}
			return e.id, nil
		}
	}
	installationID, err := api.ResolveInstallation(ctx, repoFullName)
	if err != nil {
		if !IsRateLimit(err) && (IsUnauthorized(err) || IsForbidden(err) || IsNotFound(err)) {
			r.installations.Store(repoFullName, installationCacheEntry{err: err, exp: time.Now().Add(installationNegCacheTTL)})
		}
		return 0, errs.Wrap(errs.ErrGitHubAPI.Code, fmt.Sprintf("解析仓库安装失败: %v", err), err)
	}
	r.installations.Store(repoFullName, installationCacheEntry{id: installationID, exp: time.Now().Add(installationCacheTTL)})
	return installationID, nil
}

// HookDeliveryAccess 投递对账的 GitHub App 访问面（INT-230 重投边界）：
// /app/hook/* 端点只认 App JWT、不按 installation 区分，不进 ReviewClient
// 方法面；实况闸门的 PR 实拉复用安装令牌客户端（安装 id 取自投递 payload）。
// 重投是 GitHub 专属能力，收在适配器侧，调用方不再持有第二套 GitHub 访问
// 字段。返回 hooks 为 nil（err 为 nil）表示实现没有投递记录面（静态令牌等），
// 对账整轮静默跳过。
func (r *ReviewClients) HookDeliveryAccess(ctx context.Context) (githubapp.HookDeliveries, githubapi.GitHubClient, error) {
	if r.gh == nil {
		return nil, nil, ErrGitHubAccessUnconfigured
	}
	api, err := r.gh.ClientFor(ctx)
	if err != nil {
		return nil, nil, err
	}
	hooks, ok := api.(githubapp.HookDeliveries)
	if !ok {
		return nil, nil, nil
	}
	return hooks, api, nil
}
