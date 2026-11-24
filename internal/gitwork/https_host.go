package gitwork

import (
	"net/url"
	"strings"

	"agentflow/internal/domain"
)

// HTTPSHostScope returns the canonical host used by the Git credential helper.
func HTTPSHostScope(raw string) (string, error) {
	return domain.HTTPSHostScope(raw)
}

// GitHostScope returns the canonical host of a Git credential together with
// the scheme the credential is bound to. A self-hosted GitLab commonly
// serves its clone URLs over plain HTTP, so "http" is accepted next to
// "https".
func GitHostScope(raw string) (string, string, error) {
	return domain.GitHostScope(raw)
}

// HTTPSRemoteHost returns the credential host scope of an https:// remote URL.
func HTTPSRemoteHost(remote string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(remote))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return "", false
	}
	host, err := HTTPSHostScope("https://" + u.Host)
	if err != nil {
		return "", false
	}
	return host, true
}

// GitRemoteHost returns the credential host scope of an http:// or https://
// remote URL. A self-hosted GitLab often serves its clone URLs over plain
// HTTP, so both schemes name a credential the same way.
func GitRemoteHost(remote string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(remote))
	if err != nil || u.Host == "" {
		return "", false
	}
	if !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
		return "", false
	}
	host, _, err := GitHostScope(u.Scheme + "://" + u.Host)
	if err != nil {
		return "", false
	}
	return host, true
}
