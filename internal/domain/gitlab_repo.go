package domain

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// GitLab support (issue #128 / PR #62). A GitLab repository is identified by a
// prefixed remote identity "gitlab:<base>:<path>", where <base> is the scheme
// and host (plus optional sub-path) of the GitLab instance and <path> is the
// project path. The instance is part of the identity so one platform can bind
// repositories from several self-managed GitLab servers without collisions.
//
// Examples:
//
//	gitlab:https://gitlab.com:group/repo
//	gitlab:https://gitlab.example.com:9000:group/sub/repo
//	gitlab:https://example.com/gitlab:group/repo
const (
	// RepositoryProviderGitLab is the hosting provider for GitLab.com and
	// self-managed GitLab instances.
	RepositoryProviderGitLab = "gitlab"
	// RepositoryProviderGitHub is the hosting provider for github.com.
	RepositoryProviderGitHub = "github"

	gitlabProjectPrefix = "gitlab:"

	// DefaultGitLabBaseURL is the public GitLab instance used when a user
	// configured a token without naming an instance.
	DefaultGitLabBaseURL = "https://gitlab.com"
)

// GitLabRepoInfo carries the repository identity parsed out of a pasted GitLab
// link, so a repository can be configured with a single URL. BaseURL is the
// normalized instance origin (scheme://host[:port][/subpath]) without a
// trailing slash; Project is the full namespace path.
type GitLabRepoInfo struct {
	Project  string // group/subgroup/repository
	Name     string // repository (last path segment)
	BaseURL  string // https://gitlab.example.com[/subpath]
	WebURL   string // BaseURL + / + Project
	CloneURL string // WebURL + .git
	// SSHRef is the canonical scp-like SSH clone address when the caller pasted
	// one; empty for https inputs.
	SSHRef string
}

// IsGitLabProjectID reports whether a stored repository identity belongs to the
// GitLab adapter.
func IsGitLabProjectID(id string) bool {
	return strings.HasPrefix(strings.TrimSpace(id), gitlabProjectPrefix)
}

// GitLabRemoteIdentity builds the stored identity for an instance and path.
// baseURL must already be normalized by NormalizeGitLabBaseURL.
func GitLabRemoteIdentity(baseURL, project string) string {
	baseURL = NormalizeGitLabBaseURL(baseURL)
	project = strings.Trim(strings.TrimSpace(project), "/")
	if baseURL == "" || project == "" {
		return ""
	}
	return gitlabProjectPrefix + baseURL + ":" + project
}

// SplitGitLabProjectID reverses GitLabRemoteIdentity. The instance URL itself
// contains a colon (scheme separator), so the split happens at the last colon.
func SplitGitLabProjectID(id string) (baseURL, project string, ok bool) {
	id = strings.TrimSpace(id)
	if !strings.HasPrefix(id, gitlabProjectPrefix) {
		return "", "", false
	}
	body := strings.TrimPrefix(id, gitlabProjectPrefix)
	idx := strings.LastIndex(body, ":")
	if idx <= 0 || idx == len(body)-1 {
		return "", "", false
	}
	baseURL = NormalizeGitLabBaseURL(body[:idx])
	project = strings.Trim(body[idx+1:], "/")
	if baseURL == "" || project == "" {
		return "", "", false
	}
	return baseURL, project, true
}

// NormalizeGitLabBaseURL validates and canonicalizes a GitLab instance origin.
// Only http/https are accepted: the API and git-over-HTTPS both need them, and
// an ssh:// origin would defeat the token-based API path. A trailing slash and
// any trailing "/-" or ".git" fragment are removed. Credentials in the URL are
// rejected so a pasted token can never be persisted in a repository row.
func NormalizeGitLabBaseURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	if u.Host == "" {
		return ""
	}
	if u.User != nil {
		return ""
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	host := strings.ToLower(u.Host)
	path := strings.Trim(u.Path, "/")
	// A pasted repository URL must not become an instance origin.
	if strings.HasSuffix(strings.ToLower(path), ".git") {
		return ""
	}
	if i := strings.Index(path, "/-/"); i >= 0 {
		path = strings.Trim(path[:i], "/")
	}
	if path == "" {
		return u.Scheme + "://" + host
	}
	return u.Scheme + "://" + host + "/" + path
}

// gitlabRepoSubpages are URL suffixes that point at a sub page of a GitLab
// repository. Everything before the first marker is the project path.
var gitlabRepoSubpages = []string{
	"/-/", "/tree/", "/blob/", "/commits/", "/commit/", "/merge_requests/",
	"/issues/", "/pipelines/", "/tags/", "/releases/", "/wikis/", "/snippets/",
}

