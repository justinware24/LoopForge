package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

const gitTokenEnv = gitwork.TokenEnv

type gitIdentityCtxKey struct{}

type gitIdentity struct {
	name  string
	email string
}

func withGitToken(ctx context.Context, token string) context.Context {
	return gitwork.WithToken(ctx, token)
}

// checkoutContext attaches every credential flavor a repository checkout needs
// to the context that git commands will run under. Both the SOP node path and
// the conversation-workspace path must go through this helper: attaching only
// the GitHub token leaves GitLab remotes unauthenticated, and git then reports
// "could not read Username" even when a valid GitLab token is configured.
func checkoutContext(ctx context.Context, credentials runtime.RunCredentials) context.Context {
	ctx = withGitToken(ctx, credentials.GitToken)
	ctx = withGitLabCredential(ctx, credentials)
	ctx = gitwork.WithSSHKeys(ctx, credentials.GitSSHKeys)
	return ctx
}

// withGitLabCredential attaches the GitLab instance credential of the run, so
// git commands that touch a GitLab remote carry that instance's token and no
// other host receives it.
func withGitLabCredential(ctx context.Context, cred runtime.RunCredentials) context.Context {
	if strings.TrimSpace(cred.GitLabToken) == "" || strings.TrimSpace(cred.GitLabBaseURL) == "" {
		return ctx
	}
	return gitwork.WithGitLabCredential(ctx, gitwork.GitLabCredential{
		BaseURL: cred.GitLabBaseURL,
		Token:   cred.GitLabToken,
	})
}

// runBindsGitLab reports whether any repository of the run uses the GitLab
// adapter. Credentials are attached only when they can actually be needed.
func runBindsGitLab(run domain.Run) bool {
	for _, rc := range run.RepositoryContexts() {
		if domain.IsGitLabProjectID(rc.GongfengProjectID) {
			return true
		}
	}
	return false
}

// repositoryUsesGitLab reports whether one repository is served by a GitLab
// instance. Both the explicit provider and the stored identity are checked, so
// a row saved before the provider was recorded is still recognized.
func repositoryUsesGitLab(rc domain.RepositoryContext) bool {
	if rc.Provider == domain.RepositoryProviderSSH {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(rc.Provider), domain.RepositoryProviderGitLab) {
		return true
	}
	return domain.IsGitLabProjectID(rc.GongfengProjectID)
}

func gitTokenFrom(ctx context.Context) string { return gitwork.TokenFrom(ctx) }

func withRepositoryGit(ctx context.Context, repository domain.RepositoryContext) context.Context {
	if repository.Provider == domain.RepositoryProviderSSH {
		return gitwork.WithSSHRemote(ctx, repository.CloneURL)
	}
	return ctx
}

func sshRepositoryURLs(repositories []domain.RepositoryContext) []string {
	var urls []string
	for _, repository := range repositories {
		if repository.Provider == domain.RepositoryProviderSSH {
			urls = append(urls, repository.CloneURL)
		}
	}
	return urls
}

func cloneRemoteForGit(ctx context.Context, remote string) string {
	if gitwork.UsesSSH(ctx) {
		return remote
	}
	// A GitLab remote is authenticated with its own instance token, and only
	// when the host matches. Rewriting it with the GitHub/Gongfeng token would
	// both fail and send the wrong credential to that host.
	if cred, ok := gitwork.GitLabCredentialFrom(ctx); ok {
		if rewritten := gitwork.RewriteRemoteForGitLab(remote, cred); rewritten != remote {
			return rewritten
		}
	}
	return gitwork.RewriteRemoteForToken(remote, gitTokenFrom(ctx))
}

func withGitIdentity(ctx context.Context, name, email string) context.Context {
	identity := gitIdentity{name: strings.TrimSpace(name), email: strings.TrimSpace(email)}
	if identity.name == "" || identity.email == "" {
		return ctx
	}
	return context.WithValue(ctx, gitIdentityCtxKey{}, identity)
}

func gitIdentityFrom(ctx context.Context) gitIdentity {
	if ctx != nil {
		if identity, ok := ctx.Value(gitIdentityCtxKey{}).(gitIdentity); ok {
			return identity
		}
	}
	return gitIdentity{}
}

func resolveGitIdentity(s *repo.Store, userID int64) gitIdentity {
	if s == nil || userID <= 0 {
		return gitIdentity{}
	}
	user, ok := s.User(userID)
	if !ok {
		return gitIdentity{}
	}
	login := strings.TrimSpace(user.LoginName)
	if login == "" {
		return gitIdentity{}
	}
	// Local platform accounts do not establish corporate or GitHub identity.
	// Use a non-deliverable address rather than inventing an employer email.
	return gitIdentity{name: login, email: login + "@users.noreply.loopforge.invalid"}
}

