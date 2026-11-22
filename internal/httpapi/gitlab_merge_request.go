package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// GitLab merge requests for the workspace delivery path.
//
// The tracker package owns the GitLab client used by the delivery pipeline, but
// it merges as soon as an MR exists and its client is private to that package.
// The workspace tool only opens an MR and reports its URL, so it needs its own
// small client here rather than a dependency on the delivery pipeline.

// gitLabMRClient talks to one GitLab instance about one project.
type gitLabMRClient struct {
	// base is the normalized instance origin, for example
	// http://127.0.0.1:8929 (no trailing slash, no /api/v4 suffix).
	base string
	// project is the URL-encoded full namespace path used in /projects/{id}.
	project string
	// identity is the stored "gitlab:<base>:<path>" form, kept so the MR URL
	// can be built from the same address the repository was bound with.
	identity string
	token     string
	http      *http.Client
}

// gitLabMR is the subset of GitLab's merge request payload this path needs.
type gitLabMR struct {
	ID              int64  `json:"id"`
	IID             int64  `json:"iid"`
	State           string `json:"state"`
	Title           string `json:"title"`
	SourceBranch    string `json:"source_branch"`
	TargetBranch    string `json:"target_branch"`
	SourceProjectID int64  `json:"source_project_id"`
	WebURL          string `json:"web_url"`
}

// newGitLabMRClient builds a client for a stored GitLab repository identity.
//
// The token must belong to the same instance the identity names: a GitLab
// personal access token is only valid on the instance that issued it, so
// sending it to another host would leak the credential. A mismatch is refused
// rather than used.
func newGitLabMRClient(identity, baseURL, token string) (*gitLabMRClient, error) {
	base, project, ok := domain.SplitGitLabProjectID(identity)
	if !ok {
		return nil, fmt.Errorf("仓库标识不是有效的 GitLab 项目地址")
	}
	configured := domain.NormalizeGitLabBaseURL(baseURL)
	if configured == "" {
		return nil, fmt.Errorf("GitLab 实例地址未配置：请先在个人配置中填写 GitLab 实例 URL")
	}
	if !strings.EqualFold(configured, base) {
		return nil, fmt.Errorf("仓库所属 GitLab 实例（%s）与已配置的实例（%s）不一致", base, configured)
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("GitLab 令牌未配置")
	}
	return &gitLabMRClient{
		base:     configured,
		project:  url.PathEscape(project),
		identity: strings.TrimSpace(identity),
		token:    strings.TrimSpace(token),
		http: &http.Client{
			Timeout: 20 * time.Second,
			// A redirect to another host would carry the PRIVATE-TOKEN header
			// along, so redirects are not followed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (c *gitLabMRClient) apiRoot() string { return c.base + "/api/v4" }

// request performs one API call. output receives the decoded JSON body when it
// is non-nil.
//
// Errors never include the token: GitLab echoes request headers in some
// validation errors, and a raw body could contain it.
func (c *gitLabMRClient) request(ctx context.Context, method, path string, query url.Values, input, output any) error {
	target := c.apiRoot() + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("构建请求失败")
		}
		body = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return fmt.Errorf("构建请求失败")
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", applicationJSON)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("无法连接 GitLab 实例：%v", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		if output == nil {
			return nil
		}
		if err := decodeLimitedJSON(res.Body, output); err != nil {
			return fmt.Errorf("GitLab 返回了无法识别的内容")
		}
		return nil
	}
	return gitLabMRError(res.StatusCode, res.Body)
}

// applicationJSON is the content type of every JSON body this client sends.
const applicationJSON = "application/json"

// gitLabMRError turns a non-2xx response into an actionable message. The body
// is read with a bound so a broken instance cannot make the control plane
// allocate without limit.
func gitLabMRError(status int, body io.Reader) error {
	raw, _ := io.ReadAll(io.LimitReader(body, 1<<20))
	var payload struct {
		Message any `json:"message"`
		Error   any `json:"error"`
	}
	_ = json.Unmarshal(raw, &payload)
	detail := strings.TrimSpace(fmt.Sprint(firstNonNil(payload.Message, payload.Error, "")))
	if detail == "" {
		detail = strings.TrimSpace(truncateRunes(string(raw), 300))
	}
	switch status {
	case http.StatusUnauthorized:
		return fmt.Errorf("GitLab 拒绝了该令牌（HTTP 401）：令牌无效、已过期或已被吊销，请到「个人配置 → GitLab」重新配置")
	case http.StatusForbidden:
		return fmt.Errorf("GitLab 拒绝发起 MR（HTTP 403）：令牌需要目标仓库的写权限，且你需要对该仓库有开发者及以上权限。%s", detail)
	case http.StatusNotFound:
		return fmt.Errorf("GitLab 找不到仓库或分支（HTTP 404）：%s", detail)
	case http.StatusConflict:
		return fmt.Errorf("GitLab 拒绝了这次 MR 创建（HTTP 409）：同源分支的 MR 可能已存在。%s", detail)
	}
	return fmt.Errorf("GitLab 发起 MR 失败（HTTP %d）：%s", status, detail)
}

