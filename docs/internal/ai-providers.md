# AI 提供商（INT-133）

统一管理机器人对话用的 OpenAI 兼容端点：地址与密钥按用户存一份（
`ai_providers`），机器人只引用 `provider_id` + 模型名。需求背景：此前每个
机器人各自存 `base_url`/`model`/`api_key`，多个套餐、多个模型的机器人要重复
配置；轮换一次密钥要逐台机器人改。

## 数据模型

`ai_providers`（新增，按用户隔离）：

| 列 | 说明 |
| --- | --- |
| `id` | UUID 主键 |
| `user_id` | 归属用户，FK `users` `ON DELETE CASCADE` |
| `catalog_id` | models.dev 的提供商 id（内置目录），或 `custom` |
| `name` | 展示名。目录提供商缺省用目录名，自定义缺省「自定义」；同用户内允许重名（迁移按序号区分） |
| `base_url` | 规范化地址（TrimSpace + 去尾部 `/`）；目录提供商由目录决定 |
| `api_key_encrypted` | AES-GCM 密文（`crypto` 包，与用户级 AI 配置同一口径） |
| `api_key_last4` | 只用于展示的末四位 |

`bots` 侧（INT-133）：

- 删列 `base_url`、`api_key_encrypted`、`api_key_last4`；新增 `provider_id`
  （`not null;default:''`，索引 `idx_bots_provider`）。
- `model.Bot` 保留同名字段但标 `gorm:"-"`：`BaseURL`、`APIKeyEncrypted`、
  `APIKeyLast4`，另加派生 `ProviderName`、`CatalogID`。仓库层
  （`repository/bot.go` 的 `fillProviderDerived`）在 `Get` /
  `FindByIDAndUser` / `ListByUser` 读取后按 `provider_id` 批量补齐（同批去重，
  一次 `IN` 查询）。**这是刻意的适配器边界**：run 入口、压缩边界与 schedule
  派发的「地址与密钥成对冻结」语义（INT-102/INT-103）一行都不用改，冻结读
  只是从 provider 派生值取数；绕开仓库层的裸读必须自己补（现已全部收敛）。
- 密钥只在 provider 行存一份：轮换密钥/换地址后，引用它的所有机器人在下一个
  run 生效（run 中途仍用冻结的旧值，见 `bots.md`）。
- 不建 DB 级外键：provider 删除与引用检查在同一事务里做（生产单连接天然
  串行），与机器人删除的既有模式一致。被引用时删除返回 409（`40933`）。

### 存量迁移（`cmd/server/migrate_ai_providers.go`）

AutoMigrate 之后执行，幂等：

1. `PRAGMA table_info(bots)` 检测 `base_url` 列；没有即整体跳过（第二次启动
   无副作用）。
2. 同一事务内按（用户 + 规范化地址 + 明文密钥）去重聚合存量机器人，每个
   组合建一条 provider：`catalog_id` 用目录按 `api` 地址反查，匹配不到为
   `custom`；名称用目录名或「自定义」/「自定义 2」…（按排序后的分组键稳定
   编号）。AES-GCM 每次加密取随机 nonce，同明文在两行上密文不同——按密文
   字节分组会把同一 key 拆成多条 provider，所以分组前先以迁移配置的加密
   key 逐行解密，分组键只放 `sha256(明文)` 的哈希（明文不持久化、不进
   日志）；解密失败（例如历史轮换过密钥）回退为按密文字节分组，单行
   不可解不中断迁移。每组只保留组内第一条密文写入 provider。
3. 回填 `bots.provider_id`，再 `ALTER TABLE bots DROP COLUMN` 三列。
4. 任何一步失败即启动退出，绝不带半迁移状态运行（与 `EnsureBotSchema` 的
   约定一致）。

迁移只搬运既有密文、不重加密，因此密钥不会丢；历史
`bot_runs.api_key_encrypted_snapshot` 与 `bot_model_windows` 缓存都不受影响。

## 内置目录（models.dev）

- 来源：<https://models.dev/api.json>（MIT）。生成脚本
  `scripts/gen-model-catalog.mjs`（Node，无第三方依赖）：

  ```bash
  node scripts/gen-model-catalog.mjs            # 联网拉取并重写产物
  node scripts/gen-model-catalog.mjs --in x.json # 用本地快照重放
  ```

  产物 `server/internal/catalog/modelsdev.json`（当前 192 家提供商 / 6050 个
  模型，约 1.25 MB），由 `server/internal/catalog` 用 `go:embed` 打进二进制，
  **运行时绝不联网**；发版时人工跑一次脚本刷新。
- 过滤规则：`npm === "@ai-sdk/openai-compatible"` 且 `api` 非空，且至少一个
  模型 `tool_call === true`；模型只收 `tool_call` 的，字段裁剪为
  `id/name/context_window/max_output_tokens/reasoning`。
- `generated_at` 取目录内模型的 `last_updated` 快照日（不是生成时刻）：同一份
  api.json 重跑产物字节级一致，数据新旧一眼可见。
- **第一期不收录 Anthropic 协议提供商**（`@ai-sdk/anthropic`）。models.dev 上
  MiniMax 全家（`minimax`/`minimax-cn`/coding plan）都是 Anthropic 协议，因此
  不在目录里——需求文字把它列进了示例，这里按「第一期不做 Anthropic」的硬
  边界执行。要用 MiniMax 的 OpenAI 兼容端点，走「自定义」提供商填地址。
