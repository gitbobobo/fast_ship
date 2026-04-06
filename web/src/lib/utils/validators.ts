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

export const projectSchema = z.object({
  name: z.string().min(1, "请输入项目名称").max(100),
  description: z.string().optional(),
  github_owner: z.string().min(1, "请输入 GitHub Owner"),
  github_repo: z.string().min(1, "请输入 GitHub Repo"),
  github_token: z.string().min(1, "请输入 GitHub Token"),
});

export const projectEditSchema = z.object({
  name: z.string().min(1, "请输入项目名称").max(100),
  description: z.string().optional(),
  github_owner: z.string().min(1, "请输入 GitHub Owner"),
  github_repo: z.string().min(1, "请输入 GitHub Repo"),
  github_token: z.string().optional(),
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

export type LoginInput = z.infer<typeof loginSchema>;
export type RegisterInput = z.infer<typeof registerSchema>;
export type ProjectInput = z.infer<typeof projectSchema>;
export type ProjectEditInput = z.infer<typeof projectEditSchema>;
export type VersionInput = z.infer<typeof versionSchema>;
export type ApiKeyInput = z.infer<typeof apiKeySchema>;
