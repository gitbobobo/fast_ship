import { describe, expect, it } from "vitest";
import {
  codeRepoSlugFromSaved,
  hostFromRepoInput,
  codeRepoUrlFromSlug,
  normalizeCodeRepoRef,
  ownerRepoFromInput,
} from "./github";

describe("normalizeCodeRepoRef", () => {
  it("scheme 有无与大小写、尾斜杠、.git 后缀都是同一形态（与后端 parseRepositoryURL 同口径）", () => {
    const same = "github.com/godbobo/fast_ship";
    for (const input of [
      "godbobo/fast_ship",
      "https://github.com/godbobo/fast_ship",
      "HTTPS://GitHub.COM/godbobo/FAST_SHIP/",
      "github.com/godbobo/fast_ship.git",
      "https://github.com/godbobo/fast_ship.git/",
    ]) {
      expect(normalizeCodeRepoRef(input)).toBe(same);
    }
  });

  it("非 github 主机归一小写 host:port/owner/repo；显式端口保留", () => {
    // 端口缺省补齐（443/80）与后端 parseRepositoryURL 的主机键同口径：
    // 无 scheme 与 https 都归 :443。
    const same = "git.example.com:443/godbobo/fast_ship";
    for (const input of [
      "git.example.com/godbobo/fast_ship",
      "https://Git.Example.com/godbobo/fast_ship/",
      "git.example.com/godbobo/fast_ship.git",
    ]) {
      expect(normalizeCodeRepoRef(input)).toBe(same);
    }
    expect(
      normalizeCodeRepoRef("https://git.example.com:8443/godbobo/fast_ship.git/"),
    ).toBe("git.example.com:8443/godbobo/fast_ship");
  });

  it("数字端口统一拼写：:0443 与 :443 归一成同一主机键（对齐后端 repoHostKey）", () => {
    expect(
      normalizeCodeRepoRef("https://git.example.com:0443/o/r"),
    ).toBe("git.example.com:443/o/r");
    expect(normalizeCodeRepoRef("https://git.example.com:0443/o/r")).toBe(
      normalizeCodeRepoRef("https://git.example.com:443/o/r"),
    );
    // IPv6 带端口剥方括号（与 SplitHostPort 同口径）。
    expect(normalizeCodeRepoRef("https://[::1]:8443/o/r")).toBe(
      "::1:8443/o/r",
    );
    // 无端口与默认端口两种写法剥括号后归一键相同（R48 Note）。
    expect(normalizeCodeRepoRef("https://[::1]/o/r")).toBe(
      normalizeCodeRepoRef("https://[::1]:443/o/r"),
    );
  });

  it("DNS 根点尾缀等价：forge.example. 与 forge.example 归一成同一主机键", () => {
    expect(normalizeCodeRepoRef("https://git.example.com./o/r")).toBe(
      "git.example.com:443/o/r",
    );
    expect(normalizeCodeRepoRef("https://git.example.com.:8443/o/r")).toBe(
      "git.example.com:8443/o/r",
    );
    expect(normalizeCodeRepoRef("git.example.com./o/r")).toBe(
      normalizeCodeRepoRef("git.example.com/o/r"),
    );
  });

  it("scheme 决定默认端口：http/https 同主机归一不同（集成切换不被判未改动）", () => {
    expect(normalizeCodeRepoRef("http://git.example.com/a/b")).toBe(
      "git.example.com:80/a/b",
    );
    expect(normalizeCodeRepoRef("http://git.example.com/a/b")).not.toBe(
      normalizeCodeRepoRef("https://git.example.com/a/b"),
    );
    // github.com 任意 scheme/默认端口归回裸主机名（内置 github-default）。
    expect(normalizeCodeRepoRef("http://github.com/a/b")).toBe(
      "github.com/a/b",
    );
    expect(normalizeCodeRepoRef("https://github.com:443/a/b")).toBe(
      "github.com/a/b",
    );
    expect(normalizeCodeRepoRef("https://github.com:8443/a/b")).toBe(
      "github.com:8443/a/b",
    );
  });

  it("剥除顺序先尾斜杠后 .git：「…/r.git/」剥得干净（对齐后端同序）", () => {
    expect(normalizeCodeRepoRef("godbobo/fast_ship.git/")).toBe(
      "github.com/godbobo/fast_ship",
    );
  });

  it(".git 后缀大小写敏感：非小写 .git 是仓库名的一部分，不剥（对齐后端 TrimSuffix）", () => {
    expect(normalizeCodeRepoRef("godbobo/fast_ship.GIT")).toBe(
      "github.com/godbobo/fast_ship.git",
    );
    expect(normalizeCodeRepoRef("godbobo/fast_ship.GIT")).not.toBe(
      normalizeCodeRepoRef("godbobo/fast_ship"),
    );
  });

  it("owner/repo 之外的多余路径段原样返回：归一结果与任何现值不等，交后端 40001 表态", () => {
    for (const input of [
      "a/b/c",
      "https://github.com/godbobo/fast_ship/issues",
      "https://forge.example.com/acme/app/pulls/1",
    ]) {
      expect(normalizeCodeRepoRef(input)).toBe(input);
    }
    // 哨兵必然不与任何合法现值归一相等 → 「未改动」判定失效，输入原样外发。
    expect(normalizeCodeRepoRef("a/b/c")).not.toBe(
      normalizeCodeRepoRef("github.com/a/b"),
    );
  });

  it("解析不出 owner/repo 返回空串：调用方按「有输入待服务端校验」处理", () => {
    expect(normalizeCodeRepoRef("")).toBe("");
    expect(normalizeCodeRepoRef("godbobo")).toBe("");
    expect(normalizeCodeRepoRef("/fast_ship")).toBe("");
    expect(normalizeCodeRepoRef("godbobo/")).toBe("");
    expect(normalizeCodeRepoRef("godbobo//")).toBe("");
  });

  it("「o/r/.git」先剥 .git 留下的尾斜杠不算多余路径段（与后端 SplitN 第三段为空同口径）", () => {
    expect(normalizeCodeRepoRef("godbobo/fast_ship/.git")).toBe(
      "github.com/godbobo/fast_ship",
    );
  });

  it("非 http(s) scheme 原样返回：与后端「协议无效 40001」同口径，不静默当 https", () => {
    for (const input of [
      "ssh://git.example.com/acme/app",
      "ftp://github.com/acme/app",
      "git://git.example.com/acme/app",
    ]) {
      expect(normalizeCodeRepoRef(input)).toBe(input);
    }
    expect(normalizeCodeRepoRef("ssh://git.example.com/a/b")).not.toBe(
      normalizeCodeRepoRef("https://git.example.com/a/b"),
    );
  });
});

