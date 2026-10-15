# 截图库（Screenshots）

项目级截图库：批量上传应用各页面截图，按「界面标识 + 平台/语言/亮度」定位一张截图，同变体再上传即覆盖。前端在 `web/`（本文件不展开），本文档记录后端数据模型、端点、权限分工、覆盖语义与迁移。

## 数据模型

三张表，GORM `AutoMigrate` 注册（`server/cmd/server/main.go`），无外置迁移工具。除 `screenshot_screens`、`screenshot_annotations`（见 Issue 一节）外，`screenshot_pending_deletes` 是待删文件队列：迁移折叠、覆盖上传、删除截图提交后清理旧文件失败（非 ENOENT）时把路径入队，每次启动出队重试删除，文件已不存在视同清理成功——排障「磁盘上为什么还留着截图文件」先查这张表。

### `screenshot_screens`

一张截图 = 一行。`(project_id, screen_key, platform, locale, theme)` 唯一——空维度与空维度相等，由 main.go 的 COALESCE 表达式唯一索引 `idx_screenshot_screens_variant` 保证；旧 `(project_id, screen_key)` 唯一索引在迁移中删除（同一 screen_key 的多维度变体合法）。

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | TEXT | 主键 |
| `project_id` | TEXT | FK → `projects`，`OnDelete: CASCADE` |
| `screen_key` | TEXT | 服务端规范化值（`ToLower` + `TrimSpace`），1–100 字符 |
| `title` | TEXT | 可空显示名；空时客户端回退显示 `screen_key` |
| `platform` | TEXT 可空 | 平台，开放词表（trim+lowercase，≤100 字符）；NULL = 未标注 |
| `locale` | TEXT 可空 | 语言，BCP 47 规范化（语言小写、4 字母 script 首字母大写、2 字母地区大写，≤35 字符，非法值 40001）；NULL = 未标注 |
| `theme` | TEXT 可空 | 亮度，严格 `light`/`dark`（大小写敏感，其他值 40001）；NULL = 未标注 |
| `file_path` | TEXT | 存储相对路径，`json:"-"` 永不外泄 |
| `mime_type` | TEXT | 内容嗅探结果（非客户端声明） |
| `file_size` | INTEGER | 字节数 |
| `width`/`height` | INTEGER | 原图像素宽高；解码失败为 0（未知，展示时惰性解码文件头兜底） |
| `uploaded_by` | TEXT | JWT 用户名 /「Web 用户」/「API Key: <key名>」 |
| `uploaded_at` | DATETIME | 当前文件的上传时间；覆盖上传即更新，列表排序键 |
| `created_at` | DATETIME | |

文件落在 `storage.Storage` 下 `<project_id>/screenshots/<screen_id>-<fileUUID>.<ext>`；扩展名由嗅探出的 mime 推导。每次上传写全新路径，事务内 rename 切换，提交后删被替换的旧文件——读者不会看到半截文件，事务失败只删新文件、不碰旧文件。

### `screenshot_annotations`

「矩形框 + 文字」标注，直接挂在截图（screen_id）上；供画布上的评审意见与 AI 闭环使用。

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | TEXT | 主键 |
| `screen_id` | TEXT | FK → `screenshot_screens`，`OnDelete: CASCADE` |
| `project_id` | TEXT | FK → `projects`，`OnDelete: CASCADE`；冗余便于按项目过滤 |
| `issue_id` | TEXT 可空 | FK → `issues`，`OnDelete: SET NULL`；删 Issue 只解除关联，标注保留 |
| `x`/`y`/`width`/`height` | REAL | 框选矩形，**相对图片宽高的比例（0~1）**，与原图分辨率无关 |
| `body` | TEXT | 标注文字，1–1000 字符（rune 计） |
| `status` | TEXT | `open` / `resolved`，默认 `open` |
| `outdated` | BOOLEAN | 截图被覆盖更新后仍未「确认保留」的 open 标注置位；resolved 不置位 |
| `created_by` | TEXT | 同 `uploaded_by` 口径 |
| `created_at`/`updated_at`/`resolved_at` | DATETIME | `resolved_at` 仅 resolved 时有值 |

