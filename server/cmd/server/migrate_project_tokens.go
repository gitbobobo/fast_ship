package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// legacyProjectTokenRow 是 projects 表上旧密文列的读取投影；模型已不含这两列，
// 迁移经 Raw SQL 访问。任一列 NULL 解扫描为 nil。
type legacyProjectTokenRow struct {
	ID     string `gorm:"column:id"`
	UserID string `gorm:"column:user_id"`
	Access []byte `gorm:"column:github_token_encrypted"`
	PR     []byte `gorm:"column:github_pr_token_encrypted"`
}

// migrateProjectGitHubTokens 把项目上的旧 GitHub Token 密文迁移进用户级 Token 库
// （user_github_tokens），并把 projects 切到引用列。语义（INT-84）：
//
//   - 同一用户内按 Token 明文（SHA-256 指纹）去重复用，标题按产生顺序
//     「迁移 Token 1」起；旧密文 blob 直接复用进 Token 库（同一加密密钥，不解密重加密）。
//   - 原 Access Token → 公开仓库引用；独立 PR Token → 代码仓库引用；
//     没配独立 PR Token 但有 Access 的项目把 Access 显式引用到两个位置（保留旧兜底行为）。
//   - 检测状态一律 unchecked，不依赖 GitHub 在线。
//   - 任一旧密文解密失败即返回错误（调用方启动失败），不跳过该项目。
//   - 全部待迁移用户放进同一个事务：任一用户解密失败整库回滚并中止启动，
//     避免先提交的用户在下次启动时被 DROP 旧列误伤。成功后清空该项目旧密文列，
//     再尝试 DROP 旧列。
//   - 幂等：旧列不存在（全新库或已迁移并 DROP）直接返回；旧列已 NULL 的行不会被再次选中。
//   - 只按实际存在的旧列构造 SELECT / UPDATE，避免清理中断后只剩一列时启动失败。
func migrateProjectGitHubTokens(db *gorm.DB, encryptionKey []byte, logger *zap.Logger) error {
	hasAccess, err := hasSQLiteColumn(db, "projects", "github_token_encrypted")
	if err != nil {
		return err
	}
	hasPR, err := hasSQLiteColumn(db, "projects", "github_pr_token_encrypted")
	if err != nil {
		return err
	}
	if !hasAccess && !hasPR {
		return nil
	}

	// 只选仍带旧密文的行；已迁移的行旧列已 NULL，天然不会重复处理。
	query, err := legacyProjectTokenSelectSQL(hasAccess, hasPR)
	if err != nil {
		return err
	}
	var rows []legacyProjectTokenRow
	if err := db.Raw(query).Scan(&rows).Error; err != nil {
		return fmt.Errorf("读取待迁移项目凭证失败: %w", err)
	}
	if len(rows) == 0 {
		return clearLegacyProjectTokenColumns(db, logger)
	}

	byUser := make(map[string][]legacyProjectTokenRow, len(rows))
	for _, row := range rows {
		byUser[row.UserID] = append(byUser[row.UserID], row)
	}
	migrated := 0
	if err := db.Transaction(func(tx *gorm.DB) error {
		for userID, userRows := range byUser {
			if err := migrateUserProjectTokens(tx, userID, userRows, encryptionKey, hasAccess, hasPR); err != nil {
				return fmt.Errorf("迁移用户 %s 的项目凭证失败: %w", userID, err)
			}
			migrated += len(userRows)
		}
		return nil
	}); err != nil {
		return err
	}
	logger.Info("项目 GitHub Token 迁移完成", zap.Int("projects", migrated))

	return clearLegacyProjectTokenColumns(db, logger)
}

// legacyProjectTokenSelectSQL 只引用实际存在的旧列。清理中断后可能只剩一列。
func legacyProjectTokenSelectSQL(hasAccess, hasPR bool) (string, error) {
	if !hasAccess && !hasPR {
		return "", fmt.Errorf("没有待迁移的旧 Token 列")
	}
	cols := []string{"id", "user_id"}
	var filters []string
	if hasAccess {
		cols = append(cols, "github_token_encrypted")
		filters = append(filters, "github_token_encrypted IS NOT NULL")
	} else {
		cols = append(cols, "NULL AS github_token_encrypted")
	}
	if hasPR {
		cols = append(cols, "github_pr_token_encrypted")
		filters = append(filters, "github_pr_token_encrypted IS NOT NULL")
	} else {
		cols = append(cols, "NULL AS github_pr_token_encrypted")
	}
	return fmt.Sprintf(
		"SELECT %s FROM projects WHERE %s ORDER BY created_at ASC",
		strings.Join(cols, ", "),
		strings.Join(filters, " OR "),
	), nil
}

