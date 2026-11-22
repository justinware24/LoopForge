package engine

import (
	"errors"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestExplainGitAuthFailure(t *testing.T) {
	raw := errors.New("repository checkout: git clone https://git.example.com/a/b.git: exit status 128 (fatal: could not read Username for 'https://git.example.com': terminal prompts disabled) (exit 0)")
	missing := explainGitAuthFailure(raw, false)
	if !errors.Is(missing, raw) || !strings.HasPrefix(missing.Error(), "运行责任人未配置 Git Token") {
		t.Fatalf("missing token: %v", missing)
	}
	for _, want := range []string{"Contents 和 Pull requests", "https://github.com/settings/tokens", "could not read Username"} {
		if !strings.Contains(missing.Error(), want) {
			t.Fatalf("missing %q in %v", want, missing)
		}
	}
	if rejected := explainGitAuthFailure(raw, true); !strings.HasPrefix(rejected.Error(), "运行责任人的 Git Token 无效") {
		t.Fatalf("rejected token: %v", rejected)
	}
	for _, other := range []error{
		errors.New("repository checkout: git clone: exit status 128 (fatal: repository not found)"),
		errors.New("CodeBuddy Authentication failed: invalid api key"),
		nil,
	} {
		if got := explainGitAuthFailure(other, false); got != other {
			t.Fatalf("unrelated error rewritten: %v", got)
		}
	}
}

func TestExplainGitAuthFailureForGitLabRepo(t *testing.T) {
	raw := errors.New("repository checkout: git clone: exit status 128 (fatal: could not read Username for 'http://127.0.0.1:8929': terminal prompts disabled) (exit 0)")
	repos := []domain.RepositoryContext{{Provider: domain.RepositoryProviderGitLab, GongfengProjectID: "gitlab:http://127.0.0.1:8929:lfdev/dfa-boot"}}

	missing := explainGitAuthFailureFor(raw, repos, false, false)
	if !strings.HasPrefix(missing.Error(), "运行责任人未配置 GitLab 令牌") {
		t.Fatalf("GitLab missing token hint: %v", missing)
	}
	for _, want := range []string{"个人配置 → GitLab 令牌", "Reporter", "Guest", "could not read Username"} {
		if !strings.Contains(missing.Error(), want) {
			t.Fatalf("missing %q in %v", want, missing)
		}
	}
	if strings.Contains(missing.Error(), "github.com/settings/tokens") {
		t.Fatalf("GitLab error must not point at GitHub: %v", missing)
	}
	if rejected := explainGitAuthFailureFor(raw, repos, false, true); !strings.HasPrefix(rejected.Error(), "运行责任人的 GitLab 令牌 无效") {
		t.Fatalf("GitLab rejected token: %v", rejected)
	}

	// A GitHub repo still gets the original GitHub hint.
	gh := []domain.RepositoryContext{{Provider: "github"}}
	if got := explainGitAuthFailureFor(raw, gh, false, false); !strings.HasPrefix(got.Error(), "运行责任人未配置 Git Token") {
		t.Fatalf("GitHub repo hint: %v", got)
	}
}
