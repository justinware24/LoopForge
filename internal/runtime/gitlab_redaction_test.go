package runtime

import (
	"strings"
	"testing"
)

// TestCompactCommandRedactsGitLabRewrites covers review finding P1-1: the
// process.started telemetry of `docker exec -e GIT_CONFIG_KEY_0=...` must not
// contain a GitLab token.
//
// A git `url.<authed>.insteadof` rewrite puts the credential in the *key* and
// the plain prefix in the *value*, so redacting keys is what protects the token.
func TestCompactCommandRedactsGitLabRewrites(t *testing.T) {
	const token = "glpat-supersecrettokenvalue123"
	args := []string{
		"exec",
		"-e", "GIT_CONFIG_COUNT=2",
		"-e", "GIT_CONFIG_KEY_0=url.https://private:" + token + "@gitlab.example.com/.insteadof",
		"-e", "GIT_CONFIG_VALUE_0=https://gitlab.example.com/",
		"-e", "AGENTFLOW_GIT_TOKEN=" + token,
		"-e", "GITLAB_TOKEN=" + token,
		"-e", "GITLAB_BASE_URL=https://gitlab.example.com",
		"gitlab-sandbox",
		"git",
		"clone",
		"https://gitlab.example.com/group/repo.git",
	}

	got := compactCommand(args)

	if strings.Contains(got, token) {
		t.Fatalf("a GitLab token survived redaction:\n%s", got)
	}
	// The checkout URL and the rewrite prefix must stay readable: they carry no
	// credential and are what makes a failed checkout diagnosable.
	if !strings.Contains(got, "https://gitlab.example.com/group/repo.git") {
		t.Errorf("the checkout URL was removed, log lost its value:\n%s", got)
	}
	if !strings.Contains(got, "GIT_CONFIG_VALUE_0=https://gitlab.example.com/") {
		t.Errorf("the non-secret rewrite prefix must stay readable:\n%s", got)
	}
	if !strings.Contains(got, "<redacted>") {
		t.Errorf("no redaction marker present:\n%s", got)
	}
}

// TestGitLabTokenVariablesAreSensitive guards the variable names this
// integration introduced.
func TestGitLabTokenVariablesAreSensitive(t *testing.T) {
	for _, key := range []string{"GITLAB_TOKEN", "AGENTFLOW_GITLAB_TOKEN", "AGENTFLOW_GIT_HTTP_PASSWORD"} {
		if !sensitiveProcessEnvironmentKey(key) {
			t.Errorf("%s must be treated as sensitive", key)
		}
	}
	// The instance address is not a secret; keeping it readable tells the
	// operator which host the command talked to.
	if sensitiveProcessEnvironmentKey("GITLAB_BASE_URL") {
		t.Error("GITLAB_BASE_URL is not a secret and should stay readable")
	}
}
