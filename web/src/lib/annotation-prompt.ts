// 截图标注处理的固定中文提示词模板（INT-99）。
// 结构与 buildIssuePrompt 一致：正文 + 结尾「> 」引用块；单处维护，
// 指引 Agent 用 fast-ship 技能调标注 API 逐条处理，不内联任何凭证。

import { SCREENSHOT_NONE } from "./screenshots";

/** 当前筛选范围描述：三维过滤 + 状态计数 */
export interface AnnotationPromptFilters {
  platform?: string[];
  locale?: string[];
  theme?: string[];
}

export interface AnnotationPromptInput {
  projectId: string;
  filters: AnnotationPromptFilters;
  /** 筛选范围内 open 状态的标注数 */
  openCount: number;
  /** 筛选范围内 resolved 状态的标注数 */
  resolvedCount: number;
}

/** 把一个维度的选中值转成给 Agent 看的描述：空 = 未过滤 */
function describeDimension(
  name: string,
  values: string[] | undefined,
): string | null {
  if (!values || values.length === 0) return null;
  const labels = values.map((v) => (v === SCREENSHOT_NONE ? "未标注" : v));
  return `${name}: ${labels.join("、")}`;
}

/** 组装标注列表 API 的 query 过滤串（与截图页 URL 同口径，逗号分隔）。
 * 全部参数走 URLSearchParams 编码：platform 是开放词表，值里出现
 * `&`/`#`/空格等保留字符时不编码会把查询串拆参截断，Agent 拉到的集合
 * 就会和界面范围对不上。 */
export function annotationFilterQuery(
  filters: AnnotationPromptFilters,
): string {
  const params = new URLSearchParams();
  if (filters.platform?.length) params.set("platform", filters.platform.join(","));
  if (filters.locale?.length) params.set("locale", filters.locale.join(","));
  if (filters.theme?.length) params.set("theme", filters.theme.join(","));
  params.set("status", "open");
  return params.toString();
}

/**
 * 处理标注意见的固定模板：正文交代任务与边界，引用块附项目 ID、
 * 过滤条件、计数与 fast-ship 技能指引。open 为 0 时调用方应禁用入口，
 * 模板本身不做分支（见 annotation-prompt.test.ts 的空范围断言）。
 */
export function buildAnnotationPrompt(input: AnnotationPromptInput): string {
  const lines = [
    `项目 ${input.projectId} 的截图上挂着你之前留下的评审标注意见，请逐条处理当前筛选范围内的未解决标注。`,
    "",
    "处理要求：",
    `1. 使用 fast-ship 技能调用标注列表接口 GET /api/projects/${input.projectId}/screenshot-annotations?${annotationFilterQuery(input.filters)}，拿到全部待处理标注。`,
    "2. 逐条查看标注：结合 crop_url（标注区域裁剪图）与 image_url（整图）定位对应界面，检查并修改代码，把问题修掉。",
    "3. 每处理完一条，调用 PUT /api/screenshot-annotations/{id} 传 {\"status\":\"resolved\"} 把该标注标为已解决。",
    "4. 无法确认或修不了的标注保持 open 不动，不要强行置 resolved。",
    "5. 只处理列表返回的标注，不碰范围外标注；全程不要提交代码、不要创建 PR。",
    "6. 处理完成后汇报：已处理 N 条、未处理 N 条（原因）、需人工确认 N 条。",
  ];

  const block = [
    "以下信息由 Fast Ship 附加。请使用 fast-ship 技能查询并处理截图标注。",
    "若当前环境未安装 fast-ship 技能，先征得用户同意，再执行：",
    "`npx skills add https://github.com/gitbobobo/fast_ship --skill fast-ship`",
    `项目ID：${input.projectId}`,
    `过滤条件：${annotationFilterQuery(input.filters)}`,
    describeDimension("平台", input.filters.platform),
    describeDimension("语言", input.filters.locale),
    describeDimension("亮度", input.filters.theme),
    `当前范围内未解决标注 ${input.openCount} 条，已解决 ${input.resolvedCount} 条。`,
  ].filter((line): line is string => line !== null);

  const quoted = block.map((line) => `> ${line}`).join("\n");
  return `${lines.join("\n")}\n\n${quoted}`;
}
