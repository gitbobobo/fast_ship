# Server Pkg / Storage

文件存储抽象层。定义存储接口和本地文件系统实现。

## Public API

| Export | Type | Description |
|---|---|---|
| `Storage` | interface | 存储接口：Save/GetPath/Delete |
| `LocalStorage` | struct | 本地文件系统实现 |

## Internal Structure

| File | Purpose |
|---|---|
| `server/internal/pkg/storage/storage.go` | Storage 接口定义 |
| `server/internal/pkg/storage/local.go` | 本地文件系统实现 |

## Dependencies

| Depends on | Why |
|---|---|
| server-config | 读取 `upload.storage_path` 配置 |

## Dependents

| Used by | How |
|---|---|
| server-service | Artifact 上传/下载、Issue 资产存储、GitHub 媒体缓存 |

## Implementation Notes

- 文件按日期子目录组织：`<storage_path>/YYYY/MM/DD/<uuid><ext>`
- `LocalStorage.Save` 自动创建目标目录
- 当前只有本地存储实现，可通过实现 `Storage` 接口扩展到 S3 等云存储
