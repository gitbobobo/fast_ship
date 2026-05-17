import { clsx, type ClassValue } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(...inputs))
}

// Fallback for contexts where navigator.clipboard is unavailable
export function copyToClipboard(text: string): void {
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
