# GitHub 媒体代理

`/api/github/media-proxy`（`githubmedia.NewProxyService`）是用户态代理：把 Issue 正文里的 GitHub 图片/视频改写成代理地址，前端凭地址栏里的 `?token=<JWT>` 拉取。同仓还有一条截图下载面（`githubmedia.NewReviewMediaService`），供 PR 视觉审查在服务端拉图落盘为 `review_assets`；两者共用 `ProxyService` 实现，但准入与防 SSRF 策略不同。

## 两条面

- 用户态媒体代理面：URL 准入是 GitHub 域名白名单；内容收位图 + 视频（`image/*`、`video/*`，`application/octet-stream` 或缺 Content-Type 时按扩展名回退，集合为位图 + 视频）。回退放行时把类型改写成扩展名表里的具体媒体类型再回给客户端/写进缓存：具体类型是确定信息，不依赖浏览器嗅探（实测 Chromium 下同源 `<img>` 仍会嗅探 octet-stream 并解码，但那是实现细节，`<video>` 与其他客户端不保证）。这里只做类型准入，**不校验媒体字节**——伪装成 `image/png` 的 svg 字节仍会被回，可执行性靠准入 + `Content-Disposition` + CSP 的组合堵。Content-Type 歧义即拒：同一响应多个 `Content-Type` 值、或基础类型里含逗号（多值拼接），按不可识别类型拒；参数部分的逗号不算（`video/mp4; codecs="avc1.42E01E, mp4a.40.2"` 是合法写法），回包只给去掉参数的基础类型。扩展名按重定向之后的最终 URL 取（`resp.Request.URL`），否则 `a.png` 302 到 `b.svg` 且最终类型是 octet-stream 时能借原始扩展名放行。
- 截图下载面：放行任意公网 https 主机（SSRF 防线在拨号时校验解析 IP），内容只收位图，视频与 svg 不收。该面的 SSRF 防线长在 HTTP Transport 里，所以测试注入选项 `WithHTTPTransport` 只适用于媒体面——撞上 `guardAddress` 面会直接 panic，而不是静默拆掉拨号校验。

svg（`image/svg+xml`、非标准写法 `image/svg`、扩展名 `.svg`）与 HTML 等非图片/视频类型两条面都拒，svg 判定两处共用同一份实现（INT-171）。

## 威胁模型与硬化头

`?token=` 会出现在浏览器地址栏，所以同源出口绝不能返回同源可执行文档：svg 以 Fast Ship 同源在「新标签页打开图片」时执行脚本，能读到地址栏里的 token 与 localStorage 里的 refresh token。

媒体代理的成功响应统一带：`X-Content-Type-Options: nosniff`（禁止嗅探改判）、`Content-Disposition: attachment; filename=…`（顶层导航退化为下载而非同源文档，只影响导航、不影响 `<img>`/`<video>` 子资源渲染；文件名优先取上游 URL 末段，末段不是媒体扩展名时（无扩展名、`.v2` 这类非媒体后缀、结尾一个点）换成按响应类型反查的那个，GitHub 的 uuid 附件下载下来才能直接打开）与 `Content-Security-Policy: default-src 'none'; sandbox`（兜底禁脚本禁同源）。

**同源出口不变量**：同一套硬化头也用在同一组「`?token=` + 用户可控内容」的出口上——`/api/issues/assets/:aid/content`（Issue 附件，上传只按前 512 字节嗅探求 `image/*`，「图片魔数 + HTML/SVG 体」的多态文件会被存成 `image/*`）与 `/api/avatars/:uid/:filename`（头像），两者都走 `setSameOriginAttachmentHeaders`。新增同类出口时照这个口径接上，别只给媒体代理加。

## 地址版本参数

代理地址带 `v=<ProxyURLVersion>`（Go 的 `githubmedia.BuildProxyURL` 与前端 `github-media-proxy.ts` 各一份常量，必须同步递增）。浏览器缓存的是整条响应：旧版本 URL 上缓存过的 svg 在部署后仍会命中本地缓存、根本不回源，闸门与硬化头对它们无效——窗口能长到旧条目的 24h TTL 或 token 轮换。换版本号就换了缓存键，一次跳跃让所有前置缓存失效。服务端不校验该参数，它只是缓存键的一部分。

## 闸门位置

- 整文件下载（FetchAndCache）在写缓存前按内容类型/扩展名准入。
- Range 与 HEAD 走 `ProxyPass` 流式直通，回给客户端前过同一道内容类型闸门，不构成旁路；HEAD 响应常无 Content-Type，走扩展名回退。多段 Range（`Range` 头含逗号）被忽略、按完整 200 回——上游对多段 Range 回 `multipart/byteranges`，闸门必然拒，RFC 7233 允许服务器忽略 Range。
- 缓存命中时先判过期（与内容类型无关，过期条目清掉才能让被准入规则拒掉的历史条目随 TTL 淘汰），再按当前规则复核 `entry.ContentType`。复核不过关只按 miss 走重下，不在读路径删文件（`GetCached` 不持下载锁，删 meta/data 会与正在写盘的同 key 下载竞态；下一次成功下载按同一 key 覆盖，过期由前述检查清理）——旧准入口径写进缓存的历史 svg 条目因此自动淘汰，不做全量清目录。复核放行但 meta 里存的是 octet-stream/空类型时，返回的 ContentType 换成规范化类型（只改返回值，不重写 meta）。存量条目（`77af5b8` 之前写入）的复核用的是原始 URL 而非最终 URL，重定向型条目在 TTL 内仍按旧类型交付，可执行性由硬化头堵住。

变更影响（第二轮加固后）：无扩展名且上游只给 `application/octet-stream` 的资源，`Range`/`HEAD` 不再绕过闸门（整文件 GET 本来就是 415），三条路径统一口径——这是有意的，不是回归。

## 被拒资源的重复回源

**只有 svg 的 415 带** `Cache-Control: private, max-age=600`。svg 是确定性拒绝（同一 URL 同一准入规则下永远拒），不给缓存头的话每次页面渲染都会重新请求代理、代理再回源一趟才在闸门上拒，热门 Issue 里带 svg 图片时放大明显。其它非媒体类型（`text/html` 之类）可能是上游抽风时的拦截页/登录页/限流页，同一 URL 下一次就可能正常，钉住会让图片持续裂图——所以它们照旧不带缓存头。服务侧不区分，按 `ErrUnsupportedSVG` 这个哨兵（包装 `ErrUnsupportedUpstreamMedia`）判：内容类型是 `image/svg*`，或上游没给类型而扩展名是 `.svg`。

选浏览器缓存而不是服务端负缓存（meta 墓碑），是因为后者在准入规则变化时要额外清理，语义成本高于收益；代价是不同客户端各自回源一次。