坐标语义：存比例不存像素，展示/裁剪时按原图尺寸换算（`annotationPixelRect`，四边缘取整；亚像素矩形塌成空矩形时保底 1px 且不丢位置，最后与图边界相交）。坐标系以**浏览器展示方向**为准：JPEG 带 EXIF Orientation 时，`probeScreenshotDims`/`decodeScreenshotImageConfig` 返回应用方向后的尺寸（5-8 交换宽高），裁剪解码同样先 `applyJPEGExifOrientation` 转正再换算，保证画布置框、落库尺寸与裁剪像素三者同源。`Update` 为指针语义：字段出现才更新，`issue_id` 空串 = 解除关联，都不传 40001；置 resolved 会顺带清 `outdated`。

## 覆盖上传与过期标记

`(project_id, screen_key, platform, locale, theme)` 已存在时上传 = 覆盖：替换文件、更新宽高/大小/上传时间/上传者，不新增行；不存在则新建。并发首传同一新变体两边都会错过 `FindScreenByVariant` 并撞表达式唯一索引，上传事务按 `LogRepository.UploadRunTx` 的惯例做有界重试（最多 5 次、线性退避 20ms）；重试上限内仍失败才返回 ErrInternal 并清理已落盘文件。

覆盖后该截图上全部 `open` 标注置 `outdated=true`（「确认保留」清零；再次覆盖重新置位）；`resolved` 标注不受影响。表单维度字段沿用 presence 语义：字段出现才生效（空串 = 清除为未标注），不出现按「保持原值」继承——按已给出的维度过滤同 `screen_key` 既有截图，恰有一个命中即继承其缺席维度并覆盖该行，命中多个变体（省略无法定位唯一目标）返回 40001，零个命中按缺席维度新建；`title` 非空才覆盖。

## 迁移（INT-99）

`server/cmd/server/migrate_screenshots.go`，随服务启动自动执行，幂等可重复（versions 表消失后折叠跳过，但待删队列的出队重试每次启动照常进行——清理失败的孤儿文件靠它最终删除）：

- 每 screen 折叠为其最新版本（rowid 口径）的文件信息；
- `group_name` 按 Go 侧与服务端写入相同的 `NormalizeScreenshotPlatform` 规则迁入 `platform`（trim+lowercase，空 → NULL）：合规值照常迁入；不合规值（含逗号、等于未标注哨兵 `__none__`、超 100 字符）platform 保持 NULL，原分组名追加进 `title`（title 为空则直接作为 title），分组信息不静默丢弃；规范化后为空的纯非空格空白值（如 `\n`，SQLite trim 只去空格）按未标注跳过；
- 挂在非最新版本上的 open 标注置 `outdated=true`；
- 删除旧版本行与 `screenshot_versions` 表；`screenshot_annotations.version_id` 列连同指向 versions 的外键经子表重建移除（顺序关键：标注表必须先摆脱 CASCADE 外键，否则 DROP TABLE 的隐式 DELETE 会连带删标注）；截图表的 `version_count`/`group_name`/`last_uploaded_at` 遗留列一并删除；
- 事务提交后删除被折叠的旧版本文件。
- `--dry-run` 启动标志打印将删版本数等统计后退出；正式执行把同样统计写日志。

## 端点与权限

