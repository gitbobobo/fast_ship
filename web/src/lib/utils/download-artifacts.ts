function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException("Aborted", "AbortError"));
      return;
    }

    const timeoutId = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);

    const onAbort = () => {
      clearTimeout(timeoutId);
      reject(new DOMException("Aborted", "AbortError"));
    };

    signal?.addEventListener("abort", onAbort, { once: true });
  });
}

function triggerDownload(url: string): void {
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.setAttribute("download", "");
  document.body.appendChild(anchor);
  try {
    anchor.click();
  } finally {
    document.body.removeChild(anchor);
  }
}

export async function downloadAllArtifacts(
  urls: string[],
  options?: { intervalMs?: number; signal?: AbortSignal },
): Promise<void> {
  if (urls.length === 0) {
    return;
  }

  const intervalMs = options?.intervalMs ?? 400;
  const { signal } = options ?? {};

  for (const [index, url] of urls.entries()) {
    if (signal?.aborted) {
      return;
    }

    triggerDownload(url);

    if (index < urls.length - 1) {
      try {
        await sleep(intervalMs, signal);
      } catch {
        return;
      }
    }
  }
}
