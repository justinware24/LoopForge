package httpapi

import (
	"testing"

	"agentflow/internal/domain"
)

func TestDeliveryMRTargetPicksTheRightPlatform(t *testing.T) {
	gitlabIdentity := "gitlab:http://127.0.0.1:8929:lfdev/dfa-boot"
	repos := []domain.RepositoryContext{
		{Name: "dfa-boot", Provider: "gitlab", GongfengProjectID: gitlabIdentity},
		{Name: "app", Provider: "gongfeng", GongfengProjectID: "team/app"},
	}

	// A GitLab merge request is keyed by the repository identity, because that
	// is what the polling job recognizes as a GitLab merge request.
	project, iid, ok := deliveryMRTarget(workspaceMRResult{
		Repository: "dfa-boot",
		MRURL:      "http://127.0.0.1:8929/lfdev/dfa-boot/-/merge_requests/3",
	}, repos)
	if !ok {
		t.Fatal("a GitLab merge request must be recognized")
	}
	if project != gitlabIdentity {
		t.Fatalf("project: got %q want %q", project, gitlabIdentity)
	}
	if iid != 3 {
		t.Fatalf("iid: got %d want 3", iid)
	}

	// A Gongfeng merge request keeps its project path.
	project, iid, ok = deliveryMRTarget(workspaceMRResult{
		Repository: "app",
		MRURL:      "https://git.example.com/team/app/merge_requests/9",
	}, repos)
	if !ok {
		t.Fatal("a Gongfeng merge request must be recognized")
	}
	if project != "team/app" || iid != 9 {
		t.Fatalf("project=%q iid=%d", project, iid)
	}

	// A merge request that belongs to neither repository is not registered.
	if _, _, ok := deliveryMRTarget(workspaceMRResult{
		Repository: "unknown",
		MRURL:      "http://127.0.0.1:8929/lfdev/dfa-boot/-/merge_requests/3",
	}, repos); ok {
		t.Fatal("an unrelated merge request must not be registered")
	}

	// Without an address there is nothing to register.
	if _, _, ok := deliveryMRTarget(workspaceMRResult{Repository: "dfa-boot"}, repos); ok {
		t.Fatal("a missing address must not be registered")
	}
}
