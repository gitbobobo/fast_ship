# Fast Ship — 需求文档

## 1. 产品概述

Fast Ship 是一款多用户中心化版本管理平台，支持同时管理多个项目的版本发布流程。用户注册登录后，可创建和管理自己的项目。平台将版本从创建到发布的完整生命周期进行统一管理，并在发布时与 GitHub Releases 集成，实现自动化标签创建、文件上传与 Release 说明发布。此外，用户可创建 API Key 供 CI/CD 流水线等自动化场景使用，通过 HTTP 接口补充版本信息。

## 2. 核心概念

| 概念 | 说明 |
|------|------|
| 项目 (Project) | 顶层管理单元，对应一个软件产品，关联一个 GitHub 仓库 |
| 版本 (Version) | 隶属于某个项目的一次发布，包含版本号、安装包、Release 说明等信息 |
| 安装包 (Artifact) | 各客户端平台构建产出的可分发文件（如 `.apk`、`.ipa`、`.exe`、`.dmg` 等） |
| 用户 (User) | 系统的注册用户，拥有自己的项目和 API Key |
| API Key | 用户创建的访问令牌，仅可通过 HTTP 接口补充版本信息（上传安装包、更新 Release 说明等），**不具备**创建版本和发货的权限 |

## 3. 版本状态机

```
创建版本
   │
   ▼
┌──────────┐    满足发货条件    ┌──────────┐
│  待发货   │ ───────────────▶ │  已发货   │
│ (Pending) │                  │ (Shipped) │
└──────────┘                   └──────────┘
```

### 3.1 待发货 (Pending)

- 版本创建后的初始状态
- 版本标签中的所有信息均为**可选**，可逐步补充
- 各客户端可上传安装包到服务端
- 可编辑版本的所有字段（版本号、Release 说明、安装包等）
- 可删除或替换已上传的安装包

### 3.2 已发货 (Shipped)

- 版本内容**冻结**，不可再修改
- 不可上传、删除或替换安装包
- 不可修改 Release 说明及其他版本信息
- 该状态不可回退

## 4. 功能需求

### 4.1 用户管理

#### 4.1.1 注册

- 填写用户名（必填，唯一）
- 填写邮箱（必填，唯一）
- 设置密码（必填，最低强度要求）

#### 4.1.2 登录 / 登出

- 支持用户名或邮箱 + 密码登录
- 登录后签发 JWT Token，用于后续接口鉴权
- 支持登出（使 Token 失效）

#### 4.1.3 个人信息

- 查看和修改用户名、邮箱
- 修改密码

### 4.2 API Key 管理

#### 4.2.1 创建 API Key

- 用户可创建多个 API Key
- 创建时填写备注名称（如 "CI-Android"、"CI-iOS"），便于识别用途
- 创建成功后**仅展示一次**完整 Key，之后只显示前缀掩码
- API Key 绑定创建者，继承该用户对其项目的访问权限

#### 4.2.2 API Key 权限范围

API Key **仅允许**以下操作：

| 操作 | 允许 | 说明 |
|------|------|------|
| 查看项目/版本信息 | ✅ | 读取项目、版本、安装包列表及详情 |
| 更新版本说明 | ✅ | 更新待发货版本的 Release 说明 |
| 上传安装包 | ✅ | 向待发货版本上传安装包 |
| 删除/替换安装包 | ✅ | 管理待发货版本的安装包 |
| 更新目标分支/Commit | ✅ | 补充待发货版本的 Target Commitish |
| 创建项目 | ❌ | 仅允许通过 Web 界面操作 |
| 创建版本 | ❌ | 仅允许通过 Web 界面操作 |
| 删除版本 | ❌ | 仅允许通过 Web 界面操作 |
| 执行发货 | ❌ | 仅允许通过 Web 界面操作 |
| 删除项目 | ❌ | 仅允许通过 Web 界面操作 |

#### 4.2.3 API Key 列表

- 查看当前用户的所有 API Key（名称、前缀掩码、创建时间、最后使用时间）
- 支持删除（吊销）API Key，删除后立即失效

#### 4.2.4 API Key 使用方式

- 通过 HTTP Header `Authorization: Bearer <API_KEY>` 传递
- 服务端校验 Key 有效性并识别所属用户和权限范围

### 4.3 项目管理

#### 4.3.1 创建项目

- 填写项目名称（必填，当前用户下唯一）
- 填写项目描述（可选）
- 配置关联的 GitHub 仓库信息（Owner / Repo）
- 配置 GitHub Access Token（用于后续发布操作）
- 项目归属于创建者，其他用户不可见

#### 4.3.2 项目列表

- 展示当前用户创建的所有项目
- 显示每个项目的最新版本状态
- 支持搜索/筛选

#### 4.3.3 编辑项目