describe("codeRepoSlugFromSaved", () => {
  it("剥掉 scheme 与主机段：forgejo 投影剥出 owner/repo", () => {
    expect(codeRepoSlugFromSaved("https://forge.example.com/acme/app")).toEqual({
      owner: "acme",
      repo: "app",
    });
  });

  it("GHE 投影同样剥出 owner/repo，主机归属服务端（R41 P2）", () => {
    expect(codeRepoSlugFromSaved("https://ghe.corp.example/acme/app")).toEqual({
      owner: "acme",
      repo: "app",
    });
  });

  it("裸 owner/repo 与裸 github.com/ 前缀形态都剥出 owner/repo", () => {
    expect(codeRepoSlugFromSaved("acme/app")).toEqual({ owner: "acme", repo: "app" });
    expect(codeRepoSlugFromSaved("github.com/acme/app")).toEqual({
      owner: "acme",
      repo: "app",
    });
  });

  it("尾斜杠与 .git 后缀剥掉（与后端同序）", () => {
    expect(codeRepoSlugFromSaved("https://forge.example.com/acme/app.git/")).toEqual({
      owner: "acme",
      repo: "app",
    });
  });

  it("含点的组织名（裸形态两段）不被误当主机剥掉", () => {
    expect(codeRepoSlugFromSaved("my.org/app")).toEqual({ owner: "my.org", repo: "app" });
  });

  it("解析不出两段时返回空 owner（悬空绑定/null 投影回填为空）", () => {
    expect(codeRepoSlugFromSaved("")).toEqual({ owner: "", repo: "" });
    expect(codeRepoSlugFromSaved("https://forge.example.com/acme")).toEqual({
      owner: "",
      repo: "",
    });
  });
});

