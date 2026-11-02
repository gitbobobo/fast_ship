# GitHub 媒体代理

`/api/github/media-proxy`（`githubmedia.NewProxyService`）是用户态代理：把 Issue 正文里的 GitHub 图片/视频改写成代理地址，前端凭地址栏里的 `?token=<JWT>` 拉取。同仓还有一条截图下载面（`githubmedia.NewReviewMediaService`），供 PR 视觉审查在服务端拉图落盘为 `review_assets`；两者共用 `ProxyService` 实现，但准入与防 SSRF 策略不同。

## 缓存面边界（不变量）

39 轮复审反复在同族问题上打转，根因是「面边界」没一次定清。规则可穷举，改动前先对照；与这条冲突的意见按冲突处理，不再逐条补丁：

1. **面只有三个，规则按面定，不按调用点定。**
   - 媒体代理面（`defaultCacheSubdir`）：缓存**可丢弃**。TTL 回收、scope 标记缺失时整目录清空、损坏条目回收，只发生在这里。
   - 审查截图面（`review-screenshot-cache`）：缓存**不可丢弃**——`.bin` 是 `review_assets` 持久资产（`storage_path` 直指文件、`ensureAssets` 不重下）。任何代码路径不得删除它的条目；启动只建目录+补标记；无 TTL 删除、无 purge、无条目回收 sweeper（`media-*.tmp` 清理是唯一例外，`sweepStaleTmpFiles` 不触碰 `.bin/.json`）；**读路径命中即返回**——不过期、不因类型复核 miss，否则探测时上游已删/转私的截图会被迫重下失败，或上游内容变化时新字节覆盖旧资产而 DB 的 size/mime 不同步。
   - 无缓存出口（issue assets、avatars、screenshot content、crop）：不落缓存目录，只施加同源硬化头与「attachment 或 inline 图片档」判据（按出口语义：下载类 attachment，截图/裁剪类刻意保留 inline，别「补齐」成下载）。
2. **MIME/扩展门**：媒体代理面全量（`image/svg*`、`.svg`、HTML 拒 + fallback 归一化）；审查面只收位图；下载出口 `octet-stream`+`attachment` 不进文档上下文。
3. **Cache-Control** 一律 `private`（URL 带 JWT），TTL 由 `s.cacheTTL` 派生，`cloneRelevantHeaders` 与缓存命中同源，不透传上游。
4. **写侧两步发布**（data→binPath 再 meta→metaPath）。回收者对已 rename 走的文件，恢复前必须重读校验新鲜度，且恢复前校验目标路径未被写侧占据；不能安全恢复就丢弃旧字节，绝不覆盖新条目。
5. **版本参数 v**：前端无条件重写为当前值；Go `ProxyURLVersion` 与前端 `MEDIA_PROXY_VERSION` 由 `TestProxyURLVersionMatchesFrontend` 断言一致，只涨一边直接红。

这条不变量同时写在 `server/internal/pkg/githubmedia/proxy.go` 的包注释里，两处保持同步。

## 两条面

- 用户态媒体代理面：URL 准入是 GitHub 域名白名单；内容收位图 + 视频（`image/*`、`video/*`，`application/octet-stream` 或缺 Content-Type 时按扩展名回退，集合为位图 + 视频）。回退放行时把类型改写成扩展名表里的具体媒体类型再回给客户端/写进缓存：具体类型是确定信息，不依赖浏览器嗅探（实测 Chromium 下同源 `<img>` 仍会嗅探 octet-stream 并解码，但那是实现细节，`<video>` 与其他客户端不保证）。这里只做类型准入，**不校验媒体字节**——伪装成 `image/png` 的 svg 字节仍会被回，可执行性靠准入 + `Content-Disposition` + CSP 的组合堵。Content-Type 歧义即拒：同一响应多个 `Content-Type` 值、或基础类型里含逗号（多值拼接），按不可识别类型拒；参数部分的逗号不算（`video/mp4; codecs="avc1.42E01E, mp4a.40.2"` 是合法写法），回包只给去掉参数的基础类型。扩展名按重定向之后的最终 URL 取（`resp.Request.URL`），否则 `a.png` 302 到 `b.svg` 且最终类型是 octet-stream 时能借原始扩展名放行。
- 截图下载面：放行任意公网 https 主机（SSRF 防线在拨号时校验解析 IP），内容只收位图，视频与 svg 不收。该面的 SSRF 防线长在 HTTP Transport 里，所以测试注入选项 `WithHTTPTransport` 只适用于媒体面——撞上 `guardAddress` 面会直接 panic，而不是静默拆掉拨号校验。

