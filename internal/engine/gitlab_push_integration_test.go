package engine

import (
	"context"
	"os"
	"strings"
	"testing"

	"agentflow/internal/gitwork"
)

// TestGitLabCredentialPushesToRealInstance proves the credential path the
// workspace delivery uses: with the GitLab instance token attached to the
// context, a git push to that instance is authenticated and succeeds.
//
// This is the core of the delivery path, because PushConversationWorkspace
// attaches the credential exactly this way before running git.
//
// The instance and its token come from the environment:
//
//	LF_TEST_GITLAB_BASE_URL=http://127.0.0.1:8929
//	LF_TEST_GITLAB_TOKEN=glpat-...
//	LF_TEST_GITLAB_REPO=http://127.0.0.1:8929/lfdev/dfa-boot.git
func TestGitLabCredentialPushesToRealInstance(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("LF_TEST_GITLAB_BASE_URL"))
	token := strings.TrimSpace(os.Getenv("LF_TEST_GITLAB_TOKEN"))
	remote := strings.TrimSpace(os.Getenv("LF_TEST_GITLAB_REPO"))
	if baseURL == "" || token == "" || remote == "" {
		t.Skip("set LF_TEST_GITLAB_BASE_URL, LF_TEST_GITLAB_TOKEN and LF_TEST_GITLAB_REPO to run this")
	}

	dir, err := os.MkdirTemp("", "lf-gitlab-push-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	ctx := gitwork.WithGitLabCredential(context.Background(), gitwork.GitLabCredential{
		BaseURL: baseURL,
		Token:   token,
	})

	runGit := func(args ...string) string {
		t.Helper()
		out, err := gitwork.Run(ctx, dir, args...)
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}

	runGit("init", "-b", "main")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test")
	if err := os.WriteFile(dir+"/probe.txt", []byte("probe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "probe.txt")
	runGit("commit", "-m", "probe")

	// The remote is fetched and pushed with the credential attached. Without
	// it, both fail with an authentication error.
	runGit("remote", "add", "origin", remote)
	runGit("fetch", "origin", "main")
	runGit("push", "origin", "HEAD:refs/heads/lf-test/gitlab-credential-push")

	// The branch must really exist on the remote.
	ls := runGit("ls-remote", "origin", "refs/heads/lf-test/gitlab-credential-push")
	if !strings.Contains(ls, "lf-test/gitlab-credential-push") {
		t.Fatalf("the pushed branch is missing on the remote: %q", ls)
	}
	t.Logf("pushed branch confirmed on the remote: %s", strings.TrimSpace(ls))
}