| 方法 | 路径 | 凭证 | 说明 |
|---|---|---|---|
| POST | `/api/projects/:pid/screenshots` | JWT / API Key | multipart 上传：`file`、`screen_key` 必填，`title`/`platform`/`locale`/`theme` 可选；响应 `{screen}` |
| GET | `/api/projects/:pid/screenshots` | JWT / API Key | `{items: [...]}`，query `platform`/`locale`/`theme`（逗号多值，`__none__` = 未标注桶）过滤，按 `uploaded_at` 倒序 |
| PATCH | `/api/screenshot-screens/:sid` | **仅 JWT** | `{"title"?, "platform"?, "locale"?, "theme"?}` 指针语义（维度空串清除）；改出的变体组合撞唯一约束 → 409（40921） |
| DELETE | `/api/screenshot-screens/:sid` | **仅 JWT** | 删截图行 + 磁盘文件，标注级联 |
| GET / HEAD | `/api/screenshot-screens/:sid/content` | JWT / API Key / `?token=` | 输出图片字节，`Content-Type` 用存储的 `mime_type`，`Content-Disposition: inline` |
| GET | `/api/projects/:pid/screenshot-annotations` | JWT / API Key | `{items:[...]}`，query `status`/`outdated`/`issue_id`/`screen_id` + `platform`/`locale`/`theme`（逗号多值）过滤 |
| POST | `/api/projects/:pid/screenshot-annotations/batch` | **仅 JWT** | `{action:"confirm"|"resolve", filter:{platform?,locale?,theme?,status?,outdated?,screen_id?}}`，作用于过滤集（两者都只作用于 open 标注），返回 `{updated}`；API Key 无批量能力（40301） |
| POST | `/api/screenshot-screens/:sid/annotations` | **仅 JWT** | `{x,y,width,height,body,issue_id?}`；比例坐标校验 0≤x,y、0<w,h、x+w/y+h≤1 |
| PUT | `/api/screenshot-annotations/:aid` | JWT / API Key | 指针语义更新 body/status/issue_id；**API Key 只允许整包 `{"status":"resolved"}`**（其他字段或重开 40301） |
| DELETE | `/api/screenshot-annotations/:aid` | **仅 JWT** | 删标注行 |
| GET / HEAD | `/api/screenshot-annotations/:aid/crop` | JWT / API Key / `?token=` | 输出框选区域裁剪图，统一 PNG，外边距 = 矩形短边 15%（最小 8px，不出图界）；原图超过解码像素上限（约 64MP）返回 40004 |

写操作 PATCH/DELETE/batch/创建标注走 `RequireJWT` 分组（API Key 返回 40301），上传与读走 `RequireAuth`，content 与 crop 挂 `RequireAuthWithQueryToken(cfg, apiKeyRepo, authService, "token")` 支持 `<img>` 直链。标注 PUT 两类凭证都进 service，由 service 按 `!middleware.IsJWTAuth(c)` 再收紧 API Key 只允许置 resolved——与 issue 内嵌写操作的惯例一致（路由放通、service 判凭证）。

归属校验沿 `screen → project → user_id` 链：项目不属于当前用户时 screen/annotation 一律按「不存在」返回，不区分「不存在」与「无权限」。错误码：`ErrScreenshotScreenNotFound`（40413）、`ErrScreenshotAnnotationNotFound`（40416）、`ErrScreenshotVariantExists`（40921）；参数/格式/大小非法用 `ErrInvalidParams`（40001）。

Issue 详情响应内嵌 `screenshot_annotations`（`IssueService.Get`，无关联时缺省）；项目级列表与 Issue 内嵌共用 `assembleScreenshotAnnotations` 组装：界面行经 `ScreenshotAnnotationRepository.LoadRelated` 批量取回，`issue_id → Issue` 批量查 `issueRepo.ListByIDs`（含 GitHubMeta 供 `issue_reference` 生成）。`image_width`/`image_height` 优先取截图落库值，存量行（0）惰性解码文件头，仍失败输出 0 且 `pixel_rect=null`。