svg（`image/svg+xml`、非标准写法 `image/svg`、扩展名 `.svg`）与 HTML 等非图片/视频类型两条面都拒，svg 判定两处共用同一份实现（INT-171）。

## 威胁模型与硬化头

`?token=` 会出现在浏览器地址栏，所以同源出口绝不能返回同源可执行文档：svg 以 Fast Ship 同源在「新标签页打开图片」时执行脚本，能读到地址栏里的 token 与 localStorage 里的 refresh token。

媒体代理的成功响应统一带：`X-Content-Type-Options: nosniff`（禁止嗅探改判）、`Content-Disposition: attachment; filename=…`（挡顶层导航，不让代理地址被当成同源文档打开。子资源不受影响的依据：HTML 的「Handle as a download」只写在导航算法里，图片/媒体加载算法没有 attachment 分支，WebKit 的子资源与媒体桥接源码里也没有；Chromium 实测过，Safari 未实测，属源码级判断。若未来出现可复现的 Safari 子资源拒显，再考虑按 `Sec-Fetch-Dest` 区分；文件名优先取上游 URL 末段，且后缀要与响应类型对账（后缀是上游可控的）：后缀在媒体表里、且反查出的类型与响应类型一致才保留；否则表内类型按响应类型反查替换（`.v2`、结尾一个点、无扩展名、伪装后缀 `x.png` 回 `video/mp4` → `x.mp4`）；表外类型（`image/avif`、`video/x-matroska` 这些按前缀放行但表里没有的）反查不到，保留上游自己的 `.avif`/`.mkv`，只在完全没后缀时退 `.bin`）与 `Content-Security-Policy: default-src 'none'; sandbox`（兜底禁脚本禁同源）。

**同源出口不变量**：同一套硬化头也用在同一组「`?token=` + 用户可控内容」的出口上，全部走 `server/internal/handler/github_media_proxy.go` 的 `setSameOriginContentHardeningHeaders`（`nosniff` + `CSP: default-src 'none'; sandbox`），需要下载语义的再加 `setSameOriginAttachmentHeaders`（带文件名的 `attachment`）：

| 出口 | 硬化 | 说明 |
| --- | --- | --- |
| `/api/github/media-proxy` | nosniff + CSP + attachment | 任意公网图片/视频（仅收媒体类型） |
| `/api/issues/assets/:aid/content` | nosniff + CSP + attachment | Issue 附件，上传只按前 512 字节嗅探求 `image/*`，「图片魔数 + HTML/SVG 体」的多态文件能过 |
| `/api/avatars/:uid/:filename` | nosniff + CSP + attachment | 头像 |
| `/api/screenshot-screens/:sid/content` | nosniff + CSP `sandbox`，保留 `inline` | 截图同样是用户上传 + `?token=`；「在标签页里看截图」是产品行为，`inline` 保留。这一档**不设** `default-src`：Chromium 打开图片地址会生成 `<body><img>` 包装文档，那个 img 是否受 `img-src`（回落到 `default-src`）约束随版本而异，能不能显示不该赌这个实现细节；`sandbox` 单独就够禁脚本 |
| `/api/screenshot-annotations/:aid/crop` | 同上（nosniff + CSP `sandbox` + `inline`） | 服务端重新编码的裁剪 PNG，无脚本执行风险，但按同一不变量接上，别靠「眼下没风险」豁免 |

`/api/artifacts/:aid/download`、`/api/attachments/:aid/download` 以 `application/octet-stream` + `attachment` 下发，本身不构成同源文档，不在这个不变量里。

