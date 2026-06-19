import { clsx, type ClassValue } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(...inputs))
}

export async function copyToClipboard(text: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(text)
    return
  }

  // Fallback for contexts where navigator.clipboard is unavailable
  const textarea = document.createElement("textarea")
  textarea.value = text
  textarea.style.cssText = "position:fixed;opacity:0"
  document.body.appendChild(textarea)
  textarea.select()
  try {
    if (!document.execCommand("copy")) {
      throw new Error("execCommand copy failed")
    }
  } finally {
    document.body.removeChild(textarea)
  }
}
