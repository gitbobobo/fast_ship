import { useAuthStore } from "@/lib/store/auth-store";
import { api, tryRefreshToken } from "./client";

interface UploadResult {
  status: number;
  response: ApiResponse<IssueAttachment> | null;
}

function parseUploadResponse(xhr: XMLHttpRequest) {
  if (!xhr.responseText) {
    return null;
  }

  // 反向代理的 413/502 等返回 HTML，解析失败按无响应体处理，
  // 交给 requireUploadSuccess 用状态码报错，不能让 Promise 挂起。
  try {
    return JSON.parse(xhr.responseText) as ApiResponse<IssueAttachment>;
  } catch {
    return null;
  }
}

function createUploadXhr(
  issueId: string,
  token: string | null,
  onProgress?: (percent: number) => void,
) {
  const xhr = new XMLHttpRequest();
  xhr.open("POST", `/api/issues/${issueId}/attachments`);

  if (token) {
    xhr.setRequestHeader("Authorization", `Bearer ${token}`);
  }

  xhr.upload.addEventListener("progress", (event) => {
    if (!event.lengthComputable || !onProgress) return;
    const percent = Math.round((event.loaded / event.total) * 100);
    onProgress(percent);
  });

  return xhr;
}

function sendUploadRequest(
  issueId: string,
  formData: FormData,
  token: string | null,
  onProgress?: (percent: number) => void,
) {
  return new Promise<UploadResult>((resolve, reject) => {
    const xhr = createUploadXhr(issueId, token, onProgress);

    xhr.addEventListener("load", () => {
      resolve({
        status: xhr.status,
        response: parseUploadResponse(xhr),
      });
    });

    xhr.addEventListener("error", () => {
      reject(new Error("上传失败"));
    });

    xhr.addEventListener("abort", () => {
      reject(new Error("上传已取消"));
    });

    xhr.addEventListener("timeout", () => {
      reject(new Error("上传超时"));
    });

    xhr.send(formData);
  });
}

function requireUploadSuccess(result: UploadResult) {
  if (result.status >= 200 && result.status < 300 && result.response) {
    return result.response;
  }

  throw new Error(result.response?.message || "上传失败");
}

export const attachmentApi = {
  upload: async (
    issueId: string,
    formData: FormData,
    onProgress?: (percent: number) => void,
  ) => {
    const token = useAuthStore.getState().token;
    const result = await sendUploadRequest(issueId, formData, token, onProgress);

    if (result.status !== 401) {
      return requireUploadSuccess(result);
    }

    let newToken: string;
    try {
      newToken = await tryRefreshToken();
    } catch {
      useAuthStore.getState().logout();
      window.location.href = "/login";
      throw new Error("Unauthorized");
    }

    const retryResult = await sendUploadRequest(
      issueId,
      formData,
      newToken,
      onProgress,
    );
    return requireUploadSuccess(retryResult);
  },

  delete: (aid: string) =>
    api.delete(`attachments/${aid}`).json<ApiResponse<null>>(),

  // 下载走浏览器导航而非 ky；优先用详情响应携带的 download_url，附带 jwt
  downloadUrl: (attachment: Pick<IssueAttachment, "id" | "download_url">) => {
    const token = useAuthStore.getState().token;
    const base =
      attachment.download_url || `/api/attachments/${attachment.id}/download`;
    const url = new URL(base, window.location.origin);

    if (token) {
      url.searchParams.set("token", token);
    }

    return `${url.pathname}${url.search}`;
  },
};
