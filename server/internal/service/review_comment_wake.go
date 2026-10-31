package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/forge"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 评论唤醒（INT-179）：PR 顶层评论（issue_comment 事件）正文包含
// @fast-ship-bot 时也开一轮审查——审查结论有误时实现者回帖纠正即可唤醒
// 审查者，不必推空提交。GitHub 与 Forgejo 的 issue_comment payload 同形，
// 与 pull_request 一样共用中性路径；触发语义等同 rerun 的 manual：同 head
// 也恒开新轮（action=comment），受理同 push 一样登记 seen（fast-ship-bot
// 第 38 轮 P2：push 触发面的时序判定以当前 head 的 seen 行为基准）、不走
// handleReviewTriggerTx。
// 编排沿用「事务内零网络调用」铁律，照 revalidateTiedWebhook 两阶段模式：
// 事务外只读预查 + 纯 payload 守卫 + 实拉 PR 实况，最终单事务幂等登记 +
// 复核后开轮。幂等账只随「已确定的受理/丢弃」落定——瞬时错误与事务失败
// 时库中无任何登记，平台重投可完整重走全流程。

// reviewCommentWakeMention 唤醒串的规格字面量（INT-179）：正文包含（大小
// 写不敏感）即视为点名。部署真实机器人句柄（App slug）经
// commentWakeMentions 追加。
const reviewCommentWakeMention = "@fast-ship-bot"

// commentWakeMentions 唤醒串集合：规格字面量 + 部署真实机器人句柄（App
// slug，manifest 流创建的 App slug 形如 fast-ship-<host>，与字面量不同——
// fast-ship-bot 第 20 轮 P2）。App 身份未配置（ok=false）回落字面量；读
// 取失败（瞬时 DB 故障）透出错误——评论可能用了 @<slug>，静默回落字面量
// 会让该唤醒被无声忽略且重投同样失败（codex 第 22 轮 P2：调用方按瞬时
// 失败返回 500 让平台重投）。
// mentionsSlugEntry mentionsSlugCache 的缓存项（App 句柄 + 过期时刻）。
type mentionsSlugEntry struct {
	slug    string
	expires time.Time
}

// reviewMentionsCacheTTL App 句柄的缓存时长：订阅 issue_comment 后所有
// 评论都进唤醒分支，逐条读取部署设置（DB 读 + AES 解密）是可被外部用户
// 按评论线性放大的固定成本（fast-ship-bot 第 44 轮 P2）——短缓存把成本
// 压到每 TTL 一次；slug 变更罕见，TTL 刷新足够。
const reviewMentionsCacheTTL = 5 * time.Minute

// commentWakeMentions 唤醒串集合：规格字面量 + 部署真实机器人句柄（App
// slug，manifest 流创建的 App slug 形如 fast-ship-<host>，与字面量不同——
// fast-ship-bot 第 20 轮 P2）。App 身份未配置（ok=false）回落字面量；读
// 取失败（瞬时 DB 故障）透出错误——评论可能用了 @<slug>，静默回落字面量
// 会让该唤醒被无声忽略且重投同样失败（codex 第 22 轮 P2：调用方按瞬时
// 失败返回 500 让平台重投）。读取结果短时缓存（见 reviewMentionsCacheTTL）。
// 返回值 cached：本次结果是否来自短缓存——缓存窗口内 slug 可能过期
// （部署切换/重建 App），调用方据此降级（重投闸门把 slug-only 评论按未
// 知重试而非终态，fast-ship-bot 第 46 轮 P2）。
func (s *ReviewService) commentWakeMentions(ctx context.Context) ([]string, bool, error) {
	mentions := []string{reviewCommentWakeMention}
	if s.appSource == nil {
		return mentions, false, nil
	}
	if v, ok := s.mentionsSlugCache.Load("slug"); ok {
		e := v.(mentionsSlugEntry)
		if time.Now().UTC().Before(e.expires) {
			if e.slug != "" {
				mentions = append(mentions, "@"+strings.ToLower(e.slug))
			}
			return mentions, true, nil
		}
	}
	return s.commentWakeMentionsFresh(ctx)
}

// commentWakeMentionsFresh 绕过缓存强制读取 App 身份并刷新缓存：缓存窗口
// 内 slug 可能已变更（部署切换/重建 App），唤醒判定未命中时用它复核，新
// 句柄的唤醒才不会被 202 静默吞掉（codex 第 47 轮 P2）。
func (s *ReviewService) commentWakeMentionsFresh(ctx context.Context) ([]string, bool, error) {
	mentions := []string{reviewCommentWakeMention}
	if s.appSource == nil {
		return mentions, false, nil
	}
	cfg, ok, err := s.appSource.Current(ctx)
	if err != nil {
		// 永久配置错误（如部署设置解密失败）重投只会得到同一结果——回落
		// 字面量集合，不让 App 身份读取失败卡死全部平台的评论唤醒（含
		// Forgejo 与普通讨论评论，fast-ship-bot 第 29 轮 P2）；其余（瞬时
		// DB 故障）维持透出 500 让平台重投。
		if errors.Is(err, errGitHubAppDecrypt) {
			return mentions, false, nil
		}
		return nil, false, err
	}
	// 缓存只存「实际生效」的句柄：ok=false（未配置）时 row.AppSlug 残值
	// 不得入缓存——否则缓存冷热两条路径的唤醒串集合漂移，强制刷新复核
	// 反而漏掉 slug 唤醒（fast-ship-bot 第 52 轮 Note）。读取成功都进缓存
	// （未配置部署不必每条评论重读）；瞬时故障不缓存。
	if !ok {
		cfg.AppSlug = ""
	}
	s.mentionsSlugCache.Store("slug", mentionsSlugEntry{
		slug:    cfg.AppSlug,
		expires: time.Now().UTC().Add(reviewMentionsCacheTTL),
	})
	if cfg.AppSlug == "" {
		return mentions, false, nil
	}
	return append(mentions, "@"+strings.ToLower(cfg.AppSlug)), false, nil
}