// gitLabProjectID reads the numeric project id of the addressed project.
//
// The id matters for the fork check: an MR opened from a fork carries the
// fork's source_project_id, and treating that as this project's MR would
// report someone else's branch as the delivery.
func (c *gitLabMRClient) gitLabProjectID(ctx context.Context) (int64, error) {
	var payload struct {
		ID int64 `json:"id"`
	}
	if err := c.request(ctx, http.MethodGet, "/projects/"+c.project, nil, nil, &payload); err != nil {
		return 0, err
	}
	if payload.ID <= 0 {
		return 0, fmt.Errorf("GitLab 没有返回项目编号")
	}
	return payload.ID, nil
}

// listMergeRequests queries the project's MRs for one branch pair.
func (c *gitLabMRClient) listMergeRequests(ctx context.Context, sourceBranch, targetBranch string) ([]gitLabMR, error) {
	query := url.Values{
		"per_page":      {"100"},
		"state":         {"all"},
		"order_by":      {"updated_at"},
		"sort":          {"desc"},
		"scope":         {"all"},
		"source_branch": {sourceBranch},
		"target_branch": {targetBranch},
	}
	var batch []gitLabMR
	if err := c.request(ctx, http.MethodGet, "/projects/"+c.project+"/merge_requests", query, nil, &batch); err != nil {
		return nil, err
	}
	return batch, nil
}

// findMergeRequest returns the open MR for the branch pair, if there is one.
//
// Only an MR opened from this very project counts: a fork can carry the same
// branch name, and its source_project_id differs.
func (c *gitLabMRClient) findMergeRequest(ctx context.Context, projectID int64, sourceBranch, targetBranch string) (gitLabMR, bool, error) {
	items, err := c.listMergeRequests(ctx, sourceBranch, targetBranch)
	if err != nil {
		return gitLabMR{}, false, err
	}
	var best gitLabMR
	found := false
	for _, mr := range items {
		if mr.IID <= 0 {
			continue
		}
		if !strings.EqualFold(mr.State, "opened") {
			continue
		}
		if sourceBranch != "" && !domain.SameGitBranch(mr.SourceBranch, sourceBranch) {
			continue
		}
		if targetBranch != "" && !domain.SameGitBranch(mr.TargetBranch, targetBranch) {
			continue
		}
		if projectID > 0 && mr.SourceProjectID != projectID {
			continue
		}
		if !found || mr.IID > best.IID {
			best, found = mr, true
		}
	}
	return best, found, nil
}

// createMergeRequest opens an MR from source to target.
func (c *gitLabMRClient) createMergeRequest(ctx context.Context, source, target, title, description string) (gitLabMR, error) {
	input := map[string]any{
		"source_branch": source,
		"target_branch": target,
		"title":         title,
		// The work branch is the delivery, so it is kept for review.
		"remove_source_branch": false,
	}
	if strings.TrimSpace(description) != "" {
		input["description"] = description
	}
	var mr gitLabMR
	err := c.request(ctx, http.MethodPost, "/projects/"+c.project+"/merge_requests", nil, input, &mr)
	return mr, err
}

// updateMergeRequest edits an MR's title and description.
//
// GitLab addresses the MR by its iid, the project-scoped number shown in the
// web URL. The project-wide id is not accepted here.
func (c *gitLabMRClient) updateMergeRequest(ctx context.Context, iid int64, title, description string) error {
	input := map[string]any{"title": title}
	if strings.TrimSpace(description) != "" {
		input["description"] = description
	}
	return c.request(ctx, http.MethodPut, fmt.Sprintf("/projects/%s/merge_requests/%d", c.project, iid), nil, input, nil)
}

// gitLabMRResult is what the workspace delivery reports for a GitLab MR.
type gitLabMRResult struct {
	MRURL       string
	MRIID       int64
	Existing    bool
	Updated     bool
	UpdateError string
}

// ensureGitLabMergeRequest opens the MR for a branch pair, or brings an
// existing one up to date.
//
// GitLab answers 409 when an MR for the same branch pair already exists, so
// the lookup runs first and the create is a fallback.
func ensureGitLabMergeRequest(ctx context.Context, client *gitLabMRClient, source, target, title, description string) (gitLabMRResult, error) {
	projectID, err := client.gitLabProjectID(ctx)
	if err != nil {
		return gitLabMRResult{}, err
	}
	if mr, found, err := client.findMergeRequest(ctx, projectID, source, target); err == nil && found {
		result := gitLabMRResult{MRURL: client.mergeRequestURL(mr), MRIID: mr.IID, Existing: true}
		if err := client.updateMergeRequest(ctx, mr.IID, title, description); err != nil {
			result.UpdateError = err.Error()
			return result, nil
		}
		result.Updated = true
		return result, nil
	}
	mr, err := client.createMergeRequest(ctx, source, target, title, description)
	if err != nil {
		// The lookup may have raced with another submission. A second lookup
		// settles it before the failure is reported.
		if mr, found, findErr := client.findMergeRequest(ctx, projectID, source, target); findErr == nil && found {
			return gitLabMRResult{MRURL: client.mergeRequestURL(mr), MRIID: mr.IID, Existing: true}, nil
		}
		return gitLabMRResult{}, err
	}
	return gitLabMRResult{MRURL: client.mergeRequestURL(mr), MRIID: mr.IID}, nil
}

// mergeRequestURL is the browsable address of a merge request.
func (c *gitLabMRClient) mergeRequestURL(mr gitLabMR) string {
	if url := strings.TrimSpace(mr.WebURL); url != "" {
		return url
	}
	return domain.GitLabMRURL(c.identity, int(mr.IID))
}
