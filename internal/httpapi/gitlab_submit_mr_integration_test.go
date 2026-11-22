package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// TestSubmitMRDeliversToRealGitLab drives the whole workspace delivery against
// a real GitLab instance: the guard must accept a GitLab repository, the push
// must land the work branch, and the merge request must be opened.
//
// It is the end-to-end check of the GitLab delivery path, which used to stop
// at "you have not configured a Git Token" because the guard only looked at
// the GitHub credential.
//
// The environment provides the instance and its token:
//
//	LF_TEST_GITLAB_BASE_URL=http://127.0.0.1:8929
//	LF_TEST_GITLAB_TOKEN=glpat-...
//	LF_TEST_LOOPFORGE_DSN=<mysql dsn>
//	LF_TEST_RUN_ID=<a conversation workspace run with a GitLab repository>
func TestSubmitMRDeliversToRealGitLab(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("LF_TEST_GITLAB_BASE_URL"))
	token := strings.TrimSpace(os.Getenv("LF_TEST_GITLAB_TOKEN"))
	dsn := strings.TrimSpace(os.Getenv("LF_TEST_LOOPFORGE_DSN"))
	runID := strings.TrimSpace(os.Getenv("LF_TEST_RUN_ID"))
	if baseURL == "" || token == "" || dsn == "" || runID == "" {
		t.Skip("set LF_TEST_GITLAB_BASE_URL, LF_TEST_GITLAB_TOKEN, LF_TEST_LOOPFORGE_DSN and LF_TEST_RUN_ID to run this")
	}

	store, err := repo.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	run, ok := store.RunState(runID)
	if !ok {
		t.Fatalf("run %s not found", runID)
	}
	if len(run.RepositoryContexts()) == 0 {
		t.Fatalf("run %s has no repository", runID)
	}
	rc := run.RepositoryContexts()[0]
	t.Logf("repository: name=%s provider=%s workBranch=%s base=%s identity=%s",
		rc.Name, rc.Provider, rc.WorkBranch, rc.BaseBranch, rc.GongfengProjectID)
	if !domain.IsGitLabProjectID(rc.GongfengProjectID) {
		t.Fatalf("this test needs a GitLab repository, got %q", rc.GongfengProjectID)
	}

	// The user whose credential is used. The delivery attributes the commit to
	// the requesting user, so the test acts as that user.
	user, ok := store.User(1)
	if !ok {
		t.Fatal("user 1 not found")
	}

	// The platform stores the GitLab token per user. The test supplies it
	// directly, because the stored value is write-only by design.
	workspaces := strings.TrimSpace(os.Getenv("LF_TEST_LOOPFORGE_WORKSPACES"))
	if workspaces == "" {
		t.Skip("set LF_TEST_LOOPFORGE_WORKSPACES to the platform workspace directory")
	}
	h := &agentToolGateway{deps: &deps{
		repo:   store,
		engine: engine.New(store, runtime.NewManager(workspaces)),
	}}
	session := &AgentToolSession{
		RunID:     runID,
		ProjectID: run.ExecutionContext.ProjectID,
		UserID:    user.ID,
		User:      user,
		Mode:      AgentToolAssistant,
	}
	// The gateway reads the credential from the store, so the token under test
	// is stored for this user first. The previous credential is restored
	// afterwards: this test runs against a live installation, so it must not
	// leave the user's own credential changed.
	previous := store.GitLabCredentialFor(user.ID)
	instance := baseURL
	if _, err := store.UpdateUserCredentials(user.ID, repo.UserCredentialUpdate{
		GitLabToken:   token,
		GitLabBaseURL: &instance,
	}); err != nil {
		t.Fatalf("store the GitLab credential: %v", err)
	}
	t.Cleanup(func() {
		update := repo.UserCredentialUpdate{}
		if previous.Configured() {
			update.GitLabToken = previous.Token
			previousInstance := previous.BaseURL
			update.GitLabBaseURL = &previousInstance
		} else {
			update.GitLabTokenClear = true
			update.GitLabBaseURLClear = true
		}
		_, _ = store.UpdateUserCredentials(user.ID, update)
	})

	out, err := h.workspaceSubmitMR(context.Background(), session, map[string]any{
		"title":       "LoopForge: GitLab 交付链路验证",
		"description": "由集成测试提交，用于验证 GitLab 仓库的推送与 MR 创建。",
	})
	if err != nil {
		t.Fatalf("submit_mr: %v", err)
	}
	t.Logf("submit_mr result: %s", out)

	var payload struct {
		Results []workspaceMRResult `json:"results"`
		Note    string               `json:"note"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if len(payload.Results) == 0 {
		t.Fatalf("no delivery result: %s", out)
	}
	for _, r := range payload.Results {
		t.Logf("repository=%s pushed=%v source=%s target=%s commit=%s mrUrl=%s error=%s",
			r.Repository, r.Pushed, r.SourceBranch, r.TargetBranch, r.Commit, r.MRURL, r.Error)
		if r.Error != "" {
			t.Fatalf("delivery failed: %s", r.Error)
		}
		if !r.Pushed {
			t.Fatalf("the work branch was not pushed: %+v", r)
		}
		if r.MRURL == "" {
			t.Fatalf("no merge request was opened: %+v", r)
		}
	}
	t.Logf("note: %s", payload.Note)
}