// reviewCommentWakeRevalidateMax 观察窗口复核的尝试上限：实拉前观察到的
// 总账 head 与事务内重取不一致时重读重拉；上限内不收敛（总账在同一窗口
// 内被持续并发推进）就放弃且不登记幂等账（后续投递/手动 Redeliver 可完
// 整重走，fast-ship-bot 第 52 轮 P2）。
const reviewCommentWakeRevalidateMax = 2

// commentWakeTxOutcome 评论唤醒事务结果的统一处置（纯函数，单元可测）：
// 观察窗口哨兵原样透传（外层重读重拉）；提交失败原样透出——此时幂等账
// 已随回滚撤销，平台重投整段重走，不得因 forkDenied 置位而被吞成 202
// （fast-ship-bot 第 32 轮 P2）；fork 否定仅在事务成功落账后写缓存。
func commentWakeTxOutcome(forkDenied bool, err error) (denyCache bool, retErr error) {
	if errors.Is(err, errCommentWakeObservedHeadMoved) {
		return false, err
	}
	if err != nil {
		return false, err
	}
	return forkDenied, nil
}

// errCommentWakeObservedHeadMoved 观察窗口哨兵：实拉前观察的总账 head 与
// 最终事务内重取的不一致——窗口内有并发写入推进了账本。哨兵让事务整体
// 回滚（幂等登记一并撤销，不烧账），由外层重读重拉后再试。
var errCommentWakeObservedHeadMoved = errors.New("评论唤醒观察窗口内总账被并发推进")

