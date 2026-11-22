package httpapi

import (
	"context"
	"os"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// TestSubmitMRAgainstLocalGitLab exercises the whole delivery path against a
// real GitLab instance: it pushes a work branch and opens the merge request.
//
// The instance and its token come from the environment, so the test is skipped
// unless both are present:
//
//	LF_TEST_GITLAB_BASE_URL=http://127.0.0.1:8929
//	LF_TEST_GITLAB_TOKEN=glpat-...
//	LF_TEST_GITLAB_PROJECT=lfdev/dfa-boot
func TestSubmitMRAgainstLocalGitLab(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("LF_TEST_GITLAB_BASE_URL"))
	token := strings.TrimSpace(os.Getenv("LF_TEST_GITLAB_TOKEN"))
	project := strings.TrimSpace(os.Getenv("LF_TEST_GITLAB_PROJECT"))
	if baseURL == "" || token == "" || project == "" {
		t.Skip("set LF_TEST_GITLAB_BASE_URL, LF_TEST_GITLAB_TOKEN and LF_TEST_GITLAB_PROJECT to run this")
	}

	identity := gitLabIdentityFor(t, baseURL, project)
	client, err := newGitLabMRClient(identity, baseURL, token)
	if err != nil {
		t.Fatal(err)
	}

	// The project must be readable with this credential; otherwise the rest of
	// the test would fail for the wrong reason.
	projectID, err := client.gitLabProjectID(context.Background())
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	t.Logf("project id=%d identity=%s", projectID, identity)

	// A branch pair that does not exist yet: the create must succeed and the
	// lookup must then find it.
	source := "loopforge/test-submit-mr-" + strings.ReplaceAll(t.Name(), "/", "-")
	target := "main"

	mr, err := ensureGitLabMergeRequest(context.Background(), client, source, target, "LoopForge 集成测试", "由集成测试创建")
	if err != nil {
		t.Fatalf("create MR: %v", err)
	}
	t.Logf("created MR iid=%d url=%s", mr.MRIID, mr.MRURL)
	if mr.MRIID <= 0 {
		t.Fatalf("the MR must have an iid: %+v", mr)
	}

	// A second run must find the same MR and update it instead of opening a
	// second one.
	again, err := ensureGitLabMergeRequest(context.Background(), client, source, target, "LoopForge 集成测试（改）", "描述已更新")
	if err != nil {
		t.Fatalf("update MR: %v", err)
	}
	if !again.Existing || again.MRIID != mr.MRIID {
		t.Fatalf("the second call must reuse the MR: first=%+v second=%+v", mr, again)
	}
	if !again.Updated {
		t.Fatalf("the second call must update the MR: %+v", again)
	}
	t.Logf("reused and updated MR iid=%d updated=%v", again.MRIID, again.Updated)
}

// gitLabIdentityFor builds the stored repository identity of a project on an
// instance.
func gitLabIdentityFor(t *testing.T, baseURL, project string) string {
	t.Helper()
	identity := domain.GitLabRemoteIdentity(baseURL, project)
	if identity == "" {
		t.Fatalf("cannot build an identity for %s on %s", project, baseURL)
	}
	return identity
}
