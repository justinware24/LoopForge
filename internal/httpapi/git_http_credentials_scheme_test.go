package httpapi

import "testing"

func TestGitHTTPCredentialScheme(t *testing.T) {
	cases := []struct {
		name        string
		hostScheme  string
		declared    string
		want        string
		wantErr     bool
	}{
		{"the host scheme is the default", "https", "", "https", false},
		{"an http host stays http", "http", "", "http", false},
		{"an explicit https wins", "http", "https", "https", false},
		{"an explicit http wins", "https", "http", "http", false},
		{"the declared scheme is trimmed", "https", "  HTTP  ", "http", false},
		{"another scheme is rejected", "https", "ftp", "", true},
		{"a nonsense scheme is rejected", "https", "file", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := gitHTTPCredentialScheme(tc.hostScheme, tc.declared)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("scheme = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("scheme = %q, want %q", got, tc.want)
			}
		})
	}
}
