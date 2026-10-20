package main

import (
	"crypto/sha256"
	"sort"
	"strconv"

	"github.com/godbobo/fast_ship/server/internal/catalog"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// AI 提供商迁移（INT-133）：存量 bots 行按「用户 + 规范化地址 + 明文密钥」
// 去重聚合成 ai_providers，回填 provider_id，最后 DROP bots 的 base_url /
// api_key_encrypted / api_key_last4 三列。AES-GCM 每次随机 nonce，同一明文
// 在不同 bot 行上密文不同——分组键必须先解密取明文再哈希，不能按密文字节，
// 否则同 key 的存量机器人会各建一条 provider。单行解密失败（历史密钥轮换过）
// 回退到按该行的密文字节分组，不中断启动；明文只活在局部变量里，分组键与
// 日志都只携带哈希与数量。幂等：检测不到 base_url 列即整体跳过，第二次启动
// 无副作用。建 provider、回填与删列收进同一个事务，任何一步失败即启动退出
// （与 EnsureBotSchema 的约定一致），绝不带半迁移状态运行。
func migrateBotAIProviders(db *gorm.DB, logger *zap.Logger, encryptionKey []byte) error {
	hasOld, err := hasSQLiteColumn(db, "bots", "base_url")
	if err != nil {
		return err
	}
	if !hasOld {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var rows []struct {
			ID              string
			UserID          string
			BaseURL         string
			APIKeyEncrypted []byte
			APIKeyLast4     string
		}
		if err := tx.Raw("SELECT id, user_id, base_url, api_key_encrypted, api_key_last4 FROM bots").Scan(&rows).Error; err != nil {
			return err
		}

		// 去重聚合：同用户内 (规范化地址, 明文密钥) 相同的机器人共用一条
		// provider。分组键含 user_id：provider 按用户隔离，不同用户不得跨界
		// 共享。解密成功的行按 sha256(明文) 分组——密文因随机 nonce 各不相同；
		// 解密失败的行（密文损坏/历史轮换密钥）退回按密文字节分组，宁可多建
		// 一条也不丢机器人。
		type group struct {
			userID  string
			baseURL string
			cipher  []byte
			last4   string
			botIDs  []string
		}
		groups := make(map[string]*group)
		order := []string{}
		undecryptable := 0
		for _, row := range rows {
			keyPart := ""
			if plain, derr := crypto.Decrypt(row.APIKeyEncrypted, encryptionKey); derr == nil {
				sum := sha256.Sum256(plain)
				keyPart = "p:" + string(sum[:])
			} else {
				keyPart = "c:" + string(row.APIKeyEncrypted)
				undecryptable++
			}
			key := row.UserID + "\x00" + catalog.NormalizeBaseURL(row.BaseURL) + "\x00" + keyPart
			g, ok := groups[key]
			if !ok {
				g = &group{
					userID:  row.UserID,
					baseURL: catalog.NormalizeBaseURL(row.BaseURL),
					cipher:  append([]byte(nil), row.APIKeyEncrypted...),
					last4:   row.APIKeyLast4,
				}
				groups[key] = g
				order = append(order, key)
			}
			g.botIDs = append(g.botIDs, row.ID)
		}
		// 稳定顺序创建：同名冲突的序号（自定义 2）不依赖 map 遍历顺序。
		sort.Strings(order)

		// 名称只在用户自己的 provider 列表里有意义（provider 按用户隔离），
		// 去重集合按用户分段：一个账号的迁移不得给另一个账号改名。
		usedNamesByUser := map[string]map[string]bool{}
		for _, key := range order {
			g := groups[key]
			catalogID := catalog.CustomCatalogID
			name := "自定义"
			if id, ok := catalog.FindProviderByAPI(g.baseURL); ok {
				catalogID = id
				if p, ok := catalog.FindProvider(id); ok {
					name = p.Name
				}
			}
			usedNames := usedNamesByUser[g.userID]
			if usedNames == nil {
				usedNames = map[string]bool{}
				usedNamesByUser[g.userID] = usedNames
			}
			name = uniqueProviderName(usedNames, name)
			usedNames[name] = true

			provider := &model.AIProvider{
				ID:              uuid.NewString(),
				UserID:          g.userID,
				CatalogID:       catalogID,
				Name:            name,
				BaseURL:         g.baseURL,
				APIKeyEncrypted: g.cipher,
				APIKeyLast4:     g.last4,
			}
			if err := tx.Create(provider).Error; err != nil {
				return err
			}
			for _, botID := range g.botIDs {
				if err := tx.Model(&model.Bot{}).Where("id = ?", botID).
					Update("provider_id", provider.ID).Error; err != nil {
					return err
				}
			}
		}

		// DROP 前清掉引用旧列的索引（当前无索引，防御存量库上手工建过的情况）。
		for _, column := range []string{"base_url", "api_key_encrypted", "api_key_last4"} {
			if err := dropSQLiteIndexesByColumn(tx, "bots", column); err != nil {
				return err
			}
			if err := tx.Exec("ALTER TABLE bots DROP COLUMN " + column).Error; err != nil {
				return err
			}
		}
		logger.Info("存量机器人配置已聚合为 AI 提供商",
			zap.Int("providers", len(order)),
			zap.Int("bots", len(rows)),
			zap.Int("undecryptable_keys", undecryptable))
		return nil
	})
}

// uniqueProviderName 在 usedNames 已占用 name 时追加序号（自定义 2、自定义 3 …）。
func uniqueProviderName(used map[string]bool, name string) string {
	if !used[name] {
		return name
	}
	for i := 2; ; i++ {
		candidate := name + " " + strconv.Itoa(i)
		if !used[candidate] {
			return candidate
		}
	}
}