// migrateUserProjectTokens 迁移单个用户的全部待迁移项目。
// fingerprint→tokenID 缓存保证明文相同的旧密文只产生一条 Token 库记录。
func migrateUserProjectTokens(tx *gorm.DB, userID string, rows []legacyProjectTokenRow, encryptionKey []byte, hasAccess, hasPR bool) error {
	tokenIDByFingerprint := make(map[string]string)
	titleSeq := 0

	findTokenID := func(plaintext, ciphertext []byte) (string, error) {
		fingerprint := githubTokenFingerprint(plaintext)
		if id, ok := tokenIDByFingerprint[fingerprint]; ok {
			return id, nil
		}
		titleSeq++
		token := &model.UserGitHubToken{
			ID:               uuid.NewString(),
			UserID:           userID,
			Title:            fmt.Sprintf("迁移 Token %d", titleSeq),
			TokenEncrypted:   ciphertext,
			TokenFingerprint: fingerprint,
			TokenPrefix:      githubTokenPrefix(plaintext),
			DetectionStatus:  model.TokenDetectionUnchecked,
			DetectionType:    model.TokenTypeUnknown,
			DetectionScopes:  "[]",
		}
		if err := tx.Create(token).Error; err != nil {
			return "", err
		}
		tokenIDByFingerprint[fingerprint] = token.ID
		return token.ID, nil
	}

	for _, row := range rows {
		var publicRef, codeRef string

		if len(row.Access) > 0 {
			plaintext, err := crypto.Decrypt(row.Access, encryptionKey)
			if err != nil {
				return fmt.Errorf("项目 %s 的 GitHub Access Token 解密失败: %w", row.ID, err)
			}
			id, err := findTokenID(plaintext, row.Access)
			if err != nil {
				return err
			}
			publicRef = id
			// 原来没有独立 PR Token、实际靠 Access 兜底读 PR：显式引用到两个位置。
			codeRef = id
		}
		if len(row.PR) > 0 {
			plaintext, err := crypto.Decrypt(row.PR, encryptionKey)
			if err != nil {
				return fmt.Errorf("项目 %s 的 PR 访问 Token 解密失败: %w", row.ID, err)
			}
			id, err := findTokenID(plaintext, row.PR)
			if err != nil {
				return err
			}
			// 独立 PR Token 优先作为代码仓库引用，覆盖 Access 的兜底引用。
			codeRef = id
		}

		fields := map[string]interface{}{
			"public_github_token_id": publicRef,
			"code_github_token_id":   codeRef,
		}
		if hasAccess {
			fields["github_token_encrypted"] = nil
		}
		if hasPR {
			fields["github_pr_token_encrypted"] = nil
		}
		if err := tx.Model(&model.Project{}).Where("id = ?", row.ID).Updates(fields).Error; err != nil {
			return err
		}
	}
	return nil
}

// clearLegacyProjectTokenColumns 在迁移完成后物理删除旧密文列。
// 列已全部置空，DROP 失败不阻断启动——留空列同样幂等。
func clearLegacyProjectTokenColumns(db *gorm.DB, logger *zap.Logger) error {
	for _, column := range []string{"github_token_encrypted", "github_pr_token_encrypted"} {
		hasColumn, err := hasSQLiteColumn(db, "projects", column)
		if err != nil {
			return err
		}
		if !hasColumn {
			continue
		}
		if err := db.Exec(fmt.Sprintf("ALTER TABLE %q DROP COLUMN %q", "projects", column)).Error; err != nil {
			logger.Warn("清理项目旧 Token 列失败，保留空列", zap.String("column", column), zap.Error(err))
		}
	}
	return nil
}

func githubTokenFingerprint(plaintext []byte) string {
	sum := sha256.Sum256(plaintext)
	return hex.EncodeToString(sum[:])
}

func githubTokenPrefix(plaintext []byte) string {
	if len(plaintext) > 8 {
		return string(plaintext[:8])
	}
	return string(plaintext)
}
