# Issue 文件附件

`issue_attachments` 是 Issue 的通用文件附件：任意类型、上传即持久、生命周期与正文引用完全解耦。它和 `issue_assets`（正文内联图片，image/* 嗅探 + 正文引用驱动回收）并存、互不干涉——assets 的 reconcile 只扫 assets 模式，正文里粘贴的附件 download 链接不会被追踪，删除已引用附件留下 404 死链由用户自负。

## 接口

- `POST /api/issues/{iid}/attachments`：multipart，字段名 `file`。
- `DELETE /api/attachments/{aid}`：显式删除记录 + 磁盘文件。
- `GET /api/attachments/{aid}/download`：`application/octet-stream` + `Content-Disposition: attachment`，支持 `?token=` query 凭证（`RequireAuthWithQueryToken`），供 `<a href>`/浏览器直连下载。
- `GET /api/issues/{iid}` 详情内嵌 `attachments[]`：`id / file_name / file_size / mime_type / created_at / uploader / download_url`。不开独立列表端点（同 version→artifacts 先例），不分页、不限单 Issue 数量。

## 约束

- 不限文件类型、无黑白名单；`mime_type` 由 `http.DetectContentType` 对内容嗅探后存储，仅用于 UI 图标展示，下载恒为 octet-stream。
- 大小沿用 `upload.max_file_size`（默认 500MB）。实现用 `LimitReader(max+1)` 截断读入，落盘后按计数判定超限并回收文件——超限文件不会留在磁盘上。
- 同名不去重：每次上传生成新行 + UUID 存储路径，文件名只在 `file_name` 里展示。
- 空文件拒绝（40001）。

## 权限

- 上传/删除：`RequireAuth`，JWT 与 API Key 均可。仅 internal 来源 Issue 可传/删，github 源拒绝返回 40908（对齐 assets 的 `ErrIssueReadOnly`）。
- 删除校验项目归属（`projectRepo.FindByID(projectID, userID)`），不限上传者本人——同 artifacts。
- `uploader` 沿用 artifacts 的 `uploaded_by` 约定：`middleware.ActorLabel` 产出 JWT 用户名或 `API Key: <name>`。

## 生命周期与磁盘清理

- 存储路径 `{projectID}/issues/{issueID}/attachments/{attachmentID}{ext}`，挂在项目前缀下。
- 显式 `DELETE`：删行 + `storage.Delete(file_path)`。
- Issue 删除：`issue_attachments.issue_id` 外键 `ON DELETE CASCADE` 删行；当前没有独立的 issue 删除端点，行删除实际发生在项目级联（projects → issues → attachments）里，磁盘文件由 `ProjectService.Delete` 的 `DeletePrefix(projectID)` 一并回收——这是唯一需要兜底磁盘文件的路径。
- 附件上传只挂在已存在 Issue 上，不做 draft attachment，创建表单不变。

## 设计取舍

- 独立实体而非扩展 `issue_assets`：assets 的 pending/attached 状态机与正文引用回收语义和附件模型完全不同，复用会把两套生命周期搅在一起。
- 下载鉴权走 `?token=` 而不是仅 Header：附件的典型用法是浏览器/`<a>` 直连，同 artifacts 下载。
- 详情内嵌 `omitempty`：无附件的 Issue 响应不带 `attachments` 字段，保持详情 payload 最小。

## 遗留缺陷：Issue 侧旧关联的外键是 NO ACTION

has-many/has-one 与 belongs-to 双侧声明同一关联时，GORM 生成的外键取**父侧**的 `constraint`（`issue_pull_requests` 测试注释里已有此先例）。`Issue.GitHubMeta`/`Comments`/`TimelineEvents` 与 `Version.Artifacts` 只有 belongs-to 侧写了 `constraint:OnDelete:CASCADE`，父侧没写，所以生成/存量 DDL 里 `fk_issues_git_hub_meta`、`fk_issues_comments`、`fk_issues_timeline_events`、`fk_versions_artifacts` 四个约束在所有库（含新建库）上都是 **NO ACTION**：`DELETE FROM issues`/`versions` 在有这些子行时撞约束失败，含 GitHub 同步子数据的项目删除实际会报错。

这是 main 上已存在的独立缺陷，**本需求不修**：补父侧 tag 只能改新建库的 DDL，AutoMigrate 不会重建存量库的已有 FK；而把 `IssueReadState` 从无外键改成有外键会触发 glebarez SQLite 整表重建，存量库里悬空的水位行会导致启动迁移失败（main.go `log.Fatalf`）。

对本需求的影响：`issue_attachments` 是新表、单侧 belongs-to 自带 `ON DELETE CASCADE`，建表即正确，不受影响；附件行级联与磁盘清理依赖的链路里，唯一会撞旧缺陷的是「issue 上恰好挂着 comments/timeline/github_meta 行时的项目级联删除」——那条路径本来就会失败，与本功能无关。
