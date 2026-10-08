import { useState } from "react";

type TokenField = "github_token" | "github_pr_token";
type SourceField = "source_project_id" | "pr_token_source_project_id";

export function useTokenSource(
  setValue: (
    name: TokenField | SourceField,
    value: string | undefined,
  ) => void,
  tokenField: TokenField,
  sourceField: SourceField,
) {
  const [tokenSource, setTokenSource] = useState<string>("");

  const handleTokenSourceChange = (value: string | null) => {
    const v = value ?? "";
    setTokenSource(v);
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
