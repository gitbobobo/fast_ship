package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BotMemoryRepository 是记忆文件/版本的仓库：全部写入走短事务（文件更新、
// 版本取号、50 版清理、FTS 同步由触发器完成）；读取按机器人归属隔离。
// FTS 是派生索引，文件/版本表是事实来源。

// botMemoryVersionRetention 每文件保留的最近版本数。
const botMemoryVersionRetention = 50

type BotMemoryRepository struct {
	db *gorm.DB
}

func NewBotMemoryRepository(db *gorm.DB) *BotMemoryRepository {
	return &BotMemoryRepository{db: db}
}

// FindFileTx 在事务内按 (bot, path) 取文件（含墓碑）；未命中返回
// gorm.ErrRecordNotFound。
func (r *BotMemoryRepository) FindFileTx(tx *gorm.DB, botID, path string) (*model.BotMemoryFile, error) {
	var file model.BotMemoryFile
	if err := tx.Where("bot_id = ? AND path = ?", botID, path).First(&file).Error; err != nil {
		return nil, err
	}
	return &file, nil
}

// FindFile 同 FindFileTx，事务外读取。
func (r *BotMemoryRepository) FindFile(botID, path string) (*model.BotMemoryFile, error) {
	return r.FindFileTx(r.db, botID, path)
}

// WriteVersionTx 在事务内执行一次记忆写入：确保文件行存在（新文件先落版本 0
// 空壳）、取下一版本号、插入完整版本快照、把当前文件推进到新版本，并按保留
// 策略清理旧版本。expectedVersion 非 nil 时做 CAS（当前版本必须匹配）。
// 幂等：同 (file, operation_key) 已存在时直接返回既有状态，不再写新版本。
func (r *BotMemoryRepository) WriteVersionTx(
	tx *gorm.DB,
	botID, path string,
	op model.BotMemoryOperation,
	content string,
	deleted bool,
	expectedVersion *int64,
	writer model.BotMemoryWriterKind,
	sourceRunID, maintenanceRunID, operationKey string,
	now time.Time,
) (*model.BotMemoryFile, *model.BotMemoryVersion, error) {
	file, err := r.FindFileTx(tx, botID, path)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, err
		}
		// 只有 create 能建文件；replace/append/delete/restore 对缺失路径明确报不存在，
		// 避免 expected_version 对空文件误建 v1。
		if op != model.BotMemoryOpCreate {
			return nil, nil, ErrMemoryFileNotFound
		}
		file = &model.BotMemoryFile{
			ID:             uuid.NewString(),
			BotID:          botID,
			Path:           path,
			Content:        "",
			ContentHash:    hashHex(""),
			CurrentVersion: 0,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if err := tx.Create(file).Error; err != nil {
			return nil, nil, err
		}
	} else {
		// 已有文件：先查幂等键（create/delete 重放不得先报冲突），再做状态/CAS。
		var dup model.BotMemoryVersion
		if err := tx.Where("file_id = ? AND operation_key = ?", file.ID, operationKey).
			First(&dup).Error; err == nil {
			return file, &dup, nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, err
		}
		if op == model.BotMemoryOpCreate && file.DeletedAt == nil {
			return file, nil, ErrMemoryFileExists
		}
		if op != model.BotMemoryOpCreate && file.DeletedAt != nil && op != model.BotMemoryOpRestore {
			return file, nil, ErrMemoryFileDeleted
		}
		if expectedVersion != nil && file.CurrentVersion != *expectedVersion {
			return file, nil, ErrMemoryVersionConflict
		}
	}

	nextVersion := file.CurrentVersion + 1
	hash := hashHex(content)
	version := &model.BotMemoryVersion{
		ID:           uuid.NewString(),
		FileID:       file.ID,
		Version:      nextVersion,
		Operation:    op,
		Content:      content,
		Deleted:      deleted,
		ContentHash:  hash,
		WriterKind:   writer,
		OperationKey: operationKey,
		CreatedAt:    now,
	}
	if sourceRunID != "" {
		id := sourceRunID
		version.SourceRunID = &id
	}
	if maintenanceRunID != "" {
		id := maintenanceRunID
		version.MaintenanceRunID = &id
	}
	if err := tx.Create(version).Error; err != nil {
		return nil, nil, err
	}

	// 当前文件推进到新版本（守护触发器校验恰好 +1 且版本行存在）。
	fileCols := map[string]interface{}{
		"content":         content,
		"content_hash":    hash,
		"current_version": nextVersion,
		"updated_at":      now,
	}
	if deleted {
		fileCols["deleted_at"] = now
	} else {
		fileCols["deleted_at"] = nil
	}
	if err := tx.Model(&model.BotMemoryFile{}).Where("id = ?", file.ID).Updates(fileCols).Error; err != nil {
		return nil, nil, err
	}
	file.Content = content
	file.ContentHash = hash
	file.CurrentVersion = nextVersion
	file.DeletedAt = nil
	if deleted {
		file.DeletedAt = &now
	}
	file.UpdatedAt = now

	// 保留最近 N 版：水位以下的一次性清理；级联删除不受影响。
	if nextVersion > botMemoryVersionRetention {
		if err := tx.Where("file_id = ? AND version <= ?", file.ID, nextVersion-botMemoryVersionRetention).
			Delete(&model.BotMemoryVersion{}).Error; err != nil {
			return nil, nil, err
		}
	}
	return file, version, nil
}