裁剪图（`Crop`）：解码前先按文件头尺寸做像素预算校验（`maxScreenshotDecodePixels` ≈ 64MP，超限 40004 `ErrScreenshotImageTooLarge`——压缩体积上限管不住解码后内存），且有双闸限并发：个数闸 4 并发 + 字节闸按像素加权（png/webp 权=像素×8，jpeg/gif 权=像素×12——EXIF 重排与偏移帧合成都会在解码源存活期间再占一张全尺寸 RGBA；16 位 PNG 解码为 RGBA64 且输出同为 16 位，权=像素×16，由 `isPNG16Bit` 探文件头判定；总量封顶 1GiB——小截图并行、64MP 大图独占排队，并发大裁剪不会叠出 ~2GiB）；`Crop` 返回 release 闭包，handler 写完 PNG 响应后才释放，解码与已编码缓冲的驻留期都计入上限，慢客户端不会绕过限制堆积大响应体；按存储 mime 分派解码——png/webp 流式单遍；jpeg/gif 各开两遍流（jpeg 先 `jpeg.Decode` 像素再重开扫 EXIF 头；gif 先 `gif.DecodeConfig` 取逻辑画布再重开 `gif.Decode` 只解第一帧并按偏移合成——`DecodeAll` 会把全部帧解码进内存，多帧大图能绕过单帧预算；首帧 Bounds 可能是画布上的偏移子块），不 `ReadAll` 整文件，大元数据段（GIF 注释、JPEG ICC）不驻留内存；`SubImage` 取样后统一 `png.Encode` 输出，不落盘不缓存。JPEG 的尺寸/EXIF 探测走 `jpegScanHeader` 逐段跳到 SOF（跳过非目标段负载，容忍 0xFF 填充），不整文件入内存。

## 上传语义

`screen_key` 服务端规范化：`strings.ToLower` + `strings.TrimSpace`，规范化后 1–100 字符（rune 计），否则 40001。变体维度规范化见数据模型一节。

表单字段存在性语义（service 层 `*string` 表达存在性）：

- `platform`/`locale`/`theme` 字段出现 → 规范化后写入（空串 = 清除为 NULL）；未携带则按「保持原值」继承（`resolveUploadVariants`）：按已给出的维度过滤同 `screen_key` 既有截图，恰有一个命中即继承其缺席维度并覆盖该行，命中多个变体返回 40001（省略无法定位唯一目标），零个命中按缺席维度新建（全新 `screen_key` 即未标注新行）。
- `title` 非空才覆盖 `screen.title`；空串/未携带保持不变（上传表单无法清空 title，清空走 PATCH）。

文件先落临时路径（`tmp-<uuid>.<ext>`），事务内确认 screen 后 rename 到正式路径（放在 DB 写之后、提交之前：DB 失败不动磁盘，rename 失败事务回滚）；提交成功后删被替换的旧文件，改名后提交失败由调用方删除孤儿新文件。

## 校验

mime 以内容嗅探为准：先读 512 字节 `http.DetectContentType`，仅收 `image/png` / `image/jpeg` / `image/webp` / `image/gif`，不信任文件名与客户端声明。大小沿用 `cfg.Upload.MaxFileSize`：handler 先 `http.MaxBytesReader(max + 1MB 余量)` 限制整个 multipart 体（超限 413，防止超大 body 全部落临时盘），service 再 `LimitReader(max+1)` + 计数 reader，超限文件写完后删除并返回 40001（与 `service/issue_assets.go` 同模式）。非法类型/大小/参数均 40001。

## PATCH 语义

`UpdateScreenshotScreenRequest` 四个字段均为指针：出现才更新，未出现保持原值；三个维度显式空串 = 清除为未标注，title 显式空串 = 清空回退显示 `screen_key`。全不传返回 40001。成功后返回更新后的截图。改出的 `(screen_key, platform, locale, theme)` 组合与项目内已有截图撞变体唯一索引时返回 409（40921）——同值更新（自己撞自己）不误报。

## 删除语义

