import { toast } from "sonner";

import { copyToClipboard } from "@/lib/utils";

/** 复制文本到剪贴板，并通过 toast 反馈成功/失败。 */
export async function copyWithToast(
  text: string,
  successMessage: string,
  errorMessage = "复制失败",
): Promise<void> {
  try {
    await copyToClipboard(text);
    toast.success(successMessage);
  } catch {
    toast.error(errorMessage);
  }
}