// commentWakePayload issue_comment 事件的最小字段面（其余字段原样丢弃）。
// GitHub 与 Forgejo 的 payload 同形：issue.pull_request 存在即 PR（普通
// Issue 的评论没有该字段）；Forgejo 无安装语义，installation 缺省为 0。
type commentWakePayload struct {
	Action string `json:"action"`
	Issue  struct {
		Number int `json:"number"`
		// PullRequest 存在即 PR：GitHub/Forgejo 都在 PR 的 issue payload 里
		// 带该对象，普通 Issue 评论缺省——指针判存在性即可，内容不消费。
		PullRequest *struct{} `json:"pull_request"`
		// User.Login PR 即 issue，issue.user 就是 PR 作者——payload 层即可
		// 与评论者登录名预比对，无资格者零实拉拒绝（codex 第 23 轮 P2）。
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"issue"`
	Comment struct {
		Body string `json:"body"`
		// CreatedAt 评论时刻：启用基线（INT-167）判定用——启用前的存量
		// 投递（对账重投/手动重放）不受理。
		CreatedAt *time.Time `json:"created_at"`
		// User.Type GitHub 把 App/机器人账号标为 "Bot"：机器自评论不唤醒
		// （见 commentWakePayloadGuards 的机器评论排除）。
		User struct {
			Type  string `json:"type"`
			Login string `json:"login"`
		} `json:"user"`
		// AuthorAssociation GitHub 评论者与仓库/Issue 的关系（OWNER/MEMBER/
		// COLLABORATOR/CONTRIBUTOR/AUTHOR/NONE）：GitHub 入口按它过滤无缘
		// 陌生人（见 commentWakePayloadGuards）。
		Association string `json:"author_association"`
	} `json:"comment"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// commentWakePayloadBaseGuards 不依赖唤醒串的纯 payload 守卫（不碰库、不
// 发网络）：判 action（只受理 created）、PR 归属（issue.pull_request 存在 +
// 仓库/编号可用）、机器评论标记。任一不过返回 nil——忽略判定完全由 payload
// 决定，不登记幂等账。与唤醒串读取（App 身份读取 = DB 读 + AES 解密）拆开：
// 前者先跑；App 身份读取（可能仍是每条 base 通过的投递一次）由短缓存压
// 到每 TTL 一次（见 commentWakeMentions），避免按评论线性放大（
// fast-ship-bot 第 40 轮 Note、第 44 轮 P2）。
// 评论者身份过滤在实拉 PR 后做（commentWakeAuthorized）：需要与实拉 PR
// 作者登录名比对，且 Forgejo payload 没有关系字段，无法纯 payload 判定。
func commentWakePayloadBaseGuards(body []byte) *commentWakePayload {
	var p commentWakePayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil
	}
	// 只受理新建评论；编辑/删除不唤醒。
	if p.Action != "created" {
		return nil
	}
	// 机器自评论不唤醒：审查结论与复审总结的正文由模型生成，可能引用
	// 用户评论里的 @fast-ship-bot——不排除会形成「发布结论 → 再唤醒」
	// 的无限审查循环（每轮 supersede 上一轮，持续烧额度）。两类识别：
	// 平台标记的 Bot 账号；机器评论自带的隐藏 HTML 标记。标记判定与
	// upsertPRComment 的 own 口径同源（TrimSpace 后前缀匹配）：机器结论
	// 的 marker 固定在正文开头，人类引用/摘录机器人评论片段（marker
	// 落在正文中间）不该被误杀（fast-ship-bot 第 4 轮 P2）。
	trimmedBody := strings.TrimSpace(p.Comment.Body)
	if p.Comment.User.Type == "Bot" ||
		strings.HasPrefix(trimmedBody, reviewCodeCommentMarker) ||
		strings.HasPrefix(trimmedBody, reviewVisualCommentMarker) ||
		strings.HasPrefix(trimmedBody, "<!-- codex-") {
		return nil
	}
	// 普通 Issue 评论（issue.pull_request 缺失）与缺仓库/编号的畸形投递忽略。
	if p.Issue.PullRequest == nil || p.Issue.Number <= 0 || p.Repository.FullName == "" {
		return nil
	}
	return &p
}

// commentWakeMentioned 正文点名判定（大小写不敏感包含，唤醒串 = 规格字面
// 量或部署机器人句柄）。
func commentWakeMentioned(p *commentWakePayload, mentions []string) bool {
	lower := strings.ToLower(p.Comment.Body)
	for _, m := range mentions {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// commentWakePayloadGuards 组合守卫（base + 唤醒串）：重投闸门与测试用。
func commentWakePayloadGuards(body []byte, mentions []string) *commentWakePayload {
	p := commentWakePayloadBaseGuards(body)
	if p == nil {
		return nil
	}
	if !commentWakeMentioned(p, mentions) {
		return nil
	}
	return p
}

// commentAssociationAllowed GitHub association 白名单（可用纯 payload
// 判定的部分）：仓库属主。COLLABORATOR 邀请可授 Read/Triage，与
// MEMBER/CONTRIBUTOR/NONE 一样不留 payload 层判定空间——是否 PR 作者
// 须实拉后按登录名比对（见 commentWakeAuthorized）。
func commentAssociationAllowed(association string) bool {
	return association == "OWNER"
}

// commentWakeAuthorized 评论者身份（实拉 PR 后判定，两平台同口径）：
// 仓库属主（OWNER，恒有管理员权限）或 PR 作者本人（评论者登录名与实拉
// PR 作者一致）。其余一律不收：association 枚举除 OWNER 外没有任何值能
// 编码当前写权限——COLLABORATOR 邀请可授 Read/Triage、MEMBER 只是组织
// 成员身份（组织仓库基础权限可设 Read）、CONTRIBUTOR 是「历史提交过」、
// 没有 AUTHOR 值（首评作者标记为 NONE）——逐个信任它们的补丁已被逐轮
// 反例击穿（codex 第 9/10/12/14/15 轮 P2），根治口径：与 PR 强绑定的
// 作者和属主可点名，其余（无 push 权限的陌生人、组织成员、历史贡献者、
// 只读外部协作者）一律忽略——公开仓库上他们原本触发不了任何审查（fork
// PR 被拒、开不了同仓分支），放行等于开放无限烧额度通道。需要更宽的
// 维护者点名能力须做实时权限校验 API，属产品决策另议。Forgejo payload
// 没有关系字段，只能走登录名比对：仅 PR 作者可点名。
func commentWakeAuthorized(p *commentWakePayload, pr *forge.PullRequest) bool {
	if commentAssociationAllowed(p.Comment.Association) {
		return true
	}
	return p.Comment.User.Login != "" && pr.AuthorLogin != "" &&
		strings.EqualFold(p.Comment.User.Login, pr.AuthorLogin)
}

// deterministicCommentWakeSetupErr 评论唤醒前置解析的确定性失败：访问面未
// 配置（两个哨兵：resolveGitHubInstallation 的 gh==nil 与 reviewClient 链
// 路的 githubapp.ErrAppNotConfigured——webhook-secret-only 部署的永久配置
// 态，重投只会得到同一结果）、App 部署配置解密失败（密文损坏/加密密钥
// 轮换出错，重投同样不自愈）与平台 401/403/404 且非限流（仓库无安装、
// 凭证吊销/权限不足）。限流 403 与网络/5xx/设置读取失败属瞬时，透出 500
// 让平台重投。其余 AppError（部署设置读取失败等 ErrInternal）也是瞬时
// DB 故障——只有两个哨兵按确定性处理，不能一提子以 errs.AppError 判定
// （fast-ship-bot 第 1 轮 P2、codex 第 11 轮 P2）。
func deterministicCommentWakeSetupErr(err error) bool {
	if errors.Is(err, errReviewGitHubAccessUnconfigured) ||
		errors.Is(err, githubapp.ErrAppNotConfigured) ||
		errors.Is(err, errGitHubAppDecrypt) {
		return true
	}
	var statusErr *githubapi.StatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
			return !forge.IsRateLimit(err)
		}
		return false
	}
	return false
}

// reviewForkDenyTTL 跨仓 fork 否定判定的缓存时长：判定来自实拉快照，
// PR 不会从 fork 变同仓（fork 关系不可逆），TTL 只是内存占用与改名窗口
// 的平衡——改名后 base 仓库名变化会走出判定范围（key 含 repo 名）。
const reviewForkDenyTTL = 10 * time.Minute

// reviewForkDenyMax 缓存条目上限：超出先清过期，仍超即整体清空（攻击者
// 用海量 fork PR 填缓存的边际收益为零，正确性不受影响）。
const reviewForkDenyMax = 10000

// commentWakeForkDeny 否定判定缓存：fork PR 作者本人点名会通过 payload
// 预过滤（评论者=PR 作者），每条评论都要花一次 installation 解析 + 一次
// 实拉才在事务内被 fork 守卫拒绝——公开仓库上可无限放大 App API 成本
// （fast-ship-bot 第 26 轮 P2）。命中过 fork 判定的 (repo, PR) 短时缓存，
// 重复点名边际成本降为零。
func (s *ReviewService) commentWakeForkDeny(integrationID, repo string, prNumber int, now time.Time) bool {
	key := fmt.Sprintf("%s/%s#%d", integrationID, repo, prNumber)
	v, ok := s.forkDeny.Load(key)
	if !ok {
		return false
	}
	return now.Before(v.(time.Time))
}

func (s *ReviewService) rememberCommentWakeForkDeny(integrationID, repo string, prNumber int, now time.Time) {
	key := fmt.Sprintf("%s/%s#%d", integrationID, repo, prNumber)
	// 上限保护：先清过期；仍超上限就整体清空（正确性不受影响，攻击者
	// 用海量 fork PR 填缓存没有边际收益）。
	if s.forkDenySize() >= reviewForkDenyMax {
		s.forkDeny.Range(func(k, v any) bool {
			if !now.Before(v.(time.Time)) {
				s.forkDeny.Delete(k)
			}
			return true
		})
		if s.forkDenySize() >= reviewForkDenyMax {
			s.forkDeny.Range(func(k, v any) bool {
				s.forkDeny.Delete(k)
				return true
			})
		}
	}
	s.forkDeny.Store(key, now.Add(reviewForkDenyTTL))
}

func (s *ReviewService) forkDenySize() int {
	n := 0
	s.forkDeny.Range(func(k, v any) bool {
		n++
		return true
	})
	return n
}

// observeAcceptedHead 评论唤醒实拉前的只读观察值：与 currentAcceptedHeadTx
// 同源（未开火的防抖行优先，否则总账 head_sha），项目反查同样按设置行
// (集成, 仓库) 快照。返回观察所用项目的 ID——实拉后的滞后时序判定必须用
// 同一项目的 seen 行作基准：观察与判定之间若发生改绑，两次独立反查可能
// 得到不同项目，基准错位会让滞后快照绕过判定、把新项目账本回退（codex
// 第 23 轮 P2）。找不到启用项目时返回空串——观察值不参与判定，最终事务
// 的反查会照常拒开轮。实拉窗口内账本可能被并发推进：事务内以「重取值 ==
// 观察值」复核，不一致即哨兵重试（见 openCommentWakeRoundTx）。
func (s *ReviewService) observeAcceptedHead(ctx context.Context, integrationID, repo string, prNumber int) (string, string, error) {
	db := s.db.WithContext(ctx)
	project, _, err := s.findEnabledReviewProjectTx(db, integrationID, repo)
	if err != nil || project == nil {
		return "", "", err
	}
	head, err := s.currentAcceptedHeadTx(db, integrationID, project.ID, repo, prNumber)
	return head, project.ID, err
}

// handleCommentWake 评论唤醒的两阶段编排（照 revalidateTiedWebhook 模式）：
//
//	阶段一（事务外）：幂等预查 + 纯 payload 守卫 + installation 兜底解析 +
//	只读观察总账 head + 实拉 PR 实况（实拉独享超时 ctx）。「该不该唤醒」
//	以实时 State/head 为准，不信事件体（重投/迟到的 payload 是旧快照）；
//	构造客户端/实拉/安装解析的确定性失败重投结果相同，判丢弃并补登幂等
//	账；瞬时错误返回 error 让 handler 回 500 由平台重投——此时库里没有
//	任何登记，重投会完整重走全流程（这正是幂等账不能在此前单独提交的原因）。
//	阶段二（单事务）：幂等登记 → 反查启用项目 → 复核设置行 → 观察窗口复核
//	（重取总账 head，与观察值不一致即哨兵回滚、外层重读重拉，上限
//	reviewCommentWakeRevalidateMax 次不收敛放弃且**不登记**——后续投递/
//	手动重投可完整重走）→ fork/实况关卡 → 取消
//	在途、删防抖行、按 action=comment 恒开新轮。登记与开轮同一事务：任何
//	一步失败整体回滚（幂等账随之撤销），平台重投可重走；复核不过是已确定
//	的丢弃，事务提交后幂等账落定，重投按 202 去重。
//
//	head 采信语义：账本可能因丢投递落后于平台——观察窗口内总账未被并发推进
//	时，即便实拉 head 与总账不同也采信实拉 head 开轮（openRoundTx 把总账
//	推进到 live head），不再按「账本不同」把唤醒永久锁死。
//
// 返回值 changed：本次投递实际改变了审查状态（开出新轮）——忽略类投递
// （未点名/非 PR/无资格/判丢弃/同 head 在途跳过等事务内静默路径）返回
// false，调用方据此跳过调度循环 kick，讨论密集仓库的普通评论不再放大成
// 额外 tick（codex 第 41 轮 P2、第 42 轮 P3）。
func (s *ReviewService) handleCommentWake(ctx context.Context, integrationID, event, deliveryID string, body []byte) (bool, error) {
	// 幂等预查（只读）：已登记的投递直接按重复去重，省一次实拉；预查与
	// 最终事务之间的并发窗口由事务内 insert 判重兜底。存量裸键兼容与
	// runWebhookDeliveryTx 同口径：集成前缀账键引入前的行是裸 GUID，只可
	// 能属于 github-default——升级前就订阅过 issue_comment 的安装，重投
	// 旧投递时仅查前缀键会漏掉裸键行而重复受理（codex 第 8 轮 P2）。
	key := deliveryLedgerKey(integrationID, deliveryID)
	countQuery := s.db.WithContext(ctx).Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id = ?", key)
	if integrationID == model.ForgeIntegrationBuiltinGitHubID {
		countQuery = countQuery.Or("delivery_id = ?", deliveryID)
	}
	var registered int64
	if err := countQuery.Count(&registered).Error; err != nil {
		return false, err
	}
	if registered > 0 {
		return false, ErrDeliveryDuplicated
	}

	// 纯 payload 守卫（base 先行）：非 created/非 PR/机器评论/畸形投递
	// 零成本忽略；唤醒串集合带短缓存（见 commentWakeMentions）。
	payload := commentWakePayloadBaseGuards(body)
	if payload == nil {
		return false, nil
	}
	// payload 级作者预比对（零成本）：有登录名的陌生人（非作者且非 OWNER）
	// 在任何 App 身份读取（含缓存未命中的强制刷新）之前拒绝——公开仓库的
	// 任意 @ 评论不得放大成一次设置读取（codex 第 51 轮 P2）。
	if !commentAssociationAllowed(payload.Comment.Association) &&
		payload.Comment.User.Login != "" && payload.Issue.User.Login != "" &&
		!strings.EqualFold(payload.Comment.User.Login, payload.Issue.User.Login) {
		return false, nil
	}
	// 退路：payload 缺登录名或登录名与 payload 作者一致（可能是伪造，也可能
	// 是真作者）——维持实拉后按实拉 PR 作者比对的最终判定。
	if integrationID == model.ForgeIntegrationBuiltinGitHubID &&
		!commentAssociationAllowed(payload.Comment.Association) &&
		payload.Comment.User.Login == "" {
		return false, nil
	}
	mentions, cached, err := s.commentWakeMentions(ctx)
	if err != nil {
		// 瞬时故障不落账：返回 500 让平台重投（重投时读取恢复即正常处理）。
		return false, err
	}
	mentioned := commentWakeMentioned(payload, mentions)
	if !mentioned && cached && strings.Contains(strings.ToLower(payload.Comment.Body), "@") {
		// 缓存窗口内 slug 可能已变更（部署切换/重建 App）：未命中且正文含
		// @ 时强制刷新一次再判——新句柄的唤醒不得被 202 静默吞掉（codex
		// 第 47 轮 P2）。仅对含 @ 的未命中投递触发，普通讨论评论不受影响。
		mentions, _, err = s.commentWakeMentionsFresh(ctx)
		if err != nil {
			return false, err
		}
		mentioned = commentWakeMentioned(payload, mentions)
	}
	if !mentioned {
		return false, nil
	}

	repo := strings.ToLower(payload.Repository.FullName)
	prNumber := payload.Issue.Number
	// 启用项目预查（只读，模式同 observeAcceptedHead）：未绑定/未启用的
	// 仓库零副作用返回——不写幂等账、不做任何平台 API 调用。否则任何人的
	// 点名评论（哪怕仓库根本没开启审查）都会放大成 installation 解析与
	// 实拉两次 App 级 API 调用（fast-ship-bot 第 16 轮 P2）。最终事务内
	// 会用同一判定复核，防预查与开轮之间停用/改绑。
	preProject, _, err := s.findEnabledReviewProjectTx(s.db.WithContext(ctx), integrationID, repo)
	if err != nil || preProject == nil {
		return false, err
	}
	// 显然无资格的点名不必实拉——零 API 成本拒绝（codex 第 17/23 轮 P2）：
	// fork 否定缓存命中：零 API 成本忽略（该 PR 已知是跨仓 fork）。
	// 缓存键含集成身份：不同集成可有同名仓库 + 同号 PR（完全不同的仓库），
	// 一处的 fork 否定不得错杀另一集成（codex 第 27 轮 P2）。
	if s.commentWakeForkDeny(integrationID, repo, prNumber, time.Now().UTC()) {
		return false, nil
	}
	installationID := payload.Installation.ID
	// GitHub 投递缺 installation 快照（代理剥字段等）时按仓库解析兜底；
	// Forgejo 恒 0（无安装语义，resolver 按集成凭证取 PR）。解析失败按
	// 确定性归类：访问面未配置/App 无该仓库安装（404）/凭证被拒（401、
	// 非限流 403）重投只会得到同一结果——判丢弃并补登幂等账；瞬时错误
	// （限流/网络）返回 error 让 handler 回 500。
	if integrationID == model.ForgeIntegrationBuiltinGitHubID && installationID == 0 {
		resolved, err := s.resolveGitHubInstallation(ctx, repo)
		if err != nil {
			if deterministicCommentWakeSetupErr(err) {
				s.logger.Warn("评论唤醒安装解析确定性失败，丢弃投递",
					zap.String("integration", integrationID),
					zap.String("delivery", deliveryID), zap.Error(err))
				s.recordDroppedDelivery(ctx, integrationID, deliveryID, event)
				return false, nil
			}
			s.logger.Warn("评论唤醒安装解析失败",
				zap.String("integration", integrationID),
				zap.String("delivery", deliveryID), zap.Error(err))
			return false, err
		}
		installationID = resolved
	}

	// 阶段一（事务外）：构造审查客户端。实拉独享超时预算（照
	// consumeDebounce：从 Background 派生并显式取消），开轮事务与补登账用
	// 请求 ctx——实拉的倒计时不再渗入后续库操作。
	pullCtx, pullCancel := context.WithTimeout(context.Background(), reviewForgejoPollTimeout)
	defer pullCancel()
	client, err := s.reviewClient(pullCtx, integrationID, repo, installationID)
	if err != nil {
		// 集成行已删/密文损坏/部署未配置 App 身份（ErrAppNotConfigured）等
		// 确定性失败重投只会得到同一错误——判丢弃并补登幂等账，不让平台
		// 无限重投；瞬时错误才透出 500。
		if deterministicReviewAccessErr(err) || errors.Is(err, githubapp.ErrAppNotConfigured) {
			s.logger.Warn("评论唤醒构造客户端确定性失败，丢弃投递",
				zap.String("integration", integrationID),
				zap.String("delivery", deliveryID), zap.Error(err))
			s.recordDroppedDelivery(ctx, integrationID, deliveryID, event)
			return false, nil
		}
		s.logger.Warn("评论唤醒构造客户端失败",
			zap.String("integration", integrationID),
			zap.String("delivery", deliveryID), zap.Error(err))
		return false, err
	}

	// 观察 → 实拉 → 事务复核：窗口内总账被并发推进就重读重拉，上限
	// reviewCommentWakeRevalidateMax 次仍不收敛则放弃且**不登记**幂等账
	// （窗口内总有第三方在持续受理，此刻的实拉快照一进事务就过期，再试
	// 只是空转；不登记则再次评论或手动 Redeliver 可完整重走，
	// fast-ship-bot 第 52 轮 P2）。
	for attempt := 0; attempt < reviewCommentWakeRevalidateMax; attempt++ {
		observedHead, observedProjectID, err := s.observeAcceptedHead(ctx, integrationID, repo, prNumber)
		if err != nil {
			return false, err
		}
		pr, err := client.GetPullRequest(pullCtx, repo, prNumber)
		if err != nil {
			// PR 已删/凭证吊销——确定性失败重投结果相同：判丢弃并补登幂等
			// 账；瞬时错误才透出 500 让平台重投。
			if deterministicReviewAccessErr(err) {
				s.logger.Warn("评论唤醒实拉确定性失败，丢弃投递",
					zap.String("integration", integrationID),
					zap.String("delivery", deliveryID), zap.Error(err))
				s.recordDroppedDelivery(ctx, integrationID, deliveryID, event)
				return false, nil
			}
			s.logger.Warn("评论唤醒实拉失败",
				zap.String("integration", integrationID),
				zap.String("delivery", deliveryID), zap.Error(err))
			return false, err
		}
		// 评论者身份：仓库当前授权人员或 PR 作者本人（与实拉 PR 作者登录
		// 名比对）方可点名；其余静默忽略（不登记，重投再忽略零副作用）。
		if !commentWakeAuthorized(payload, pr) {
			return false, nil
		}
		// 实拉快照时序判定（观察基准 observedHead 的 seen 行存有
		// pr_updated_at 时可比序）：live.UpdatedAt 更早说明实拉读到的是滞后
		// 快照（单 PR 读偶发滞后、自托管实例前置缓存/主从）——采信会把总
		// 账回退到旧 head，新 head 的投递反因「非当前 head」被永久吞掉。
		// 滞后按瞬时失败处理：不落任何账，返回 error 让 handler 回 500——
		// GitHub 侧投递未送达会进对账循环重投，拿到新鲜快照后自然恢复
		// （重投上限 3 次仍滞后则发故障通知）；与触发面的过期判定同口径：
		// 任一侧缺时间戳或无 seen 行（comment 受理前从未受理过的 head）维持采信
		// （fast-ship-bot 第 21 轮 P2：滞后不得烧幂等账静默丢唤醒）。
		var accepted model.ReviewSeenSHA
		snErr := s.db.WithContext(ctx).Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ? AND integration_id = ?",
			observedProjectID, repo, prNumber, observedHead, integrationID).First(&accepted).Error
		// 基准查询的瞬时 DB 故障不得当作「无 seen 行」放行——那正是需要拦
		// 滞后快照的时刻；透出 500 让平台重投（codex 第 22 轮 P2）。
		if snErr != nil && !errors.Is(snErr, gorm.ErrRecordNotFound) {
			return false, snErr
		}
		baseline := (*time.Time)(nil)
		if snErr == nil {
			baseline = accepted.PRUpdatedAt
		}
		if baseline == nil {
			// seen 行未命中**或其快照时刻为空**（tied 重验等缺 updatedAt 的
			// 受理）时读总账行：受理该 head 时落库的实拉快照时刻就是基准——
			// 否则这些 head 永久无基准，滞后快照恒被采信、总账可回退
			// （fast-ship-bot 第 26 轮 P2、codex 第 50 轮 P2）。
			var ledger model.ReviewPullRequest
			if lErr := s.db.WithContext(ctx).Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ?",
				observedProjectID, repo, prNumber, integrationID).First(&ledger).Error; lErr == nil &&
				ledger.HeadSHA == observedHead {
				baseline = ledger.PRUpdatedAt
			} else if lErr != nil && !errors.Is(lErr, gorm.ErrRecordNotFound) {
				return false, lErr
			}
		}
		// 同刻异 head = 时序歧义（Forgejo 秒级精度下两次推进同一秒，滞后
		// 读可能拿到旧 head 且 updated_at 与基准相等——严格 Before 拦不住）：
		// 与触发面的同刻异 head 歧义同口径，按瞬时失败重投换新鲜快照
		// （codex 第 28 轮 P2）。
		// 滞后判定只防「回退到旧 head」：实拉 head == 已受理 head 时总账
		// 无从回退，同 head 早时间戳（缓存/只读副本滞后）照常采信开轮
		// （fast-ship-bot 第 33 轮 P2：误杀会推迟甚至丢掉合法唤醒）。
		stale := baseline != nil && pr.UpdatedAt != nil && pr.UpdatedAt.Before(*baseline) &&
			pr.HeadSHA != observedHead
		tied := baseline != nil && pr.UpdatedAt != nil &&
			pr.UpdatedAt.Equal(*baseline) && pr.HeadSHA != observedHead
		if stale {
			s.logger.Warn("评论唤醒实拉快照滞后于已受理 head，按瞬时失败重投",
				zap.String("integration", integrationID),
				zap.String("repo", repo), zap.Int("pr", prNumber),
				zap.String("live", pr.HeadSHA), zap.String("accepted", observedHead))
			return false, fmt.Errorf("评论唤醒实拉快照滞后于已受理 head")
		}
		if tied {
			// 同刻异 head 有两种可能：账本落后 + 同秒新 head（本功能要修的
			// 场景，应采信）；滞后读旧快照（应重投）。复核：再实拉一次（一
			// 致才算稳定），再查 head 是否曾受理过（seen 行）——曾受理 = 旧
			// 快照拒绝；未受理 = 同秒新 head 采信（fast-ship-bot 第 29 轮
			// P2：无条件重投会让同秒新 head 永久丢唤醒）。
			pr2, ferr := client.GetPullRequest(pullCtx, repo, prNumber)
			if ferr != nil {
				// 与首次实拉同口径：确定性失败（PR 已删/凭证吊销/仓库转走）
				// 重投只会得到同一结果——判丢弃并补登幂等账，不再按瞬时失
				// 败刷满重投上限后发误导通知（fast-ship-bot 第 40 轮 P2）。
				if deterministicReviewAccessErr(ferr) {
					s.logger.Warn("评论唤醒同刻复核实拉确定性失败，丢弃投递",
						zap.String("integration", integrationID),
						zap.String("delivery", deliveryID), zap.Error(ferr))
					s.recordDroppedDelivery(ctx, integrationID, deliveryID, event)
					return false, nil
				}
				return false, ferr
			}
			if pr2.HeadSHA != pr.HeadSHA {
				s.logger.Warn("评论唤醒同刻异 head 复核未收敛，按瞬时失败重投",
					zap.String("integration", integrationID),
					zap.String("repo", repo), zap.Int("pr", prNumber),
					zap.String("live", pr.HeadSHA), zap.String("rechecked", pr2.HeadSHA))
				return false, fmt.Errorf("评论唤醒同刻异 head 复核未收敛")
			}
			// 复核快照本身也要过时序判定：head 一致但 updated_at 早于基准
			// 说明两次读到的都是滞后快照，采信同样会回退总账（codex 第 31
			// 轮 P2）。缺时间戳维持采信口径（与首次实拉一致）。
			if pr2.UpdatedAt != nil && baseline != nil && pr2.UpdatedAt.Before(*baseline) {
				s.logger.Warn("评论唤醒同刻异 head 复核快照仍滞后，按瞬时失败重投",
					zap.String("integration", integrationID),
					zap.String("repo", repo), zap.Int("pr", prNumber),
					zap.String("live", pr.HeadSHA), zap.String("accepted", observedHead))
				return false, fmt.Errorf("评论唤醒实拉快照滞后于已受理 head（复核仍滞后）")
			}
			// 受理史判定按观察项目过滤（与基准查询同口径：改绑后旧项目的
			// seen 行不得错杀新项目的合法 head，codex 第 30 轮 P2）；除
			// 运行行兜底（codex 第 30 轮 P2）：seen 与运行行任一存在即
			// 曾受理，只查 seen 会漏掉历史受理。
			headAccepted := false
			var seenOld model.ReviewSeenSHA
			sErr := s.db.WithContext(ctx).Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ? AND integration_id = ?",
				observedProjectID, repo, prNumber, pr.HeadSHA, integrationID).First(&seenOld).Error
			switch {
			case sErr == nil:
				headAccepted = true
			case errors.Is(sErr, gorm.ErrRecordNotFound):
				var runCount int64
				if rErr := s.db.WithContext(ctx).Model(&model.ReviewRun{}).
					Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ? AND integration_id = ?",
						observedProjectID, repo, prNumber, pr.HeadSHA, integrationID).
					Count(&runCount).Error; rErr != nil {
					return false, rErr
				}
				headAccepted = runCount > 0
			default:
				return false, sErr
			}
			if headAccepted {
				s.logger.Warn("评论唤醒同刻异 head 为曾受理旧快照（滞后读），按瞬时失败重投",
					zap.String("integration", integrationID),
					zap.String("repo", repo), zap.Int("pr", prNumber),
					zap.String("live", pr.HeadSHA), zap.String("accepted", observedHead))
				return false, fmt.Errorf("评论唤醒实拉快照滞后于已受理 head（同刻旧 head）")
			}
			pr = pr2
		}
		opened, forkDenied, txErr := s.openCommentWakeRoundTx(ctx, integrationID, event, deliveryID, repo, prNumber,
			installationID, observedProjectID, observedHead, pr, payload.Comment.CreatedAt, time.Now().UTC())
		denyCache, err := commentWakeTxOutcome(forkDenied, txErr)
		if errors.Is(err, errCommentWakeObservedHeadMoved) {
			s.logger.Info("评论唤醒观察窗口内总账被并发推进，重读重拉",
				zap.String("integration", integrationID),
				zap.String("repo", repo), zap.Int("pr", prNumber),
				zap.Int("attempt", attempt+1))
			continue
		}
		if err != nil {
			return false, err
		}
		if denyCache {
			// 判丢弃已随事务落幂等账；否定缓存是事务外副作用（判定来自
			// 实拉快照，与事务成败无关）——后续同 (repo, PR) 点名零 API
			// 成本拒绝（fast-ship-bot 第 26 轮 P2）。
			s.rememberCommentWakeForkDeny(integrationID, repo, prNumber, time.Now().UTC())
			return false, nil
		}
		return opened, nil
	}
	// 放弃路径不登记幂等账：窗口内 PR 被连续推送（agent amend/force-push
	// 正是本功能的目标场景）时，静默丢弃 + 烧账会把合法唤醒永久吞掉且用
	// 户无感知；不登记则用户再次评论或平台侧手动 Redeliver 都能完整重走
	// 全流程（新鲜观察+实拉，大概率收敛），202 语义不受影响（fast-ship-bot
	// 第 52 轮 P2）。
	s.logger.Warn("评论唤醒观察窗口未收敛，放弃且不登记（后续投递可完整重走）",
		zap.String("integration", integrationID),
		zap.String("repo", repo), zap.Int("pr", prNumber),
		zap.String("delivery", deliveryID))
	return false, nil
}

