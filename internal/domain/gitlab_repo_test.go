package domain

import "testing"

func TestParseGitLabRepoURL(t *testing.T) {
	const inst = "https://gitlab.example.com"

	cases := []struct {
		name    string
		raw     string
		base    string
		want    string // expected Project; empty means "must not parse"
		webURL  string
		sshRef  string
	}{
		{
			name:   "https project url",
			raw:    "https://gitlab.example.com/group/repo",
			base:   inst,
			want:   "group/repo",
			webURL: "https://gitlab.example.com/group/repo",
		},
		{
			name:   "clone url with .git",
			raw:    "https://gitlab.example.com/group/repo.git",
			base:   inst,
			want:   "group/repo",
			webURL: "https://gitlab.example.com/group/repo",
		},
		{
			name:   "tree sub page",
			raw:    "https://gitlab.example.com/group/repo/-/tree/main/src",
			base:   inst,
			want:   "group/repo",
			webURL: "https://gitlab.example.com/group/repo",
		},
		{
			name:   "merge request sub page",
			raw:    "https://gitlab.example.com/group/repo/-/merge_requests/12",
			base:   inst,
			want:   "group/repo",
			webURL: "https://gitlab.example.com/group/repo",
		},
		{
			name:   "nested group",
			raw:    "https://gitlab.example.com/a/b/c/repo",
			base:   inst,
			want:   "a/b/c/repo",
			webURL: "https://gitlab.example.com/a/b/c/repo",
		},
		{
			name:   "scp style ssh",
			raw:    "git@gitlab.example.com:group/repo.git",
			base:   inst,
			want:   "group/repo",
			webURL: "https://gitlab.example.com/group/repo",
			sshRef: "git@gitlab.example.com:group/repo.git",
		},
		{
			name:   "ssh url with port",
			raw:    "ssh://git@gitlab.example.com:2222/group/repo.git",
			base:   inst,
			want:   "group/repo",
			webURL: "https://gitlab.example.com/group/repo",
			sshRef: "git@gitlab.example.com:group/repo.git",
		},
		{
			name:   "bare project path",
			raw:    "group/repo",
			base:   inst,
			want:   "group/repo",
			webURL: "https://gitlab.example.com/group/repo",
		},
		{
			name:   "git clone prefix",
			raw:    "git clone https://gitlab.example.com/group/repo.git",
			base:   inst,
			want:   "group/repo",
			webURL: "https://gitlab.example.com/group/repo",
		},
		{
			name: "other host is rejected",
			raw:  "https://gitlab.other.example/group/repo",
			base: inst,
		},
		{
			name: "instance root is not a repository",
			raw:  "https://gitlab.example.com/",
			base: inst,
		},
		{
			name: "credentials in url are rejected",
			raw:  "https://user:token@gitlab.example.com/group/repo",
			base: inst,
		},
		{
			name: "single segment is not a repository",
			raw:  "https://gitlab.example.com/repo",
			base: inst,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, ok := ParseGitLabRepoURL(tc.raw, tc.base)
			if tc.want == "" {
				if ok {
					t.Fatalf("expected rejection, got project %q", info.Project)
				}
				return
			}
			if !ok {
				t.Fatalf("expected parse, got rejection")
			}
			if info.Project != tc.want {
				t.Errorf("project = %q, want %q", info.Project, tc.want)
			}
			if info.WebURL != tc.webURL {
				t.Errorf("webURL = %q, want %q", info.WebURL, tc.webURL)
			}
			if tc.sshRef != "" && info.SSHRef != tc.sshRef {
				t.Errorf("sshRef = %q, want %q", info.SSHRef, tc.sshRef)
			}
			if tc.sshRef == "" && info.SSHRef != "" {
				t.Errorf("https input must not produce an SSH ref, got %q", info.SSHRef)
			}
		})
	}
}

// TestParseGitLabRepoURLWithSubpathInstance covers self-managed instances served
// under a path prefix (https://example.com/gitlab).
func TestParseGitLabRepoURLWithSubpathInstance(t *testing.T) {
	const base = "https://example.com/gitlab"

	info, ok := ParseGitLabRepoURL("https://example.com/gitlab/group/repo", base)
	if !ok {
		t.Fatal("expected parse for a sub-path instance")
	}
	if info.Project != "group/repo" {
		t.Fatalf("project = %q, want group/repo", info.Project)
	}
	if info.WebURL != "https://example.com/gitlab/group/repo" {
		t.Fatalf("webURL = %q", info.WebURL)
	}
	// The sub-path must not leak into the project path of a bare paste.
	bare, ok := ParseGitLabRepoURL("group/repo", base)
	if !ok || bare.Project != "group/repo" {
		t.Fatalf("bare path under a sub-path instance parsed as %q (ok=%v)", bare.Project, ok)
	}
}

func TestGitLabRemoteIdentityRoundTrip(t *testing.T) {
	identity := GitLabRemoteIdentity("https://gitlab.example.com:8443", "group/sub/repo")
	if !IsGitLabProjectID(identity) {
		t.Fatalf("identity is not recognized: %q", identity)
	}
	base, project, ok := SplitGitLabProjectID(identity)
	if !ok {
		t.Fatalf("split failed for %q", identity)
	}
	if base != "https://gitlab.example.com:8443" {
		t.Errorf("base = %q", base)
	}
	if project != "group/sub/repo" {
		t.Errorf("project = %q", project)
	}
	// The scheme colon must not be mistaken for the separator.
	if url := GitLabMRURL(identity, 7); url != "https://gitlab.example.com:8443/group/sub/repo/-/merge_requests/7" {
		t.Errorf("MR url = %q", url)
	}
}

func TestNormalizeGitLabBaseURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://gitlab.com", "https://gitlab.com"},
		{"https://gitlab.com/", "https://gitlab.com"},
		{"gitlab.com", "https://gitlab.com"},
		{"HTTPS://GitLab.Com", "https://gitlab.com"},
		{"https://gitlab.example.com:8443/", "https://gitlab.example.com:8443"},
		{"https://example.com/gitlab/", "https://example.com/gitlab"},
		{"https://cert.example.com/", "https://cert.example.com"},
		{"https://user:pw@gitlab.com", ""},        // credentials rejected
		{"ftp://gitlab.com", ""},                  // unsupported scheme
		{"https://gitlab.com/group/repo.git", ""}, // a repo URL is not an instance
		{"", ""},
	}
	for _, tc := range cases {
		if got := NormalizeGitLabBaseURL(tc.in); got != tc.want {
			t.Errorf("NormalizeGitLabBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestGitLabRepositoryDoesNotCollideWithGithub ensures the two identities stay
// distinct: the same group/repo on GitHub and GitLab are different repositories.
func TestGitLabRepositoryDoesNotCollideWithGithub(t *testing.T) {
	github := "github:group/repo"
	gitlab := GitLabRemoteIdentity("https://gitlab.com", "group/repo")
	if github == gitlab {
		t.Fatal("GitHub and GitLab identities must differ")
	}
	if IsGitLabProjectID(github) {
		t.Fatal("a github: identity must not be treated as GitLab")
	}
	if !IsGitLabProjectID(gitlab) {
		t.Fatal("a gitlab: identity must be recognized")
	}
}
