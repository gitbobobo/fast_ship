// 生成 web/src/lib/acp-registry-icons.ts：从 ACP registry 拉取全部 agent 的官方
// 图标，转成 ProviderIcon 能渲染的数据。图标是他人商标，仅用于展示对应 harness。
//
// 用法：node scripts/gen-acp-registry-icons.mjs
// 需要联网。生成后提交产物；registry 有新增 agent 时重跑即可。
import { writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const REGISTRY_URL = "https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json";
const OUT = join(dirname(fileURLToPath(import.meta.url)), "..", "web", "src", "lib", "acp-registry-icons.ts");
const NON_PATH_PRIMITIVE = /<(circle|rect|line|polygon|polyline|text|ellipse|use|image|g[\s>])/;

// 图标来自外部 CDN，raw 分支会进 DOM，所以只保留白名单里的 SVG 元素/属性：
// 任何 on* 事件、href/xlink:href、style、script/style/foreignObject/image/use
// 都会被去掉。白名单在生成时生效，提交的是已清洗的内容。
const ALLOWED_TAGS = new Set([
  "path",
  "circle",
  "line",
  "polygon",
  "polyline",
  "rect",
  "ellipse",
  "g",
  "text",
  "tspan",
]);
const ALLOWED_ATTRS = new Set([
  "d",
  "fill",
  "fill-rule",
  "fill-opacity",
  "clip-rule",
  "stroke",
  "stroke-width",
  "stroke-linecap",
  "stroke-linejoin",
  "stroke-opacity",
  "opacity",
  "cx",
  "cy",
  "r",
  "rx",
  "ry",
  "x1",
  "y1",
  "x2",
  "y2",
  "x",
  "y",
  "dx",
  "dy",
  "width",
  "height",
  "points",
  "transform",
  "text-anchor",
  "font-family",
  "font-size",
  "font-style",
  "font-weight",
]);

function sanitizeSvgInner(inner) {
  let out = inner
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/<!\[CDATA\[[\s\S]*?\]\]>/g, "")
    // 整段丢掉危险容器（含自闭合）：它们可以加载外部内容或执行脚本。
    .replace(/<(script|style|foreignObject|image|use|a|animate|set|iframe)\b[^>]*\/?>/gi, "")
    .replace(/<\/(script|style|foreignObject|image|use|a|animate|set|iframe)>/gi, "");
  out = out.replace(
    /<(\/?)([a-zA-Z][\w:-]*)((?:\s+[\w:.-]+\s*=\s*"[^"]*")*)\s*(\/?)>/g,
    (_m, close, tag, attrs, selfClose) => {
      const name = tag.toLowerCase();
      if (!ALLOWED_TAGS.has(name)) return "";
      if (close) return `</${name}>`;
      const kept = [];
      for (const match of attrs.matchAll(/([\w:.-]+)\s*=\s*"([^"]*)"/g)) {
        const attr = match[1].toLowerCase();
        if (ALLOWED_ATTRS.has(attr)) kept.push(`${attr}="${match[2]}"`);
      }
      return `<${name}${kept.length ? " " + kept.join(" ") : ""}${selfClose ? " /" : ""}>`;
    },
  );
  return normalizeWhitespace(out);
}

function normalizeWhitespace(value) {
  return value.replace(/\s+/g, " ").trim();
}

/** 提取 `<svg>` 的 viewBox 与可渲染内容：纯 path 用结构化 paths，其他原语用 raw。 */
function parseIcon(svg) {
  const viewBox = /\sviewBox="([^"]+)"/.exec(svg)?.[1] ?? "0 0 16 16";
  // 有些 SVG 带 <?xml ...?> 声明，必须从真正的 <svg> 开始取 inner markup。
  const svgStart = svg.indexOf("<svg");
  const inner = svg.slice(svg.indexOf(">", svgStart) + 1, svg.lastIndexOf("</svg>"));
  if (NON_PATH_PRIMITIVE.test(inner)) {
    return { viewBox, raw: sanitizeSvgInner(inner) };
  }
  const paths = [...inner.matchAll(/<path\b([^>]*)>/gs)].flatMap(([, attrs]) => {
    const d = /\sd\s*=\s*"([^"]*)"/s.exec(attrs)?.[1];
    if (!d) return [];
    const fillRule = /fill-rule\s*=\s*"([^"]+)"/.exec(attrs)?.[1];
    return [{ d: normalizeWhitespace(d), ...(fillRule ? { fillRule } : {}) }];
  });
  if (paths.length === 0) {
    return { viewBox, raw: sanitizeSvgInner(inner) };
  }
  return { viewBox, paths };
}

const registry = await (await fetch(REGISTRY_URL)).json();
const agents = [...registry.agents, ...(registry.extensions ?? [])].sort((a, b) =>
  a.id.localeCompare(b.id),
);

const blocks = [];
for (const agent of agents) {
  if (!agent.icon) {
    console.warn(`skip ${agent.id}: no icon`);
    continue;
  }
  const response = await fetch(agent.icon);
  if (!response.ok) {
    throw new Error(`icon fetch failed for ${agent.id}: ${response.status}`);
  }
  const { viewBox, paths, raw } = parseIcon(await response.text());
  const body = raw
    ? `    raw: ${JSON.stringify(raw)},`
    : [
        "    paths: [",
        ...paths.flatMap((path) => [
          "      {",
          `        d: ${JSON.stringify(path.d)},${path.fillRule ? ` fillRule: ${JSON.stringify(path.fillRule)},` : ""}`,
          "      },",
        ]),
        "    ],",
      ].join("\n");
  blocks.push(
    [
      `  ${JSON.stringify(agent.id)}: {`,
      `    viewBox: ${JSON.stringify(viewBox)},`,
      '    fill: { light: "#0F0F0F", dark: "#F5F5F5" },',
      body,
      "  },",
    ].join("\n"),
  );
}

const out = [
  "/**",
  " * ACP registry agent 的官方图标（仅供 ACP 接入的 harness 使用）。",
  ` * 快照自 ${REGISTRY_URL}（version ${registry.version}），共 ${blocks.length} 个 agent。`,
  " * 单色图标：颜色由 ProviderIcon 的主题变量驱动；含 circle/line/text 等非路径原语的",
  " * 图标保留原始 inner markup。这是生成文件，重跑 scripts/gen-acp-registry-icons.mjs 覆盖，",
  " * 不要手工编辑单个图标。",
  " */",
  'import type { ProviderIconData } from "./provider-icons";',
  "",
  "export const ACP_REGISTRY_ICONS: Record<string, ProviderIconData> = {",
  ...blocks,
  "};",
  "",
].join("\n");

writeFileSync(OUT, out);
console.log(`wrote ${OUT}: ${blocks.length} agents`);