**残余窗口**：头像地址存在用户表里、改不了缓存键，没法像媒体代理那样加版本参数；只有头像需要按 60 分钟评估——基线里头像的 `Cache-Control` 是 `private, max-age=3600`，部署前已进浏览器的旧响应（没有硬化头）会按它继续被复用，真实残余窗口最长 60 分钟。Issue 附件基线本来就是 `max-age=300`，残余窗口仍是最长 5 分钟（媒体代理旧版是 24h，靠 `v` 版本参数提前退役）。两条出口的 `Cache-Control` 都压到 `private, max-age=300`，只是把窗口之后的新请求压回分钟级，不是旧缓存的退役时间。新增同类出口时照上表接上，别只给媒体代理加。

**版本参数的单一来源**：`v` 的权威来源是 Go 的 `ProxyURLVersion`（服务端 `BuildProxyURL` 写进 body_html）。前端 `attachMediaProxyVersion` 把内容来源 URL 上的 v **一律重写为当前常量**：代理地址嵌在用户可控的正文里，预置的旧版本号（甚至大于当前值的号）都能把浏览器钉在错误的缓存键上，不能信任内容里带的值。本项目 web dist 与 Go 二进制同一镜像下发，不存在「Go 常量先涨、前端还没发」的窗口；递增版本时两边常量一起改。

## 地址版本参数

代理地址带 `v=<ProxyURLVersion>`（Go 的 `githubmedia.BuildProxyURL` 与前端 `github-media-proxy.ts` 各一份常量，必须同步递增）。浏览器缓存的是整条响应：旧版本 URL 上缓存过的 svg 在部署后仍会命中本地缓存、根本不回源，闸门与硬化头对它们无效——窗口能长到旧条目的 24h TTL 或 token 轮换。换版本号就换了缓存键，一次跳跃让所有前置缓存失效。服务端不校验该参数，它只是缓存键的一部分。

## 闸门位置