// openCommentWakeRoundTx 阶段二：最终单事务，幂等登记与复核开轮同生共死。
// 守卫不过静默返回——事务提交后幂等账落定，平台重投按 202 去重；任何一步
// 出错整体回滚（幂等账随之撤销），瞬时失败由平台重投完整重走。观察窗口
// 复核不过时回 errCommentWakeObservedHeadMoved（事务回滚不烧账，由外层
// 重读重拉）。
// 返回值 forkDenied：事务内 fork 守卫判丢弃（幂等账已随事务落定）——外层
// 据此写否定缓存（INT-97：事务闭包内不得调非 tx 方法面，非事务副作用外置）。
// 返回值 opened：事务内实际开出新轮——静默跳过路径（设置行移除/无资格/
// 同 head 在途/草稿等）保持 false，调度 kick 不为空跳误报（codex 第 42 轮 P3）。
func (s *ReviewService) openCommentWakeRoundTx(ctx context.Context, integrationID, event, deliveryID, repo string,
	prNumber int, installationID int64, observedProjectID, observedHead string,
	pr *forge.PullRequest, commentAt *time.Time, now time.Time) (opened, forkDenied bool, err error) {
	forkDenied = false
	opened = false
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 幂等登记先行：预查后的并发重投在事务内判重兜底。
		inserted, err := s.deliveries.InsertTx(tx, &model.GitHubWebhookDelivery{
			DeliveryID: deliveryLedgerKey(integrationID, deliveryID),
			Event:      event,
			ReceivedAt: now,
		})
		if err != nil {
			return err
		}
		if !inserted {
			return ErrDeliveryDuplicated
		}
		// 库侧守卫：反查启用项目（与 pull_request 触发同款判定：按设置行
		// (集成, 仓库) 快照）。未命中静默忽略。
		project, settings, err := s.findEnabledReviewProjectTx(tx, integrationID, repo)
		if err != nil || project == nil {
			return err
		}
		// 项目身份复核：观察值（含时序基准）属于观察时绑定的项目——预查/
		// 观察与最终事务之间改绑时，即便两项目账本 head 字符串相同，基准
		// 也不可跨项目沿用；哨兵重读重拉，新绑定下重新观察+实拉（codex
		// 第 35 轮 P2）。
		if project.ID != observedProjectID {
			return errCommentWakeObservedHeadMoved
		}
		// 复核设置行（rerun 同款）：取 PR 与开轮之间可能发生停用/换仓库/
		// 改绑集成——设置行已删或停用、快照 (集成, 仓库) 不再匹配时不开轮。
		var fresh model.ProjectReviewSettings
		if err := tx.Select("repo_full_name", "integration_id", "enabled", "enabled_at").
			Where("project_id = ?", project.ID).First(&fresh).Error; err != nil {
			// 只有「设置行已删（审查被移除）」才是已确定的丢弃——静默忽略并
			// 随事务落幂等账；其余库错误必须透出让事务整体回滚（不烧幂等
			// 账），否则一次 DB 抖动就把投递记成已处理，平台重投被 202 永吞。
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			return nil
		}
		if !fresh.Enabled || !strings.EqualFold(fresh.RepoFullName, repo) ||
			fresh.IntegrationID != settings.IntegrationID {
			return nil
		}
		// 启用基线（INT-167）：评论时刻早于 enabled_at 基线 = 启用前的存量
		// 投递（24h 内对账重投/后台手动 Redeliver），按当前 live head 开轮
		// 会违背「启用前的存量状态不受理」——静默忽略并随事务落幂等账
		// （与 pull_request 触发面同口径，fast-ship-bot 第 52 轮 P2）。
		if fresh.EnabledAt != nil && commentAt != nil &&
			commentAt.Before(fresh.EnabledAt.Add(-forgejoPollBaselineGrace)) {
			return nil
		}
		// 观察窗口复核：实拉前观察到的总账 head 与事务内重取不一致——窗口
		// 内有并发写入推进了账本，此刻的实拉快照已经过期。哨兵让整体回滚
		// （幂等登记一并撤销），外层重读重拉后再试；上限内不收敛由调用方
		// 放弃且不登记幂等账（后续投递可完整重走）。一致时采信实拉 head：
		// 即便它与总账不同（丢
		// 投递造成的落后账本），也照常开轮并把总账推进到 live head。
		current, err := s.currentAcceptedHeadTx(tx, integrationID, project.ID, repo, prNumber)
		if err != nil {
			return err
		}
		if current != observedHead {
			return errCommentWakeObservedHeadMoved
		}
		// PR 实况关卡：跨仓 fork 不审（既有不变量，与 pull_request 触发面
		// 一致）——实拉 head 仓库缺失（已删 fork/异常 payload）或与 base
		// 仓库不同名即 fork，不开轮；closed/merged/draft 没有可审的实现，
		// 空 head 不开轮。
		if pr.HeadRepoFullName == "" || !strings.EqualFold(pr.HeadRepoFullName, pr.RepoFullName) ||
			// 实拉 base 与投递快照仓库不同名 = 已改名/转移：投递属于旧仓，
			// 新仓审查由迁移后的配置决定，按未绑定忽略（fast-ship-bot 第
			// 24 轮 P2：不得把旧仓投递开进新仓账本）。
			!strings.EqualFold(pr.RepoFullName, repo) {
			// 判丢弃随事务落幂等账；否定缓存由外层写（INT-97：闭包内不得
			// 调非 tx 方法面）。判定来自实拉快照（与事务成败无关的事实），
			// 后续同 (repo, PR) 点名零 API 成本拒绝——fork PR 作者本人点
			// 名的放大通道（fast-ship-bot 第 26 轮 P2）。
			forkDenied = true
			return nil
		}
		if !prReviewable(pr) || pr.Draft || pr.HeadSHA == "" {
			return nil
		}
		// 同 head 已有在途审查（queued/dispatched/running）：该 head 正在
		// 审，再点名是重复请求——不重开（否则两次点名互相作废在途轮，
		// 公开仓库可被连续点名饿死审查）。结论已出的轮不受影响：终态运行
		// 不计入在途，照常开新轮重审；在途运行停留在旧 head 上时也照常
		// supersede 重开。只计代码运行（kind=code）：视觉运行在途说明代码
		// 结论已发布——这正是「结论有误请重审」的主场景，点名必须开新轮
		// 并 supersede 在途视觉段（fast-ship-bot 第 7 轮 P2）。
		var active int64
		if err := tx.Model(&model.ReviewRun{}).
			Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ? AND head_sha = ? AND kind = ? AND status IN ?",
				project.ID, repo, prNumber, integrationID, pr.HeadSHA, model.ReviewKindCode,
				[]model.ReviewRunStatus{model.ReviewRunQueued, model.ReviewRunDispatched, model.ReviewRunRunning}).
			Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			s.logger.Info("同 head 已有在途审查，评论唤醒不重开",
				zap.String("integration", integrationID),
				zap.String("repo", repo), zap.Int("pr", prNumber),
				zap.String("head", pr.HeadSHA))
			return nil
		}
		// 恒开新轮：取消在途（含排队）、删防抖行（旧轮不得再开火），按
		// comment 开轮——comment 与 manual 一样同 head 也分配新轮次。
		if err := s.supersedePRRunsTx(tx, integrationID, project.ID, repo, prNumber, now); err != nil {
			return err
		}
		if err := tx.Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ?",
			project.ID, repo, prNumber, integrationID).Delete(&model.ReviewDebounce{}).Error; err != nil {
			return err
		}
		if _, err = s.openRoundTx(tx, project, settings, repo, prNumber,
			pr.HeadSHA, "comment", pr.Title, pr.HTMLURL, pr.BaseRef, pr.HeadRef, installationID, pr.UpdatedAt, now); err != nil {
			return err
		}
		// 受理登记 seen（与 push 受理同款 OnConflict DoNothing）：push 触发
		// 面的过期/同刻判定以当前 head 的 seen 行为时序基准——comment 受理
		// 不写 seen 会让基准缺失，旧 head 投递的重投复核可回滚账本（
		// fast-ship-bot 第 38 轮 P2；受理史语义与 push 对齐，force-push 回
		// 评论审过的 head 同样按已受理处理）。
		seen := &model.ReviewSeenSHA{
			ProjectID: project.ID, RepoFullName: repo, PRNumber: prNumber,
			HeadSHA: pr.HeadSHA, IntegrationID: integrationID,
			PRUpdatedAt: pr.UpdatedAt, AcceptedAt: now,
		}
		// 重新受理（force-push 回已见 head 的评论唤醒）要刷新 seen 时戳：
		// DoNothing 会留下旧值，滞后基准取旧值会放行中间时间戳的滞后读、
		// 回退总账（codex 第 42 轮 P2）。刷新仅在快照时刻非空且**更新**时
		// 生效——同 head 分支有意放行的早时间戳（缓存/只读副本滞后）不得
		// 把基准往回拨，nil 不得清成 NULL（fast-ship-bot 第 43 轮 P2）。
		opened = true
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "project_id"}, {Name: "repo_full_name"}, {Name: "pr_number"}, {Name: "head_sha"}, {Name: "integration_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"accepted_at": now,
				"pr_updated_at": gorm.Expr(
					"CASE WHEN excluded.pr_updated_at IS NOT NULL AND " +
						"(review_seen_shas.pr_updated_at IS NULL OR excluded.pr_updated_at > review_seen_shas.pr_updated_at) " +
						"THEN excluded.pr_updated_at ELSE review_seen_shas.pr_updated_at END"),
			}),
		}).Create(seen).Error
	})
	return opened, forkDenied, txErr
}
