# 截图库（Screenshots）

项目级截图库：批量上传应用各页面截图，按 group 自由分组浏览，同一界面保留全部历史版本供对比。前端在 `web/`（本文件不展开），本文档记录后端数据模型、端点、权限分工与上传语义。

## 数据模型

两张表，GORM `AutoMigrate` 注册（`server/cmd/server/main.go`），无外置迁移工具。唯一索引 `(project_id, screen_key)` 由 main.go 手工索引段建立（`idx_screenshot_screens_project_key`）。

### `screenshot_screens`

同一界面的聚合行，`(project_id, screen_key)` 唯一。

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | TEXT | 主键 |
| `project_id` | TEXT | FK → `projects`，`OnDelete: CASCADE` |
| `screen_key` | TEXT | 服务端规范化值（`ToLower` + `TrimSpace`），1–100 字符 |
| `title` | TEXT | 可空显示名；空时客户端回退显示 `screen_key` |
| `group_name` | TEXT | 分组标签（`group` 是 SQL 关键字，列名用 `group_name`，JSON 仍为 `group`）；自由文本无需预建，空串 = 未分组 |
| `version_count` | INTEGER | 冗余计数，版本增删时在写路径重算 |
| `last_uploaded_at` | DATETIME | 冗余最新上传时间，列表排序键 |
| `created_at` | DATETIME | |

### `screenshot_versions`

每次上传一行，全量保留、不去重、不清理。

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | TEXT | 主键 |
| `screen_id` | TEXT | FK → `screenshot_screens`，`OnDelete: CASCADE` |
| `file_name` | TEXT | 规范化后的原始文件名 |
| `file_path` | TEXT | 存储相对路径，`json:"-"` 永不外泄 |
| `mime_type` | TEXT | 内容嗅探结果（非客户端声明） |
| `file_size` | INTEGER | 字节数 |
| `note` | TEXT | 上传备注 |
| `uploaded_by` | TEXT | JWT 用户名 /「Web 用户」/「API Key: <key名>」 |
| `uploaded_at` | DATETIME | |

文件落在 `storage.Storage` 下 `<project_id>/screenshots/<version_id>.<ext>`；扩展名由嗅探出的 mime 推导（`normalizeIssueAssetFileName` 与 issue assets 共用）。删版本删文件、删 screen 删全部文件；文件删除均在 DB 事务提交后做，事务回滚时也会清掉已落盘的新文件，不留孤儿。

## 端点与权限

| 方法 | 路径 | 凭证 | 说明 |
|---|---|---|---|
| POST | `/api/projects/:pid/screenshots` | JWT / API Key | multipart 上传：`file`、`screen_key` 必填，`group`/`title`/`note` 可选 |
| GET | `/api/projects/:pid/screenshots` | JWT / API Key | `{items: [...]}`，screen 字段 + `version_count` + `latest_version`（可 null），按 `last_uploaded_at` 倒序 |
| GET | `/api/screenshot-screens/:sid` | JWT / API Key | screen 字段 + `versions`（按落库先后倒序，即 rowid DESC 全量） |
| PATCH | `/api/screenshot-screens/:sid` | **仅 JWT** | `{"group": ..., "title": ...}` 指针语义，返回 detail 形状 |
| DELETE | `/api/screenshot-screens/:sid` | **仅 JWT** | 删 screen + 全部版本行 + 磁盘文件 |
| DELETE | `/api/screenshot-versions/:vid` | **仅 JWT** | 删单版本 + 文件；删到最后一个版本时连带删 screen |
| GET / HEAD | `/api/screenshot-versions/:vid/content` | JWT / API Key / `?token=` | 输出图片字节，`Content-Type` 用存储的 `mime_type`，`Content-Disposition: inline` |

写操作 PATCH/DELETE 走 `RequireJWT` 分组（API Key 返回 40301），上传与读走 `RequireAuth`，content 挂 `RequireAuthWithQueryToken(cfg, apiKeyRepo, authService, "token")` 支持 `<img>` 直链。

归属校验沿 `screen → project → user_id` 链：项目不属于当前用户时 screen/version 一律按「不存在」返回，不区分「不存在」与「无权限」。错误码：`ErrScreenshotScreenNotFound`（40413）、`ErrScreenshotVersionNotFound`（40414）；参数/格式/大小非法用 `ErrInvalidParams`（40001）。

## 上传语义

`screen_key` 服务端规范化：`strings.ToLower` + `strings.TrimSpace`，规范化后 1–100 字符（rune 计），否则 40001。首次见到的 key 自动建 screen，重复上传归并到同一 screen 追加新版本。

表单字段存在性语义（service 层 `Group *string` 表达存在性）：

- 表单含 `group` 字段 → 更新 `screen.group_name`，**允许空串**（= 置为未分组）；未携带则保持不变。
- `title` 非空才覆盖 `screen.title`；空串/未携带保持不变（上传表单无法清空 title，清空走 PATCH）。
- `note` 只落在当次版本行上，不回写 screen。

`version_count` / `last_uploaded_at` 在同一事务内由 `COUNT(*)` 与本次 `uploaded_at` 重算。

并发首传同一新 `screen_key` 时，两边都会错过 `FindScreenByKey` 并在 `CreateScreen` 撞唯一索引（多连接下还可能撞 SQLITE_LOCKED/SQLITE_BUSY_SNAPSHOT 锁错误）。上传事务按 `LogRepository.UploadRunTx` 的惯例做有界重试（最多 5 次、线性退避 20ms）：失败事务已整体回滚，重跑时 `FindScreenByKey` 命中对方已提交的行即归并追加版本。重试上限内仍失败才返回 ErrInternal 并删除已落盘文件。

## 校验

