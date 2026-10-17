// 生成 web/src/lib/acp-registry-icons.ts：从 ACP registry 拉取全部 agent 的官方
// 图标，转成 ProviderIcon 能渲染的数据。图标是他人商标，仅用于展示对应 harness。
//
// 用法：node scripts/gen-acp-registry-icons.mjs
// 需要联网。生成后提交产物；registry 有新增 agent 时重跑即可。
import { writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

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

// 这些属性的值必须匹安全字面量：`fill`/`stroke` 允许颜色与 currentColor/none，
// 不允许 `url(...)`（可指向外部 paint server）等；数值类属性只允许数字。
const PAINT_VALUE =
  /^(none|currentColor|transparent|#[0-9a-f]{3,8}|rgba?\([^()]*\)|hsla?\([^()]*\)|oklch\([^()]*\)|[a-z]+)$/i;
const NUMBER_VALUE = /^(0|1|0?\.\d+|\d+(\.\d+)?)(%|px|pt|em|rem)?$/i;
const ATTR_VALUE_PATTERNS = {
  fill: PAINT_VALUE,
  stroke: PAINT_VALUE,
  "fill-opacity": NUMBER_VALUE,
  "stroke-opacity": NUMBER_VALUE,
  opacity: NUMBER_VALUE,
  "stroke-width": NUMBER_VALUE,
  "font-size": NUMBER_VALUE,
  "fill-rule": /^(nonzero|evenodd)$/i,
  "clip-rule": /^(nonzero|evenodd)$/i,
  "font-family": /^[-\w\s,'"]+$/,
};
function attrValueAllowed(name, value) {
  const pattern = ATTR_VALUE_PATTERNS[name];
  return pattern ? pattern.test(value) : true;
}

function parseTag(tag) {
  const match = /^<\s*(\/?)\s*([a-zA-Z][\w:-]*)([\s\S]*?)>$/.exec(tag);
  if (!match) return null;
  const [, close, rawName, rawAttrs] = match;
  const name = rawName.toLowerCase();
  if (!ALLOWED_TAGS.has(name)) return null;
  if (close) return { name, close: true, selfClose: false, rebuilt: `</${name}>` };
  const kept = [];
  // 单引号、双引号、无引号三种写法都解析；不在白名单的属性一律丢弃。
  for (const attr of rawAttrs.matchAll(
    /([\w:.-]+)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>`]+))/g,
  )) {
    const attrName = attr[1].toLowerCase();
    if (!ALLOWED_ATTRS.has(attrName)) continue;
    const rawValue = attr[2] ?? attr[3] ?? attr[4] ?? "";
    if (!attrValueAllowed(attrName, rawValue)) continue;
    const value = rawValue.replace(/"/g, "&quot;");
    kept.push(`${attrName}="${value}"`);
  }
  const selfClose = /\/\s*$/.test(rawAttrs);
  return {
    name,
    close: false,
    selfClose,
    rebuilt: `<${name}${kept.length ? " " + kept.join(" ") : ""}${selfClose ? " /" : ""}>`,
  };
}

/**
 * 清洗图标 inner markup：不信任 CDN 内容，逐标签重建，只保留白名单元素/属性。
 * 不能解析的 `<...>`、未闭合的 `<`、非白名单标签一律丢弃；文本只在
 * `<text>`/`<tspan>` 内保留（其余位置的裸文本也丢掉，fail closed）。
 */
export function sanitizeSvgInner(inner) {
  const source = inner
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/<!\[CDATA\[[\s\S]*?\]\]>/g, "");
  let out = "";
  let cursor = 0;
  let textDepth = 0;
  while (cursor < source.length) {
    const lt = source.indexOf("<", cursor);
    if (lt === -1) {
      if (textDepth > 0) out += source.slice(cursor);
      break;
    }
    if (textDepth > 0) out += source.slice(cursor, lt);
    const gt = source.indexOf(">", lt);
    if (gt === -1) break;
    const parsed = parseTag(source.slice(lt, gt + 1));
    if (parsed) {
      out += parsed.rebuilt;
      if (parsed.name === "text" || parsed.name === "tspan") {
        if (parsed.close) textDepth = Math.max(0, textDepth - 1);
        else if (!parsed.selfClose) textDepth += 1;
      }
    }
    cursor = gt + 1;
  }
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

export async function generate() {
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
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  await generate();
}