// ParseGitLabRepoURL parses a GitLab repository reference for a known instance.
// baseURL is the instance the platform is configured for (usually the user's
// gitlabBaseUrl); a reference whose host does not match it is rejected so one
// instance's token is never sent to another host.
//
// Accepted inputs:
//   - https://gitlab.example.com/group/repository
//   - https://gitlab.example.com/group/repository/-/tree/main
//   - https://gitlab.example.com/group/sub/repository/-/merge_requests/12
//   - git@gitlab.example.com:group/repository.git
//   - ssh://git@gitlab.example.com:2222/group/repository.git
//   - group/repository (bare path, resolved against baseURL)
func ParseGitLabRepoURL(raw, baseURL string) (GitLabRepoInfo, bool) {
	want := NormalizeGitLabBaseURL(baseURL)
	if want == "" {
		return GitLabRepoInfo{}, false
	}
	wantURL, err := url.Parse(want)
	if err != nil {
		return GitLabRepoInfo{}, false
	}
	wantHost := strings.ToLower(wantURL.Host)
	// An SSH clone address may use its own port, so host comparison is by name.
	wantHostName := strings.ToLower(wantURL.Hostname())
	wantPath := strings.Trim(wantURL.Path, "/")

	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "git clone ")
	s = strings.Trim(s, "\"'")
	if s == "" {
		return GitLabRepoInfo{}, false
	}
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		s = s[:i]
	}
	if strings.ContainsAny(s, `"'<>()`) {
		return GitLabRepoInfo{}, false
	}
	// Strip the stored identity prefix so a repository row can be re-parsed.
	if IsGitLabProjectID(s) {
		base, project, ok := SplitGitLabProjectID(s)
		if !ok || !strings.EqualFold(base, want) {
			return GitLabRepoInfo{}, false
		}
		s = project
	}

	scheme := wantURL.Scheme
	sshPaste := false
	path := ""

	switch {
	case strings.HasPrefix(strings.ToLower(s), "git@"), strings.HasPrefix(strings.ToLower(s), "ssh://"):
		sshPaste = true
		host, rawPath, ok := splitSSHReference(s)
		// The instance origin carries the HTTP(S) port, while SSH may use a
		// different one, so only the host name is compared here.
		if !ok || !strings.EqualFold(hostNameOnly(host), wantHostName) {
			return GitLabRepoInfo{}, false
		}
		path = rawPath
	case strings.Contains(s, "://"):
		u, parseErr := url.Parse(s)
		if parseErr != nil || u.User != nil {
			return GitLabRepoInfo{}, false
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return GitLabRepoInfo{}, false
		}
		if !strings.EqualFold(strings.ToLower(u.Host), wantHost) {
			return GitLabRepoInfo{}, false
		}
		path = u.Path
	default:
		// Bare path or host-less paste. An instance with a sub-path
		// (https://example.com/gitlab) keeps it as a required prefix.
		p := strings.Trim(s, "/")
		if strings.HasPrefix(strings.ToLower(p), wantHost+"/") {
			p = p[len(wantHost)+1:]
		}
		if wantPath != "" {
			if strings.EqualFold(p, wantPath) {
				return GitLabRepoInfo{}, false
			}
			if strings.HasPrefix(strings.ToLower(p), strings.ToLower(wantPath)+"/") {
				p = p[len(wantPath)+1:]
			}
		}
		path = p
	}

	project := normalizeGitLabProjectPath(path, wantPath)
	if project == "" {
		return GitLabRepoInfo{}, false
	}
	info := GitLabRepoInfo{
		Project: project,
		Name:    project[strings.LastIndex(project, "/")+1:],
		BaseURL: want,
	}
	info.WebURL = want + "/" + project
	info.CloneURL = info.WebURL + ".git"
	if sshPaste {
		info.SSHRef = gitlabSSHReference(want, project)
	}
	_ = scheme
	return info, true
}

// normalizeGitLabProjectPath strips the instance sub-path, clone suffixes and
// sub page markers, then validates the remaining multi-segment project path.
func normalizeGitLabProjectPath(path, instancePath string) string {
	p := strings.TrimSpace(path)
	if strings.HasPrefix(p, "/") {
		p = strings.TrimLeft(p, "/")
	}
	if instancePath != "" {
		if strings.EqualFold(p, instancePath) {
			return ""
		}
		if strings.HasPrefix(strings.ToLower(p), strings.ToLower(instancePath)+"/") {
			p = p[len(instancePath)+1:]
		}
	}
	if strings.Contains(p, "..") {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(p), ".git") {
		p = p[:len(p)-4]
	}
	for _, marker := range gitlabRepoSubpages {
		if i := strings.Index(p, marker); i >= 0 {
			p = p[:i]
		}
	}
	p = strings.Trim(p, "/")
	if p == "" || !strings.Contains(p, "/") {
		// must look like group/repository (at least two segments)
		return ""
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, " \t\\?#") {
			return ""
		}
	}
	return p
}

