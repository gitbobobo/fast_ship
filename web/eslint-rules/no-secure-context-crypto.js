import path from "node:path";

// crypto.randomUUID 与 crypto.subtle 只在安全上下文（HTTPS / localhost）存在；
// 局域网 HTTP 部署下为 undefined，裸调会在提交瞬间抛 TypeError——界面上就是
// 「点了没反应」。UUID 统一走 @/lib/uuid 的 uuid()（内部自带 getRandomValues
// / Math.random 兜底）；subtle 暂无业务用途，同样禁用。helper 实现文件是唯一
// 豁免点，登记在 ALLOWED_FILES。
const HERE = import.meta.dirname;
const ALLOWED_FILES = new Set([path.resolve(HERE, "../src/lib/uuid.ts")]);

const BLOCKED = new Set(["randomUUID", "subtle"]);
const GLOBAL_HOSTS = new Set(["globalThis", "window", "self", "navigator"]);

function unwrapTs(node) {
  while (
    node &&
    (node.type === "TSNonNullExpression" ||
      node.type === "TSAsExpression" ||
      node.type === "TSTypeAssertion")
  ) {
    node = node.expression;
  }
  return node;
}

function propName(node) {
  if (node.computed) {
    return node.property.type === "Literal" ? node.property.value : null;
  }
  return node.property.type === "Identifier" ? node.property.name : null;
}

// `X.crypto`：X ∈ globalThis/window/self/navigator（同一个 Crypto 对象）。
function isCryptoRef(node) {
  node = unwrapTs(node);
  if (!node) return false;
  if (node.type === "Identifier") return node.name === "crypto";
  return (
    node.type === "MemberExpression" &&
    propName(node) === "crypto" &&
    unwrapTs(node.object)?.type === "Identifier" &&
    GLOBAL_HOSTS.has(unwrapTs(node.object).name)
  );
}

// crypto.X、globalThis.crypto.X（含可选链、计算属性与 TS 断言写法）命中的
// 被禁属性名；其他对象上的同名成员不算。
function blockedName(node) {
  const prop = propName(node);
  if (typeof prop !== "string" || !BLOCKED.has(prop)) return null;
  return isCryptoRef(node.object) ? prop : null;
}

export default {
  meta: {
    type: "problem",
    docs: {
      description:
        "禁止直接使用仅安全上下文可用的 crypto API（randomUUID/subtle），UUID 统一走 @/lib/uuid",
    },
    messages: {
      blocked:
        "crypto.{{name}} 仅在安全上下文（HTTPS/localhost）可用，HTTP 部署下会抛 TypeError；请改用 @/lib/uuid 的 uuid()。",
    },
    schema: [],
  },
  create(context) {
    if (ALLOWED_FILES.has(context.filename)) return {};

    return {
      MemberExpression(node) {
        const name = blockedName(node);
        if (name) {
          context.report({ node, messageId: "blocked", data: { name } });
        }
      },
      // const { randomUUID } = crypto / globalThis.crypto ——解构也是直接使用。
      VariableDeclarator(node) {
        if (node.id.type !== "ObjectPattern" || !isCryptoRef(node.init)) return;
        for (const prop of node.id.properties) {
          if (prop.type !== "Property") continue;
          const key = prop.computed
            ? prop.key.type === "Literal"
              ? prop.key.value
              : null
            : prop.key.type === "Identifier"
              ? prop.key.name
              : null;
          if (typeof key === "string" && BLOCKED.has(key)) {
            context.report({
              node: prop.key,
              messageId: "blocked",
              data: { name: key },
            });
          }
        }
      },
      // Node 内置 crypto 的具名导入同样按直接使用拦截（浏览器里本就不可用）。
      // 默认/命名空间导入的对象别名不追踪——MemberExpression 只认字面 crypto。
      ImportDeclaration(node) {
        const source = node.source.value;
        if (source !== "crypto" && source !== "node:crypto") return;
        for (const spec of node.specifiers) {
          if (spec.type === "ImportSpecifier" && BLOCKED.has(spec.imported.name)) {
            context.report({
              node: spec,
              messageId: "blocked",
              data: { name: spec.imported.name },
            });
          }
        }
      },
    };
  },
};
