# 截图库（Screenshots）

项目级截图库：批量上传应用各页面截图，按 group 自由分组浏览，同一界面保留全部历史版本供对比。前端在 `web/`（本文件不展开），本文档记录后端数据模型、端点、权限分工与上传语义。

## 数据模型

三张表，GORM `AutoMigrate` 注册（`server/cmd/server/main.go`），无外置迁移工具。唯一索引 `(project_id, screen_key)` 由 main.go 手工索引段建立（`idx_screenshot_screens_project_key`）。

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

`screenshot_versions` 还带 `width` / `height`（INTEGER，默认 0）：上传落盘后用 `probeScreenshotDims` 重开文件解码图片头部回填，解码失败不阻塞上传，0 表示未知（存量版本同语义）。

### `screenshot_annotations`

「矩形框 + 文字」标注，挂在具体版本上（不是 screen 聚合）；供画布上的评审意见与 AI 闭环使用。

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | TEXT | 主键 |
| `version_id` | TEXT | FK → `screenshot_versions`，`OnDelete: CASCADE` |
| `screen_id` | TEXT | FK → `screenshot_screens`，`OnDelete: CASCADE`；由版本冗余带出，便于按界面过滤 |
| `project_id` | TEXT | FK → `projects`，`OnDelete: CASCADE`；同上冗余 |
| `issue_id` | TEXT 可空 | FK → `issues`，`OnDelete: SET NULL`；删 Issue 只解除关联，标注保留 |
| `x`/`y`/`width`/`height` | REAL | 框选矩形，**相对图片宽高的比例（0~1）**，与原图分辨率无关 |
| `body` | TEXT | 标注文字，1–1000 字符（rune 计） |
| `status` | TEXT | `open` / `resolved`，默认 `open` |
| `created_by` | TEXT | 同 `uploaded_by` 口径 |
| `created_at`/`updated_at`/`resolved_at` | DATETIME | `resolved_at` 仅 resolved 时有值 |

坐标语义：存比例不存像素，展示/裁剪时按原图尺寸换算（`annotationPixelRect`，四边缘取整；亚像素矩形塌成空矩形时保底 1px 且不丢位置，最后与图边界相交）。坐标系以**浏览器展示方向**为准：JPEG 带 EXIF Orientation 时，`probeScreenshotDims`/`decodeScreenshotImageConfig` 返回应用方向后的尺寸（5-8 交换宽高），裁剪解码同样先 `applyJPEGExifOrientation` 转正再换算，保证画布置框、落库尺寸与裁剪像素三者同源。`Update` 为指针语义：字段出现才更新，`issue_id` 空串 = 解除关联，都不传 40001。

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
| GET | `/api/projects/:pid/screenshot-annotations` | JWT / API Key | `{items:[...]}`，query `status`/`issue_id`/`screen_id` 过滤；含旧版本上的标注 |
| POST | `/api/screenshot-versions/:vid/annotations` | **仅 JWT** | `{x,y,width,height,body,issue_id?}`；比例坐标校验 0≤x,y、0<w,h、x+w/y+h≤1 |
| PUT | `/api/screenshot-annotations/:aid` | JWT / API Key | 指针语义更新 body/status/issue_id；**API Key 只允许整包 `{"status":"resolved"}`**（其他字段或重开 40301） |
| DELETE | `/api/screenshot-annotations/:aid` | **仅 JWT** | 删标注行 |
| GET / HEAD | `/api/screenshot-annotations/:aid/crop` | JWT / API Key / `?token=` | 输出框选区域裁剪图，统一 PNG，外边距 = 矩形短边 15%（最小 8px，不出图界）；原图超过解码像素上限（约 64MP）返回 40004 |

写操作 PATCH/DELETE 走 `RequireJWT` 分组（API Key 返回 40301），上传与读走 `RequireAuth`，content 与 crop 挂 `RequireAuthWithQueryToken(cfg, apiKeyRepo, authService, "token")` 支持 `<img>` 直链。标注 PUT 两类凭证都进 service，由 service 按 `!middleware.IsJWTAuth(c)` 再收紧 API Key 只允许置 resolved——与 issue 内嵌写操作的惯例一致（路由放通、service 判凭证）。

归属校验沿 `screen → project → user_id` 链：项目不属于当前用户时 screen/version 一律按「不存在」返回，不区分「不存在」与「无权限」。错误码：`ErrScreenshotScreenNotFound`（40413）、`ErrScreenshotVersionNotFound`（40414）、`ErrScreenshotAnnotationNotFound`（40416）；参数/格式/大小非法用 `ErrInvalidParams`（40001）。

Issue 详情响应内嵌 `screenshot_annotations`（`IssueService.Get`，无关联时缺省）；项目级列表与 Issue 内嵌共用 `assembleScreenshotAnnotations` 组装：关联行（版本/界面/最新版本）经 `ScreenshotAnnotationRepository.LoadRelated` 批量取回，`issue_id → Issue` 批量查 `issueRepo.ListByIDs`（含 GitHubMeta 供 `issue_reference` 生成）。`image_width`/`image_height` 优先取版本落库值，存量行（0）惰性解码文件头，仍失败输出 0 且 `pixel_rect=null`。

