package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func TestAssistantEngineModelsKeepPersonalCatalogOnOfficialKey(t *testing.T) {
	previous := assistantEngineDiscover
	t.Cleanup(func() { assistantEngineDiscover = previous })
	var discovered []string
	assistantEngineDiscover = func(_ context.Context, _ bool, _, baseURL string) ([]assistantDiscoveredModel, error) {
		discovered = append(discovered, baseURL)
		return []assistantDiscoveredModel{{ID: "remote-model", Name: "Remote"}}, nil
	}
	ids := func(user domain.User) []string {
		var out []string
		for _, model := range assistantEngineModels(context.Background(), user) {
			out = append(out, model.ID)
		}
		return out
	}

	official := domain.User{CodexAPIKey: "codex-key", CodexModels: []domain.CLIModel{{ID: "gpt-5.5", Name: "GPT-5.5"}}}
	if got := ids(official); !slices.Equal(got, []string{"codex:gpt-5.5"}) || len(discovered) != 0 {
		t.Fatalf("official catalog: %v discovered %v", got, discovered)
	}

	builtin := ids(domain.User{CodexAPIKey: "codex-key", ClaudeCodeAPIKey: "claude-key"})
	if !slices.Contains(builtin, "codex:gpt-6.1-terra") || !slices.Contains(builtin, "claude-code:claude-haiku-5-5") {
		t.Fatalf("builtin catalog: %v", builtin)
	}
	if len(discovered) != 0 {
		t.Fatalf("official endpoint discovered models: %v", discovered)
	}

	gemini := domain.User{AgentCLICredentials: map[string]domain.AgentCLICredential{
		runtime.AgentCLIAntigravity: {APIKey: "gemini-key", BaseURL: "https://generativelanguage.googleapis.com", Protocol: domain.AgentCLIProtocolGemini, Models: []domain.CLIModel{{ID: "gemini-custom"}}},
	}}
	if got := ids(gemini); !slices.Equal(got, []string{"agy:gemini-custom"}) || len(discovered) != 0 {
		t.Fatalf("gemini catalog: %v discovered %v", got, discovered)
	}

	remote := domain.User{CodexAPIKey: "codex-key", CodexBaseURL: "https://catalog-test.example/v1"}
	if got := ids(remote); !slices.Equal(got, []string{"codex:remote-model"}) || len(discovered) != 1 {
		t.Fatalf("base url catalog: %v discovered %v", got, discovered)
	}
}

// A gateway that publishes its catalog on the host root (Tencent TokenHub
// does) must still be found when the Base URL points at a sub-path.
func TestAssistantEngineDiscoveryFallsBackToHostRootCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer gateway-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []map[string]any{
			{"id": "deepseek-v4.1-flash-1017", "name": "DeepSeek-V4.1-Flash-1017"},
			{"id": "kimi-k3.1-pro", "name": "Kimi-K3.1-Pro"},
		}})
	}))
	defer server.Close()

	models, err := discoverAssistantEngineModels(context.Background(), false, "gateway-key", server.URL+"/plan/v3")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "deepseek-v4.1-flash-1017" {
		t.Fatalf("discovered models: %+v", models)
	}
}

// The Base URL's own path is preferred; the host root is only a fallback.
func TestAssistantEngineDiscoveryPrefersBaseURLPath(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "m1"}}})
	}))
	defer server.Close()

	if _, err := discoverAssistantEngineModels(context.Background(), false, "k", server.URL+"/v1"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(paths, []string{"/v1/models"}) {
		t.Fatalf("probed paths: %v", paths)
	}
}

// A gateway that publishes image, video and 3D models must not offer them as
// chat models.
func TestAssistantEngineDiscoveryDropsMediaModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"id": "deepseek-v4.1-flash-1017"},
			{"id": "hy-image-v3.5-preview"},
			{"id": "kling-video-v4"},
			{"id": "tripo-3d-3.1"},
			{"id": "minimax-music-v3.0"},
			{"id": "wand-asr-v1"},
			{"id": "hi3d-2.1"},
			{"id": "kinfra-text-embedding-4b"},
		}})
	}))
	defer server.Close()

	models, err := discoverAssistantEngineModels(context.Background(), false, "k", server.URL+"/v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "deepseek-v4.1-flash-1017" {
		t.Fatalf("discovered models: %+v", models)
	}
}

// A rejected key must be reported as such, not as a missing catalog.
func TestAssistantEngineDiscoveryReportsRejectedKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"authentication_error"}}`))
	}))
	defer server.Close()

	_, err := discoverAssistantEngineModels(context.Background(), false, "bad-key", server.URL+"/v1")
	if err == nil || !assistantAuthFailureText(err.Error()) {
		t.Fatalf("error = %v", err)
	}
}

// The domestic models behind a Chinese OpenAI-compatible gateway must be
// selectable on Codex without a custom catalog.
func TestAssistantCodexCatalogListsDomesticModels(t *testing.T) {
	catalog := executorModelCatalogs()["codex"]
	ids := make([]string, 0, len(catalog.Models))
	for _, model := range catalog.Models {
		ids = append(ids, model.ID)
	}
	if !slices.Contains(ids, "deepseek-v4.1-flash-1017") {
		t.Fatalf("codex catalog: %v", ids)
	}
}

// A model that runs on another CLI keeps its own effort capability.
func TestAssistantEffortFollowsModelBehindEnginePrefix(t *testing.T) {
	if got := assistantModelEfforts("codex:deepseek-v4.1-flash-1017"); !slices.Equal(got, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("efforts = %v", got)
	}
	if got := assistantModelEfforts("deepseek-v4.1-flash-1017"); !slices.Equal(got, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("efforts = %v", got)
	}
	if err := validateAssistantEffort("codex:deepseek-v4.1-flash-1017", "max"); err != nil {
		t.Fatal(err)
	}
}
