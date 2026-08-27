import { toast } from "sonner";

import { copyToClipboard } from "@/lib/utils";

/** 复制文本到剪贴板，并通过 toast 反馈成功/失败。成功返回 true，失败返回 false。 */
export async function copyWithToast(
  text: string,
  successMessage: string,
  errorMessage = "复制失败",
): Promise<boolean> {
  try {
    await copyToClipboard(text);
    toast.success(successMessage);
    return true;
  } catch {
    toast.error(errorMessage);
    return false;
  }
}