裁剪图（`Crop`）：解码前先按文件头尺寸做像素预算校验（`maxScreenshotDecodePixels` ≈ 64MP，超限 40004 `ErrScreenshotImageTooLarge`——压缩体积上限管不住解码后内存），且有双闸限并发：个数闸 4 并发 + 字节闸按像素加权（权=像素×8，RGBA 解码源与 PNG 输出各估一份，总量封顶 512MiB——小截图并行、64MP 大图独占排队，并发大裁剪不会叠出 ~2GiB）；`Crop` 返回 release 闭包，handler 写完 PNG 响应后才释放，解码与已编码缓冲的驻留期都计入上限，慢客户端不会绕过限制堆积大响应体；按存储 mime 分派解码——png/webp 流式单遍；jpeg/gif 各开两遍流（jpeg 先 `jpeg.Decode` 像素再重开扫 EXIF 头；gif 先 `gif.DecodeConfig` 取逻辑画布再重开 `gif.Decode` 只解第一帧并按偏移合成——`DecodeAll` 会把全部帧解码进内存，多帧大图能绕过单帧预算；首帧 Bounds 可能是画布上的偏移子块），不 `ReadAll` 整文件，大元数据段（GIF 注释、JPEG ICC）不驻留内存；`SubImage` 取样后统一 `png.Encode` 输出，不落盘不缓存。JPEG 的尺寸/EXIF 探测走 `jpegScanHeader` 逐段跳到 SOF（跳过非目标段负载，容忍 0xFF 填充），不整文件入内存。

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

- 删版本：DB 行删除后删磁盘文件；删到 screen 最后一个版本时同事务连带删除 screen（空壳界面不留）。挂在版本上的标注由 FK `OnDelete: CASCADE` 一并删除（前端确认框会提示「将同时删除 N 条未解决标注」）。
- 删 screen：同一事务内先收集全部版本 `file_path`、再删版本行 + screen 行（事务内收集保证并发上传的新版本行要么被一并删掉文件，要么整个删除失败，不会留孤儿文件），提交后逐个删文件；FK `OnDelete: CASCADE` 兜底项目级联，标注同样级联。
- 删 Issue：标注保留，仅 `issue_id` 置空（FK `OnDelete: SET NULL`）。
- 版本删除后 `version_count` / `last_uploaded_at` 按剩余版本重算（`last_uploaded_at` 取最新剩余版本的 `uploaded_at`）。

## 响应形状

```
Screen  = {id, project_id, screen_key, title, group, version_count, last_uploaded_at, created_at}
Version = {id, screen_id, note, file_name, file_size, mime_type, uploaded_by, uploaded_at, width, height, content_url}
Annotation = {id, project_id, screen_id, version_id, issue_id, issue_reference, issue_title,
              screen_key, screen_title, screen_group, image_url, image_width, image_height,
              is_latest_version, x, y, width, height, pixel_rect, body, status, crop_url,
              created_by, created_at, updated_at, resolved_at}
```

`content_url` = `/api/screenshot-versions/{id}/content`，`image_url` 同形（取标注所在版本），`crop_url` = `/api/screenshot-annotations/{id}/crop`；前端自行追加 `?token=`。list 每项额外带 `latest_version`（Version 或 null）；detail / PATCH 响应的 screen 额外带 `versions` 数组。上传返回 `{screen, version}`。

## 前端画布与标注

画布视图 `web/src/components/screenshots/canvas/`（`screenshot-canvas.tsx` 为入口）：自研 CSS transform 视口（`use-canvas-viewport.ts`），无 tldraw/Konva 依赖；排版为纯函数（`lib/screenshot-canvas.ts`，按分组分区、组内按 `last_uploaded_at` 倒序、固定 4 列网格，位置不持久化）。

