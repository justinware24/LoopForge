// Package gitwork holds the git mechanics shared by the control plane and the
// runner daemon: authenticated git commands, work-branch push/pull and the
// rules for which workspace files belong in a repository change set. It has
// no knowledge of runs, stores or policy; callers decide what to sync.
package gitwork

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// TokenEnv is the host-level fallback token, only used for output redaction.
const TokenEnv = "AGENTFLOW_GIT_TOKEN"

// HTTPUser is the default Basic-auth user for personal access tokens.
const HTTPUser = "private"

// OAuthHTTPUser is the Basic-auth user for Gongfeng OAuth access tokens.
// Stored OAuth tokens may carry an "oauth2:" prefix so gitwork and runtime
// pick the right HTTP Basic user without changing every RPC struct.
const OAuthHTTPUser = "oauth2"

// SplitToken extracts the HTTP Basic username ("private" or "oauth2") and
// raw token from a stored git credential.
func SplitToken(raw string) (user, token string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return HTTPUser, ""
	}
	if strings.HasPrefix(strings.ToLower(raw), OAuthHTTPUser+":") {
		tok := strings.TrimSpace(raw[len(OAuthHTTPUser)+1:])
		if tok != "" {
			return OAuthHTTPUser, tok
		}
	}
	if strings.HasPrefix(strings.ToLower(raw), HTTPUser+":") {
		tok := strings.TrimSpace(raw[len(HTTPUser)+1:])
		if tok != "" {
			return HTTPUser, tok
		}
	}
	return HTTPUser, raw
}

type tokenCtxKey struct{}

// EnvToken returns the host-level token from TokenEnv.
func EnvToken() string {
	return strings.TrimSpace(os.Getenv(TokenEnv))
}

// WithToken attaches the token Run uses for this context's git commands.
func WithToken(ctx context.Context, token string) context.Context {
	token = strings.TrimSpace(token)
	if token == "" {
		return ctx
	}
	return context.WithValue(ctx, tokenCtxKey{}, token)
}

// TokenFrom returns the token attached with WithToken.
func TokenFrom(ctx context.Context) string {
	if ctx != nil {
		if t, ok := ctx.Value(tokenCtxKey{}).(string); ok {
			if t = strings.TrimSpace(t); t != "" {
				return t
			}
		}
	}
	return ""
}

// gongfengInsteadOfPrefixes are rewritten to HTTPS + private:token. Same list
// for command-line -c and GIT_CONFIG_COUNT so clone/fetch/push and an Agent
// CLI's own git all see one rewrite.
var gongfengInsteadOfPrefixes = []string{
	"git@git.example.com:",
	"ssh://git@git.example.com/",
	"https://git.example.com/",
	"http://git.example.com/",
}

// gitlabCredentialsCtxKey carries the per-instance GitLab credential used for
// URL rewriting. A GitLab token is only valid on its own host, so the instance
// origin travels with the token and the rewrite prefixes are derived from it
// instead of being hard-coded.
type gitlabCredentialsCtxKey struct{}

// GitLabCredential is one GitLab instance plus the token issued for it.
type GitLabCredential struct {
	// BaseURL is the normalized instance origin, for example
	// https://gitlab.example.com or https://example.com/gitlab.
	BaseURL string
	// Token is a personal access token. GitLab accepts it as the password of
	// any non-empty user name, so the standard private:token pair works.
	Token string
}

// WithGitLabCredential attaches a GitLab instance credential to ctx. Later git
// commands rewrite that instance's URLs to carry the token.
func WithGitLabCredential(ctx context.Context, cred GitLabCredential) context.Context {
	if strings.TrimSpace(cred.BaseURL) == "" || strings.TrimSpace(cred.Token) == "" {
		return ctx
	}
	return context.WithValue(ctx, gitlabCredentialsCtxKey{}, cred)
}

// GitLabCredentialFrom returns the credential attached with
// WithGitLabCredential.
func GitLabCredentialFrom(ctx context.Context) (GitLabCredential, bool) {
	if ctx == nil {
		return GitLabCredential{}, false
	}
	cred, ok := ctx.Value(gitlabCredentialsCtxKey{}).(GitLabCredential)
	if !ok || strings.TrimSpace(cred.BaseURL) == "" || strings.TrimSpace(cred.Token) == "" {
		return GitLabCredential{}, false
	}
	return cred, true
}