// ListFiles 列出文件元数据（可选路径前缀过滤；includeDeleted 控制墓碑）。
func (r *BotMemoryRepository) ListFiles(botID, prefix string, includeDeleted bool, limit int) ([]model.BotMemoryFile, error) {
	q := r.db.Where("bot_id = ?", botID)
	if prefix != "" {
		q = q.Where("path LIKE ? ESCAPE ?", escapeLike(prefix)+"%", `\`)
	}
	if !includeDeleted {
		q = q.Where("deleted_at IS NULL")
	}
	var rows []model.BotMemoryFile
	err := q.Order("path ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ListResidentTx 在一个事务内取常驻文件的当前正文（一致版本快照）：
// USER.md 与 MEMORY.md 缺失视为空。
func (r *BotMemoryRepository) ListResidentTx(tx *gorm.DB, botID string) (map[string]*model.BotMemoryFile, error) {
	var rows []model.BotMemoryFile
	err := tx.Where("bot_id = ? AND path IN ? AND deleted_at IS NULL", botID,
		[]string{"USER.md", "MEMORY.md"}).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := map[string]*model.BotMemoryFile{}
	for i := range rows {
		out[rows[i].Path] = &rows[i]
	}
	return out, nil
}

// ListDailyFilesTx 取 daily/ 前缀下未删除的文件（按路径倒序=新日期在前）。
func (r *BotMemoryRepository) ListDailyFilesTx(tx *gorm.DB, botID string) ([]model.BotMemoryFile, error) {
	var rows []model.BotMemoryFile
	err := tx.Where("bot_id = ? AND deleted_at IS NULL AND path LIKE ?", botID, "daily/%").
		Order("path DESC").Find(&rows).Error
	return rows, err
}

// FindVersionTx 取文件某版本的完整快照。
func (r *BotMemoryRepository) FindVersionTx(tx *gorm.DB, fileID string, version int64) (*model.BotMemoryVersion, error) {
	var row model.BotMemoryVersion
	if err := tx.Where("file_id = ? AND version = ?", fileID, version).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// ListVersionsTx 分页列出版本元数据（新版本在前）。
func (r *BotMemoryRepository) ListVersionsTx(tx *gorm.DB, fileID string, beforeVersion int64, limit int) ([]model.BotMemoryVersion, error) {
	q := tx.Where("file_id = ?", fileID)
	if beforeVersion > 0 {
		q = q.Where("version < ?", beforeVersion)
	}
	var rows []model.BotMemoryVersion
	err := q.Order("version DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// FindVersionByOperationKeyTx 按幂等键查版本（启动恢复核对记忆写副作用）。
func (r *BotMemoryRepository) FindVersionByOperationKeyTx(tx *gorm.DB, fileID, operationKey string) (*model.BotMemoryVersion, error) {
	var row model.BotMemoryVersion
	if err := tx.Where("file_id = ? AND operation_key = ?", fileID, operationKey).
		First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// HasVersionWithOperationKey 报告某机器人的记忆版本表里是否存在该幂等键
// （恢复时判断写入是否已发生；跨文件查询限定机器人归属）。
func (r *BotMemoryRepository) HasVersionWithOperationKey(botID, operationKey string) (bool, error) {
	var count int64
	err := r.db.Model(&model.BotMemoryVersion{}).
		Joins("JOIN bot_memory_files f ON f.id = bot_memory_versions.file_id").
		Where("f.bot_id = ? AND bot_memory_versions.operation_key = ?", botID, operationKey).
		Count(&count).Error
	return count > 0, err
}

// SearchFTS 全文检索：≥3 字符（Unicode 计）走参数化 MATCH（字面量引号编码，
// 不开放任意 FTS 表达式）；1–2 字符走同一 FTS 表限定机器人的 LIKE（转义
// %/_）。始终限定本机器人；只覆盖未删除文件的当前正文（触发器维护）。
func (r *BotMemoryRepository) SearchFTS(botID, query, pathPrefix string, offset, limit int) ([]BotMemorySearchHit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	runes := []rune(strings.TrimSpace(query))
	base := `FROM bot_memory_fts WHERE bot_id = ?`
	args := []interface{}{botID}
	if len(runes) >= 3 {
		base += ` AND body MATCH ?`
		args = append(args, ftsMatchLiteral(query))
	} else {
		base += ` AND body LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(query)+"%")
	}
	if pathPrefix != "" {
		base += ` AND path LIKE ? ESCAPE '\'`
		args = append(args, escapeLike(pathPrefix)+"%")
	}
	var total int64
	if err := r.db.Raw(`SELECT COUNT(*) `+base, args...).Scan(&total).Error; err != nil {
		return nil, err
	}
	rows := []BotMemorySearchHit{}
	sel := `SELECT file_id, bot_id, path, body,
		snippet(bot_memory_fts, 3, '[', ']', '…', 12) AS fragment,
		bm25(bot_memory_fts) AS rank ` + base +
		` ORDER BY rank, path LIMIT ? OFFSET ?`
	selArgs := append(append([]interface{}{}, args...), limit, offset)
	if err := r.db.Raw(sel, selArgs...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) > 0 {
		rows[0].Total = total
	}
	return rows, nil
}