- Go API：`Catalog()`、`FindProvider(id)`、`FindModel(providerID, modelID)`、
  `ResolveBaseURL(catalogID)`、`FindProviderByAPI(api)`、
  `NormalizeBaseURL`；`init` 时解析一次。

## 窗口解析新增 catalog 一级

`capabilityFor` 顺序改为 **manual（部署 `bots.context.model_windows`）>
detected（`bot_model_windows` 探测缓存）> catalog（目录 `limit.context` /
`limit.output`）> fallback（`bots.context.window_tokens`）**：

- `context_source="catalog"`，`context_detail` 为「内置模型目录（models.dev）」；
  目录里窗口 ≤ 0 或缺模型时放行到 fallback，绝不当成 0 上限参与预算。
- 目录归属取 `bots` 派生的 `catalog_id`，`custom` 或空直接跳过。辅助模型
  （摘要/归档/夜间整理）沿用机器人的 provider，因此同一条解析链生效——
  部署级 `summary_model`/`archive_model`/`nightly_model` 仍只是模型名。
- 展示层（机器人详情、表单）按 `context_source` 标注来源，不得把目录值说成
  已探测。

## REST（仅 JWT，按登录用户隔离）

```
GET    /api/ai-providers             -> {items:[AIProvider]}
POST   /api/ai-providers             {catalog_id, name?, base_url?, api_key}
PUT    /api/ai-providers/{id}        {name?, base_url?, api_key?}
DELETE /api/ai-providers/{id}        被机器人引用时 409（40933）
POST   /api/ai-providers/test        {catalog_id?, base_url?, api_key, model?}
GET    /api/ai-providers/catalog     -> {items:[CatalogProvider]}
```

- `AIProvider = {id, catalog_id, name, base_url, api_key_last4, bot_count,
  created_at, updated_at}`；`bot_count` 与删除前检查同口径。
- `POST` 只在 `catalog_id === "custom"` 时接受 `base_url`（http/https，规范化
  后存）；目录提供商的地址由目录决定，请求里的 `base_url` 被忽略（改不动）。
- `catalog_id` 创建后不可改（换目录归属等于换提供商，应新建一条）；
  `api_key` 缺省保留、空串拒绝（与机器人旧 key 三态语义一致）。
- `/test` 不落库、不写窗口缓存：`streaming` + `tool_call` 两项检查，另尽力
  返回 `/models` 拉到的模型 id 列表。`model` 缺省用目录第一个模型，custom
  必填。
- `/catalog` 返回全量目录（约 1.2 MB，前端一次拉取后 `staleTime: Infinity`）；
  目录提供商 `models` 供 ModelPicker 分组渲染。
- 错误码：`40433` provider 不存在/不属于该用户，`40933` 仍被机器人引用。

### 机器人端点随之改形

- `POST /api/bots`：`{name, persona_prompt?, provider_id, model}`；
  `PUT /api/bots/{id}`：`{name?, persona_prompt?, provider_id?, model?}`。
  不再接受 `base_url` / `api_key`（密钥只在 provider 上），`provider_id` 必须
  属于当前用户。
- `BotResponse` 新增 `provider_id`、`provider_name`、`catalog_id`；
  `base_url` / `api_key_last4` 保留为 provider 派生值（前端与既有展示继续可读）。
- `POST /api/bots/test-connection`：`{provider_id, bot_id?, model?}`，地址与密钥
  一律取 provider；`model` 缺省时沿用 `bot_id` 已保存的模型。

## Web

- 设置 →「AI 提供商」（`/settings/ai-providers`）：列表（名称/地址/Key 末四位/
  引用数/创建时间）+ 添加/编辑/删除 + 测试连接。添加流程：搜索选择目录提供商
  → 填 API key → 测试连接 → 保存；选「自定义」才显示地址（以及测试用模型）
  输入框。编辑不回填密钥，未填新密钥时测试连接禁用；目录归属与目录地址不可改。
- 机器人表单（`bot-form-dialog.tsx`）：名称 + 人设（可选，留空即用默认说话
  方式）+ 模型。模型统一走 `ModelPicker`：把**已配置的提供商**映射成 picker 的
  providers（`instanceId = ai_providers.id`，`iconId = catalog_id`，模型清单来自
  目录），rail 按提供商分组。目录里没有模型清单的提供商（custom）不进 picker，
  表单出现提供商下拉并回落到手填模型名输入框。零提供商时给空态并链到设置页。
- 机器人详情右栏配置摘要：提供商 / 模型 / Key 末四位 / 窗口 / 创建时间。

## 决策与已知限制

- **地址与密钥的适配收敛在仓库层**，不在 run 循环里到处查 provider：这样
  冻结语义与压缩/恢复路径完全不动，代价是绕过仓库层的裸读拿不到派生值。
- **测试连接不写窗口缓存**：未保存的配置不该污染 `bot_model_windows`；窗口
  探测的持久化仍归机器人测试连接与 run 前置探测。
- **目录请求体约 1.2 MB**：一次拉取、进程内缓存；若以后嫌大，可加裁剪参数，
  属增量不破坏现有契约。
- **不做**：Anthropic 协议提供商、provider 级模型自定义（除 custom 手填）、
  跨用户共享 provider。
