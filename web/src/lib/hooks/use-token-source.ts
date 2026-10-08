import { useWatch, type Control } from "react-hook-form";
import type {
  ProjectInput,
  ProjectEditInput,
} from "@/lib/utils/validators";

type FormData = ProjectInput | ProjectEditInput;
type TokenField = "github_token" | "github_pr_token";
type SourceField = "source_project_id" | "pr_token_source_project_id";

// 下拉选中值直接 derive 自表单字段（useWatch），不在 hook 里另存 state：
// 对话框常驻挂载、reset 只清表单值，本地 state 会残留导致显示与提交不一致。
export function useTokenSource(
  control: Control<FormData>,
  setValue: (
    name: TokenField | SourceField,
    value: string | undefined,
  ) => void,
  tokenField: TokenField,
  sourceField: SourceField,
) {
  const tokenSource = useWatch({ control, name: sourceField }) ?? "";

  const handleTokenSourceChange = (value: string | null) => {
    const v = value ?? "";
    if (v === "") {
      setValue(tokenField, "");
      setValue(sourceField, undefined);
    } else {
      setValue(tokenField, undefined);
      setValue(sourceField, v);
    }
  };

  return { tokenSource, handleTokenSourceChange };
}
