import { Linter } from "eslint";
import path from "node:path";
import tseslint from "typescript-eslint";
import plugin from "./index.js";

const tsParser = tseslint.parser;

const linter = new Linter();
const SRC = path.resolve(process.cwd(), "src");
const BUSINESS_FILE = `${SRC}/routes/example.tsx`;
const OPTION_SELECT_FILE = `${SRC}/components/ui/option-select.tsx`;

function lint(code, filename = BUSINESS_FILE) {
  return linter.verify(
    `function App() { return (${code}); }`,
    [
      {
        files: ["**/*.{ts,tsx}"],
        plugins: { "fast-ship": plugin },
        languageOptions: {
          ecmaVersion: 2023,
          sourceType: "module",
          parserOptions: { ecmaFeatures: { jsx: true } },
        },
        rules: {
          "fast-ship/select-value-label": "error",
          "fast-ship/no-raw-select": "error",
        },
      },
    ],
    { filename },
  );
}

describe("fast-ship/select-value-label", () => {
  it("报错：SelectValue 无子内容且 Select 没传 items", () => {
    const messages = lint(
      `<Select value={v}><SelectTrigger><SelectValue /></SelectTrigger></Select>`,
    );
    expect(messages).toHaveLength(1);
    expect(messages[0].ruleId).toBe("fast-ship/select-value-label");
  });

  it("通过：Select 传了 items", () => {
    const messages = lint(
      `<Select value={v} items={items}><SelectTrigger><SelectValue /></SelectTrigger></Select>`,
    );
    expect(messages).toHaveLength(0);
  });

  it("通过：SelectValue 有子内容", () => {
    const messages = lint(
      `<Select value={v}><SelectTrigger><SelectValue>{labelOf(v)}</SelectValue></SelectTrigger></Select>`,
    );
    expect(messages).toHaveLength(0);
  });

  it("通过：SelectValue 的子内容是函数", () => {
    const messages = lint(
      `<Select value={v}><SelectTrigger><SelectValue>{(value) => labelOf(value)}</SelectValue></SelectTrigger></Select>`,
    );
    expect(messages).toHaveLength(0);
  });

  it("报错：placeholder 不算子内容", () => {
    const messages = lint(
      `<Select value={v}><SelectTrigger><SelectValue placeholder="选择" /></SelectTrigger></Select>`,
    );
    expect(messages).toHaveLength(1);
  });

  it("报错：SelectValue 不在任何 Select 里", () => {
    const messages = lint(`<div><SelectValue /></div>`);
    expect(messages).toHaveLength(1);
  });

  it("通过：成员写法 <Select.Root items> 下的 <Select.Value />", () => {
    const messages = lint(
      `<Select.Root value={v} items={items}><Select.Trigger><Select.Value /></Select.Trigger></Select.Root>`,
    );
    expect(messages).toHaveLength(0);
  });

  it("报错：成员写法 <Select.Root> 没传 items", () => {
    const messages = lint(
      `<Select.Root value={v}><Select.Trigger><Select.Value /></Select.Trigger></Select.Root>`,
    );
    expect(messages).toHaveLength(1);
  });

  it("报错：{undefined} / {null} / {false} / {\"\"} 不算子内容", () => {
    for (const empty of ["undefined", "null", "false", '""']) {
      const messages = lint(
        `<Select value={v}><SelectTrigger><SelectValue>{${empty}}</SelectValue></SelectTrigger></Select>`,
      );
      expect(messages).toHaveLength(1);
    }
  });

  it("报错：children={undefined} 不算子内容", () => {
    const messages = lint(
      `<Select value={v}><SelectTrigger><SelectValue children={undefined} /></SelectTrigger></Select>`,
    );
    expect(messages).toHaveLength(1);
  });

  it("报错：{...props} 展开不算传了 items", () => {
    const messages = lint(
      `<Select value={v} {...props}><SelectTrigger><SelectValue /></SelectTrigger></Select>`,
    );
    expect(messages).toHaveLength(1);
  });

  it("报错：items={undefined} / {null} / {\"\"} 等于没传", () => {
    for (const empty of ["undefined", "null", '""']) {
      const messages = lint(
        `<Select value={v} items={${empty}}><SelectTrigger><SelectValue /></SelectTrigger></Select>`,
      );
      expect(messages).toHaveLength(1);
    }
  });
});

