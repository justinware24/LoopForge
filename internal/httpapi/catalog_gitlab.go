package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"agentflow/internal/domain"
)

var (
	// errGitLabInstanceNotConfigured tells the operator where to configure the
	// instance rather than leaving them with an unparsable repository.
	errGitLabInstanceNotConfigured = errors.New("尚未配置 GitLab 实例地址：请先在「个人配置」中填写 GitLab 实例 URL 与访问令牌，再绑定该实例上的仓库")
	// errGitLabRepositoryNotOnInstance reports a link from a different GitLab
	// host than the configured one. Refusing is a security property: a token is
	// only valid on its own instance.
	errGitLabRepositoryNotOnInstance = errors.New("仓库链接不属于已配置的 GitLab 实例：一个实例的访问令牌只能用于该实例上的仓库")
)

// resolveGitLabRepository decides whether a pasted repository reference belongs
// to the GitLab adapter and, if so, parses it against the caller's configured
// instance.
//
// The instance comes from the actor's own GitLab settings. Using the actor
// rather than the project Owner is deliberate and fixes review finding P2-4:
// an administrator managing someone else's project must resolve the reference
// with the same instance the browser showed them, otherwise the form accepts a
// URL the server then rejects.
//
// A reference is treated as GitLab when any of these hold:
//   - the caller set provider=gitlab explicitly;
//   - the stored identity already carries the gitlab: prefix;
//   - the link's host matches the actor's configured GitLab instance.
//
// Returning (info, false, nil) means "not a GitLab reference", so the caller
// continues with the GitHub/Gongfeng parsers.
func (h *catalogHandlers) resolveGitLabRepository(c *gin.Context, provider, reference, storedIdentity string) (domain.GitLabRepoInfo, bool, error) {
	reference = strings.TrimSpace(reference)
	storedIdentity = strings.TrimSpace(storedIdentity)
	explicit := strings.EqualFold(strings.TrimSpace(provider), domain.RepositoryProviderGitLab)
	if reference == "" && storedIdentity == "" {
		return domain.GitLabRepoInfo{}, false, nil
	}

	baseURL := h.gitLabBaseURLForActor(c.Request)

	// An explicit GitLab request without a configured instance cannot be
	// resolved. Say so instead of silently storing an unusable repository.
	if baseURL == "" {
		if explicit || domain.IsGitLabProjectID(storedIdentity) {
			return domain.GitLabRepoInfo{}, false, errGitLabInstanceNotConfigured
		}
		return domain.GitLabRepoInfo{}, false, nil
	}

	// Prefer the pasted reference; fall back to the stored identity so a
	// repository row can be re-saved without re-pasting its URL.
	for _, candidate := range []string{reference, storedIdentity} {
		if candidate == "" {
			continue
		}
		if info, ok := domain.ParseGitLabRepoURL(candidate, baseURL); ok {
			return info, true, nil
		}
	}
	// The caller said GitLab, or the stored row is one, so a parse failure is a
	// real error rather than "this is a GitHub link".
	if explicit || domain.IsGitLabProjectID(storedIdentity) {
		return domain.GitLabRepoInfo{}, false, errGitLabRepositoryNotOnInstance
	}
	// A link that names the configured GitLab host but is not a project path
	// (the instance root, an unrelated page) is still not a GitHub reference.
	if referenceNamesHost(reference, baseURL) {
		return domain.GitLabRepoInfo{}, false, errGitLabRepositoryNotOnInstance
	}
	return domain.GitLabRepoInfo{}, false, nil
}

// gitLabBaseURLForActor resolves the GitLab instance the current caller is
// configured for. Anonymous or unauthenticated contexts have none.
func (h *catalogHandlers) gitLabBaseURLForActor(r *http.Request) string {
	if h == nil || h.repo == nil || r == nil {
		return ""
	}
	user, ok := h.requestUser(r, true)
	if !ok || user == nil {
		return ""
	}
	return h.repo.EffectiveGitLabBaseURL(user.ID)
}

// referenceNamesHost reports whether raw mentions base's host, which means the
// user pasted a link for the instance they configured but the path is not a
// repository.
func referenceNamesHost(raw, base string) bool {
	host := base
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/"); i >= 0 {
		host = host[:i]
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	return strings.Contains(strings.ToLower(raw), strings.ToLower(host))
}
