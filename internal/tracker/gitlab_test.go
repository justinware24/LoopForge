package tracker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// gitlabTestServer is a minimal GitLab API double. It records the requests it
// receives so tests can assert that no token reaches a wrong host and that
// merges pin the reviewed commit.
type gitlabTestServer struct {
	*httptest.Server
	t *testing.T

	projectID   int64
	projectPath string

	mergeRequests []mergeRequest
	branchHeads   map[string]string
	created       []map[string]any
	mergeCalls    []map[string]any
	mergeFailure  string
}

func newGitLabTestServer(t *testing.T) *gitlabTestServer {
	t.Helper()
	g := &gitlabTestServer{
		t:           t,
		projectID:   42,
		projectPath: "group/repo",
		branchHeads: map[string]string{},
	}

	// One catch-all handler: the client URL-escapes the project path, and
	// ServeMux patterns would re-split the decoded form, so routing is done by
	// hand on the unescaped tail.
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if unescaped, err := url.PathUnescape(path); err == nil {
			path = unescaped
		}

		if path == "/api/v4/user" {
			if r.Header.Get("PRIVATE-TOKEN") == "" {
				http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
				return
			}
			writeJSON(w, gitlabUser{ID: 7, Username: "tester", Name: "Tester"})
			return
		}

		const prefix = "/api/v4/projects/"
		if !strings.HasPrefix(path, prefix) {
			http.NotFound(w, r)
			return
		}
		tail := strings.TrimPrefix(path, prefix)
		// tail is "<projectPath>" or "<projectPath>/<sub-resource>".
		rest := ""
		if strings.HasPrefix(tail, g.projectPath) {
			rest = strings.TrimPrefix(strings.TrimPrefix(tail, g.projectPath), "/")
		}

		switch {
		case rest == "" && r.Method == http.MethodGet:
			writeJSON(w, gitlabProject{ID: g.projectID, PathWithNamespace: g.projectPath, DefaultBranch: "main"})

		case rest == "merge_requests" && r.Method == http.MethodGet:
			writeJSON(w, g.mergeRequests)

		case rest == "merge_requests" && r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			g.created = append(g.created, body)
			source := asString(body["source_branch"])
			mr := mergeRequest{
				ID: 900, IID: 9, ProjectID: g.projectID,
				SourceBranch: source, TargetBranch: asString(body["target_branch"]),
				SourceProjectID: g.projectID, TargetProjectID: g.projectID,
				State: "opened", SHA: g.branchHeads[source],
			}
			// A created MR is visible to later lookups and merges, exactly as the
			// real API behaves.
			g.mergeRequests = append(g.mergeRequests, mr)
			writeJSON(w, mr)

		case strings.HasPrefix(rest, "repository/branches/"):
			branch := strings.TrimPrefix(rest, "repository/branches/")
			head, ok := g.branchHeads[branch]
			if !ok {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, map[string]any{"name": branch, "commit": map[string]any{"id": head}})

		case strings.HasSuffix(rest, "/merge") && r.Method == http.MethodPut:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			g.mergeCalls = append(g.mergeCalls, body)
			if g.mergeFailure != "" {
				w.WriteHeader(http.StatusMethodNotAllowed)
				writeJSON(w, map[string]any{"message": []string{g.mergeFailure}})
				return
			}
			iid := atoiSafe(strings.TrimSuffix(strings.TrimPrefix(rest, "merge_requests/"), "/merge"))
			for i := range g.mergeRequests {
				if g.mergeRequests[i].IID == iid {
					g.mergeRequests[i].State = "merged"
					g.mergeRequests[i].MergedAt = "2026-11-17T00:00:00Z"
					g.mergeRequests[i].MergeCommitSHA = "merged-sha"
					writeJSON(w, g.mergeRequests[i])
					return
				}
			}
			http.NotFound(w, r)

		case strings.HasPrefix(rest, "merge_requests/") && r.Method == http.MethodGet:
			// GET one MR: the client polls this before merging to wait out
			// GitLab's asynchronous mergeability computation.
			iid := atoiSafe(strings.TrimPrefix(rest, "merge_requests/"))
			for i := range g.mergeRequests {
				if g.mergeRequests[i].IID == iid {
					mr := g.mergeRequests[i]
					if mr.DetailedMergeStatus == "" {
						mr.DetailedMergeStatus = "mergeable"
					}
					writeJSON(w, mr)
					return
				}
			}
			http.NotFound(w, r)

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *gitlabTestServer) identity() string {
	return domain.GitLabRemoteIdentity(g.URL, g.projectPath)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// TestGitLabMergeRefusesForkSourceBranch covers review finding P1-2: an MR whose
// source project differs from the target project must never be merged, even when
// the branch name matches exactly.
func TestGitLabMergeRefusesForkSourceBranch(t *testing.T) {
	srv := newGitLabTestServer(t)
	srv.mergeRequests = []mergeRequest{{
		ID: 1, IID: 1, ProjectID: srv.projectID,
		SourceBranch: "loopforge/work", TargetBranch: "main",
		SourceProjectID: 999, TargetProjectID: srv.projectID,
		State: "opened", SHA: "abc",
	}}
	srv.branchHeads["loopforge/work"] = "abc"

	s := NewService()
	_, err := s.gitlabEnsureAndMerge(srv.identity(), srv.URL, "token", "loopforge/work", "main", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(srv.created) != 1 {
		t.Fatalf("expected one created MR in the target project, got %d", len(srv.created))
	}
	if len(srv.mergeCalls) != 1 {
		t.Fatalf("expected the new MR to be merged, got %d merge calls", len(srv.mergeCalls))
	}
}

// TestGitLabMergedMRWithNewCommitsCreatesFreshMR covers review finding P1-3: a
// merged MR that no longer covers the branch head must not be reported as this
// delivery.
func TestGitLabMergedMRWithNewCommitsCreatesFreshMR(t *testing.T) {
	srv := newGitLabTestServer(t)
	srv.mergeRequests = []mergeRequest{{
		ID: 1, IID: 1, ProjectID: srv.projectID,
		SourceBranch: "loopforge/work", TargetBranch: "main",
		SourceProjectID: srv.projectID, TargetProjectID: srv.projectID,
		State: "merged", MergedAt: "2026-11-01T00:00:00Z", SHA: "old-sha",
	}}
	srv.branchHeads["loopforge/work"] = "new-sha"

	s := NewService()
	ref, err := s.gitlabEnsureAndMerge(srv.identity(), srv.URL, "token", "loopforge/work", "main", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(srv.created) != 1 {
		t.Fatalf("branch moved since the merge: a new MR must be created, got %d", len(srv.created))
	}
	if ref != "!9" {
		t.Fatalf("expected the new MR reference !9, got %q", ref)
	}
}

// TestGitLabMergedMRWithSameHeadIsIdempotent is the other half of P1-3: an
// unchanged branch means the existing merge already delivered this work.
func TestGitLabMergedMRWithSameHeadIsIdempotent(t *testing.T) {
	srv := newGitLabTestServer(t)
	srv.mergeRequests = []mergeRequest{{
		ID: 1, IID: 3, ProjectID: srv.projectID,
		SourceBranch: "loopforge/work", TargetBranch: "main",
		SourceProjectID: srv.projectID, TargetProjectID: srv.projectID,
		State: "merged", MergedAt: "2026-11-01T00:00:00Z", SHA: "same-sha",
	}}
	srv.branchHeads["loopforge/work"] = "same-sha"

	s := NewService()
	ref, err := s.gitlabEnsureAndMerge(srv.identity(), srv.URL, "token", "loopforge/work", "main", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref != "!3" {
		t.Fatalf("expected the existing merged MR !3, got %q", ref)
	}
	if len(srv.created) != 0 {
		t.Fatalf("no new MR should be created, got %d", len(srv.created))
	}
	if len(srv.mergeCalls) != 0 {
		t.Fatalf("no merge should be attempted, got %d", len(srv.mergeCalls))
	}
}

// TestGitLabMergePinsReviewedCommit asserts the merge request carries the sha
// that was looked up, so a branch that moved after review is not merged.
func TestGitLabMergePinsReviewedCommit(t *testing.T) {
	srv := newGitLabTestServer(t)
	srv.mergeRequests = []mergeRequest{{
		ID: 1, IID: 5, ProjectID: srv.projectID,
		SourceBranch: "loopforge/work", TargetBranch: "main",
		SourceProjectID: srv.projectID, TargetProjectID: srv.projectID,
		State: "opened", SHA: "reviewed-sha",
	}}
	srv.branchHeads["loopforge/work"] = "reviewed-sha"

	s := NewService()
	if _, err := s.gitlabEnsureAndMerge(srv.identity(), srv.URL, "token", "loopforge/work", "main", "title", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(srv.mergeCalls) != 1 {
		t.Fatalf("expected one merge call, got %d", len(srv.mergeCalls))
	}
	if got := asString(srv.mergeCalls[0]["sha"]); got != "reviewed-sha" {
		t.Fatalf("merge must pin the reviewed sha, got %q", got)
	}
}

// TestGitLabTokenNeverSentToWrongInstance covers review finding P1-1 from the
// client side: a repository identity on another host than the configured
// instance is refused before any request is made.
func TestGitLabTokenNeverSentToWrongInstance(t *testing.T) {
	srv := newGitLabTestServer(t)
	identityOnOtherHost := domain.GitLabRemoteIdentity("https://gitlab.other.example", srv.projectPath)

	_, err := newGitLabClient(identityOnOtherHost, srv.URL, "secret-token")
	if err == nil {
		t.Fatal("a repository on another GitLab instance must be rejected")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("the error must not contain the token: %v", err)
	}
}

// TestGitLabErrorDoesNotLeakToken asserts that a GitLab error body echoing the
// credential cannot surface it.
func TestGitLabErrorDoesNotLeakToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/user" {
			writeJSON(w, gitlabUser{ID: 1, Username: "u"})
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"401 Unauthorized PRIVATE-TOKEN=glpat-secretvalue1234567890"}`))
	}))
	defer srv.Close()

	g, err := newGitLabClient(domain.GitLabRemoteIdentity(srv.URL, "group/repo"), srv.URL, "glpat-secretvalue1234567890")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	_, err = g.getProject()
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "glpat-secretvalue1234567890") {
		t.Fatalf("error leaked the token: %v", err)
	}
}
