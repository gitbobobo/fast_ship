package main

import (
	"sort"
	"strconv"

	"github.com/godbobo/fast_ship/server/internal/catalog"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// AI 提供商迁移（INT-133）：存量 bots 行按「用户 + 规范化地址 + 密钥密文字节」
// 去重聚合成 ai_providers，回填 provider_id，最后 DROP bots 的 base_url /
// api_key_encrypted / api_key_last4 三列。幂等：检测不到 base_url 列即整体
// 跳过，第二次启动无副作用。建 provider、回填与删列收进同一个事务，任何一步
// 失败即启动退出（与 EnsureBotSchema 的约定一致），绝不带半迁移状态运行。
func migrateBotAIProviders(db *gorm.DB, logger *zap.Logger) error {
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

		// 去重聚合：同用户内 (规范化地址, 密文) 相同的机器人共用一条 provider。
		// 分组键含 user_id：provider 按用户隔离，不同用户不得跨界共享。
		type group struct {
			userID  string
			baseURL string
			cipher  []byte
			last4   string
			botIDs  []string
		}
		groups := make(map[string]*group)
		order := []string{}
		for _, row := range rows {
			key := row.UserID + "\x00" + catalog.NormalizeBaseURL(row.BaseURL) + "\x00" + string(row.APIKeyEncrypted)
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

		usedNames := map[string]bool{}
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
			zap.Int("bots", len(rows)))
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
