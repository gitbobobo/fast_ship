import { useWatch, type Control } from "react-hook-form";
import type {
  ProjectInput,
  ProjectEditInput,
} from "@/lib/utils/validators";
import { parsePrTokenSourceValue } from "@/lib/utils/token-source";

type FormData = ProjectInput | ProjectEditInput;
type TokenField = "github_token" | "github_pr_token";
type SourceField = "source_project_id" | "pr_token_source_project_id";
type KindField = "pr_token_source_kind";

// 下拉选中值直接 derive 自表单字段（useWatch），不在 hook 里另存 state：
// 对话框常驻挂载、reset 只清表单值，本地 state 会残留导致显示与提交不一致。
// PR 侧传入 kindField 后，选中值是 `<projectId>:<kind>` 复合值，选中即同时
// 写入源项目与凭证种类；Access 侧不传，tokenSource 仍是源项目 id。
export function useTokenSource(
  control: Control<FormData>,
  setValue: (
    name: TokenField | SourceField | KindField,
    value: string | undefined,
  ) => void,
  tokenField: TokenField,
  sourceField: SourceField,
  kindField?: KindField,
) {
  const sourceId = useWatch({ control, name: sourceField }) ?? "";
  const sourceKind = useWatch({ control, name: kindField ?? sourceField });
  const tokenSource =
    kindField && sourceId ? `${sourceId}:${sourceKind ?? "pr"}` : sourceId;

  const handleTokenSourceChange = (value: string | null) => {
    const v = value ?? "";
    if (v === "") {
      setValue(tokenField, "");
      setValue(sourceField, undefined);
      if (kindField) setValue(kindField, undefined);
      return;
    }
    setValue(tokenField, undefined);
    if (kindField) {
      const parsed = parsePrTokenSourceValue(v);
      setValue(sourceField, parsed?.projectId);
      setValue(kindField, parsed?.kind);
    } else {
      setValue(sourceField, v);
    }
  };

  return { tokenSource, handleTokenSourceChange };
}