- 删截图：DB 行删除（标注由 FK `OnDelete: CASCADE` 一并删除）后删磁盘文件。
- 删 Issue：标注保留，仅 `issue_id` 置空（FK `OnDelete: SET NULL`）。
- FK 兜底项目级联：删项目连截图与标注一起消失。

## 响应形状

```
Screen     = {id, project_id, screen_key, title, platform, locale, theme,
              mime_type, file_size, width, height, uploaded_by, uploaded_at,
              created_at, content_url}
Annotation = {id, project_id, screen_id, issue_id, issue_reference, issue_title,
              screen_key, screen_title, screen_platform, screen_locale, screen_theme,
              image_url, image_width, image_height, outdated,
              x, y, width, height, pixel_rect, body, status, crop_url,
              created_by, created_at, updated_at, resolved_at}
```

`content_url` = `/api/screenshot-screens/{sid}/content?v=<uploaded_at 纳秒值>`（v 随文件变化：覆盖上传推进 uploaded_at 即换 URL，浏览器缓存按完整 URL 记键自然失效，画布占位重置与草稿作废也拿它当换图指纹），`image_url` 同形（标注所在截图），`crop_url` = `/api/screenshot-annotations/{id}/crop?v=<uploaded_at 纳秒值>`（与 content_url 同一个 v，覆盖上传后旧裁剪图的浏览器缓存随之失效）；前端自行追加 `&token=`。上传返回 `{screen}`；批量返回 `{updated}`。

## 前端画布与标注

`/screenshots` 直达画布（旧链接 `?view=` 忽略、`?annotation=` 深链保留：数据就绪后一次性放宽把目标截图过滤掉的维度、并把目标标注的状态并入 status 过滤，再交画布定位；目标真正可定位后才消费深链请求）。URL 无 status 参数 = 默认只看未解决；状态筛选不允许清空到空集（清空即回到默认 open）。画布视图 `web/src/components/screenshots/canvas/`（`screenshot-canvas.tsx` 为入口）：自研 CSS transform 视口（`use-canvas-viewport.ts`），无 tldraw/Konva 依赖；排版为纯函数（`lib/screenshot-canvas.ts`）。