describe("fast-ship/no-raw-select", () => {
  // type-only 语法需要 TS parser，单独开关切换。
  function lintModule(code, filename = BUSINESS_FILE, useTsParser = false) {
    return linter.verify(
      code,
      [
        {
          files: ["**/*.{ts,tsx}"],
          plugins: { "fast-ship": plugin },
          languageOptions: {
            ...(useTsParser
              ? { parser: tsParser }
              : { ecmaVersion: 2023 }),
            sourceType: "module",
            parserOptions: { ecmaFeatures: { jsx: true } },
          },
          rules: { "fast-ship/no-raw-select": "error" },
        },
      ],
      { filename },
    );
  }

  it("报错：业务文件引入 @/components/ui/select", () => {
    const messages = lintModule(
      `import { Select } from "@/components/ui/select";`,
    );
    expect(messages).toHaveLength(1);
    expect(messages[0].ruleId).toBe("fast-ship/no-raw-select");
  });

  it("报错：业务文件用相对路径引入 ui/select", () => {
    const messages = lintModule(
      `import { Select } from "../components/ui/select";`,
    );
    expect(messages).toHaveLength(1);
  });

  it("报错：业务文件直接从 @base-ui/react/select 引入", () => {
    const messages = lintModule(
      `import { Select } from "@base-ui/react/select";`,
    );
    expect(messages).toHaveLength(1);
  });

  it("通过：引入 OptionSelect", () => {
    const messages = lintModule(
      `import { OptionSelect } from "@/components/ui/option-select";`,
    );
    expect(messages).toHaveLength(0);
  });

  it("通过：option-select.tsx 封装文件内部可以使用原始 Select", () => {
    const messages = lintModule(
      `import { Select } from "./select";`,
      OPTION_SELECT_FILE,
    );
    expect(messages).toHaveLength(0);
  });

  it("报错：带扩展名的 ui/select 引入", () => {
    for (const source of [
      "@/components/ui/select.tsx",
      "../components/ui/select.tsx",
      "@base-ui/react/select.js",
    ]) {
      const messages = lintModule(`import { Select } from "${source}";`);
      expect(messages).toHaveLength(1);
    }
  });

  it("报错：export ... from 转发 ui/select", () => {
    const messages = lintModule(
      `export { Select } from "@/components/ui/select";`,
    );
    expect(messages).toHaveLength(1);
  });

  it("报错：动态 import ui/select 或 @base-ui/react/select", () => {
    for (const source of [
      "@/components/ui/select",
      "@base-ui/react/select",
    ]) {
      const messages = lintModule(
        `async function load() { return import("${source}"); }`,
      );
      expect(messages).toHaveLength(1);
    }
  });

  it("报错：从 @base-ui/react 根包具名引入 Select", () => {
    const messages = lintModule(
      `import { Select } from "@base-ui/react";`,
    );
    expect(messages).toHaveLength(1);
  });

  it("通过：从 @base-ui/react 根包引入其他组件", () => {
    const messages = lintModule(
      `import { Dialog } from "@base-ui/react";`,
    );
    expect(messages).toHaveLength(0);
  });

  it("报错：@base-ui/react 根包的命名空间/默认导入都能拿到 Select", () => {
    for (const code of [
      `import * as BUI from "@base-ui/react";`,
      `import BUI from "@base-ui/react";`,
      `async function load() { return import("@base-ui/react"); }`,
    ]) {
      const messages = lintModule(code);
      expect(messages).toHaveLength(1);
    }
  });

  it("报错：export * 转发 @base-ui/react 根包", () => {
    for (const code of [
      `export * from "@base-ui/react";`,
      `export * as ns from "@base-ui/react";`,
      `export { Select } from "@base-ui/react";`,
    ]) {
      const messages = lintModule(code);
      expect(messages).toHaveLength(1);
    }
  });

  it("通过：type-only 引入 Select 不产出运行时代码", () => {
    for (const code of [
      `import type { Select } from "@base-ui/react";`,
      `import { type Select, Combobox } from "@base-ui/react";`,
      `export type { Select } from "@base-ui/react";`,
      `import type { Select } from "@/components/ui/select";`,
    ]) {
      const messages = lintModule(code, BUSINESS_FILE, true);
      expect(messages).toHaveLength(0);
    }
  });

  it("通过：@base-ui/react 副作用导入不接触 Select", () => {
    const messages = lintModule(`import "@base-ui/react";`);
    expect(messages).toHaveLength(0);
  });
});