// splitSSHReference parses git@host:path and ssh://[user@]host[:port]/path into
// a host and a repository path. The port is dropped: the instance origin is
// what identifies the API endpoint.
func splitSSHReference(s string) (host, path string, ok bool) {
	if strings.HasPrefix(strings.ToLower(s), "ssh://") {
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return "", "", false
		}
		return strings.ToLower(u.Hostname() + portSuffix(u.Port(), 22)), strings.Trim(u.Path, "/"), true
	}
	rest := s
	if i := strings.Index(rest, "@"); i >= 0 {
		rest = rest[i+1:]
	}
	host, path, ok = strings.Cut(rest, ":")
	if !ok || host == "" || path == "" {
		return "", "", false
	}
	path = strings.TrimLeft(path, "/")
	// git@host:2222/group/repo.git carries a port in scp-like form.
	if i := strings.Index(path, "/"); i > 0 && isAllDigits(path[:i]) {
		port := path[:i]
		path = path[i+1:]
		return strings.ToLower(host + portSuffix(port, 22)), path, true
	}
	return strings.ToLower(host), path, true
}

func portSuffix(port string, defaultPort int) string {
	if port == "" || port == fmt.Sprint(defaultPort) {
		return ""
	}
	return ":" + port
}

// hostNameOnly strips an optional port from a host, so an SSH reference on its
// own port still matches the instance's HTTP host.
func hostNameOnly(host string) string {
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i:], "]") {
		return host[:i]
	}
	return host
}

// gitlabSSHReference renders the scp-like SSH clone address for an instance.
// Self-managed GitLab often serves SSH on a dedicated port, which the instance
// origin cannot carry, so callers that need it configure gitlabSshPort.
func gitlabSSHReference(baseURL, project string) string {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return "git@" + u.Hostname() + ":" + project + ".git"
}

// GitLabBaseURLFromIdentity returns the instance origin of a stored identity.
func GitLabBaseURLFromIdentity(id string) string {
	base, _, ok := SplitGitLabProjectID(id)
	if !ok {
		return ""
	}
	return base
}

// GitLabMRURL is the canonical web URL of a GitLab merge request.
func GitLabMRURL(projectID string, iid int) string {
	if iid <= 0 {
		return ""
	}
	base, project, ok := SplitGitLabProjectID(projectID)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s/%s/-/merge_requests/%d", base, project, iid)
}

// gitLabMRURLPattern matches the web address of a GitLab merge request.
// GitLab serves the number in two shapes: "/-/merge_requests/3" on modern
// instances and "/merge_requests/3" on older ones.
var gitLabMRURLPattern = regexp.MustCompile(`^(https?://[^/]+)(/.+?)/-?merge_requests/(\d+)/?$`)

// ParseGitLabMR extracts the instance origin, the project path and the iid from
// a GitLab merge request URL.
//
// It is the counterpart of GitLabMRURL: the delivery path needs the numbers
// back to register the merge request, and the Gongfeng URL parser does not
// recognize a GitLab host.
func ParseGitLabMR(text string) (baseURL, project string, iid int, ok bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", "", 0, false
	}
	match := gitLabMRURLPattern.FindStringSubmatch(text)
	if match == nil {
		return "", "", 0, false
	}
	baseURL = strings.TrimRight(match[1], "/")
	project = strings.Trim(match[2], "/")
	// GitLab also serves "/group/project/-/merge_requests/3"; the project path
	// must not carry the marker.
	project = strings.TrimSuffix(project, "/-")
	iid, _ = strconv.Atoi(match[3])
	if baseURL == "" || project == "" || iid <= 0 {
		return "", "", 0, false
	}
	return baseURL, project, iid, true
}

// RepositoryWebURL resolves the browsable URL of a repository identity for any
// provider the platform understands.
func RepositoryWebURL(identity string) string {
	identity = strings.TrimSpace(identity)
	switch {
	case strings.HasPrefix(identity, "github:"):
		return "https://github.com/" + strings.TrimPrefix(identity, "github:")
	case IsGitLabProjectID(identity):
		base, project, ok := SplitGitLabProjectID(identity)
		if !ok {
			return ""
		}
		return base + "/" + project
	default:
		return ""
	}
}
