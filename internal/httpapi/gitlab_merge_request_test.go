package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// gitLabFake is a GitLab API double. Its handler can be swapped between calls
// so one server serves the whole test, which keeps the repository identity
// (and therefore the instance origin) stable.
type gitLabFake struct {
	server  *httptest.Server
	handler http.HandlerFunc
}

func newGitLabFake(t *testing.T) *gitLabFake {
	t.Helper()
	fake := &gitLabFake{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fake.handler == nil {
			w.WriteHeader(404)
			return
		}
		fake.handler(w, r)
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *gitLabFake) serve(handler http.HandlerFunc) { f.handler = handler }

func (f *gitLabFake) instance() string { return f.server.URL }

func (f *gitLabFake) identity(project string) string {
	return domain.GitLabRemoteIdentity(f.server.URL, project)
}

func TestEnsureGitLabMergeRequestCreatesAndThenUpdates(t *testing.T) {
	fake := newGitLabFake(t)
	var created, updated map[string]any
	tokenSeen := ""
	updatePath := ""

	fake.serve(func(w http.ResponseWriter, r *http.Request) {
		tokenSeen = r.Header.Get("PRIVATE-TOKEN")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/merge_requests"):
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"id":42}`))
		case r.Method == http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &created)
			_, _ = w.Write([]byte(`{"id":900,"iid":9,"web_url":"http://gitlab.example.com/g/p/-/merge_requests/9","source_project_id":42}`))
		case r.Method == http.MethodPut:
			updatePath = r.URL.Path
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &updated)
			_, _ = w.Write([]byte(`{"id":900,"iid":9}`))
		default:
			w.WriteHeader(404)
		}
	})

	ctx := context.Background()
	client, err := newGitLabMRClient(fake.identity("group/project"), fake.instance(), "glpat-secret")
	if err != nil {
		t.Fatal(err)
	}
	result, err := ensureGitLabMergeRequest(ctx, client, "feature/x", "main", "标题", "描述")
	if err != nil {
		t.Fatal(err)
	}
	if result.MRIID != 9 {
		t.Fatalf("iid: got %d want 9", result.MRIID)
	}
	if result.MRURL != "http://gitlab.example.com/g/p/-/merge_requests/9" {
		t.Fatalf("mr url: %q", result.MRURL)
	}
	if result.Existing {
		t.Fatalf("a fresh MR must not be reported as existing")
	}
	if tokenSeen != "glpat-secret" {
		t.Fatalf("token header: got %q", tokenSeen)
	}
	if created["source_branch"] != "feature/x" || created["target_branch"] != "main" {
		t.Fatalf("create payload: %+v", created)
	}
	if created["title"] != "标题" {
		t.Fatalf("create title: %+v", created)
	}

	// A second submission finds the open MR and updates its title and body
	// instead of opening a second one.
	fake.serve(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/merge_requests"):
			_, _ = w.Write([]byte(`[{"id":900,"iid":9,"state":"opened","source_branch":"feature/x","target_branch":"main","source_project_id":42,"web_url":"http://gitlab.example.com/g/p/-/merge_requests/9"}]`))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"id":42}`))
		case r.Method == http.MethodPut:
			updatePath = r.URL.Path
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &updated)
			_, _ = w.Write([]byte(`{"id":900,"iid":9}`))
		default:
			w.WriteHeader(404)
		}
	})
	client, err = newGitLabMRClient(fake.identity("group/project"), fake.instance(), "glpat-secret")
	if err != nil {
		t.Fatal(err)
	}
	result, err = ensureGitLabMergeRequest(ctx, client, "feature/x", "main", "新标题", "新描述")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Existing || !result.Updated {
		t.Fatalf("an existing MR must be reused and updated: %+v", result)
	}
	if updated["title"] != "新标题" {
		t.Fatalf("update title: %+v", updated)
	}
	// GitLab addresses the MR by its iid, not by the project-wide id.
	if !strings.HasSuffix(updatePath, "/merge_requests/9") {
		t.Fatalf("update path must use the MR iid: %q", updatePath)
	}
}

func TestGitLabMergeRequestIgnoresForeignFork(t *testing.T) {
	// An MR opened from a fork carries a different source_project_id. It must
	// not be treated as this project's MR.
	fake := newGitLabFake(t)
	fake.serve(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/merge_requests"):
			_, _ = w.Write([]byte(`[{"id":900,"iid":9,"state":"opened","source_branch":"feature/x","target_branch":"main","source_project_id":777,"web_url":"http://evil/9"}]`))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"id":42}`))
		default:
			w.WriteHeader(404)
		}
	})
	client, err := newGitLabMRClient(fake.identity("group/project"), fake.instance(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	_, found, err := client.findMergeRequest(context.Background(), 42, "feature/x", "main")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatalf("an MR from a fork must not be adopted")
	}
}

func TestNewGitLabMRClientRefusesAnotherInstance(t *testing.T) {
	fake := newGitLabFake(t)
	_, err := newGitLabMRClient(fake.identity("group/project"), "http://other-instance.example.com", "tok")
	if err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("a token must not be sent to another instance: %v", err)
	}
}

func TestNewGitLabMRClientNeedsAToken(t *testing.T) {
	fake := newGitLabFake(t)
	_, err := newGitLabMRClient(fake.identity("group/project"), fake.instance(), "  ")
	if err == nil || !strings.Contains(err.Error(), "令牌") {
		t.Fatalf("a missing token must be reported: %v", err)
	}
}

func TestGitLabMRErrorExplainsTheFailure(t *testing.T) {
	for status, want := range map[int]string{
		http.StatusUnauthorized: "401",
		http.StatusForbidden:    "403",
		http.StatusNotFound:     "404",
	} {
		err := gitLabMRError(status, strings.NewReader(`{"message":"nope"}`))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("status %d: %v", status, err)
		}
	}
}