- 修改项目名称、描述
- 更新 GitHub 仓库关联信息和 Token

#### 4.3.4 删除项目

- 删除项目及其下所有版本和安装包数据
- 需二次确认

### 4.4 版本管理

#### 4.4.1 创建版本

- 选择所属项目
- 填写版本号（必填，如 `v1.0.0`，项目内唯一）
- 以下信息**创建时可选**，发货前必须补全：
  - Release 说明（Markdown 格式）
  - 目标分支（用于 GitHub 创建 Tag）
- **仅允许通过 Web 界面操作**，API Key 无此权限

#### 4.4.2 版本详情

- 显示版本基本信息（版本号、状态、创建时间、发货时间）
- 显示 Release 说明（支持 Markdown 预览）
- 显示已上传的安装包列表（文件名、大小、上传时间、上传者/平台）
- 待发货状态下提供编辑入口

#### 4.4.3 版本列表

- 按项目查看所有版本
- 按状态筛选（待发货 / 已发货）
- 按时间排序

#### 4.4.4 删除版本

- 仅**待发货**状态的版本可删除
- 删除时同步清理已上传的安装包
- 需二次确认
- **仅允许通过 Web 界面操作**，API Key 无此权限

### 4.5 安装包管理

#### 4.5.1 上传安装包

- 前置条件：版本处于**待发货**状态
- 支持多文件上传
- 记录上传元信息：文件名、文件大小、上传时间、平台标识
- 同一版本下允许上传多个不同平台的安装包
- Web 界面和 API Key 均可操作

#### 4.5.2 删除/替换安装包

- 前置条件：版本处于**待发货**状态
- 支持删除单个安装包
- 支持替换（覆盖上传同名文件）
- Web 界面和 API Key 均可操作

#### 4.5.3 下载安装包

- 待发货和已发货状态均可下载

### 4.6 发货流程（GitHub 集成）

#### 4.6.1 发货前校验

发货操作前，系统自动校验以下必填项是否已填写完整：

| 校验项 | 说明 |
|--------|------|
| Release 说明 | 不能为空 |
| 安装包 | 至少上传一个安装包 |
| 目标分支 | 用于在 GitHub 上创建 Tag |
| GitHub 配置 | 项目已关联有效的仓库和 Token |

校验不通过时，提示用户缺失的具体项目，阻止发货操作。

#### 4.6.2 执行发货

**仅允许通过 Web 界面操作**，API Key 无此权限。

发货操作按以下顺序执行：

1. **创建 Git Tag** — 在关联的 GitHub 仓库上，基于指定的分支/Commit 创建与版本号一致的 Tag
2. **创建 GitHub Release** — 基于新创建的 Tag，创建 Release，写入 Release 说明
3. **上传安装包** — 将所有安装包作为 Release Assets 上传到 GitHub Release
4. **更新版本状态** — 以上步骤全部成功后，将版本状态变更为「已发货」

#### 4.6.3 发货失败处理

- 任一步骤失败时，记录错误信息并提示用户
- 版本状态保持「待发货」，不做状态变更
- 用户可修复问题后重新发货
- 支持查看失败原因日志

## 5. 非功能需求

### 5.1 安全性

- 用户密码使用 bcrypt 等安全算法哈希存储
- JWT Token 设置合理过期时间
- GitHub Token 加密存储，不以明文展示
- API Key 使用安全随机算法生成，存储时仅保留哈希值
- API Key 可多次展示完整值，且提供一键复制功能
- API Key 权限严格限制，不可越权执行创建版本、发货等操作
- 所有接口需鉴权（JWT Token 或 API Key）
- 文件上传限制大小（可配置，默认单文件 500MB）
- 用户只能访问自己创建的项目和版本数据

### 5.2 可靠性

- 发货流程支持幂等：重复发货同一版本不会在 GitHub 上创建重复 Tag/Release
- 安装包存储使用可靠的文件存储方案

### 5.3 可用性

- 提供清晰的版本状态标识
- 发货前校验给出明确的缺失项提示
- 发货过程提供进度反馈

## 6. 数据模型（概要）

### User

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| id | String | 是 | 主键 |
| username | String | 是 | 用户名，唯一 |
| email | String | 是 | 邮箱，唯一 |
| password_hash | String | 是 | 密码哈希 |
| created_at | DateTime | 是 | 注册时间 |
| updated_at | DateTime | 是 | 更新时间 |

### ApiKey

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| id | String | 是 | 主键 |
| user_id | String | 是 | 所属用户 |
| name | String | 是 | 备注名称（如 "CI-Android"） |
| key_prefix | String | 是 | Key 前缀（用于列表展示掩码） |
| key_hash | String | 是 | Key 哈希值（用于验证） |
| last_used_at | DateTime | 否 | 最后使用时间 |
| created_at | DateTime | 是 | 创建时间 |

