import { test, beforeAll, describe, expect } from "vitest";
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";

// The GitLab repository parser lives in the runtime scripts under web/app, not
// in chatbot/src. This suite loads them the same way index.html does, so the
// GitHub / GitLab / generic-SSH dispatch is verified end to end.

const APP_DIR = path.resolve(__dirname, "../../web/app");

function loadScripts() {
  const context = {
    console,
    URL,
    URLSearchParams,
    document: { createElement: () => ({ style: {}, classList: { add() {}, remove() {} } }) },
    location: { origin: "http://localhost:8090", href: "http://localhost:8090/" },
    localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
    setTimeout,
    clearTimeout,
    globalThis: undefined,
  };
  context.globalThis = context;
  context.window = context;
  vm.createContext(context);
  for (const file of ["git-ssh.js", "issues.js"]) {
    const code = fs.readFileSync(path.join(APP_DIR, file), "utf8");
    vm.runInContext(code, context, { filename: file });
  }
  return context;
}

let ctx;

beforeAll(() => {
  ctx = loadScripts();
  // The parser resolves the instance from the signed-in user.
  ctx.state = { currentUser: { id: 1, gitlabBaseUrl: "https://gitlab.example.com" } };
  ctx.userById = (id) => (Number(id) === 1 ? ctx.state.currentUser : null);
});

test("a GitLab https link is recognized as a GitLab repository", () => {
  const info = ctx.parseGongfengRepoURL("https://gitlab.example.com/group/repo");
  expect(info).toBeTruthy();
  expect(info.provider).toBe("gitlab");
  expect(info.project).toBe("gitlab:https://gitlab.example.com:group/repo");
  expect(info.name).toBe("repo");
  expect(info.webUrl).toBe("https://gitlab.example.com/group/repo");
  expect(info.cloneUrl).toBe("https://gitlab.example.com/group/repo.git");
});

test("a nested GitLab group keeps every path segment", () => {
  const info = ctx.parseGongfengRepoURL("https://gitlab.example.com/a/b/c/repo");
  expect(info.provider).toBe("gitlab");
  expect(info.project).toBe("gitlab:https://gitlab.example.com:a/b/c/repo");
  expect(info.name).toBe("repo");
});

test("a GitLab sub page is reduced to the project path", () => {
  const info = ctx.parseGongfengRepoURL("https://gitlab.example.com/group/repo/-/merge_requests/12");
  expect(info.provider).toBe("gitlab");
  expect(info.project).toBe("gitlab:https://gitlab.example.com:group/repo");
});

test("a GitLab scp-style ssh link stays a GitLab repository, not generic Git SSH", () => {
  // The generic Git SSH parser runs first. Without the GitLab host hand-off it
  // would claim this address and the repository would lose its hosting API.
  const info = ctx.parseGongfengRepoURL("git@gitlab.example.com:group/repo.git");
  expect(info).toBeTruthy();
  expect(info.provider).toBe("gitlab");
  expect(info.project).toBe("gitlab:https://gitlab.example.com:group/repo");
  expect(info.cloneUrl).toBe("git@gitlab.example.com:group/repo.git");
});

test("a GitLab ssh url on a dedicated port stays a GitLab repository", () => {
  const info = ctx.parseGongfengRepoURL("ssh://git@gitlab.example.com:2222/group/repo.git");
  expect(info).toBeTruthy();
  expect(info.provider).toBe("gitlab");
  expect(info.project).toBe("gitlab:https://gitlab.example.com:group/repo");
});

test("a bare GitLab project path is resolved against the instance", () => {
  const info = ctx.parseGongfengRepoURL("group/repo");
  expect(info.provider).toBe("gitlab");
  expect(info.project).toBe("gitlab:https://gitlab.example.com:group/repo");
});

test("a link on another host is not a GitLab repository", () => {
  // Another instance's link must not be silently accepted: the configured
  // token is only valid for the configured host.
  const info = ctx.parseGongfengRepoURL("https://gitlab.other.example/group/repo");
  expect(info).toBeFalsy();
});

test("GitHub links keep working", () => {
  const info = ctx.parseGongfengRepoURL("https://github.com/owner/repo");
  expect(info.provider).toBe("github");
  expect(info.project).toBe("github:owner/repo");
});

test("generic git ssh addresses keep working", () => {
  const info = ctx.parseGongfengRepoURL("git@gogs.example.com:group/repo.git");
  expect(info).toBeTruthy();
  expect(info.provider).toBe("ssh");
});

test("the provider label distinguishes GitLab from the other hosts", () => {
  expect(ctx.repoProviderLabel({ provider: "gitlab" })).toBe("GitLab");
  expect(ctx.repoProviderLabel({ provider: "github" })).toBe("GitHub");
  expect(ctx.repoProviderLabel({ provider: "ssh" })).toBe("Git SSH");
});

// A self-managed GitLab commonly runs on a non-standard HTTP port while its
// SSH service uses another one. Both spellings must reach the GitLab adapter,
// not the generic Git SSH one.
describe("an instance on a non-standard port", () => {
  let ported;
  beforeAll(() => {
    ported = loadScripts();
    ported.state = { currentUser: { id: 1, gitlabBaseUrl: "http://gitlab.example.com:8929" } };
    ported.userById = (id) => (Number(id) === 1 ? ported.state.currentUser : null);
  });

  test("an https link on that port is GitLab", () => {
    const info = ported.parseGongfengRepoURL("http://gitlab.example.com:8929/group/repo");
    expect(info.provider).toBe("gitlab");
    expect(info.project).toBe("gitlab:http://gitlab.example.com:8929:group/repo");
  });

  test("an ssh link on a different port is still GitLab", () => {
    const info = ported.parseGongfengRepoURL("git@gitlab.example.com:group/repo.git");
    expect(info).toBeTruthy();
    expect(info.provider).toBe("gitlab");
    expect(info.project).toBe("gitlab:http://gitlab.example.com:8929:group/repo");
  });

  test("an ssh url with its own port is still GitLab", () => {
    const info = ported.parseGongfengRepoURL("ssh://git@gitlab.example.com:2222/group/repo.git");
    expect(info).toBeTruthy();
    expect(info.provider).toBe("gitlab");
  });

  test("a link on a different host is still rejected", () => {
    expect(ported.parseGongfengRepoURL("https://gitlab.other.example:8929/group/repo")).toBeFalsy();
  });
});
