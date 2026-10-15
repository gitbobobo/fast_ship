import { Linter } from "eslint";
import path from "node:path";
import plugin from "./index.js";

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
});

describe("fast-ship/no-raw-select", () => {
  function lintModule(code, filename = BUSINESS_FILE) {
    return linter.verify(
      code,
      [
        {
          files: ["**/*.{ts,tsx}"],
          plugins: { "fast-ship": plugin },
          languageOptions: {
            ecmaVersion: 2023,
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
});