// gitLabInsteadOfPrefixes lists the URL forms rewritten to the authenticated
// HTTPS base for one GitLab instance: scp-like SSH, explicit ssh:// on the
// instance port, and plain HTTP(S).
func gitLabInsteadOfPrefixes(baseURL string) []string {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return nil
	}
	host := u.Host
	path := strings.Trim(u.Path, "/")
	suffix := ""
	if path != "" {
		suffix = "/" + path
	}
	prefixes := []string{
		"git@" + host + ":",
		"ssh://git@" + host + "/",
		"https://" + host + suffix + "/",
		"http://" + host + suffix + "/",
	}
	// Self-managed instances commonly serve SSH on a dedicated port, in both
	// scp-like (host:2222/path) and ssh:// form.
	for _, port := range gitLabSSHPorts(u) {
		prefixes = append(prefixes,
			"git@"+u.Hostname()+":"+port+"/",
			"ssh://git@"+u.Hostname()+":"+port+"/",
		)
	}
	return prefixes
}

// gitLabSSHPorts returns the SSH ports to rewrite in addition to the default.
// The instance origin cannot carry the SSH port, so the conventional 2222 (the
// value the deployment guide uses) is included; git ignores a rewrite whose
// prefix never matches.
func gitLabSSHPorts(u *url.URL) []string {
	ports := []string{"2222"}
	if p := u.Port(); p != "" && p != "2222" && p != "80" && p != "443" {
		ports = append(ports, p)
	}
	return ports
}

// gitLabAuthedBase renders the scheme://user:token@host/prefix that replaces
// the instance's plain URLs. The scheme is taken from the configured instance:
// self-managed GitLab is often HTTP-only, and forcing https would make git
// fail the TLS handshake before it ever presents the token.
func gitLabAuthedBase(cred GitLabCredential) string {
	u, err := url.Parse(cred.BaseURL)
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		scheme = "https"
	}
	user, token := SplitToken(cred.Token)
	base := scheme + "://" + user + ":" + token + "@" + u.Host
	if path := strings.Trim(u.Path, "/"); path != "" {
		base += "/" + path
	}
	return base + "/"
}

func authedBase(token string) string {
	user, secret := SplitToken(token)
	return "https://" + user + ":" + secret + "@git.example.com/"
}

// SandboxEnv is the git environment for a child process: no interactive
// prompts, no command-running remote helpers, plus Gongfeng URL rewrites
// carrying the token when one is set.
func SandboxEnv(token string) map[string]string {
	return SandboxEnvWithGitLab(token, nil)
}

// SandboxEnvWithGitLab is SandboxEnv plus zero or more GitLab instance
// credentials. Every instance contributes its own rewrite prefixes, so one
// command can authenticate against several hosts without any of them receiving
// another instance's token.
func SandboxEnvWithGitLab(token string, gitlab []GitLabCredential) map[string]string {
	extra := map[string]string{"GIT_TERMINAL_PROMPT": "0", "GIT_ALLOW_PROTOCOL": AllowedProtocols}

	type rewrite struct{ base string; prefix string }
	var rewrites []rewrite

	user, secret := SplitToken(token)
	if secret != "" {
		// The credential helper reads these. The token stays out of the helper
		// text, which git runs through a shell.
		extra[gitHTTPUserEnv] = user
		extra[gitHTTPPassEnv] = secret
		authed := authedBase(token)
		for _, prefix := range gongfengInsteadOfPrefixes {
			rewrites = append(rewrites, rewrite{base: authed, prefix: prefix})
		}
	}
	for _, cred := range gitlab {
		authed := gitLabAuthedBase(cred)
		if authed == "" {
			continue
		}
		for _, prefix := range gitLabInsteadOfPrefixes(cred.BaseURL) {
			rewrites = append(rewrites, rewrite{base: authed, prefix: prefix})
		}
	}
	if len(rewrites) == 0 {
		return extra
	}
	// git applies the longest matching prefix first, so mixing the Gongfeng and
	// GitLab rewrites in one config block is safe even when both are present.
	extra["GIT_CONFIG_COUNT"] = fmt.Sprintf("%d", len(rewrites))
	for i, rw := range rewrites {
		extra[fmt.Sprintf("GIT_CONFIG_KEY_%d", i)] = "url." + rw.base + ".insteadof"
		extra[fmt.Sprintf("GIT_CONFIG_VALUE_%d", i)] = rw.prefix
	}
	return extra
}

