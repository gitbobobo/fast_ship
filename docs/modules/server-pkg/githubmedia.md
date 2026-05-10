# Server Pkg / GitHubmedia

GitHub 媒体资源代理。解决私有仓库中的图片等资源无法在浏览器直接展示的问题。

## Public API

| Export | Type | Description |
|---|---|---|
| `ProxyMedia(targetURL, token, cacheDir)` | func | 代理获取 GitHub 媒体资源并缓存到本地 |

## Internal Structure

| File | Purpose |
|---|---|
| `server/internal/pkg/githubmedia/proxy.go` | 媒体代理和缓存逻辑 |

## Dependencies

| Depends on | Why |
|---|---|
| server-pkg/storage | 本地缓存文件存储 |

## Dependents

| Used by | How |
|---|---|
| server-handler | `GitHubMediaProxyHandler` 调用代理方法 |

## Implementation Notes

- 对目标 URL 做 SHA256 哈希作为缓存文件名，同 URL 第二次请求直接返回本地缓存
- 支持 `Accept` header 中的图片类型，按内容类型缓存 `.bin` 和 `.json` 元数据文件
- 缓存目录为 `data/uploads/github-media-cache/`
