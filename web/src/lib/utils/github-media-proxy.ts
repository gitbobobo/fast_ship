const MEDIA_PROXY_PATH = "/api/github/media-proxy";
const MEDIA_PROXY_TOKEN_PARAM = "token";

function isGitHubMediaUrl(value: string) {
  try {
    const parsed = new URL(value, window.location.origin);
    const host = parsed.hostname.toLowerCase();

    if (host === "github.com") {
      return parsed.pathname.startsWith("/user-attachments/assets/");
    }

    return (
      host === "githubusercontent.com" ||
      host === "githubassets.com" ||
      host.endsWith(".githubusercontent.com") ||
      host.endsWith(".githubassets.com")
    );
  } catch {
    return false;
  }
}

function buildRelativeUrl(url: URL) {
  return `${url.pathname}${url.search}${url.hash}`;
}

function attachMediaProxyToken(url: URL, token?: string | null) {
  if (token) {
    url.searchParams.set(MEDIA_PROXY_TOKEN_PARAM, token);
    return;
  }
  url.searchParams.delete(MEDIA_PROXY_TOKEN_PARAM);
}

function isSameOriginMediaProxyUrl(value: string) {
  try {
    const parsed = new URL(value, window.location.origin);
    return parsed.origin === window.location.origin && parsed.pathname === MEDIA_PROXY_PATH;
  } catch {
    return false;
  }
}

export function toGitHubMediaProxyUrl(value?: string | null, token?: string | null): string | undefined {
  if (!value) {
    return undefined;
  }

  if (isSameOriginMediaProxyUrl(value)) {
    const proxyURL = new URL(value, window.location.origin);
    attachMediaProxyToken(proxyURL, token);
    return buildRelativeUrl(proxyURL);
  }

  if (!isGitHubMediaUrl(value)) {
    return value;
  }

  const proxyURL = new URL(MEDIA_PROXY_PATH, window.location.origin);
  proxyURL.searchParams.set("url", value);
  attachMediaProxyToken(proxyURL, token);
  return buildRelativeUrl(proxyURL);
}

export function rewriteGitHubMediaHtml(html?: string | null, token?: string | null): string | undefined {
  if (!html) {
    return undefined;
  }

  if (typeof DOMParser === "undefined") {
    return html;
  }

  const doc = new DOMParser().parseFromString(html, "text/html");
  let changed = false;

  const rewriteAttr = (selector: string, attr: string) => {
    doc.body.querySelectorAll<HTMLElement>(selector).forEach((element) => {
      const current = element.getAttribute(attr);
      const next = toGitHubMediaProxyUrl(current, token);
      if (!current || !next || next === current) {
        return;
      }
      element.setAttribute(attr, next);
      changed = true;
    });
  };

  rewriteAttr("img", "src");
  rewriteAttr("video", "src");
  rewriteAttr("video", "poster");
  rewriteAttr("source", "src");

  return changed ? doc.body.innerHTML : html;
}