// Env is the process environment with inherited git config/askpass overrides
// replaced by SandboxEnv(token).
func Env(token string) []string {
	return EnvWithGitLab(token, nil)
}

// EnvWithGitLab is Env with GitLab instance credentials applied.
func EnvWithGitLab(token string, gitlab []GitLabCredential) []string {
	var env []string
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "GIT_CONFIG_COUNT="),
			strings.HasPrefix(kv, "GIT_CONFIG_KEY_"),
			strings.HasPrefix(kv, "GIT_CONFIG_VALUE_"),
			strings.HasPrefix(kv, "GIT_ASKPASS="),
			strings.HasPrefix(kv, "SSH_ASKPASS="),
			strings.HasPrefix(kv, "GIT_ALLOW_PROTOCOL="),
			strings.HasPrefix(kv, "GIT_PROTOCOL_FROM_USER="),
			strings.HasPrefix(kv, gitHTTPUserEnv+"="),
			strings.HasPrefix(kv, gitHTTPPassEnv+"="):
			continue
		}
		env = append(env, kv)
	}
	for k, v := range SandboxEnvWithGitLab(token, gitlab) {
		env = append(env, k+"="+v)
	}
	return env
}

// CommandDir picks an existing directory to run git from: the workspace, its
// parent (before a clone creates it), or the process working directory.
func CommandDir(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace != "" {
		if st, err := os.Stat(workspace); err == nil && st.IsDir() {
			return workspace
		}
		parent := filepath.Dir(workspace)
		if st, err := os.Stat(parent); err == nil && st.IsDir() {
			return parent
		}
	}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		return wd
	}
	return os.TempDir()
}

// WithEnvValue sets key in env, replacing every existing entry for it.
func WithEnvValue(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	found := false
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			if !found {
				out = append(out, prefix+value)
				found = true
			}
			continue
		}
		out = append(out, kv)
	}
	if !found {
		out = append(out, prefix+value)
	}
	return out
}

// gitHTTPUserEnv and gitHTTPPassEnv carry the Basic-auth pair for one git
// process. credentialHelper reads them so a token cannot break out of the
// shell snippet git executes for a `!` helper.
const (
	gitHTTPUserEnv = "AGENTFLOW_GIT_HTTP_USER"
	gitHTTPPassEnv = "AGENTFLOW_GIT_HTTP_PASSWORD"
)

func credentialHelper() string {
	return `!f() { test "$1" = get || exit 0; printf 'username=%s\npassword=%s\n' "$` + gitHTTPUserEnv + `" "$` + gitHTTPPassEnv + `"; }; f`
}

// AuthConfigArgs are `git -c` arguments that authenticate one command with
// token without writing it to any git config file.
func AuthConfigArgs(token string) []string {
	_, secret := SplitToken(token)
	if secret == "" {
		return nil
	}
	args := []string{"-c", "credential.helper=", "-c", "credential.helper=" + credentialHelper()}
	authed := authedBase(token)
	for _, prefix := range gongfengInsteadOfPrefixes {
		args = append(args, "-c", "url."+authed+".insteadof="+prefix)
	}
	return args
}

