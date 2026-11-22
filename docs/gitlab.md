# GitLab 仓库接入

LoopForge 支持 GitLab.com 与自建 GitLab 作为一等代码托管：可以绑定仓库、按用户身份检出与推送、创建与合入 Merge Request（MR），并回写交付状态。

对应上游 [Issue #128](https://github.com/Tencent/LoopForge/issues/128) 与 [PR #62](https://github.com/Tencent/LoopForge/pull/62)。

## 能力边界

| 能力 | 状态 |
| --- | --- |
| 绑定 GitLab.com / 自建 GitLab 仓库 | 支持 |
| 沙箱按用户身份 clone / fetch / push | 支持（HTTPS token 或 SSH 密钥） |
| 创建 MR | 支持 |
| 查找并合入 MR（含源项目身份校验、按 SHA 固定合入） | 支持 |
| MR 状态回写（合并、关闭） | 支持 |
| 拉取 GitLab Issue 到本地需求 | 支持 |
| GitLab Issue 写回（澄清摘要回评） | 未实现 |
| 合入默认分支后的 promote 分支对齐 | 未实现 |

Gitea、Gogs 等其它托管仍走通用 Git SSH（见[部署指南](deployment.md#git-repositories-over-ssh)），不提供 Issue / MR API。

## 配置

### 1. 创建访问令牌

在 GitLab「User Settings → Access Tokens」新建个人访问令牌，勾选：

- `api`（推荐，覆盖全部读写），或
- `read_repository` + `write_repository`（仅仓库读写，MR 操作需要 `api`）

### 2. 填写实例与令牌

「个人配置 → GitLab」填写：

| 字段 | 说明 |
| --- | --- |
| 实例地址 | 自建 GitLab 必填，例如 `https://gitlab.example.com`；子路径部署填 `https://example.com/gitlab`；GitLab.com 可留空 |
| 访问令牌 | `glpat-` 开头的个人访问令牌 |

保存前会调用 `GET /api/v4/user` 校验身份并读取 scope。校验通过才写入，失败不保存。

### 3. 绑定仓库

项目「仓库」页粘贴 GitLab 仓库链接即可，以下形式都接受：

```
https://gitlab.example.com/group/repo
https://gitlab.example.com/group/repo/-/tree/main
https://gitlab.example.com/group/sub/repo/-/merge_requests/12
git@gitlab.example.com:group/repo.git
group/repo
```

`provider` 会自动识别为 `gitlab`，仓库标识形如 `gitlab:https://gitlab.example.com:group/sub/repo`。

## 安全约束

**一个令牌只发往它所属的实例。** 令牌与实例地址成对存储：

- 校验请求只发往填写的实例地址，不跟随重定向（避免 `PRIVATE-TOKEN` 头被带到别的 host）。
- 仓库身份里包含实例 origin，与已配置实例不一致的仓库链接会被拒绝，不会退化为「先存下、clone 时再失败」。
- Git 命令的 URL 重写前缀由实例 origin 生成，不会把甲实例的令牌重写到乙实例的地址上。
- 令牌不回显：API 响应只返回 `gitlabTokenConfigured`，`gitlabToken` 恒为空。进程日志中的 `GIT_CONFIG_KEY_n`（承载 `url.https://user:token@host/`）和 `*_TOKEN` 变量会被脱敏。
- GitLab 只在运行确实绑定了 GitLab 仓库时才注入凭据，纯 GitHub 运行不会携带 GitLab 令牌。

## 交付语义

合入路径按以下规则决定，避免把历史 MR 误报为本次交付：

| 现状 | 行为 |
| --- | --- |
| 无 MR | 创建后合入 |
| 打开的 MR | 直接合入 |
| 已合入，且分支 HEAD 与 MR 的 `sha` 一致 | 复用该 MR（幂等重试） |
| 已合入，但分支已有新提交 | 为新的提交创建新 MR |
| 已关闭 | 创建新 MR |

合入请求会带上被审阅的 `sha`，分支在审阅后移动会被 GitLab 拒绝而不是合入未审阅的代码。MR 只在 `source_project_id` 等于目标项目 id 时才被认为有效，因此 fork 上的同名分支不会被视为可合入的 MR。

MR 创建后 GitLab 会异步计算可合并性（`detailed_merge_status=preparing`），此时合入返回 405。客户端会轮询直到可合并或超时（默认 30 秒），并把真实原因（如 `ci_must_pass`、`discussions_not_resolved`）写进错误信息。

## Runner 要求

GitLab 任务要求执行机 runner 为 **0.8.0+**。更早的 runner 会因缺少新凭据字段被拒绝下发，不会拿到不完整的凭据绑定。

GitLab 凭据通过 `RunnerInvocation.GitLabBaseURL` / `GitLabToken` 下发，任务完成后与 `GitToken` 一样从存储中擦除。

## 本地验证

开发环境可用 Docker 起一个受限配置的 GitLab CE：

```bash
# 见 setup/90-install-gitlab.sh
docker compose -f ~/gitlab-dev/docker-compose.yml up -d
```

端到端验证（会真实创建并合入 MR，请指向一次性仓库）：

```bash
export LFORGE_GITLAB_URL=http://127.0.0.1:8929
export LFORGE_GITLAB_TOKEN=glpat-xxxxxxxx
export LFORGE_GITLAB_PROJECT=lfdev/loopforge-demo
export LFORGE_GITLAB_BRANCH=loopforge/e2e-test
go test -tags gitlab_live ./internal/tracker/ -run TestGitLabLiveMergeRequest -v
```

## 实现位置

| 文件 | 职责 |
| --- | --- |
| `internal/domain/gitlab_repo.go` | 仓库 URL 解析、实例规范化、身份编解码、MR URL |
| `internal/tracker/gitlab.go` | REST 客户端、MR 查找/创建/合入、Issue 拉取 |
| `internal/tracker/gitlab_auth.go` | 按项目 Owner 解析凭据 |
| `internal/gitwork/auth.go` | 按实例生成 git URL 重写 |
| `internal/runtime/command_binding.go` | 沙箱内 git 环境与 insteadOf 配置 |
| `internal/httpapi/gitlab_token_verify.go` | 令牌校验端点 |
| `internal/httpapi/catalog_gitlab.go` | 仓库绑定时解析并校验实例归属 |
| `web/app/workbench.js` | 个人配置的 GitLab 凭据行 |