- **筛选条**：工具行的 platform/locale/theme/status 多选（`dimension-multi-select.tsx`，选项由全量数据推导 + 计数，维度含「未标注」桶 `__none__`）；筛选写入 URL（逗号分隔，`status` 缺省 `open`），作用于画布、标注面板、批量动作与提示词范围；面板的状态 tab 与 URL 状态过滤双向同源。
- **分区与聚类**：画布按 platform 分区（NULL = 「未标注」区，固定最后；命名平台按区内最近上传倒序）；区内同 `screen_key` 的 locale/theme 变体相邻成簇，行首能整簇放下时不拆行，簇内按 locale/theme 字典序。
- **卡片徽标**：locale、亮度（亮色/暗色）、「N 条待确认」（outdated open 标注数）；元信息行显示相对上传时间（版本概念已删）。
- **平移/缩放**：拖空白、Space+拖、中键、单指触摸平移；滚轮与 ctrl+滚轮（触控板捏合）以指针为锚缩放，双指捏合同理；缩放范围 0.05–4。平移手势位移 >4px 后吞掉随后的 click，避免拖拽误触卡片/标注。
- **自动列数**：`pickCanvasColumns` 在 2–24 列内逐个试排，取「适应全部」缩放率最大的列数（缩放率相同取更少的列）；容器未量到尺寸时回退 4 列。布局在容器尺寸、截图集合或实测宽高比变化后重算，输入不变时布局对象不变。
- **视口剔除与分批挂载**：只渲染与视口（外扩一屏）相交的卡片；`<img>`（`loading="lazy"` + `decoding="async"`）分批挂载——前 30 张即时放行，之后每 80ms 放行 30 张，放行集合按截图 id 记录、归位到 resetKey（项目|筛选）。无缩略图，直接用 content 原图；覆盖上传换图（content_url 变化）后重新占位。
- **框选标注**：工具栏或 `R` 键切框选态（十字光标），在卡片图片区拖出矩形即按比例换算（与画布缩放无关），松开弹出文字输入（`annotation-draft.tsx`）；过小的框（<1% 边长）视为误触忽略。草稿只认画上去的那张截图：截图被覆盖（content_url 变化）或被过滤出画布时草稿自动取消。
- **叠加层**：标注矩形按比例定位在图片盒内，状态范围由 URL 筛选决定（缺省只画 open）；面板点选/悬停与画布矩形双向联动（hover 高亮、定位闪烁 2.4s）。矩形几何按比例随图缩放，描边宽度、序号角标、选中/闪烁光圈等「皮肤」尺寸保持屏幕恒定：世界容器写 `--canvas-zoom`（`CANVAS_ZOOM_VAR`），世界缩放用 `scale: var(--canvas-zoom)` 渲染、皮肤用 `calc(Npx / var(--canvas-zoom, 1))` 反算同一变量；描边由 SVG `<rect>` 叠加层绘制，不参与盒模型。
- **右侧面板**（`annotation-panel.tsx`）：列当前范围内全部状态范围内标注（新的在上）；头部提供「全部确认保留 / 全部标记已解决」（作用于当前维度筛选范围，走 batch API）；条目支持编辑文字、解决/重开、删除（AlertDialog）、改 Issue 关联（Dialog 内嵌搜索框，走 `useIssues` 的 `q` 服务端过滤）；outdated 条目带「待确认」徽标。「定位」平移聚焦卡片并闪烁。
- **空状态**：画布中央「上传截图」按钮；项目无截图时同理。
- **视口恢复**：筛选/数据刷新让全部卡片挪出视口时自动「适应全部」；「适应全部」给浮动工具栏预留顶部净空（ResizeObserver 实测高度 + 间距）。分区标题统一渲染为悬浮标签，缩放低于约 0.58 时反向放大保持 ≥14px 可读。
- **预览弹窗**：`screenshot-lightbox.tsx` 由筛选后的导航列表 + 当前 screenId 驱动（无独立详情请求——列表项即全量数据）；适配/放大两档（放大宽度 = naturalWidth ÷ dpr，以点击点为中心），←/→ 翻页，Esc 先退放大再关弹窗；标注只读叠加层；编辑对话框（标题 + 三维度）与删除截图（确认框提示级联标注数）入口保留，版本相关 UI（版本切换/对比/删除版本）已随版本概念删除。
- **Issue 详情**：`issue-screenshot-annotations-card.tsx` 渲染 `issue.screenshot_annotations`（裁剪图缩略 + 文字 + 状态），点击跳 `/screenshots?project=<pid>&annotation=<id>`。

## 处理标注意见（提示词与启动）

- 画布工具栏固定入口：「启动线程」「复制提示词」；范围内 open 为 0 时启动按钮置灰，复制不确认，启动总是先经弹框内二次确认（文案含数量）。
- 固定中文模板单处维护于 `web/src/lib/annotation-prompt.ts`（`buildAnnotationPrompt`，含参数填充单测）：结构 = 正文 + 结尾 `>` 引用块（同 `buildIssuePrompt`），指引使用 fast-ship 技能（附安装命令），不内联凭证；参数：project_id、三维过滤（描述 + API query 串，恒定 `status=open`）、open/resolved 计数。
- 模板指示代理调标注列表 API（带过滤 + `status=open`）逐条处理并置 resolved；不碰范围外标注；不提交/开 PR；汇报已处理/未处理/需确认。
- 启动对话框 `launch-annotation-thread-dialog.tsx` 复用 `useThreadLaunch`（`launch-issue-thread-dialog` 同款环境/模型/工作区机制，模板选择与偏好持久化留在 Issue 弹框），提示词预览只读；失败给明确提示并可退回复制提示词。
