import { describe, expect, it } from "vitest";
import {
  annotationFilterQuery,
  buildAnnotationPrompt,
} from "./annotation-prompt";

const BASE = {
  projectId: "proj-1",
  filters: {},
  openCount: 3,
  resolvedCount: 5,
};

describe("buildAnnotationPrompt", () => {
  it("embeds project id, filter query and open/resolved counts", () => {
    const prompt = buildAnnotationPrompt(BASE);
    expect(prompt).toContain("项目ID：proj-1");
    expect(prompt).toContain(
      "GET /api/projects/proj-1/screenshot-annotations?status=open",
    );
    expect(prompt).toContain("未解决标注 3 条，已解决 5 条");
    expect(prompt).toContain("PUT /api/screenshot-annotations/{id}");
  });

  it("describes the three dimensions and encodes the untagged bucket", () => {
    const prompt = buildAnnotationPrompt({
      ...BASE,
      filters: {
        platform: ["ios", "__none__"],
        locale: ["zh-CN"],
        theme: ["dark"],
      },
    });
    expect(prompt).toContain("平台: ios、未标注");
    expect(prompt).toContain("语言: zh-CN");
    expect(prompt).toContain("亮度: dark");
    expect(prompt).toContain(
      "platform=ios%2C__none__&locale=zh-CN&theme=dark&status=open",
    );
  });

  it("keeps the fast-ship skill guidance and boundaries in the quote block", () => {
    const prompt = buildAnnotationPrompt(BASE);
    expect(prompt).toContain("fast-ship 技能");
    expect(prompt).toContain(
      "`npx skills add https://github.com/gitbobobo/fast_ship --skill fast-ship`",
    );
    // 引用块行都以 > 开头；正文与引用块之间空行分隔
    const [body, block] = prompt.split("\n\n>");
    expect(body).toContain("不要提交代码");
    expect(block).toContain("项目ID");
  });

  it("renders a valid prompt for an empty scope (button is disabled upstream)", () => {
    const prompt = buildAnnotationPrompt({ ...BASE, openCount: 0 });
    expect(prompt).toContain("未解决标注 0 条");
    expect(prompt).toContain("status=open");
    // 模板本身不分支：引用块与正文仍然完整
    expect(prompt.split("\n\n>").length).toBe(2);
  });
});

describe("annotationFilterQuery", () => {
  it("always pins status=open and omits empty dimensions", () => {
    expect(annotationFilterQuery({})).toBe("status=open");
    expect(annotationFilterQuery({ theme: ["light"] })).toBe(
      "theme=light&status=open",
    );
  });

  it("URL-encodes reserved characters in open-vocabulary values", () => {
    // platform 是开放词表：&/# 不编码会把查询串拆参或截断
    expect(
      annotationFilterQuery({ platform: ["web&app", "ios#2"] }),
    ).toBe("platform=web%26app%2Cios%232&status=open");
    // 服务端按逗号解码回多值（与截图页 URL 同口径）
    const parsed = new URLSearchParams(
      annotationFilterQuery({ platform: ["a b", "__none__"] }),
    );
    expect(parsed.get("platform")).toBe("a b,__none__");
    expect(parsed.get("status")).toBe("open");
  });
});
