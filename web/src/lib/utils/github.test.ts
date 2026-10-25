import { describe, expect, it } from "vitest";
import { codeRepoUrlForInput, normalizeCodeRepoRef } from "./github";

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

describe("codeRepoUrlForInput", () => {
  it("服务端投影有值时原样回填（forgejo 集成是实例基址形态）", () => {
    expect(
      codeRepoUrlForInput({
        code_repo_url: "https://forge.example.com/acme/app",
      }),
    ).toBe("https://forge.example.com/acme/app");
  });

  it("GHE/公共主机由服务端选好：回填不自拼 github.com", () => {
    expect(
      codeRepoUrlForInput({
        code_repo_url: "https://ghe.corp.example/acme/app",
      }),
    ).toBe("https://ghe.corp.example/acme/app");
  });

  it("投影为 null（无代码仓或悬空绑定）回填空串，不回落 github 地址", () => {
    expect(codeRepoUrlForInput({ code_repo_url: null })).toBe("");
    expect(codeRepoUrlForInput({})).toBe("");
  });
});
