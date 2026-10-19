// Package catalog 是内置的模型提供商目录（INT-133）：数据来自 models.dev
// （MIT License），由 scripts/gen-model-catalog.mjs 生成并 go:embed 进二进制。
// 运行时绝不联网刷新；发版时由人重跑脚本更新快照。查表在 init 时解析一次，
// 之后全部只读。
package catalog

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"
)

//go:embed modelsdev.json
var raw []byte

// CustomCatalogID 是「自定义提供商」的 catalog_id：不来自目录，base_url 与
// 模型清单完全由用户提供。
const CustomCatalogID = "custom"

// Model 是目录内的一个模型：窗口与输出上限缺失时为 0（未知），调用方不得把
// 0 当成 0 上限参与预算。
type Model struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ContextWindow   int64  `json:"context_window"`
	MaxOutputTokens int64  `json:"max_output_tokens"`
	Reasoning       bool   `json:"reasoning"`
}

// Provider 是目录内的一个提供商：api 是官方 OpenAI 兼容端点。
type Provider struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	API    string  `json:"api"`
	Models []Model `json:"models"`
}

type catalogFile struct {
	GeneratedAt string     `json:"generated_at"`
	Source      string     `json:"source"`
	Providers   []Provider `json:"providers"`
}

var (
	parsed     catalogFile
	byID       map[string]Provider
	modelByIDs map[string]map[string]Model // provider id -> model id -> Model
	byAPI      map[string]string           // 规范化 api 地址 -> provider id
	once       sync.Once
)

func load() {
	once.Do(func() {
		if err := json.Unmarshal(raw, &parsed); err != nil {
			// 目录随二进制发布且由脚本生成；解析失败属于构建期事故，启动即崩
			// 比带空目录运行更诚实。
			panic("catalog: 内置 modelsdev.json 解析失败: " + err.Error())
		}
		byID = make(map[string]Provider, len(parsed.Providers))
		modelByIDs = make(map[string]map[string]Model, len(parsed.Providers))
		byAPI = make(map[string]string, len(parsed.Providers))
		for _, p := range parsed.Providers {
			byID[p.ID] = p
			models := make(map[string]Model, len(p.Models))
			for _, m := range p.Models {
				models[m.ID] = m
			}
			modelByIDs[p.ID] = models
			// 反查表只收首个命中的 provider id（同一 api 地址目录里存在极少
			// 数重复）；排序后的生成顺序保证结果稳定。
			key := NormalizeBaseURL(p.API)
			if _, ok := byAPI[key]; !ok {
				byAPI[key] = p.ID
			}
		}
	})
}

// Catalog 返回全部提供商（生成顺序，即按 id 排序）。返回的是共享切片的副本
// 列表，调用方不得修改其中的 Model。
func Catalog() []Provider {
	load()
	out := make([]Provider, len(parsed.Providers))
	copy(out, parsed.Providers)
	return out
}

// GeneratedAt 是数据快照时间（目录内模型的最大 last_updated）。
func GeneratedAt() string {
	load()
	return parsed.GeneratedAt
}

// FindProvider 按目录 id 查找；custom 不是目录成员。
func FindProvider(id string) (Provider, bool) {
	load()
	p, ok := byID[id]
	return p, ok
}

// FindModel 在指定目录提供商下查模型；先精确匹配，再大小写不敏感兜底（与
// 窗口缓存查找同一口径）。
func FindModel(providerID, modelID string) (Model, bool) {
	load()
	models, ok := modelByIDs[providerID]
	if !ok {
		return Model{}, false
	}
	if m, ok := models[modelID]; ok {
		return m, true
	}
	for _, m := range models {
		if strings.EqualFold(m.ID, modelID) {
			return m, true
		}
	}
	return Model{}, false
}

// ResolveBaseURL 返回目录提供商的 api 地址；custom 或未知 id 返回 false。
func ResolveBaseURL(catalogID string) (string, bool) {
	load()
	p, ok := byID[catalogID]
	if !ok {
		return "", false
	}
	return p.API, true
}

// FindProviderByAPI 按 api 地址反查目录 id（地址先按 NormalizeBaseURL 规范化）。
// 用于存量迁移：按机器人旧 base_url 猜目录归属，匹配不到回 custom。
func FindProviderByAPI(baseURL string) (string, bool) {
	load()
	id, ok := byAPI[NormalizeBaseURL(baseURL)]
	return id, ok
}

// NormalizeBaseURL 规范化参与比对/落库的 api 地址：TrimSpace + 去尾部 /。
func NormalizeBaseURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}
