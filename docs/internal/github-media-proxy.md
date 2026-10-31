# GitHub 媒体代理

`/api/github/media-proxy`（`githubmedia.NewProxyService`）是用户态代理：把 Issue 正文里的 GitHub 图片/视频改写成代理地址，前端凭地址栏里的 `?token=<JWT>` 拉取。同仓还有一条截图下载面（`githubmedia.NewReviewMediaService`），供 PR 视觉审查在服务端拉图落盘为 `review_assets`；两者共用 `ProxyService` 实现，但准入与防 SSRF 策略不同。

## 两条面

- 用户态媒体代理面：URL 准入是 GitHub 域名白名单；内容收位图 + 视频（`image/*`、`video/*`，`application/octet-stream` 或缺 Content-Type 时按扩展名回退，集合为位图 + 视频）。回退放行时把类型改写成扩展名表里的具体媒体类型再回给客户端/写进缓存——响应统一带 `nosniff`，octet-stream/空类型不会被浏览器嗅探成图片，`<img>`/`<video>` 会拒收。Content-Type 歧义即拒：同一响应多个 `Content-Type` 值、或单值里含逗号，都按不可识别类型拒。扩展名按重定向之后的最终 URL 取（`resp.Request.URL`），否则 `a.png` 302 到 `b.svg` 且最终类型是 octet-stream 时能借原始扩展名放行。
- 截图下载面：放行任意公网 https 主机（SSRF 防线在拨号时校验解析 IP），内容只收位图，视频与 svg 不收。

svg（`image/svg+xml`、非标准写法 `image/svg`、扩展名 `.svg`）与 HTML 等非图片/视频类型两条面都拒，svg 判定两处共用同一份实现（INT-171）。

## 威胁模型与硬化头

`?token=` 会出现在浏览器地址栏，所以代理绝不能返回同源可执行文档：svg 以 Fast Ship 同源在「新标签页打开图片」时执行脚本，能读到地址栏里的 token 与 localStorage 里的 refresh token。所有成功响应统一带硬化头：`X-Content-Type-Options: nosniff`（禁止浏览器嗅探改判）、`Content-Disposition: attachment`（顶层导航退化为下载而非同源文档；只影响导航，不影响 `<img>`/`<video>` 子资源渲染，可无条件加）与 `Content-Security-Policy: default-src 'none'; sandbox`（兜底禁脚本禁同源）。

## 闸门位置

- 整文件下载（FetchAndCache）在写缓存前按内容类型/扩展名准入。
- Range 与 HEAD 走 `ProxyPass` 流式直通，回给客户端前过同一道内容类型闸门，不构成旁路；HEAD 响应常无 Content-Type，走扩展名回退。多段 Range（`Range` 头含逗号）被忽略、按完整 200 回——上游对多段 Range 回 `multipart/byteranges`，闸门必然拒，RFC 7233 允许服务器忽略 Range。
- 缓存命中时按当前规则复核 `entry.ContentType`，不过关只按 miss 走重下，不在读路径删文件（`GetCached` 不持下载锁，删 meta/data 会与正在写盘的同 key 下载竞态；下一次成功下载按同一 key 覆盖，TTL 到期另有清理）——旧准入口径写进缓存的历史 svg 条目因此自动淘汰，不做全量清目录。复核放行但 meta 里存的是 octet-stream/空类型时，返回的 ContentType 换成规范化类型（只改返回值，不重写 meta）。

变更影响（第二轮加固后）：无扩展名且上游只给 `application/octet-stream` 的资源，`Range`/`HEAD` 不再绕过闸门（整文件 GET 本来就是 415），三条路径统一口径——这是有意的，不是回归。