describe("fast-ship/no-secure-context-crypto", () => {
  const UUID_FILE = `${SRC}/lib/uuid.ts`;

  function lintCode(code, filename = BUSINESS_FILE, useTsParser = false) {
    return linter.verify(
      code,
      [
        {
          files: ["**/*.{ts,tsx}"],
          plugins: { "fast-ship": plugin },
          languageOptions: {
            ...(useTsParser ? { parser: tsParser } : { ecmaVersion: 2023 }),
            sourceType: "module",
            parserOptions: { ecmaFeatures: { jsx: true } },
          },
          rules: { "fast-ship/no-secure-context-crypto": "error" },
        },
      ],
      { filename },
    );
  }

  it("报错：crypto.randomUUID 的各种直读/直调写法", () => {
    for (const code of [
      `crypto.randomUUID();`,
      `const f = crypto.randomUUID;`,
      `globalThis.crypto.randomUUID();`,
      `globalThis.crypto?.randomUUID?.();`,
      `window.crypto.randomUUID();`,
      `self.crypto.randomUUID();`,
      `navigator.crypto.randomUUID();`,
      `crypto["randomUUID"]();`,
      `globalThis["crypto"].randomUUID();`,
    ]) {
      const messages = lintCode(code);
      expect(messages, code).toHaveLength(1);
      expect(messages[0].ruleId).toBe("fast-ship/no-secure-context-crypto");
    }
  });

  it("报错：crypto.subtle 同属仅安全上下文 API", () => {
    const messages = lintCode(`crypto.subtle.digest("SHA-256", data);`);
    expect(messages).toHaveLength(1);
    expect(messages[0].messageId).toBe("blocked");
  });

  it("报错：TS 断言/非空断言不改写就拦不住", () => {
    for (const code of [`crypto!.randomUUID();`, `(crypto as Crypto).randomUUID();`]) {
      const messages = lintCode(code, BUSINESS_FILE, true);
      expect(messages, code).toHaveLength(1);
    }
  });

  it("报错：解构 crypto 拿到 randomUUID", () => {
    for (const code of [
      `const { randomUUID } = crypto;`,
      `const { randomUUID: gen } = globalThis.crypto;`,
      `const { ["randomUUID"]: gen } = crypto;`,
    ]) {
      const messages = lintCode(code);
      expect(messages, code).toHaveLength(1);
    }
  });

  it("报错：Node 内置 crypto 的具名导入", () => {
    for (const code of [
      `import { randomUUID } from "crypto";`,
      `import { randomUUID } from "node:crypto";`,
    ]) {
      const messages = lintCode(code);
      expect(messages, code).toHaveLength(1);
    }
  });

  it("通过：getRandomValues、第三方对象与 helper 调用", () => {
    for (const code of [
      `crypto.getRandomValues(buf);`,
      `crypto.randomBytes(16);`,
      `foo.randomUUID();`,
      `getCrypto().randomUUID();`,
      `uuid();`,
      `const { getRandomValues } = crypto;`,
      `import { createHash } from "node:crypto";`,
    ]) {
      expect(lintCode(code), code).toHaveLength(0);
    }
  });

  it("放行：src/lib/uuid.ts 是 helper 的唯一实现点", () => {
    const messages = lintCode(
      `export function uuid() { return globalThis.crypto?.randomUUID?.() ?? "x"; }`,
      UUID_FILE,
    );
    expect(messages).toHaveLength(0);
  });
});