describe("ownerRepoFromInput", () => {
  it("接受裸 owner/repo、带 scheme 的完整地址与 github.com/ 前缀", () => {
    expect(ownerRepoFromInput("acme/app")).toEqual({ owner: "acme", repo: "app" });
    expect(ownerRepoFromInput("https://forge.example.com/acme/app")).toEqual({
      owner: "acme",
      repo: "app",
    });
    expect(ownerRepoFromInput("github.com/acme/app")).toEqual({
      owner: "acme",
      repo: "app",
    });
  });

  it("无 scheme 的三段输入不猜主机：含点的首段可能是组织名", () => {
    expect(ownerRepoFromInput("my.org/a/b")).toEqual({ owner: "", repo: "" });
  });

  it("单段、空段与 .git/尾斜杠外的残段返回空 owner", () => {
    expect(ownerRepoFromInput("acme")).toEqual({ owner: "", repo: "" });
    expect(ownerRepoFromInput("acme/")).toEqual({ owner: "", repo: "" });
    expect(ownerRepoFromInput("https://forge.example.com/")).toEqual({
      owner: "",
      repo: "",
    });
  });

  it("尾斜杠与 .git 后缀剥掉", () => {
    expect(ownerRepoFromInput("acme/app.git/")).toEqual({ owner: "acme", repo: "app" });
  });
});

describe("codeRepoUrlFromSlug", () => {
  const githubDefault: Pick<IntegrationOption, "kind" | "base_url"> = {
    kind: "github",
    base_url: "https://github.com",
  };
  const forgejo: Pick<IntegrationOption, "kind" | "base_url"> = {
    kind: "forgejo",
    base_url: "https://forge.example.com",
  };

  it("内置 github 传裸 owner/repo，不自拼 scheme（部署 GHE 由服务端分辨）", () => {
    expect(codeRepoUrlFromSlug(githubDefault, { owner: "acme", repo: "app" })).toBe(
      "acme/app",
    );
  });

  it("forgejo 传 {base_url}/{owner}/{repo}，基址尾斜杠归一", () => {
    expect(codeRepoUrlFromSlug(forgejo, { owner: "acme", repo: "app" })).toBe(
      "https://forge.example.com/acme/app",
    );
    expect(
      codeRepoUrlFromSlug({ kind: "forgejo", base_url: "https://forge.example.com/" }, { owner: "acme", repo: "app" }),
    ).toBe("https://forge.example.com/acme/app");
  });

  it("合成结果与归一口径互通：未改动的 forgejo 绑定归一相等", () => {
    expect(
      normalizeCodeRepoRef(codeRepoUrlFromSlug(forgejo, { owner: "acme", repo: "app" })),
    ).toBe(normalizeCodeRepoRef("https://forge.example.com/acme/app"));
  });
});

describe("hostFromRepoInput", () => {
  it("scheme 形态与裸主机段都能取出主机，owner/repo 与裸 github.com 前缀返回空", () => {
    expect(hostFromRepoInput("https://forge-b.example.com/acme/app")).toBe("forge-b.example.com");
    expect(hostFromRepoInput("forge-b.example.com/acme/app")).toBe("forge-b.example.com");
    expect(hostFromRepoInput("forge.example.com:8443/acme/app")).toBe("forge.example.com:8443");
    expect(hostFromRepoInput("acme/app")).toBe("");
    expect(hostFromRepoInput("")).toBe("");
    // github.com/ 前缀是裸主机段（含点）——跨主机比对仍生效。
    expect(hostFromRepoInput("github.com/acme/app")).toBe("github.com");
  });
});
