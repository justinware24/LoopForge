package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"agentflow/internal/domain"
)

// verifyGitLabToken checks the caller's GitLab personal access token against the
// instance it is configured for.
//
// Two rules make this different from the GitHub verifier:
//
//   - The token is sent only to the instance origin taken from the request or
//     the stored setting. A token must never travel to a host it was not issued
//     for, so an empty instance is an error rather than a default.
//   - Verification checks identity and repository scopes only. It deliberately
//     does not claim the token can write a specific repository: that depends on
//     the project's member permissions, which GitLab reports per project
//     (review finding in PR #62).
//
// It always answers 200 with a structured result so the settings form can show
// the outcome without treating a bad token as a transport failure.
func (h *usersHandlers) verifyGitLabToken(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gitTokenVerifyResult{Error: "valid user id is required"})
		return
	}
	current, ok := h.requestUser(c.Request, false)
	if !ok || current.ID != id {
		c.JSON(http.StatusForbidden, gitTokenVerifyResult{Error: "只能校验当前登录用户的凭据"})
		return
	}

	var body struct {
		Token   string `json:"token"`
		BaseURL string `json:"baseUrl"`
	}
	if c.Request.Method == http.MethodPost {
		if !decode(c, &body) {
			return
		}
	}

	token := strings.TrimSpace(body.Token)
	baseURL := strings.TrimSpace(body.BaseURL)
	if token == "" || baseURL == "" {
		// Fall back to what is stored, so the row's "re-check" button works
		// without re-pasting the token.
		if h.repo == nil {
			c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "存储不可用"})
			return
		}
		cred := h.repo.GitLabCredentialFor(id)
		if token == "" {
			token = cred.Token
		}
		if baseURL == "" {
			baseURL = cred.BaseURL
		}
	}
	if token == "" {
		c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "尚未配置 GitLab 访问令牌，请先在个人配置中填写"})
		return
	}
	if baseURL == "" {
		// A token with no instance cannot be scoped; ask for it explicitly
		// instead of guessing gitlab.com, which could send the token elsewhere.
		c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "请先填写 GitLab 实例地址（GitLab.com 填 https://gitlab.com）"})
		return
	}
	normalized := domain.NormalizeGitLabBaseURL(baseURL)
	if normalized == "" {
		c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "GitLab 实例地址无效：必须是 http(s)://主机[:端口][/子路径]，且不能包含凭据"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	c.JSON(http.StatusOK, verifyGitLabAccessToken(ctx, normalized, token))
}

// decodeLimitedJSON decodes at most 1 MiB of a GitLab response. A hostile or
// broken instance cannot make the control plane allocate without bound.
func decodeLimitedJSON(r io.Reader, v any) error {
	return json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(v)
}

// verifyGitLabAccessToken calls GET /user and returns the identity plus the
// token's scopes.
func verifyGitLabAccessToken(ctx context.Context, baseURL, token string) gitTokenVerifyResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v4/user", nil)
	if err != nil {
		return gitTokenVerifyResult{Error: "构建校验请求失败"}
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("Accept", "application/json")
	// A redirect would carry the token to whatever host it points at.
	client := &http.Client{
		Timeout:       12 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := client.Do(req)
	if err != nil {
		return gitTokenVerifyResult{Error: "无法连接 GitLab 实例，请检查实例地址与网络"}
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return gitTokenVerifyResult{Error: "GitLab 拒绝了该令牌（HTTP 401）：令牌无效、已过期或已被吊销"}
	}
	if res.StatusCode != http.StatusOK {
		return gitTokenVerifyResult{Error: fmt.Sprintf("GitLab 返回 HTTP %d，请检查实例地址是否正确", res.StatusCode)}
	}
	var payload struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	}
	if err := decodeLimitedJSON(res.Body, &payload); err != nil || strings.TrimSpace(payload.Username) == "" {
		return gitTokenVerifyResult{Error: "GitLab 返回了无法识别的内容，请确认该地址是 GitLab 实例"}
	}

	// Scopes come from a second header-based call; GitLab exposes them on any
	// authenticated endpoint. A missing scope list is not a failure: older
	// versions and some proxies omit it, so an empty list means "unknown".
	scopes, missing := gitLabTokenScopes(ctx, baseURL, token)
	return gitTokenVerifyResult{
		Valid:   true,
		Login:   payload.Username,
		Scopes:  scopes,
		Missing: missing,
	}
}

// gitLabTokenScopes reports the token's scopes and which required ones are
// absent. GitLab returns them in the PRIVATE-TOKEN-SCOPES header of a 2xx
// response.
func gitLabTokenScopes(ctx context.Context, baseURL, token string) (scopes []string, missing []string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v4/personal_access_tokens/self", nil)
	if err != nil {
		return nil, nil
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	client := &http.Client{
		Timeout:       12 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, nil
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		// Self-introspection needs GitLab 15.5+; without it the token may still
		// be perfectly usable, so this is not an error.
		return nil, nil
	}
	var payload struct {
		Scopes []string `json:"scopes"`
	}
	if err := decodeLimitedJSON(res.Body, &payload); err != nil {
		return nil, nil
	}
	scopes = payload.Scopes
	have := map[string]bool{}
	for _, scope := range scopes {
		have[strings.ToLower(strings.TrimSpace(scope))] = true
	}
	// `api` covers repository read and write, so it satisfies both.
	if have["api"] {
		return scopes, nil
	}
	for _, required := range []string{"read_repository", "write_repository"} {
		if !have[required] {
			missing = append(missing, required)
		}
	}
	return scopes, missing
}
