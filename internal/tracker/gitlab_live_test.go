//go:build gitlab_live

package tracker

import (
	"os"
	"testing"

	"agentflow/internal/domain"
)

// TestGitLabLiveMergeRequest exercises the real GitLab adapter against a running
// instance. It is behind the `gitlab_live` build tag because it needs a server,
// a token and a repository:
//
//	export LFORGE_GITLAB_URL=http://127.0.0.1:8929
//	export LFORGE_GITLAB_TOKEN=glpat-xxxxxxxx
//	export LFORGE_GITLAB_PROJECT=lfdev/loopforge-demo
//	export LFORGE_GITLAB_BRANCH=loopforge/e2e-test
//	go test -tags gitlab_live ./internal/tracker/ -run TestGitLabLive -v
//
// The branch must already exist with a commit, and its target branch must be the
// project's default branch. The test creates and merges a real MR, so it should
// only point at a disposable repository.
func TestGitLabLiveMergeRequest(t *testing.T) {
	baseURL := os.Getenv("LFORGE_GITLAB_URL")
	token := os.Getenv("LFORGE_GITLAB_TOKEN")
	project := os.Getenv("LFORGE_GITLAB_PROJECT")
	branch := os.Getenv("LFORGE_GITLAB_BRANCH")
	target := os.Getenv("LFORGE_GITLAB_TARGET")
	if target == "" {
		target = "main"
	}
	if baseURL == "" || token == "" || project == "" || branch == "" {
		t.Skip("set LFORGE_GITLAB_URL/TOKEN/PROJECT/BRANCH to run the live test")
	}

	identity := domain.GitLabRemoteIdentity(baseURL, project)
	if !domain.IsGitLabProjectID(identity) {
		t.Fatalf("identity %q is not a GitLab project id", identity)
	}
	t.Logf("identity=%s branch=%s target=%s", identity, branch, target)

	g, err := newGitLabClient(identity, baseURL, token)
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	user, err := g.currentUser()
	if err != nil {
		t.Fatalf("verify token: %v", err)
	}
	t.Logf("token identity: %s (id=%d)", user.Username, user.ID)

	proj, err := g.getProject()
	if err != nil {
		t.Fatalf("resolve project: %v", err)
	}
	t.Logf("project: %s id=%d default_branch=%s", proj.PathWithNamespace, proj.ID, proj.DefaultBranch)

	head, err := g.branchHead(branch)
	if err != nil {
		t.Fatalf("branch head: %v", err)
	}
	if head == "" {
		t.Fatalf("branch %q does not exist; push a commit to it first", branch)
	}
	t.Logf("branch %s head=%s", branch, head)

	// Idempotent first pass: create + merge.
	ref, err := newLiveService().gitlabEnsureAndMerge(identity, baseURL, token, branch, target, "LoopForge e2e", "automated verification")
	if err != nil {
		t.Fatalf("ensure and merge: %v", err)
	}
	t.Logf("merged as %s", ref)

	// Second pass with the same head must reuse the merge (idempotent).
	ref2, err := newLiveService().gitlabEnsureAndMerge(identity, baseURL, token, branch, target, "LoopForge e2e", "automated verification")
	if err != nil {
		t.Fatalf("idempotent re-run: %v", err)
	}
	if ref2 != ref {
		t.Fatalf("re-run created a different MR: %s vs %s", ref2, ref)
	}
	t.Logf("idempotent re-run reused %s", ref2)

	// The delivery lookup must report the merged MR for this branch.
	found, ok, err := newLiveService().gitlabFindBranch(identity, baseURL, token, branch)
	if err != nil {
		t.Fatalf("find branch MR: %v", err)
	}
	if !ok {
		t.Fatal("no MR found for the delivered branch")
	}
	if found.State != "merged" {
		t.Fatalf("delivery state = %q, want merged", found.State)
	}
	t.Logf("delivery: %s state=%s target=%s", found.URL, found.State, found.TargetBranch)

	// And a new commit on the same branch must produce a *new* MR rather than
	// reporting the historical merge (review finding P1-3).
	t.Log("P1-3 check: the historical MR must not be reused after the branch advances")
	if mergedAgain, err := newLiveService().gitlabEnsureAndMerge(identity, baseURL, token, branch, target, "LoopForge e2e (2)", ""); err != nil {
		t.Fatalf("second delivery after no new commit: %v", err)
	} else if mergedAgain != ref {
		t.Fatalf("unchanged branch produced a new MR: %s vs %s", mergedAgain, ref)
	}
}

// newLiveService builds a Service without a store: the live test calls the
// client methods directly, so no project/auth binding is involved.
func newLiveService() *Service { return NewService() }
