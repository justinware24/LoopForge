package gitwork

import "testing"

func TestGitHostScopeReportsTheScheme(t *testing.T) {
	cases := map[string]struct {
		host       string
		scheme     string
		wantErr    bool
	}{
		"github.com":                   {"github.com", "https", false},
		"https://gitlab.example":       {"gitlab.example", "https", false},
		"http://gitlab.example":        {"gitlab.example", "http", false},
		"HTTP://GitLab.Example:8929/":  {"gitlab.example:8929", "http", false},
		"https://git.example.org:443":  {"git.example.org", "https", false},
		"http://git.example.org:8929":  {"git.example.org:8929", "http", false},
		"ssh://git@github.com":         {"", "", true},
		"ftp://github.com":             {"", "", true},
		"http://github.com/org/repo":    {"", "", true},
		"http://user@github.com":       {"", "", true},
		"http://127.0.0.1:8929":        {"", "", true},
		"http://":                      {"", "", true},
	}
	for input, want := range cases {
		host, scheme, err := GitHostScope(input)
		if want.wantErr {
			if err == nil {
				t.Errorf("GitHostScope(%q) = %q, want error", input, host)
			}
			continue
		}
		if err != nil {
			t.Errorf("GitHostScope(%q): %v", input, err)
			continue
		}
		if host != want.host || scheme != want.scheme {
			t.Errorf("GitHostScope(%q) = %q, %q, want %q, %q", input, host, scheme, want.host, want.scheme)
		}
	}
}

// A credential bound to an https host must never be handed to a plain HTTP
// request, so HTTPSHostScope keeps rejecting an http scheme.
func TestHTTPSHostScopeStillRejectsPlainHTTP(t *testing.T) {
	if _, err := HTTPSHostScope("http://gitlab.example"); err == nil {
		t.Fatal("HTTPSHostScope must reject an http scheme")
	}
	if host, err := HTTPSHostScope("https://gitlab.example"); err != nil || host != "gitlab.example" {
		t.Fatalf("%q %v", host, err)
	}
}

func TestGitRemoteHostAcceptsBothSchemes(t *testing.T) {
	for remote, want := range map[string]string{
		"https://gitlab.example/group/repo":  "gitlab.example",
		"http://gitlab.example:8929/g/r":     "gitlab.example:8929",
		"HTTP://GitLab.Example/group/repo":   "gitlab.example",
		"ssh://git@gitlab.example/group/repo": "",
		"git@gitlab.example:group/repo":      "",
		"":                                    "",
	} {
		got, ok := GitRemoteHost(remote)
		if want == "" {
			if ok {
				t.Errorf("GitRemoteHost(%q) = %q, want no host", remote, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("GitRemoteHost(%q) = %q, %v, want %q", remote, got, ok, want)
		}
	}
}
