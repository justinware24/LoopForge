package domain

import "testing"

func TestParseGitLabMR(t *testing.T) {
	for _, tc := range []struct {
		url     string
		base    string
		project string
		iid     int
		ok      bool
	}{
		{"http://127.0.0.1:8929/lfdev/dfa-boot/-/merge_requests/3", "http://127.0.0.1:8929", "lfdev/dfa-boot", 3, true},
		{"http://localhost:8929/lfdev/dfa-boot/-/merge_requests/1", "http://localhost:8929", "lfdev/dfa-boot", 1, true},
		// Older instances serve the number without the dash marker.
		{"https://gitlab.example.com/group/repo/merge_requests/12", "https://gitlab.example.com", "group/repo", 12, true},
		// An instance hosted under a sub-path keeps it in the project path.
		{"https://example.com/gitlab/group/repo/-/merge_requests/7", "https://example.com", "gitlab/group/repo", 7, true},
		// Not a merge request address.
		{"https://gitlab.example.com/group/repo", "", "", 0, false},
		{"https://gitlab.example.com/group/repo/-/merge_requests/abc", "", "", 0, false},
		{"", "", "", 0, false},
	} {
		base, project, iid, ok := ParseGitLabMR(tc.url)
		if ok != tc.ok {
			t.Errorf("%s: ok got %v want %v", tc.url, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if base != tc.base || project != tc.project || iid != tc.iid {
			t.Errorf("%s: got %q/%q/%d want %q/%q/%d", tc.url, base, project, iid, tc.base, tc.project, tc.iid)
		}
	}
}

func TestGitLabMRURLRoundTrip(t *testing.T) {
	identity := GitLabRemoteIdentity("http://127.0.0.1:8929", "lfdev/dfa-boot")
	url := GitLabMRURL(identity, 5)
	if url == "" {
		t.Fatal("the MR url must be built")
	}
	base, project, iid, ok := ParseGitLabMR(url)
	if !ok {
		t.Fatalf("the built url must parse back: %q", url)
	}
	if base != "http://127.0.0.1:8929" || project != "lfdev/dfa-boot" || iid != 5 {
		t.Fatalf("round trip: got %q/%q/%d", base, project, iid)
	}
}
