package repo

import (
	"strings"

	"agentflow/internal/domain"
)

// GitLabCredential is the resolved GitLab credential of one user: the personal
// access token plus the instance origin it was issued for.
//
// A GitLab token is only valid on its own instance, so the pair travels
// together and callers must pass the instance to the API client. The GitLab
// token is deliberately separate from GitToken: binding a GitLab repository
// must not change the credential a GitHub or Gongfeng clone uses.
type GitLabCredential struct {
	Token   string
	BaseURL string
}

// Configured reports whether a token is stored.
func (c GitLabCredential) Configured() bool { return strings.TrimSpace(c.Token) != "" }

// GitLabCredentialFor returns the user's GitLab token and instance origin.
// An empty struct means the user has not configured GitLab yet.
func (r *UsersRepo) GitLabCredentialFor(id int64) GitLabCredential {
	user, ok := r.User(id)
	if !ok {
		return GitLabCredential{}
	}
	return GitLabCredential{
		Token:   strings.TrimSpace(user.GitLabToken),
		BaseURL: strings.TrimSpace(user.GitLabBaseURL),
	}
}

// EffectiveGitLabBaseURL resolves the instance origin for a user, falling back
// to the platform-wide default when the user configured a token but no
// instance (gitlab.com is the common case).
func (r *UsersRepo) EffectiveGitLabBaseURL(id int64) string {
	cred := r.GitLabCredentialFor(id)
	if cred.BaseURL != "" {
		return cred.BaseURL
	}
	if cred.Configured() {
		return domain.DefaultGitLabBaseURL
	}
	return ""
}
