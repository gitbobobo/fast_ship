import { useState } from "react";

export function useTokenSource(
  setValue: (name: "github_token" | "source_project_id", value: string | undefined) => void,
) {
  const [tokenSource, setTokenSource] = useState<string>("");

  const handleTokenSourceChange = (value: string | null) => {
    const v = value ?? "";
    setTokenSource(v);
    if (v === "") {
      setValue("github_token", "");
      setValue("source_project_id", undefined);
    } else {
      setValue("github_token", undefined);
      setValue("source_project_id", v);
    }
  };

  return { tokenSource, handleTokenSourceChange };
}