- 整文件下载（FetchAndCache）在写缓存前按内容类型/扩展名准入。
- Range 与 HEAD 走 `ProxyPass` 流式直通，回给客户端前过同一道内容类型闸门，不构成旁路；HEAD 响应常无 Content-Type，走扩展名回退。多段 Range（`Range` 头含逗号）被忽略、按完整 200 回——上游对多段 Range 回 `multipart/byteranges`，闸门必然拒，RFC 7233 允许服务器忽略 Range。
- 媒体代理面的缓存命中全程（meta 读失败/损坏、data 缺失、过期、内容复核不过关）**只按 miss 处理，读路径不删任何文件**（review 截图面不同：meta 读失败/损坏而 .bin 在时按命中兜底——大小取 stat、魔数判位图类型，见上方不变量第 1 条；仅 data 缺失、字节判不出位图才 miss，文件同样不删；**重下也绝不覆盖已落盘的 .bin**——fetch 入口护栏：.bin 存在即返回现有字节（类型判不出时 mime 留空，不把 octet-stream 写进 review_assets；截图端点按魔数兜底判定下发类型），不依赖兜底嗅探的覆盖面）。`GetCached` 不持下载锁，而写侧发布是非原子的两步（rename data + WriteFile meta），无锁删除会与同 key 写盘竞态，最坏抹掉别人刚发布的新条目。条目退役走两条路：下次读取按 miss 触发重下、`fetchAndCache` 持下载锁写盘按同一 key 覆盖；「不再被成功重下」的（上游已删、永久 415）由服务内低频目录过期扫描回收（**只有媒体代理面**启动：生产装配显式调 `StartExpiredSweeper`，启动即扫 + 每 6h；删除前持该 key 下载闸——闸引用计数在锁内增减、归零才从 map 摘除，锁内复核 URL 未变且仍过期才动手；中断下载的 `media-*.tmp` 与孤儿 `.bin` 按 mtime 超 2×TTL 一并回收；tmp 清理是独立方法 `sweepStaleTmpFiles`，截图面也通过 `StartStaleTmpSweeper` 启动它——不触碰 `.bin/.json`；扫描末尾会**按目录实际字节校准容量水位**（`recalibrateCacheUsage`，媒体面准入闸只增不减，见下）；**容量闸长在写入点，不删任何文件**（INT-172 R4 方向审计）：`fetchAndCache` 在写 tmp 之前按 `Content-Length`（分块响应按单文件上限）预留字节，并发下载也计入；水位会越过 `defaultCacheCapacity`（1 GiB，`WithCacheCapacity` 可覆盖）时返回 `ErrCacheFull`、不缓存。拒绝分两级，都不重复打上游：**水位已满**时预检在回源前就拒绝（`cacheAdmissionFull`），handler 走 `ProxyPass` 打一次上游；**请求中途**才超限时把已经打开、已过内容类型闸门的上游响应交给 handler 直接流式回包（`CacheFullError.Response`），不再发第二次 GET——容量满不是偶发而是稳态，二次请求会让上游请求数与连接数翻倍。水位 = 已落盘（`capCommitted`，只在成功落盘时增加）+ 进行中预留（`capReserved`），偏差方向恒为「多算」（同 key 覆盖不扣旧值，被删字节等下次校准；校准在锁外扫目录，扫描期间完成的落盘按累计增量叠加，不漏算也不覆盖快照），多算只会让准入更早拒绝，绝不删已落盘条目。校准读不完整（目录/单条 stat 失败）时保留旧水位：已校准过的继续按偏多水位跑，**从未校准过的拒绝缓存**（否则水位只剩本进程启动后的落盘量、重启前的存量整段不在账上，能写到上限两倍），并按 `capacityCalibrateRetryInterval` 退避重试——校准单飞，并发写请求只扫一遍目录。此前的「扫描末尾按 mtime 最旧淘汰」已删除——上限只在 6h 扫描执行（写路径无闸，瞬时越界能把同卷数据盘写满），且为凑上限必须删 TTL 内的活条目（回源失败时命中变破图）；口径只统计 `.bin` 与 `media-*.tmp`，meta 每条几百字节可忽略）——旧准入口径写进缓存的历史 svg 条目因此自动淘汰，不做全量清目录。**截图下载面不接扫描与整目录清空**：它的缓存目录同时是 `review_assets` 的持久存储（`storage_path` 直接指向缓存文件、无副本、ensure 不会重下），按 TTL 删除或 scope 标记缺失时清空都会让历史截图永久消失。启动时只做两件事：确保目录存在、补写 scope 标记；无 Scope 字段的旧条目**不删**（与匿名条目在 meta 层面无法区分，且是持久资产），跨授权域风险由缓存键按 Scope 隔离与接口鉴权兜住。该面目录的治理（复制进上传存储或按引用回收）另行处理。meta 里存的是 octet-stream/空类型的旧条目（放行依赖扩展名，而扩展名要用重定向后的最终 URL、meta 没存）保守判 miss 走重下，不按原始 URL 猜。存量条目（`77af5b8` 之前写入）因此在一个 TTL 内会被重下一次，换来与写入侧完全同一套判定。

变更影响（第二轮加固后）：无扩展名且上游只给 `application/octet-stream` 的资源，`Range`/`HEAD` 不再绕过闸门（整文件 GET 本来就是 415），三条路径统一口径——这是有意的，不是回归。

## 被拒资源的重复回源

svg 的 415 带缓存头，但分两档：内容类型判定（`image/svg*`，同一资源下次还是 svg）带 `private, max-age=600`；扩展名判定（上游没给 Content-Type + `.svg`，判据来自上游本次响应——下次它可能给正常类型）只带 `private, max-age=60`，钉 10 分钟会把裂图窗口也钉住。其它非媒体类型（`text/html` 之类可能是上游抽风时的拦截页/登录页/限流页，同一 URL 下一次就可能正常）不带缓存头。不给缓存头的话每次页面渲染都会重新请求代理、代理再回源一趟才在闸门上拒，热门 Issue 里带 svg 图片时放大明显。服务侧按两个哨兵区分：`ErrUnsupportedSVG`（内容类型档）与 `ErrUnsupportedSVGByExtension`（扩展名档，包装前者）。

选浏览器缓存而不是服务端负缓存（meta 墓碑），是因为后者在准入规则变化时要额外清理，语义成本高于收益；代价是不同客户端各自回源一次。