// BotMemorySearchHit 是检索命中（GORM Raw 扫描目标；body 只用于计数判定，
// 上层不外发全文）。
type BotMemorySearchHit struct {
	FileID   string  `gorm:"column:file_id"`
	BotID    string  `gorm:"column:bot_id"`
	Path     string  `gorm:"column:path"`
	Body     string  `gorm:"column:body"`
	Fragment string  `gorm:"column:fragment"`
	Rank     float64 `gorm:"column:rank"`
	Version  int64   `gorm:"column:current_version"`
	Total    int64   `gorm:"column:total"`
}

// CountFiles 统计文件数（list 分页用）。
func (r *BotMemoryRepository) CountFiles(botID, prefix string, includeDeleted bool) (int64, error) {
	q := r.db.Model(&model.BotMemoryFile{}).Where("bot_id = ?", botID)
	if prefix != "" {
		q = q.Where("path LIKE ? ESCAPE ?", escapeLike(prefix)+"%", `\`)
	}
	if !includeDeleted {
		q = q.Where("deleted_at IS NULL")
	}
	var count int64
	err := q.Count(&count).Error
	return count, err
}

// escapeLike 转义 LIKE 通配符（ESCAPE '\'）。
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// ---------- 错误 ----------

var (
	// ErrMemoryFileExists create 动作命中已存在的非墓碑文件。
	ErrMemoryFileExists = errors.New("memory file already exists")
	// ErrMemoryFileDeleted 目标文件处于删除墓碑状态。
	ErrMemoryFileDeleted = errors.New("memory file is deleted")
	// ErrMemoryFileNotFound 目标路径尚无文件（非 create 不得建壳）。
	ErrMemoryFileNotFound = errors.New("memory file not found")
	// ErrMemoryVersionConflict expected_version 与当前版本不符。
	ErrMemoryVersionConflict = errors.New("memory version conflict")
)

// ftsMatchLiteral 把查询编成 FTS5 字面量（双引号包裹，内部引号加倍），
// 不开放任意 FTS 表达式、不删改查询里的引号字符。
func ftsMatchLiteral(query string) string {
	return `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
}

// hashHex 记忆内容哈希：SHA-256 十六进制。
func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