mime 以内容嗅探为准：先读 512 字节 `http.DetectContentType`，仅收 `image/png` / `image/jpeg` / `image/webp` / `image/gif`，不信任文件名与客户端声明。大小沿用 `cfg.Upload.MaxFileSize`：handler 先 `http.MaxBytesReader(max + 1MB 余量)` 限制整个 multipart 体（超限 413，防止超大 body 全部落临时盘），service 再 `LimitReader(max+1)` + 计数 reader，超限文件写完后删除并返回 40001（与 `service/issue_assets.go` 同模式）。非法类型/大小/参数均 40001。

## PATCH 语义

`UpdateScreenshotScreenRequest` 两个字段均为指针：出现才更新，未出现保持原值；`group` 显式空串 = 未分组，`title` 显式空串 = 清空回退显示 `screen_key`。两字段都不传返回 40001。成功后返回与 GET detail 相同的形状（screen + `versions` 倒序数组）。

「最新版本」统一按 rowid（插入顺序）判定：列表缩略图、详情首版本、删版本后重算 `last_uploaded_at` 共用同一口径；`uploaded_at` 取自事务开始时间，不用于排序。

注意：rowid 是 SQLite 隐式列，`screenshot_versions` 主键为 text，没有 `INTEGER PRIMARY KEY`——VACUUM、dump 导入等重建表的操作会重排 rowid。代码路径里不做这类操作；若未来需要跨运维操作保持版本顺序，应改为显式自增序号列。

## 删除语义

- 删版本：DB 行删除后删磁盘文件；删到 screen 最后一个版本时同事务连带删除 screen（空壳界面不留）。
- 删 screen：同一事务内先收集全部版本 `file_path`、再删版本行 + screen 行（事务内收集保证并发上传的新版本行要么被一并删掉文件，要么整个删除失败，不会留孤儿文件），提交后逐个删文件；FK `OnDelete: CASCADE` 兜底项目级联。
- 版本删除后 `version_count` / `last_uploaded_at` 按剩余版本重算（`last_uploaded_at` 取最新剩余版本的 `uploaded_at`）。

## 响应形状

```
Screen  = {id, project_id, screen_key, title, group, version_count, last_uploaded_at, created_at}
Version = {id, screen_id, note, file_name, file_size, mime_type, uploaded_by, uploaded_at, content_url}
```

`content_url` = `/api/screenshot-versions/{id}/content`，前端自行追加 `?token=`。list 每项额外带 `latest_version`（Version 或 null）；detail / PATCH 响应的 screen 额外带 `versions` 数组。上传返回 `{screen, version}`。

## 前端预览交互

预览弹窗 `web/src/components/screenshots/screenshot-lightbox.tsx`，由截图列表页以「过滤后的导航列表 + 当前 screenId」驱动；弹窗自身按 screenId 拉详情，导航只改 screenId。

- **翻页区**：图片区左右各一条固定宽（w-16/sm:w-20）全高点击条，悬停整条高亮、居中箭头；到首尾时对应侧变暗禁用，不循环。两条始终渲染（含加载中、对比模式），不随内容分支卸载——这是翻页失灵的修复点：旧实现把按钮挂在「详情已加载」分支里，切到未缓存界面时骨架屏把它一起卸掉。
- **加载中保留旧图**：记录上一份成功加载的 detail；新界面请求期间继续显示旧界面内容（含用户当时选中的版本/对比侧），顶部叠「加载中」角标（isFetching 驱动）；版本与对比选择在新详情到达时才重置。旧图回退仅限同一次打开期间的翻页——弹窗关闭即清空，重新打开未缓存界面时只显示骨架屏。只有从未加载过任何详情时才整块骨架屏。
- **预取**：`prefetchQuery` 对 navIndex±1 的界面拉详情，queryKey/queryFn 与 `useScreenshotScreen` 相同（`["screenshots","detail",id]`）。
- **放大**（仅单图）：点击或 Enter/Space 在「适配 / 放大」两档间切换，无滚轮缩放/捏合/百分比。放大宽度 = `naturalWidth ÷ devicePixelRatio`，下限为适配宽度 2 倍；以点击点为中心（按点击处归一化坐标回算 scroll，图片未就绪时等 onLoad 补算；尺寸未知不进入放大）。放大态是 `overflow:auto` 容器 + 显式宽度 img，触控板/滚轮/Shift+滚轮横移走原生滚动；鼠标拖拽手动滚，按下点起累计位移 >4px 不算点击。切界面/版本、进出对比、关弹窗都重置回适配。
- **键盘**：←/→ 始终翻页（含对比、放大态）；图片可聚焦（role=button），Enter/Space 以图中心放大，分支切换时焦点迁往新图。Esc 在放大态先退放大（document capture 阶段拦截，preventDefault+stopPropagation，避免 Dialog 同步收 Esc 关闭），再按才关弹窗。下拉/菜单/确认框打开或焦点在输入框时不响应——Base UI 关闭的浮层保留在 `[hidden]` 容器里，判定"浮层开着"只看未隐藏的节点。
- **对比模式**：跨界面翻页保持对比，左默认最新、右默认次新，单版本时两侧同图；不支持放大，不显示版本缩略图条。两个 pane 顶部控件行 z-20 压在翻页区之上，底部各自保留文件信息。
- **布局**：弹窗铺满视口留 16px（`h/w-[calc(100dvh|vw-2rem)]`），版本文件信息并入标题栏第二行，无独立底栏；版本缩略图条悬浮在图区底部、仅多版本时出现，两侧各留 5rem 不伸进翻页区；图像显示区无内边距（对比模式每个 pane 内的文件说明除外）。
- **触屏**：不做滑动/捏合，点翻页区翻页、点图切放大。
