#!/usr/bin/env node
// 从 models.dev 生成内置模型目录（INT-133）：过滤出「OpenAI 兼容协议 + 提供
// API 地址 + 至少一个模型支持工具调用」的提供商，写入
// server/internal/catalog/modelsdev.json（go:embed 打进二进制，运行时不联网）。
//
// 用法（仓库根目录或本脚本目录均可）：
//   node scripts/gen-model-catalog.mjs              # 拉取 https://models.dev/api.json
//   node scripts/gen-model-catalog.mjs --in api.json  # 用本地快照（离线复核）
//
// 发版时由人手动执行刷新；脚本是纯函数式的——同一份 api.json 输入永远产出
// 字节级相同的文件（provider/model 按 id 排序，无随机内容）。
// generated_at 取目录内模型的最大 last_updated（数据快照时间，RFC3339），
// 而不是本次运行的时刻：同一份数据重复生成不产生无意义 diff，数据陈旧与否
// 一眼可见。
//
// 来源与许可：https://models.dev/api.json（MIT License，见 https://github.com/sst/models.dev）。

import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const SOURCE_URL = "https://models.dev/api.json";
const OUT_PATH = join(dirname(fileURLToPath(import.meta.url)), "..", "server", "internal", "catalog", "modelsdev.json");

function parseArgs(argv) {
  const args = { in: null };
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === "--in") {
      args.in = argv[++i] ?? null;
    }
  }
  return args;
}

async function loadSource(args) {
  if (args.in) {
    return readFileSync(args.in, "utf8");
  }
  const res = await fetch(SOURCE_URL);
  if (!res.ok) {
    throw new Error(`GET ${SOURCE_URL} 返回 ${res.status}`);
  }
  return await res.text();
}

// api.json 的模型字段是自由形状：窗口/输出缺失或非正数按 0（未知）落库，
// 绝不把缺失当成 0 上限参与预算（与服务端解析同一约定）。
function positiveNumber(value) {
  const n = typeof value === "string" ? Number(value) : value;
  return typeof n === "number" && Number.isFinite(n) && n > 0 ? Math.trunc(n) : 0;
}

// cost 单价透射（INT-139 费用估算）：models.dev 的 cost.* 是 USD/百万 tokens。
// 显式 0（免费模型、免缓存读取费）与缺失严格区分——前者是已知价格，
// 后者输出 null（缺失 ≠ 0，无价模型不能显示 $0）。cache_read/cache_write
// 可选——不提供的厂商按 input 价计缓存命中由调用方回退处理。
function costNumber(value) {
  const n = typeof value === "string" ? Number(value) : value;
  return typeof n === "number" && Number.isFinite(n) && n >= 0 ? n : null;
}

function modelCost(m) {
  const c = m && typeof m === "object" ? m.cost : null;
  if (!c || typeof c !== "object") return null;
  const out = {
    input: costNumber(c.input),
    output: costNumber(c.output),
    cache_read: costNumber(c.cache_read),
    cache_write: costNumber(c.cache_write),
  };
  if (out.input == null && out.output == null) return null;
  return out;
}

function toRFC3339(dateOnly) {
  return `${dateOnly}T00:00:00Z`;
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  const raw = await loadSource(args);
  const data = JSON.parse(raw);

  let generatedAt = "1970-01-01T00:00:00Z";
  const providers = [];
  for (const [id, p] of Object.entries(data)) {
    if (!p || typeof p !== "object") continue;
    // 契约第 2 节：只收 OpenAI 兼容协议（@ai-sdk/openai-compatible）且提供
    // api 地址的提供商；Anthropic 协议第一期不收录。
    if (p.npm !== "@ai-sdk/openai-compatible") continue;
    const api = typeof p.api === "string" ? p.api.trim() : "";
    if (!api) continue;
    const models = [];
    for (const [modelID, m] of Object.entries(p.models ?? {})) {
      if (!m || typeof m !== "object" || m.tool_call !== true) continue;
      const limit = m.limit ?? {};
      const updated = typeof m.last_updated === "string" ? m.last_updated : "";
      if (/^\d{4}-\d{2}-\d{2}$/.test(updated) && toRFC3339(updated) > generatedAt) {
        generatedAt = toRFC3339(updated);
      }
      models.push({
        id: modelID,
        name: typeof m.name === "string" && m.name.trim() !== "" ? m.name : modelID,
        context_window: positiveNumber(limit.context),
        max_output_tokens: positiveNumber(limit.output),
        reasoning: m.reasoning === true,
        cost: modelCost(m),
      });
    }
    if (models.length === 0) continue;
    models.sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
    providers.push({
      id,
      name: typeof p.name === "string" && p.name.trim() !== "" ? p.name.trim() : id,
      api,
      models,
    });
  }
  providers.sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));

  const catalog = {
    generated_at: generatedAt,
    source: SOURCE_URL,
    providers,
  };
  mkdirSync(dirname(OUT_PATH), { recursive: true });
  writeFileSync(OUT_PATH, JSON.stringify(catalog, null, 2) + "\n", "utf8");

  const modelCount = providers.reduce((sum, p) => sum + p.models.length, 0);
  console.log(
    `models.dev 目录已生成：${providers.length} 家提供商、${modelCount} 个模型 -> ${OUT_PATH}`,
  );
}

main().catch((err) => {
  console.error(`gen-model-catalog: ${err?.message ?? err}`);
  process.exit(1);
});
