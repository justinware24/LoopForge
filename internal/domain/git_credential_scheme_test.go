package domain

import (
	"errors"
	"testing"
)

// The control plane reaches the API on the transport the credential was
// registered for. A self-hosted GitLab commonly serves its API over plain
// HTTP on a port of its own.
func TestGitHTTPCredentialAPIBaseFollowsTheScheme(t *testing.T) {
	cases := []struct {
		name string
		cred GitHTTPCredential
		want string
	}{
		{"gitlab over https", GitHTTPCredential{Platform: GitPlatformGitLab, Host: "gitlab.example"}, "https://gitlab.example/api/v4"},
		{"gitlab over http", GitHTTPCredential{Platform: GitPlatformGitLab, Host: "gitlab.example:8929", Scheme: "http"}, "http://gitlab.example:8929/api/v4"},
		{"gitlab over an explicit http", GitHTTPCredential{Platform: GitPlatformGitLab, Host: "gitlab.example", Scheme: "HTTP"}, "http://gitlab.example/api/v4"},
		{"github stays on https", GitHTTPCredential{Platform: GitPlatformGitHub, Host: "github.com"}, "https://api.github.com"},
		{"github enterprise over http", GitHTTPCredential{Platform: GitPlatformGitHub, Host: "ghe.example", Scheme: "http"}, "http://ghe.example/api/v3"},
		{"an unknown platform has no api", GitHTTPCredential{Platform: GitPlatformOther, Host: "gitlab.example", Scheme: "http"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cred.APIBase(); got != tc.want {
				t.Fatalf("APIBase() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A credential that names no scheme keeps the https one it always had.
func TestGitHTTPCredentialSchemeDefaultsToHTTPS(t *testing.T) {
	cred := GitHTTPCredential{Platform: GitPlatformGitLab, Host: "gitlab.example"}
	if got := cred.GitScheme(); got != "https" {
		t.Fatalf("GitScheme() = %q, want https", got)
	}
	for _, declared := range []string{"", "  ", "HTTPS", "https", "unknown"} {
		cred.Scheme = declared
		if got := cred.GitScheme(); got != "https" {
			t.Fatalf("GitScheme() for %q = %q, want https", declared, got)
		}
	}
	cred.Scheme = "http"
	if got := cred.GitScheme(); got != "http" {
		t.Fatalf("GitScheme() = %q, want http", got)
	}
}

// Plain HTTP is for a self-hosted instance the user registered on purpose.
// The public hosts the platform knows serve TLS only, so a credential for one
// of them stays on https no matter what the form asked for.
func TestGitHTTPCredentialValidateGitScheme(t *testing.T) {
	if err := (GitHTTPCredential{Platform: GitPlatformGitLab, Host: "gitlab.example:8929", Scheme: "http"}).ValidateGitScheme(); err != nil {
		t.Fatalf("a self-hosted instance may use plain HTTP: %v", err)
	}
	if err := (GitHTTPCredential{Platform: GitPlatformGitLab, Host: "gitlab.example"}).ValidateGitScheme(); err != nil {
		t.Fatalf("https is always allowed: %v", err)
	}
	for _, host := range []string{"github.com", "GitHub.com", "gitlab.com"} {
		err := (GitHTTPCredential{Platform: GitPlatformGitLab, Host: host, Scheme: "http"}).ValidateGitScheme()
		if !errors.Is(err, ErrGitHTTPSchemeNotAllowed) {
			t.Fatalf("%s must not accept plain HTTP: %v", host, err)
		}
	}
}