func applyWorkspaceGitIdentity(ctx context.Context, workspace string) error {
	identity := gitIdentityFrom(ctx)
	if identity.name == "" || identity.email == "" {
		return nil
	}
	for _, item := range []struct {
		key, value string
	}{
		{key: "user.name", value: identity.name},
		{key: "user.email", value: identity.email},
	} {
		if _, err := gitC(ctx, workspace, "config", "--local", item.key, item.value); err != nil {
			return fmt.Errorf("configure git %s: %w", item.key, err)
		}
	}
	return nil
}

func resolveGitToken(s *repo.Store, userID int64) string {
	if s != nil && userID > 0 {
		return s.GitCredential(context.Background(), userID)
	}
	return ""
}

var (
	gitCommandFailure = regexp.MustCompile(`(?i)repository checkout|git (clone|fetch|pull|push|ls-remote)`)
	gitAuthFailure    = regexp.MustCompile(`(?i)could not read Username|Authentication failed|HTTP Basic: Access denied|requested URL returned error: 40[13]|Invalid username or password`)
)

const gitTokenFixHint = "请责任人到「个人配置 → GitHub Git Token」配置：打开 https://github.com/settings/tokens 新建令牌，为目标仓库授予 Contents 和 Pull requests 的读写权限，复制后粘贴并点校验，然后重试本节点"

const gitLabTokenFixHint = "请责任人到「个人配置 → GitLab 令牌」配置：在 GitLab 实例的 User Settings → Access Tokens 新建个人访问令牌，勾选 api、read_repository、write_repository scope，并确认该账号对目标仓库至少有 Reporter 及以上角色（Guest 无法拉取代码），复制后粘贴并点校验，然后重试本节点"

// explainGitAuthFailure says what to fix when git rejects the clone or push.
// SOP runs use the responsible user's Git Token, which git's own
// "could not read Username" never mentions. The raw error stays appended.
func explainGitAuthFailure(err error, tokenConfigured bool) error {
	return explainGitAuthFailureHint(err, tokenConfigured, "Git Token", gitTokenFixHint)
}

// explainGitAuthFailureFor chooses the hint that matches the failing repository.
// A GitLab checkout authenticated with a GitHub-token hint sends the responsible
// user to github.com, which can never fix a self-managed GitLab remote.
func explainGitAuthFailureFor(err error, repos []domain.RepositoryContext, gitTokenConfigured, gitLabTokenConfigured bool) error {
	for _, repo := range repos {
		if repo.Provider == domain.RepositoryProviderGitLab || domain.IsGitLabProjectID(repo.GongfengProjectID) {
			return explainGitAuthFailureHint(err, gitLabTokenConfigured, "GitLab 令牌", gitLabTokenFixHint)
		}
	}
	return explainGitAuthFailureHint(err, gitTokenConfigured, "Git Token", gitTokenFixHint)
}

func explainGitAuthFailureHint(err error, tokenConfigured bool, label, hint string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "Git Token") || strings.Contains(msg, "令牌") || !gitCommandFailure.MatchString(msg) || !gitAuthFailure.MatchString(msg) {
		return err
	}
	if !tokenConfigured {
		return fmt.Errorf("运行责任人未配置 %s，无法拉取仓库。%s。原始错误：%w", label, hint, err)
	}
	return fmt.Errorf("运行责任人的 %s 无效、已过期或权限不足，无法拉取仓库。%s；并确认责任人对该仓库有权限。原始错误：%w", label, hint, err)
}

const gitAuthIncludeFile = "agentflow.auth"
const gitAskPassFile = "agentflow-askpass"
const gitAskPassSecretFile = "agentflow-askpass-secret"

// applyWorkspaceGitAuth removes credentials persisted by older AgentFlow
// versions. Git authentication is now supplied only to the relevant child
// process, so user tokens never become part of the run workspace.
func applyWorkspaceGitAuth(ctx context.Context, workspace string) error {
	gitDir := filepath.Join(workspace, ".git")
	info, err := os.Stat(gitDir)
	if err != nil || !info.IsDir() {
		return nil
	}
	for _, name := range []string{gitAuthIncludeFile, gitAskPassFile, gitAskPassSecretFile} {
		if err := os.Remove(filepath.Join(gitDir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	configured, _ := gitC(ctx, workspace, "config", "--local", "--get-all", "include.path")
	_, _ = gitC(ctx, workspace, "config", "--local", "--unset-all", "include.path")
	for _, include := range strings.Split(configured, "\n") {
		include = strings.TrimSpace(include)
		if include == "" || include == gitAuthIncludeFile {
			continue
		}
		if _, err := gitC(ctx, workspace, "config", "--local", "--add", "include.path", include); err != nil {
			return err
		}
	}
	return nil
}
