package gitwork

import (
	"context"
	"strings"
	"testing"
)

// A credential answers only for the transport it was registered for. A token
// bound to an https host is never handed out over plain HTTP, and the other
// way round.
func TestHTTPCredentialAnswersOnlyItsOwnScheme(t *testing.T) {
	cases := []struct {
		name        string
		scheme      string
		remote      string
		protocol    string
		wantHandout bool
	}{
		{"https credential serves https", "https", "https://gitlab.example/group/repo", "https", true},
		{"https credential stays off http", "https", "http://gitlab.example/group/repo", "http", false},
		{"http credential serves http", "http", "http://gitlab.example:8929/group/repo", "http", true},
		{"http credential stays off https", "http", "https://gitlab.example/group/repo", "https", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithHTTPRemote(WithHTTPCredentials(WithToken(context.Background(), "legacy-must-not-answer"), map[string]HTTPCredential{
				"gitlab.example:8929": {Username: "gitlab", Token: "fake-token", Scheme: tc.scheme},
				"gitlab.example":       {Username: "gitlab", Token: "fake-token", Scheme: tc.scheme},
			}), tc.remote)
			cmd := command(ctx, t.TempDir(), nil, []string{"credential", "fill"})
			cmd.Stdin = strings.NewReader("protocol=" + tc.protocol + "\nhost=" + hostOf(tc.remote) + "\n\n")
			out, err := cmd.CombinedOutput()
			handed := err == nil && strings.Contains(string(out), "password=")
			if handed != tc.wantHandout {
				t.Fatalf("handed out = %v, want %v (%v)", handed, tc.wantHandout, err)
			}
			if strings.Contains(string(out), "fake-token") && !tc.wantHandout {
				t.Fatal("the token was handed out for the wrong scheme")
			}
		})
	}
}

func hostOf(remote string) string {
	rest := remote
	for _, prefix := range []string{"https://", "http://"} {
		rest = strings.TrimPrefix(rest, prefix)
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// The credential helper is told which scheme the credential was registered
// for, so it never answers a request on the other transport.
func TestScopedCredentialHelperIsBoundToOneScheme(t *testing.T) {
	helper := scopedCredentialHelper()
	if !strings.Contains(helper, gitHTTPSchemeEnv) {
		t.Fatalf("the helper must read the scheme: %s", helper)
	}
	if !strings.Contains(helper, "test \"$protocol\" = \"$scheme\"") {
		t.Fatalf("the helper must compare the protocol with the scheme: %s", helper)
	}
}
