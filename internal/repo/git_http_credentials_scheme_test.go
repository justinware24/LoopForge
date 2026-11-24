package repo

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"agentflow/internal/domain"
)

// An https credential keeps the identity it has always had, so a credential
// created before the scheme became configurable stays addressable.
func TestGitHTTPCredentialIDIsStableForHTTPS(t *testing.T) {
	legacy := fmt.Sprintf("%x", sha256.Sum256([]byte("gitlab.example")))
	if got := gitHTTPCredentialID("gitlab.example", ""); got != legacy {
		t.Fatalf("id = %q, want %q", got, legacy)
	}
	if got := gitHTTPCredentialID("gitlab.example", "https"); got != legacy {
		t.Fatalf("id = %q, want %q", got, legacy)
	}
}

// An http credential is a different credential on the same host.
func TestGitHTTPCredentialIDSeparatesTheSchemes(t *testing.T) {
	https := gitHTTPCredentialID("gitlab.example", "https")
	http := gitHTTPCredentialID("gitlab.example", "http")
	if https == http {
		t.Fatal("an http credential must not share the identity of an https one")
	}
}

// The scheme of the host the user wrote decides the scheme of the
// credential, and an explicit scheme on the credential wins over it.
func TestSaveGitHTTPCredentialKeepsTheScheme(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	users := s.UsersRepo
	userID := int64(1)

	for _, tc := range []struct {
		name       string
		host       string
		scheme     string
		wantScheme string
	}{
		{"a bare host is https", "gitlab.example", "", "https"},
		{"an https host stays https", "https://gitlab.example", "", "https"},
		{"an http host is http", "http://gitlab.example:8929", "", "http"},
		{"an explicit scheme wins", "http://gitlab.example:8929", "https", "https"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := domain.GitHTTPCredentialSecret{
				GitHTTPCredential: domain.GitHTTPCredential{
					Name: tc.name, Platform: domain.GitPlatformGitLab, Host: tc.host,
					Scheme: tc.scheme, Username: "oauth2",
				},
				Token: "fake-token",
			}
			if err := users.SaveGitHTTPCredential(userID, "", input); err != nil {
				t.Fatal(err)
			}
			listed, err := users.GitHTTPCredentials(userID)
			if err != nil {
				t.Fatal(err)
			}
			var found *domain.GitHTTPCredential
			for i := range listed {
				if listed[i].Host == "gitlab.example" || listed[i].Host == "gitlab.example:8929" {
					found = &listed[i]
				}
			}
			if found == nil {
				t.Fatalf("the credential was not stored: %+v", listed)
			}
			if found.Scheme != tc.wantScheme {
				t.Fatalf("scheme = %q, want %q", found.Scheme, tc.wantScheme)
			}
			// Clean up so the next case starts from a clean table.
			if err := users.DeleteGitHTTPCredential(userID, found.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A stored MR names a host without its scheme, so a host the user only
// registered over plain HTTP still finds its credential.
func TestGitHTTPCredentialForHostFallsBackToHTTP(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	users := s.UsersRepo
	userID := int64(1)

	input := domain.GitHTTPCredentialSecret{
		GitHTTPCredential: domain.GitHTTPCredential{
			Name: "self hosted", Platform: domain.GitPlatformGitLab,
			Host: "http://gitlab.example:8929", Username: "oauth2",
		},
		Token: "fake-token",
	}
	if err := users.SaveGitHTTPCredential(userID, "", input); err != nil {
		t.Fatal(err)
	}
	credential, ok := users.GitHTTPCredentialForHost(context.Background(), userID, "gitlab.example:8929")
	if !ok || credential.Token != "fake-token" {
		t.Fatalf("a bare host must find the http credential: %+v", credential)
	}
	if credential.Scheme != "http" {
		t.Fatalf("scheme = %q, want http", credential.Scheme)
	}
}

// A host that carries a scheme never falls back to the other transport.
func TestGitHTTPCredentialForHostDoesNotFallBack(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	users := s.UsersRepo
	userID := int64(1)

	input := domain.GitHTTPCredentialSecret{
		GitHTTPCredential: domain.GitHTTPCredential{
			Name: "self hosted", Platform: domain.GitPlatformGitLab,
			Host: "http://gitlab.example:8929", Username: "oauth2",
		},
		Token: "fake-token",
	}
	if err := users.SaveGitHTTPCredential(userID, "", input); err != nil {
		t.Fatal(err)
	}
	if _, ok := users.GitHTTPCredentialForHost(context.Background(), userID, "https://gitlab.example:8929"); ok {
		t.Fatal("an https request must not borrow the http credential")
	}
}