// RewriteRemoteForToken embeds token in an HTTPS (or git@ SSH) remote URL.
// The token is only embedded when the remote's host matches the token's
// instance; an unrelated remote is returned unchanged so one instance's
// credential never leaks into another's URL.
func RewriteRemoteForToken(remote, token string) string {
	remote = strings.TrimSpace(remote)
	user, secret := SplitToken(token)
	if remote == "" || secret == "" {
		return remote
	}
	if strings.HasPrefix(remote, "git@") {
		hostPath := strings.TrimPrefix(remote, "git@")
		host, path, ok := strings.Cut(hostPath, ":")
		if !ok || host == "" || path == "" {
			return remote
		}
		return "https://" + user + ":" + secret + "@" + host + "/" + strings.TrimPrefix(path, "/")
	}
	u, err := url.Parse(remote)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return remote
	}
	if u.User != nil && u.User.Username() != "" && !strings.EqualFold(u.User.Username(), "oauth2") {
		return remote
	}
	u.User = url.UserPassword(user, secret)
	return u.String()
}

// RewriteRemoteForGitLab embeds a GitLab instance's token in a remote URL that
// belongs to that instance. A remote on another host is returned unchanged.
// The scp-like form git@host:port/path is normalized to HTTPS because a token
// cannot be presented over SSH.
func RewriteRemoteForGitLab(remote string, cred GitLabCredential) string {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return remote
	}
	u, err := url.Parse(cred.BaseURL)
	if err != nil || u.Host == "" {
		return remote
	}
	wantHost := strings.ToLower(u.Hostname())
	user, secret := SplitToken(cred.Token)
	if secret == "" {
		return remote
	}

	// scp-like: user@host:path or user@host:port/path
	if !strings.Contains(remote, "://") && strings.Contains(remote, "@") {
		at := strings.Index(remote, "@")
		rest := remote[at+1:]
		host, path, ok := strings.Cut(rest, ":")
		if !ok || !strings.EqualFold(host, wantHost) {
			return remote
		}
		// Strip a leading numeric port segment.
		if i := strings.Index(path, "/"); i > 0 && isAllDigits(path[:i]) {
			path = path[i+1:]
		}
		scheme := u.Scheme
		if scheme != "http" && scheme != "https" {
			scheme = "https"
		}
		out := scheme + "://" + user + ":" + secret + "@" + u.Host
		if p := strings.Trim(u.Path, "/"); p != "" {
			out += "/" + p
		}
		return out + "/" + strings.TrimLeft(path, "/")
	}

	parsed, err := url.Parse(remote)
	if err != nil {
		return remote
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return remote
	}
	if !strings.EqualFold(strings.ToLower(parsed.Hostname()), wantHost) {
		return remote
	}
	if parsed.User != nil && parsed.User.Username() != "" && !strings.EqualFold(parsed.User.Username(), "oauth2") {
		return remote
	}
	parsed.User = url.UserPassword(user, secret)
	return parsed.String()
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// urlUserinfo matches the userinfo of a URL anywhere in free text.
var urlUserinfo = regexp.MustCompile(`(?i)\b(https?|git|ssh|git\+ssh|ssh\+git)://([^\s/@]+)@`)

// redactURLUserinfo masks credentials embedded in URLs, including ones that
// were never passed in as tokens (for example pasted into a clone URL).
// HTTP(S) and git:// userinfo is masked whole; an ssh user name is kept.
func redactURLUserinfo(s string) string {
	return urlUserinfo.ReplaceAllStringFunc(s, func(match string) string {
		parts := urlUserinfo.FindStringSubmatch(match)
		scheme, info := parts[1], parts[2]
		if strings.HasPrefix(strings.ToLower(scheme), "ssh") || strings.HasSuffix(strings.ToLower(scheme), "+ssh") {
			if user, _, hasPassword := strings.Cut(info, ":"); hasPassword {
				return scheme + "://" + user + ":***@"
			}
			return match
		}
		return scheme + "://***@"
	})
}

// Redact masks tokens (and the host-level EnvToken) in git output, plus any
// credentials embedded in URLs.
func Redact(s string, tokens ...string) string {
	s = redactURLUserinfo(s)
	seen := map[string]struct{}{}
	for _, raw := range append(tokens, EnvToken()) {
		for _, candidate := range []string{strings.TrimSpace(raw), func() string { _, sec := SplitToken(raw); return sec }()} {
			if candidate == "" {
				continue
			}
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			s = strings.ReplaceAll(s, candidate, "***")
		}
	}
	return s
}