- **平移/缩放**：拖空白、Space+拖、中键、单指触摸平移；滚轮与 ctrl+滚轮（触控板捏合）以指针为锚缩放，双指捏合同理；缩放范围 0.05–4。平移手势位移 >4px 后吞掉随后的 click，避免拖拽误触卡片/标注。
- **视口剔除**：只渲染与视口（外扩一屏）相交的卡片；卡片屏幕宽 <48px 时只画占位不加载 `<img>`。无缩略图，直接用 content 原图。
- **框选标注**：工具栏或 `R` 键切框选态（十字光标），在卡片图片区拖出矩形即按比例换算（与画布缩放无关），松开弹出文字输入（`annotation-draft.tsx`）；过小的框（<1% 边长）视为误触忽略。只允许在最新版本上创建——画布本就只有最新版本。
- **叠加层**：标注矩形按 `x/y/w/h` 百分比定位在图片盒内；画布上默认只显示 open，工具栏可切「显示已解决」；面板点选/悬停与画布矩形双向联动（hover 高亮、定位闪烁 2.4s）。
- **右侧面板**（`annotation-panel.tsx`）：列当前画布范围内全部标注（含旧版本上的，带「旧版本」标），默认未解决 tab；支持编辑文字、解决/重开、删除（AlertDialog）、改 Issue 关联（Dialog 内嵌搜索框，走 `useIssues` 的 `q` 服务端过滤，不受前 100 条限制；当前关联不在结果里时单独补项）。「定位」：最新版本标注 → 画布聚焦该卡片并闪烁；旧版本标注 → 打开预览弹窗直接选中该版本并高亮该标注。
- **URL 定位**：`?view=canvas&annotation=<id>`；页面先把分组 tab 与搜索放宽到能看到目标界面，再交画布执行定位（等标注数据**当次拉取落地**与容器尺寸就绪，nonce 去重；`isSuccess` 命中过期缓存时不判定存在性）。
- **视口恢复**：搜索/数据刷新让全部卡片挪出视口时自动「适应全部」，避免整屏空白；只在布局对象变化时评估，手动平移到空白处不触发。标注草稿只认画上去的那个版本，界面换版或被过滤出画布时草稿自动取消。
- **旧版本提示**：卡片元信息行显示「上一版有 N 条未解决」徽标（`countOpenOnOlderVersions`）。
- **预览弹窗**：`AnnotationOverlay` 只读叠加（open/resolved 都显示，不可交互），量 img 实际显示盒贴上去；适配/放大/对比两侧都挂。`initialVersionId`/`focusAnnotationId` 支持从画布定位直接落在旧版本上。
- **Issue 详情**：`issue-screenshot-annotations-card.tsx` 渲染 `issue.screenshot_annotations`（裁剪图缩略 + 文字 + 状态），点击跳 `?view=canvas&annotation=<id>`。

## 前端预览交互

预览弹窗 `web/src/components/screenshots/screenshot-lightbox.tsx`，由截图列表页以「过滤后的导航列表 + 当前 screenId」驱动；弹窗自身按 screenId 拉详情，导航只改 screenId。

- **翻页区**：图片区左右各一条固定宽（w-16/sm:w-20）全高点击条，悬停整条高亮、居中箭头；到首尾时对应侧变暗禁用，不循环。两条始终渲染（含加载中、对比模式），不随内容分支卸载——这是翻页失灵的修复点：旧实现把按钮挂在「详情已加载」分支里，切到未缓存界面时骨架屏把它一起卸掉。
- **加载中保留旧图**：记录上一份成功加载的 detail；新界面请求期间只保留旧图本体（含用户当时选中的版本/对比侧），旧版本的文件信息行与悬浮缩略图条不渲染，界面级操作（编辑/删除）在 detail 缺失时禁用，顶部叠「加载中」角标（isFetching 驱动）；版本与对比选择在新详情到达时才重置。旧图回退仅限同一次打开期间的翻页——弹窗关闭即清空，重新打开未缓存界面时只显示骨架屏；请求失败（isError）也不回退，显示空态并 toast+关弹窗。只有从未加载过任何详情时才整块骨架屏。
- **预取**：`prefetchQuery` 对 navIndex±1 的界面拉详情，与 `useScreenshotScreen` 共用 `screenshotScreenDetailQueryOptions`（`["screenshots","detail",id]`，retry:false），避免两处查询配置静默失配。
- **放大**（仅单图）：点击或 Enter/Space 在「适配 / 放大」两档间切换，无滚轮缩放/捏合/百分比。放大宽度 = `naturalWidth ÷ devicePixelRatio`，下限为适配宽度 2 倍；以点击点为中心（按点击处归一化坐标回算 scroll，图片未就绪时等 onLoad 补算；尺寸未知不进入放大）。放大态是 `overflow:auto` 容器 + 显式宽度 img，触控板/滚轮/Shift+滚轮横移走原生滚动；鼠标拖拽手动滚，按下点起累计位移 >4px 不算点击。切界面/版本、进出对比、关弹窗都重置回适配。
- **键盘**：←/→ 始终翻页（含对比、放大态）；图片可聚焦（role=button），Enter/Space 以图中心放大，分支切换时焦点迁往新图。Esc 在放大态先退放大（document capture 阶段拦截，preventDefault+stopPropagation，避免 Dialog 同步收 Esc 关闭），再按才关弹窗。下拉/菜单/确认框打开或焦点在输入框时不响应——Base UI 关闭的浮层保留在 `[hidden]` 容器里，判定"浮层开着"只看未隐藏的节点。
- **对比模式**：跨界面翻页保持对比，左默认最新、右默认次新，单版本时两侧同图；不支持放大，不显示版本缩略图条。两个 pane 顶部控件行 z-20 压在翻页区之上，底部各自保留文件信息。
- **布局**：弹窗铺满视口留 16px（`h/w-[calc(100dvh|vw-2rem)]`），版本文件信息并入标题栏第二行，无独立底栏；版本缩略图条悬浮在图区底部、仅多版本时出现，两侧各留 5rem 不伸进翻页区；图像显示区无内边距（对比模式每个 pane 内的文件说明除外）。
- **触屏**：不做滑动/捏合，点翻页区翻页、点图切放大。
