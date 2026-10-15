import path from "node:path";

// 业务代码只允许通过 OptionSelect 使用选择器：它把 options 同时传给下拉项和
// Select 的 items，保证收起态显示标签。直接使用原始 Select（components/ui/select
// 或 @base-ui/react/select）一律报错；需要新封装时在 ui/ 层加文件并在此登记。
// happy-dom 环境下全局 URL 被替换，不能用 new URL() 解析 import.meta.url；
// import.meta.dirname 需要 Node 20.11+，本仓库工具链（ESLint 9 / Vitest 4）满足。
const HERE = import.meta.dirname;
const SRC_ROOT = path.resolve(HERE, "../src");
const UI_SELECT = path.join(SRC_ROOT, "components/ui/select");
const ALLOWED_FILES = new Set([
  path.join(SRC_ROOT, "components/ui/select.tsx"),
  path.join(SRC_ROOT, "components/ui/option-select.tsx"),
]);

const MODULE_EXT = /\.(tsx?|jsx?|mts|cts)$/;

function resolvesToUiSelect(filename, source) {
  const bare = source.replace(MODULE_EXT, "");
  if (bare === "@/components/ui/select") return true;
  if (!bare.startsWith(".")) return false;
  const resolved = path.resolve(path.dirname(filename), bare);
  return resolved === UI_SELECT;
}

// 模块说明符里 Select 组件的来源：返回 "ui" | "baseui" | null。
// @base-ui/react 根包会再导出 Select 命名空间：具名引入/转发 Select、
// 命名空间/默认导入、export *、import() 这些能拿到 Select 的形式全部拦截；
// import type / export type 不产出运行时代码，不算使用。
function selectSource(filename, source, specifiers) {
  if (source === "@base-ui/react/select") return "baseui";
  if (source === "@base-ui/react") {
    if (specifiers == null) return "baseui";
    const hasSelect = specifiers.some(
      (s) =>
        s.type === "ImportNamespaceSpecifier" ||
        s.type === "ImportDefaultSpecifier" ||
        s.type === "ExportNamespaceSpecifier" ||
        ((s.type === "ImportSpecifier" || s.type === "ExportSpecifier") &&
          s.importKind !== "type" &&
          (s.imported?.name ?? s.local?.name) === "Select"),
    );
    return hasSelect ? "baseui" : null;
  }
  return resolvesToUiSelect(filename, source) ? "ui" : null;
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
        "业务代码请使用 OptionSelect（@/components/ui/option-select），不要直接从 @base-ui/react（/select）引入 Select。",
    },
    schema: [],
  },
  create(context) {
    if (ALLOWED_FILES.has(context.filename)) return {};

    function check(node, sourceNode, specifiers) {
      if (node.importKind === "type" || node.exportKind === "type") return;
      const source = sourceNode?.value;
      if (typeof source !== "string") return;
      const kind = selectSource(context.filename, source, specifiers);
      if (kind === "baseui") {
        context.report({ node: sourceNode, messageId: "noBaseSelect" });
      } else if (kind === "ui") {
        context.report({ node: sourceNode, messageId: "noRawSelect" });
      }
    }

    return {
      ImportDeclaration(node) {
        check(node, node.source, node.specifiers);
      },
      ExportNamedDeclaration(node) {
        if (node.source) check(node, node.source, node.specifiers);
      },
      ExportAllDeclaration(node) {
        check(node, node.source, null);
      },
      ImportExpression(node) {
        check(node, node.source, null);
      },
    };
  },
};
