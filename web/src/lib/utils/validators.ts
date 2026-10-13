import { z } from "zod/v4";

export const loginSchema = z.object({
  login: z.string().min(1, "请输入用户名或邮箱"),
  password: z.string().min(1, "请输入密码"),
});

export const registerSchema = z.object({
  username: z
    .string()
    .min(2, "用户名至少 2 个字符")
    .max(50, "用户名最多 50 个字符")
    .regex(/^[a-zA-Z0-9_]+$/, "仅支持字母、数字和下划线"),
  email: z.email("请输入有效的邮箱地址"),
  password: z
    .string()
    .min(8, "密码至少 8 位")
    .regex(/[a-z]/, "需包含小写字母")
    .regex(/[A-Z]/, "需包含大写字母")
    .regex(/[0-9]/, "需包含数字"),
});

// 仓库与 Token 相互独立：仓库链接可选，不再要求同时配置 Token。
export const projectSchema = z.object({
  name: z.string().min(1, "请输入项目名称").max(100),
  description: z.string().optional(),
  repository_url: z.string().optional(),
  public_github_token_id: z.string().optional(),
  code_github_token_id: z.string().optional(),
});

export const projectEditSchema = z.object({
  name: z.string().min(1, "请输入项目名称").max(100),
  description: z.string().optional(),
  repository_url: z.string().optional(),
  public_github_token_id: z.string().optional(),
  code_github_token_id: z.string().optional(),
});

export const versionSchema = z.object({
  version_number: z
    .string()
    .min(1, "请输入版本号")
    .regex(/^v\d+\.\d+\.\d+/, "格式: v1.0.0"),
  release_notes: z.string().optional(),
  target_commitish: z.string().optional(),
});

export const apiKeySchema = z.object({
  name: z.string().min(1, "请输入备注名称").max(100),
});

export const githubTokenSchema = z.object({
  title: z.string().min(1, "请输入 Token 标题").max(100),
  token: z.string().trim().min(1, "请输入 GitHub Token"),
});

export const githubTokenTitleSchema = z.object({
  title: z.string().min(1, "请输入 Token 标题").max(100),
});

export type LoginInput = z.infer<typeof loginSchema>;
export type RegisterInput = z.infer<typeof registerSchema>;
export type ProjectInput = z.infer<typeof projectSchema>;
export type ProjectEditInput = z.infer<typeof projectEditSchema>;
export type VersionInput = z.infer<typeof versionSchema>;
export type ApiKeyInput = z.infer<typeof apiKeySchema>;
export type GithubTokenInput = z.infer<typeof githubTokenSchema>;
export type GithubTokenTitleInput = z.infer<typeof githubTokenTitleSchema>;
