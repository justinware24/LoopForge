package tracker

import (
	"strings"

	"agentflow/internal/domain"
)

// gitLabCredentialsForProject resolves the GitLab credential that owns a bound
// repository identity.
//
// A GitLab token belongs to a person, not to a repository, so the credential is
// taken from the repository's local project owner — the same rule the GitHub
// adapter uses. Resolving by identity rather than by the acting user keeps
// scheduled runs and background MR refreshes working without a browser session.
func (s *Service) gitLabCredentialsForProject(identity string) (baseURL, token string, err error) {
	identity = strings.TrimSpace(identity)
	base, _, ok := domain.SplitGitLabProjectID(identity)
	if !ok {
		return "", "", errGitLabNoCredential
	}
	if s == nil || s.projectAuth == nil || s.gitLabCredentials == nil {
		return base, "", errGitLabNoCredential
	}
	for _, repository := range s.projectAuth.Repositories() {
		if !strings.EqualFold(strings.TrimSpace(repository.GongfengProjectID), identity) {
			continue
		}
		project, found := s.projectAuth.Project(repository.ProjectID)
		if !found || project.OwnerID <= 0 {
			return base, "", errGitLabNoCredential
		}
		cred := s.gitLabCredentials.GitLabCredentialFor(project.OwnerID)
		if !cred.Configured() {
			return base, "", errGitLabNoCredential
		}
		instance := strings.TrimSpace(cred.BaseURL)
		if instance == "" {
			instance = domain.DefaultGitLabBaseURL
		}
		// The instance the repository belongs to is authoritative: a token
		// configured for a different host must not be sent to this one.
		if !strings.EqualFold(instance, base) {
			return base, "", errGitLabNoCredential
		}
		return base, cred.Token, nil
	}
	return base, "", errGitLabNoCredential
}

// isGitLabIdentity reports whether project names the GitLab adapter.
func isGitLabIdentity(project string) bool {
	return domain.IsGitLabProjectID(project)
}
