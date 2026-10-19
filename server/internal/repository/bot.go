package repository

import (
	"errors"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// ErrNotFound 统一映射 gorm.ErrRecordNotFound，便于 service 层判断。
var ErrNotFound = gorm.ErrRecordNotFound

type BotRepository struct {
	db *gorm.DB
}

func NewBotRepository(db *gorm.DB) *BotRepository {
	return &BotRepository{db: db}
}

func (r *BotRepository) Create(bot *model.Bot) error {
	return r.db.Create(bot).Error
}

// UpdateConfigTx 在事务内按列写配置，不碰水位；toolset_version 是运行时
// 激活水位（只在压缩/重启边界切换），这里不回写——并发编辑不得把已激活的
// 新版本覆盖回旧值（INT-102）。includePrompt=false 表示人设未变：persona_prompt
// 与提示词状态三列一并不写，改名/换模型/换 provider 不得把读行时的旧提示词
// 状态写回、反向撤销并发的提示词激活。
func (r *BotRepository) UpdateConfigTx(tx *gorm.DB, bot *model.Bot, includePrompt bool) error {
	cols := map[string]interface{}{
		"name":        bot.Name,
		"provider_id": bot.ProviderID,
		"model":       bot.Model,
		"updated_at":  bot.UpdatedAt,
	}
	if includePrompt {
		cols["persona_prompt"] = bot.PersonaPrompt
		cols["system_prompt_text"] = bot.SystemPromptText
		cols["prompt_revision"] = bot.PromptRevision
		cols["platform_prompt_version"] = bot.PlatformPromptVersion
	}
	return tx.Model(&model.Bot{}).Where("id = ?", bot.ID).Updates(cols).Error
}

// ActivateToolsetTx 在事务内把激活水位推进到目标工具/平台版本并按当前人设
// 重建平台规则（存量提示词里写着「记忆不可用」，只改常量不会更新存量正文；
// 必须在压缩/重启边界重建、递增 revision 再冻结）。返回是否命中。
func (r *BotRepository) ActivateToolsetTx(tx *gorm.DB, botID string, toolsetVersion, platformPromptVersion int64, systemPromptText string, promptRevision int64, now time.Time) (bool, error) {
	res := tx.Model(&model.Bot{}).
		Where("id = ? AND (toolset_version < ? OR platform_prompt_version < ?)", botID, toolsetVersion, platformPromptVersion).
		Updates(map[string]interface{}{
			"toolset_version":         toolsetVersion,
			"platform_prompt_version": platformPromptVersion,
			"system_prompt_text":      systemPromptText,
			"prompt_revision":         promptRevision,
			"updated_at":              now,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// TellToolsetVersionTx 在事务内推进告知水位（CAS 旧水位，防止并发重复告知）。
func (r *BotRepository) TellToolsetVersionTx(tx *gorm.DB, botID string, fromVersion, toVersion int64) (bool, error) {
	res := tx.Model(&model.Bot{}).
		Where("id = ? AND last_told_version = ?", botID, fromVersion).
		Update("last_told_version", toVersion)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// fillProviderDerived 把 provider 派生字段补进一批 bot（INT-133）：BaseURL /
// 密钥密文 / last4 / 提供商名 / 目录 ID 全部来自 ai_providers 行——密钥只在
// provider 行存一份，轮换后所有引用它的机器人在下一次读取生效。同一批内重复
// 的 provider 合并为一次查询。provider 行缺失（理论不可达：删除有引用检查）
// 时该 bot 的派生字段保持空值，调用方按「配置已失效」处理。
func (r *BotRepository) fillProviderDerived(db *gorm.DB, bots []*model.Bot) error {
	ids := make([]string, 0, len(bots))
	seen := make(map[string]bool, len(bots))
	for _, b := range bots {
		if b.ProviderID != "" && !seen[b.ProviderID] {
			seen[b.ProviderID] = true
			ids = append(ids, b.ProviderID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var providers []model.AIProvider
	if err := db.Where("id IN (?)", ids).Find(&providers).Error; err != nil {
		return err
	}
	byID := make(map[string]model.AIProvider, len(providers))
	for _, p := range providers {
		byID[p.ID] = p
	}
	for _, b := range bots {
		p, ok := byID[b.ProviderID]
		if !ok {
			continue
		}
		b.BaseURL = p.BaseURL
		b.APIKeyEncrypted = append([]byte(nil), p.APIKeyEncrypted...)
		b.APIKeyLast4 = p.APIKeyLast4
		b.ProviderName = p.Name
		b.CatalogID = p.CatalogID
	}
	return nil
}

// withDerived 查回 bot 后统一补齐派生字段：任何 Find 返回的 bot 都带
// provider 派生的 BaseURL/密钥/last4，调用方（含 run 冻结与 schedule 派发）
// 不必各自再查 provider。
func (r *BotRepository) withDerived(rows []model.Bot) ([]model.Bot, error) {
	pointers := make([]*model.Bot, len(rows))
	for i := range rows {
		pointers[i] = &rows[i]
	}
	if err := r.fillProviderDerived(r.db, pointers); err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *BotRepository) Get(id string) (*model.Bot, error) {
	var bot model.Bot
	if err := r.db.Where("id = ?", id).First(&bot).Error; err != nil {
		return nil, err
	}
	if err := r.fillProviderDerived(r.db, []*model.Bot{&bot}); err != nil {
		return nil, err
	}
	return &bot, nil
}

func (r *BotRepository) FindByIDAndUser(id, userID string) (*model.Bot, error) {
	var bot model.Bot
	if err := r.db.Where("id = ? AND user_id = ?", id, userID).First(&bot).Error; err != nil {
		return nil, err
	}
	if err := r.fillProviderDerived(r.db, []*model.Bot{&bot}); err != nil {
		return nil, err
	}
	return &bot, nil
}

func (r *BotRepository) ListByUser(userID string) ([]model.Bot, error) {
	var bots []model.Bot
	if err := r.db.Where("user_id = ?", userID).Order("created_at DESC, id DESC").Find(&bots).Error; err != nil {
		return nil, err
	}
	bots, err := r.withDerived(bots)
	if err != nil {
		return nil, err
	}
	return bots, nil
}

// BotLastMessageRow 是机器人列表「最近消息」预览的仓储投影：主线最近一条
// 正文 trim 后非空的 user/assistant 消息，键为 bot_id。
type BotLastMessageRow struct {
	BotID string
	Kind  string
	Text  string
}

// botMessageTextExpr 抽取 bot_messages 的 content_json 列（JSON 列，json tag
// 为 content）里的 text 正文。坏 JSON 时经 jsonColumnOr 回落 '{}'，json_extract
// 得 NULL——坏数据按「没有正文」处理，不得让整条列表查询失败，与 issue.go 的
// JSON 防护同一约定。
func botMessageTextExpr(column string) string {
	return "json_extract(" + jsonColumnOr(column, "{}") + ", '$.text')"
}

// botTrimWhitespaceExpr 是 SQL trim 的字符集参数，覆盖 Go strings.TrimSpace
// （unicode.IsSpace）认定的全部空白：ASCII 的空格、制表、换行、垂直制表、
// 换页、回车，以及 U+0085、U+00A0、U+1680、U+2000–U+200A、U+2028、U+2029、
// U+202F、U+205F、U+3000。SQLite 的单参 trim 只去空格，挡不住纯 "\n\n" 或
// 用全角空格（U+3000）、不换行空格（U+00A0）凑出来的正文，必须显式给字符集
// 才能与 Go 侧 strings.TrimSpace 的判定对齐；trim 只用于过滤，返回的 text
// 保持原文不改写。
const botTrimWhitespaceExpr = "char(9)||char(10)||char(11)||char(12)||char(13)||char(32)" +
	"||char(133)||char(160)||char(5760)" +
	"||char(8192)||char(8193)||char(8194)||char(8195)||char(8196)" +
	"||char(8197)||char(8198)||char(8199)||char(8200)||char(8201)||char(8202)" +
	"||char(8232)||char(8233)||char(8239)||char(8287)||char(12288)"

// botLastMessagesSQL 构造最近消息预览的选取语句：bots 表列出全部目标机器人，
// 每个机器人经相关子查询沿 (bot_id, session_kind, seq) 复合索引倒序找第一条
// 正文 trim 后非空的主线 user/assistant 消息即停——早退，不必物化整个分区、
// 不必对每条消息算一遍 json_extract；子查询无命中时 JOIN 落空，该机器人自然
// 不出现在结果里。全部机器人合在一条语句里一次往返取回，不是逐机器人发查询。
func botLastMessagesSQL() string {
	textExpr := botMessageTextExpr("m.content_json")
	subTextExpr := botMessageTextExpr("m2.content_json")
	return "SELECT b.id AS bot_id, m.kind AS kind, " + textExpr + " AS text" +
		" FROM bots b" +
		" JOIN bot_messages m ON m.id = (" +
		"SELECT m2.id FROM bot_messages m2" +
		" WHERE m2.bot_id = b.id AND m2.session_kind = ? AND m2.kind IN (?, ?)" +
		" AND trim(COALESCE(" + subTextExpr + ", ''), " + botTrimWhitespaceExpr + ") <> ''" +
		" ORDER BY m2.seq DESC LIMIT 1)" +
		" WHERE b.id IN (?)"
}

// LastMessagesByBotIDs 一次查询批量返回每个机器人的最近消息预览，供机器人
// 列表端点填充 last_message。选取范围是主线（session_kind='main'）user/
// assistant 消息里 seq 最大的一条正文 trim 后非空者：正文纯空白（只带
// tool_calls、text 为空或全是换行/空格的 assistant）、content 坏 JSON 的消息
// 跳过并继续往前找；定时任务会话绝不参与选取。没有符合条件消息的机器人不在
// 返回 map 里，由上层按 null 处理。
func (r *BotRepository) LastMessagesByBotIDs(botIDs []string) (map[string]BotLastMessageRow, error) {
	out := make(map[string]BotLastMessageRow, len(botIDs))
	if len(botIDs) == 0 {
		return out, nil
	}
	var rows []BotLastMessageRow
	err := r.db.Raw(botLastMessagesSQL(),
		model.BotSessionKindMain, model.BotMessageKindUser, model.BotMessageKindAssistant, botIDs,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.BotID] = row
	}
	return out, nil
}

func (r *BotRepository) Delete(id string) error {
	res := r.db.Where("id = ?", id).Delete(&model.Bot{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("bot not found")
	}
	return nil
}

// NextMessageSeqTx 在事务内取号并推进水位。SQLite 没有 FOR UPDATE：生产库
// SetMaxOpenConns(1) 下事务天然串行，读-改-写放在同一短事务内即安全。
func (r *BotRepository) NextMessageSeqTx(tx *gorm.DB, botID string) (int64, error) {
	var seq int64
	if err := tx.Raw("SELECT next_message_seq FROM bots WHERE id = ?", botID).Scan(&seq).Error; err != nil {
		return 0, err
	}
	if seq == 0 {
		return 0, gorm.ErrRecordNotFound
	}
	if err := tx.Exec("UPDATE bots SET next_message_seq = next_message_seq + 1 WHERE id = ?", botID).Error; err != nil {
		return 0, err
	}
	return seq, nil
}

// NextEventSeqTx 同 NextMessageSeqTx，作用于 SSE 事件水位。
func (r *BotRepository) NextEventSeqTx(tx *gorm.DB, botID string) (int64, error) {
	var seq int64
	if err := tx.Raw("SELECT next_event_seq FROM bots WHERE id = ?", botID).Scan(&seq).Error; err != nil {
		return 0, err
	}
	if seq == 0 {
		return 0, gorm.ErrRecordNotFound
	}
	if err := tx.Exec("UPDATE bots SET next_event_seq = next_event_seq + 1 WHERE id = ?", botID).Error; err != nil {
		return 0, err
	}
	return seq, nil
}

// SetOfferedInputSeq 记录已交给某次模型请求的输入水位。
func (r *BotRepository) SetOfferedInputSeq(botID string, seq int64) error {
	return r.db.Model(&model.Bot{}).Where("id = ?", botID).
		Update("offered_input_seq", seq).Error
}
