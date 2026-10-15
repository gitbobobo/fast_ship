import path from "node:path";
import { fileURLToPath } from "node:url";

// 业务代码只允许通过 OptionSelect 使用选择器：它把 options 同时传给下拉项和
// Select 的 items，保证收起态显示标签。直接使用原始 Select（components/ui/select
// 或 @base-ui/react/select）一律报错；需要新封装时在 ui/ 层加文件并在此登记。
// happy-dom 环境下全局 URL 被替换，不能用 new URL() 解析 import.meta.url。
const HERE =
  import.meta.dirname ?? path.dirname(fileURLToPath(import.meta.url));
const SRC_ROOT = path.resolve(HERE, "../src");
const UI_SELECT = path.join(SRC_ROOT, "components/ui/select");
const ALLOWED_FILES = new Set([
  path.join(SRC_ROOT, "components/ui/select.tsx"),
  path.join(SRC_ROOT, "components/ui/option-select.tsx"),
]);

function resolvesToUiSelect(filename, source) {
  if (source === "@/components/ui/select") return true;
  if (!source.startsWith(".")) return false;
  const resolved = path.resolve(path.dirname(filename), source);
  return resolved === UI_SELECT;
}

export default {
  meta: {
    type: "problem",
    docs: {
      description:
        "禁止业务代码直接使用原始 Select，统一走 OptionSelect 以保证标签显示",
    },
    messages: {
      noRawSelect:
        "业务代码请使用 OptionSelect（@/components/ui/option-select），不要直接使用原始 Select：它不传 items 时收起会显示内部 value。",
      noBaseSelect:
        "业务代码请使用 OptionSelect（@/components/ui/option-select），不要直接从 @base-ui/react/select 引入 Select。",
    },
    schema: [],
  },
  create(context) {
    if (ALLOWED_FILES.has(context.filename)) return {};
    return {
      ImportDeclaration(node) {
        const source = node.source.value;
        if (typeof source !== "string") return;
        if (source === "@base-ui/react/select") {
          context.report({ node: node.source, messageId: "noBaseSelect" });
          return;
        }
        if (resolvesToUiSelect(context.filename, source)) {
          context.report({ node: node.source, messageId: "noRawSelect" });
        }
      },
    };
  },
};