### Project

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| id | String | 是 | 主键 |
| user_id | String | 是 | 所属用户 |
| name | String | 是 | 项目名称（用户下唯一） |
| description | String | 否 | 项目描述 |
| github_owner | String | 是 | GitHub 仓库 Owner |
| github_repo | String | 是 | GitHub 仓库名称 |
| github_token | String | 是 | GitHub Access Token（加密存储） |
| created_at | DateTime | 是 | 创建时间 |
| updated_at | DateTime | 是 | 更新时间 |

### Version

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| id | String | 是 | 主键 |
| project_id | String | 是 | 所属项目 |
| version_number | String | 是 | 版本号（如 v1.0.0） |
| status | Enum | 是 | pending / shipped |
| release_notes | Text | 否 | Release 说明（Markdown） |
| target_commitish | String | 否 | 分支名 |
| github_release_url | String | 否 | 发货后的 GitHub Release 链接 |
| created_at | DateTime | 是 | 创建时间 |
| shipped_at | DateTime | 否 | 发货时间 |

### Artifact

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| id | String | 是 | 主键 |
| version_id | String | 是 | 所属版本 |
| file_name | String | 是 | 文件名 |
| file_size | Long | 是 | 文件大小（字节） |
| file_path | String | 是 | 服务端存储路径 |
| platform | String | 否 | 平台标识（如 android、ios、windows、macos） |
| uploaded_at | DateTime | 是 | 上传时间 |

## 7. 页面清单

| 页面 | 路径 | 说明 |
|------|------|------|
| 注册 | `/register` | 用户注册表单 |
| 登录 | `/login` | 用户登录表单 |
| 个人设置 | `/settings` | 个人信息修改、密码修改 |
| API Key 管理 | `/settings/api-keys` | API Key 列表、创建、删除 |
| 项目列表 | `/projects` | 当前用户的项目一览 |
| 创建项目 | `/projects/new` | 新建项目表单 |
| 项目详情 | `/projects/:id` | 项目信息 + 版本列表 |
| 编辑项目 | `/projects/:id/edit` | 编辑项目信息 |
| 创建版本 | `/projects/:id/versions/new` | 新建版本表单 |
| 版本详情 | `/projects/:id/versions/:vid` | 版本信息 + 安装包列表 + 发货操作 |

## 8. 接口清单（概要）

### 8.1 认证接口

| 方法 | 路径 | 鉴权方式 | 说明 |
|------|------|----------|------|
| POST | `/api/auth/register` | 无 | 用户注册 |
| POST | `/api/auth/login` | 无 | 用户登录，返回 JWT |
| POST | `/api/auth/logout` | JWT | 登出 |
| GET | `/api/auth/me` | JWT | 获取当前用户信息 |
| PUT | `/api/auth/me` | JWT | 更新个人信息 |
| PUT | `/api/auth/password` | JWT | 修改密码 |

### 8.2 API Key 接口

| 方法 | 路径 | 鉴权方式 | 说明 |
|------|------|----------|------|
| GET | `/api/api-keys` | JWT | 获取 API Key 列表 |
| POST | `/api/api-keys` | JWT | 创建 API Key（返回完整 Key） |
| DELETE | `/api/api-keys/:id` | JWT | 删除（吊销）API Key |

### 8.3 项目接口

| 方法 | 路径 | 鉴权方式 | 说明 |
|------|------|----------|------|
| GET | `/api/projects` | JWT / API Key | 获取项目列表 |
| POST | `/api/projects` | JWT | 创建项目 |
| GET | `/api/projects/:id` | JWT / API Key | 获取项目详情 |
| PUT | `/api/projects/:id` | JWT | 更新项目 |
| DELETE | `/api/projects/:id` | JWT | 删除项目 |

### 8.4 版本接口

| 方法 | 路径 | 鉴权方式 | 说明 |
|------|------|----------|------|
| GET | `/api/projects/:id/versions` | JWT / API Key | 获取版本列表 |
| POST | `/api/projects/:id/versions` | JWT | 创建版本 |
| GET | `/api/versions/:vid` | JWT / API Key | 获取版本详情 |
| PUT | `/api/versions/:vid` | JWT / API Key | 更新版本信息（Release 说明、Target Commitish） |
| DELETE | `/api/versions/:vid` | JWT | 删除版本 |
| POST | `/api/versions/:vid/ship` | JWT | 执行发货 |

### 8.5 安装包接口

| 方法 | 路径 | 鉴权方式 | 说明 |
|------|------|----------|------|
| POST | `/api/versions/:vid/artifacts` | JWT / API Key | 上传安装包 |
| DELETE | `/api/artifacts/:aid` | JWT / API Key | 删除安装包 |
| GET | `/api/artifacts/:aid/download` | JWT / API Key | 下载安装包 |
