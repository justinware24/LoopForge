package gitwork

import (
	"strings"
	"testing"
)

// TestGitLabRewriteIsScopedToItsInstance is the security property behind the
// whole GitLab credential design: the rewrite prefixes derived from one instance
// must only ever carry that instance's token.
func TestGitLabRewriteIsScopedToItsInstance(t *testing.T) {
	cred := GitLabCredential{BaseURL: "https://gitlab.example.com", Token: "glpat-secret"}

	env := SandboxEnvWithGitLab("", []GitLabCredential{cred})

	// Every rewrite must point at the configured host with the token embedded.
	count := 0
	for key, value := range env {
		if !strings.HasPrefix(key, "GIT_CONFIG_KEY_") {
			continue
		}
		count++
		index := strings.TrimPrefix(key, "GIT_CONFIG_KEY_")
		if !strings.Contains(value, "gitlab.example.com") {
			t.Errorf("rewrite key %s = %q must target the configured instance", key, value)
		}
		if !strings.Contains(value, "glpat-secret") {
			t.Errorf("rewrite key %s = %q must carry the token", key, value)
		}
		prefix := env["GIT_CONFIG_VALUE_"+index]
		if strings.Contains(prefix, "github.com") {
			t.Errorf("rewrite must not cover GitHub: %q", prefix)
		}
	}
	if count == 0 {
		t.Fatal("expected rewrite entries for the GitLab instance")
	}
	if got := env["GIT_CONFIG_COUNT"]; got != itoa(count) {
		t.Errorf("GIT_CONFIG_COUNT = %q, want %d", got, count)
	}
}

// TestGitLabAndGongfengRewritesCoexist checks that adding GitLab does not remove
// the existing Gongfeng rewrites.
func TestGitLabAndGongfengRewritesCoexist(t *testing.T) {
	env := SandboxEnvWithGitLab("private:gongfeng-token", []GitLabCredential{{
		BaseURL: "https://gitlab.example.com",
		Token:   "glpat-secret",
	}})

	var hasGongfeng, hasGitLab bool
	for key, value := range env {
		if !strings.HasPrefix(key, "GIT_CONFIG_KEY_") {
			continue
		}
		if strings.Contains(value, "gongfeng-token") {
			hasGongfeng = true
		}
		if strings.Contains(value, "glpat-secret") {
			hasGitLab = true
		}
	}
	if !hasGongfeng {
		t.Error("the Gongfeng rewrite disappeared when GitLab was added")
	}
	if !hasGitLab {
		t.Error("the GitLab rewrite is missing")
	}
}

// TestRewriteRemoteForGitLabIgnoresOtherHosts makes sure a GitLab token cannot
// be attached to a remote on a different host.
func TestRewriteRemoteForGitLabIgnoresOtherHosts(t *testing.T) {
	cred := GitLabCredential{BaseURL: "https://gitlab.example.com", Token: "glpat-secret"}

	sameHost := RewriteRemoteForGitLab("https://gitlab.example.com/group/repo.git", cred)
	if !strings.Contains(sameHost, "glpat-secret") {
		t.Fatalf("same-host remote should be rewritten, got %q", sameHost)
	}

	otherHost := RewriteRemoteForGitLab("https://github.com/group/repo.git", cred)
	if strings.Contains(otherHost, "glpat-secret") {
		t.Fatalf("GitLab token leaked into another host's remote: %q", otherHost)
	}
	if otherHost != "https://github.com/group/repo.git" {
		t.Fatalf("another host's remote must be returned unchanged, got %q", otherHost)
	}

	sshOther := RewriteRemoteForGitLab("git@gitlab.other.example:group/repo.git", cred)
	if strings.Contains(sshOther, "glpat-secret") {
		t.Fatalf("GitLab token leaked into an SSH remote on another host: %q", sshOther)
	}
}

// TestRewriteRemoteForGitLabNormalizesScpForm checks the scp-like SSH address of
// the same instance becomes an HTTPS URL carrying the token, because a token
// cannot be presented over SSH.
func TestRewriteRemoteForGitLabNormalizesScpForm(t *testing.T) {
	cred := GitLabCredential{BaseURL: "https://gitlab.example.com:8443", Token: "glpat-secret"}
	out := RewriteRemoteForGitLab("git@gitlab.example.com:group/repo.git", cred)
	if !strings.HasPrefix(out, "https://") {
		t.Fatalf("scp-like remote should become HTTPS, got %q", out)
	}
	if !strings.Contains(out, "gitlab.example.com:8443") {
		t.Fatalf("the instance port must be preserved, got %q", out)
	}
	if !strings.Contains(out, "group/repo.git") {
		t.Fatalf("the repository path must be preserved, got %q", out)
	}
	if !strings.Contains(out, "glpat-secret") {
		t.Fatalf("the token is missing, got %q", out)
	}
}

// TestSandboxEnvWithoutCredentials keeps the no-credential case clean: no
// GIT_CONFIG_* entries, so git falls back to its own configuration.
func TestSandboxEnvWithoutCredentials(t *testing.T) {
	env := SandboxEnvWithGitLab("", nil)
	if _, ok := env["GIT_CONFIG_COUNT"]; ok {
		t.Fatal("no credential should produce no rewrite block")
	}
	if env["GIT_TERMINAL_PROMPT"] != "0" {
		t.Fatal("interactive prompts must always be disabled")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// TestGitLabRewritePreservesInstanceScheme is a regression test for self-managed
// HTTP-only instances: forcing https makes git fail the TLS handshake before it
// presents the token.
func TestGitLabRewritePreservesInstanceScheme(t *testing.T) {
	cred := GitLabCredential{BaseURL: "http://127.0.0.1:8929", Token: "glpat-secret"}
	env := SandboxEnvWithGitLab("", []GitLabCredential{cred})
	if got := gitLabAuthedBase(cred); !strings.HasPrefix(got, "http://") {
		t.Fatalf("rewrite base must keep http scheme, got %q", got)
	}
	for key, value := range env {
		if strings.HasPrefix(key, "GIT_CONFIG_KEY_") {
			if !strings.HasPrefix(value, "url.http://private:glpat-secret@127.0.0.1:8929/.insteadof") {
				t.Errorf("rewrite key %s = %q must use http", key, value)
			}
			return
		}
	}
	t.Fatal("no GIT_CONFIG rewrite produced")
}
