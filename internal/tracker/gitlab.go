package tracker

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// GitLab adapter (issue #128, follows the scope of PR #62).
//
// Differences from the GitHub adapter that matter here:
//
//   - The REST API lives under /api/v4 and the project is addressed by its
//     URL-encoded full path, so self-managed instances and nested groups both
//     work with one code path.
//   - Merge requests are addressed by IID (the per-project !N counter), not by
//     the global id.
//   - A token is only ever sent to the instance it was configured for, which is
//     why every entry point takes a project identity that embeds its origin.
//
// Three review findings on the original PR are addressed structurally:
//
//	P1-1 no credential reaches a log: the token never enters an error string
//	     and every message is passed through the git token redactor.
//	P1-2 a branch match is not enough: MergeRequest.SourceProjectID must equal
//	     the project we are merging into, so a fork's same-named branch is
//	     never picked.
//	P1-3 a historical merged MR is not a delivery: when the reusable MR is
//	     already merged or closed, it is reopened with a fresh source branch
//	     or a new MR is created, so a later commit cannot be reported as
//	     already delivered.
const (
	// gitlabAPITimeout bounds one GitLab API call.
	gitlabAPITimeout = 30 * time.Second
	// gitlabMaxPages caps pagination so a misconfigured instance cannot make a
	// sync run forever.
	gitlabMaxPages = 100
	// gitlabPerPage is the largest page GitLab serves.
	gitlabPerPage = 100
	// gitLabMergeableWait bounds how long a merge waits for GitLab to finish
	// computing mergeability. GitLab checks conflicts asynchronously after an
	// MR is created, so an immediate merge answers 405 until it is done.
	gitLabMergeableWait = 30 * time.Second
	// gitLabMergeablePoll is the interval between mergeability checks.
	gitLabMergeablePoll = 500 * time.Millisecond
)

// errGitLabNoCredential reports a missing personal access token. Callers turn
// it into an actionable message naming where to configure it.
var errGitLabNoCredential = errors.New("GitLab 凭据未配置")

// gitLabTokenFixHint names the exact setting to fill in. A GitLab run fails
// with a bare "token missing" otherwise, which is not actionable.
const gitLabTokenFixHint = "请项目 Owner 到「个人配置 → GitLab」填写该实例的访问令牌（需 api、read_repository、write_repository 权限）并点校验"

// explainGitLabCredentialError turns the credential sentinels into operator
// guidance while leaving other errors untouched.
func explainGitLabCredentialError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errGitLabNoCredential) {
		return fmt.Errorf("%s。%s", errGitLabNoCredential.Error(), gitLabTokenFixHint)
	}
	return err
}

type gitlabClient struct {
	// base is the normalized instance origin, for example
	// https://gitlab.example.com (no trailing slash, no /api/v4 suffix).
	base string
	// project is the URL-encoded full namespace path used in /projects/{id}.
	project   string
	projectID string
	token     string
	http      *http.Client
}

// newGitLabClient builds a client for a stored GitLab repository identity. The
// token must belong to the same instance: an identity carrying one origin and a
// credential configured for another is rejected rather than used.
func newGitLabClient(identity, baseURL, token string) (*gitlabClient, error) {
	base, project, ok := domain.SplitGitLabProjectID(identity)
	if !ok {
		return nil, fmt.Errorf("invalid GitLab repository identity")
	}
	configured := domain.NormalizeGitLabBaseURL(baseURL)
	if configured == "" {
		return nil, fmt.Errorf("GitLab 实例地址未配置：请先在个人配置中填写 GitLab 实例 URL")
	}
	if !strings.EqualFold(configured, base) {
		// Sending a token to a different host than the one it was issued for
		// would leak the credential; refuse instead.
		return nil, fmt.Errorf("仓库所属 GitLab 实例（%s）与已配置的实例（%s）不一致", base, configured)
	}
	if strings.TrimSpace(token) == "" {
		return nil, errGitLabNoCredential
	}
	return &gitlabClient{
		base:      configured,
		project:   url.PathEscape(project),
		projectID: identity,
		token:     strings.TrimSpace(token),
		http: &http.Client{
			Timeout: gitlabAPITimeout,
			// A redirect to another host would carry the PRIVATE-TOKEN header
			// with it, so redirects are not followed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// apiRoot is the /api/v4 prefix of this instance.
func (g *gitlabClient) apiRoot() string { return g.base + "/api/v4" }

// request performs one API call. output receives the decoded JSON body when it
// is non-nil. Errors never include the token: GitLab echoes request headers in
// some validation errors, and a raw body could contain it.
func (g *gitlabClient) request(method, path string, query url.Values, input, output any) error {
	target := g.apiRoot() + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode GitLab request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		return fmt.Errorf("build GitLab request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", g.token)
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := g.http.Do(req)
	if err != nil {
		// The transport error can quote the URL but never the header we set.
		return fmt.Errorf("GitLab 请求失败：%s", redactGitLabError(err.Error()))
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		message := readGitLabErrorMessage(res.Body)
		return fmt.Errorf("GitLab 返回 HTTP %d：%s", res.StatusCode, message)
	}
	if output == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<20))
		return err
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(output); err != nil {
		return fmt.Errorf("解析 GitLab 响应失败：%w", err)
	}
	return nil
}

// readGitLabErrorMessage extracts GitLab's own error text ({"message": ...} or
// {"error": ...}) so the operator sees "404 Project Not Found" instead of an
// empty failure. The token is stripped in case the instance echoes it.
func readGitLabErrorMessage(body io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(body, 8<<10))
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "无响应内容"
	}
	var payload struct {
		Message any    `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(raw, &payload) == nil {
		switch {
		case payload.Error != "":
			text = payload.Error
		case payload.Message != nil:
			switch v := payload.Message.(type) {
			case string:
				text = v
			default:
				if encoded, err := json.Marshal(v); err == nil {
					text = string(encoded)
				}
			}
		}
	}
	text = redactGitLabError(text)
	if runes := []rune(text); len(runes) > 300 {
		text = string(runes[:300]) + "…"
	}
	return text
}

// redactGitLabError removes anything credential-shaped from a GitLab message.
//
// GitLab and the proxies in front of it echo request headers in some 4xx pages,
// so a raw body can contain the token that was just sent. Two passes are
// applied: known token formats are replaced wherever they appear, and any
// "PRIVATE-TOKEN=..." or "PRIVATE-TOKEN: ..." pairing is masked whole so an
// unrecognized token shape still cannot survive.
func redactGitLabError(s string) string {
	// Personal access tokens, project/group tokens and pipeline tokens all carry
	// the glpat-/glptt-/glcbt- prefixes; OAuth tokens are 64 hex characters.
	s = gitLabTokenPattern.ReplaceAllString(s, "***")
	// Header echoes: "PRIVATE-TOKEN=xxx", "PRIVATE-TOKEN: xxx".
	s = gitLabTokenHeaderPattern.ReplaceAllString(s, "$1***")
	return s
}

var (
	// glpat- / glptt- / glcbt- style tokens, 20+ characters.
	gitLabTokenPattern = regexp.MustCompile(`\bgl(?:pat|ptt|cbt|dt|ft|imt|rt|soat|agent)-[A-Za-z0-9_-]{16,}`)
	// "PRIVATE-TOKEN" followed by an optional separator and any non-space run.
	gitLabTokenHeaderPattern = regexp.MustCompile(`(?i)(PRIVATE-TOKEN\s*[:=]\s*)\S+`)
)

// gitlabUser is the subset of GET /user the platform needs to verify a token.
type gitlabUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	State    string `json:"state"`
}

// currentUser verifies the token and returns the identity behind it.
func (g *gitlabClient) currentUser() (gitlabUser, error) {
	var user gitlabUser
	if err := g.request(http.MethodGet, "/user", nil, nil, &user); err != nil {
		return gitlabUser{}, err
	}
	if user.Username == "" {
		return gitlabUser{}, fmt.Errorf("GitLab 未返回用户信息，令牌可能无效")
	}
	return user, nil
}

// gitlabProject is the subset of a project payload used for verification.
type gitlabProject struct {
	ID                int64  `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
	DefaultBranch     string `json:"default_branch"`
	WebURL            string `json:"web_url"`
	HTTPURLToRepo     string `json:"http_url_to_repo"`
	SSHURLToRepo      string `json:"ssh_url_to_repo"`
}

// getProject resolves the project by its full path. This doubles as a
// permission check: a token that cannot see the project fails here, before any
// write is attempted.
func (g *gitlabClient) getProject() (gitlabProject, error) {
	var project gitlabProject
	err := g.request(http.MethodGet, "/projects/"+g.project, nil, nil, &project)
	return project, err
}

// mergeRequest is the subset of a GitLab MR payload the platform uses. The
// SourceProjectID field is what makes finding an MR safe: a fork's branch with
// the same name lives on a different project id (review finding P1-2).
type mergeRequest struct {
	ID        int64  `json:"id"`
	IID       int    `json:"iid"`
	ProjectID int64  `json:"project_id"`
	Title     string `json:"title"`
	State     string `json:"state"`
	WebURL    string `json:"web_url"`
	MergedAt  string `json:"merged_at"`
	ClosedAt  string `json:"closed_at"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	SourceProjectID int64 `json:"source_project_id"`
	TargetProjectID int64 `json:"target_project_id"`
	SHA       string `json:"sha"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	// DetailedMergeStatus explains why an MR cannot merge yet
	// (checking, ci_must_pass, discussions_not_resolved, ...).
	DetailedMergeStatus string `json:"detailed_merge_status"`
	HasConflicts        bool   `json:"has_conflicts"`
}

// delivery converts an MR payload into the platform's delivery record.
func (m mergeRequest) delivery(identity string) domain.DeliveryMR {
	state := "opened"
	switch {
	case m.MergedAt != "":
		state = "merged"
	case strings.EqualFold(m.State, "merged"):
		state = "merged"
	case strings.EqualFold(m.State, "closed"):
		state = "closed"
	case strings.EqualFold(m.State, "locked"):
		state = "opened"
	}
	if state == "merged" && strings.TrimSpace(m.TargetBranch) == "" {
		// Without a target branch the merge cannot be attributed to the default
		// branch, so the caller must not claim 已合入.
		state = "unknown"
	}
	url := strings.TrimSpace(m.WebURL)
	if url == "" {
		url = domain.GitLabMRURL(identity, m.IID)
	}
	return domain.DeliveryMR{
		Project:       identity,
		IID:           m.IID,
		URL:           url,
		State:         state,
		TargetBranch:  m.TargetBranch,
		GongfengState: state,
	}
}

// mergedAt parses merged_at; zero until the MR is merged.
func (m mergeRequest) mergedAt() time.Time {
	if strings.TrimSpace(m.MergedAt) == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, m.MergedAt); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, m.MergedAt); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// listMergeRequests queries the project's MRs. state is GitLab's own filter
// ("opened", "merged", "closed", "all").
func (g *gitlabClient) listMergeRequests(sourceBranch, targetBranch, state string) ([]mergeRequest, error) {
	var out []mergeRequest
	for page := 1; page <= gitlabMaxPages; page++ {
		query := url.Values{
			"per_page":  {strconv.Itoa(gitlabPerPage)},
			"page":      {strconv.Itoa(page)},
			"state":     {state},
			"order_by":  {"updated_at"},
			"sort":      {"desc"},
			"scope":     {"all"},
		}
		if s := strings.TrimSpace(sourceBranch); s != "" {
			query.Set("source_branch", s)
		}
		if t := strings.TrimSpace(targetBranch); t != "" {
			query.Set("target_branch", t)
		}
		var batch []mergeRequest
		if err := g.request(http.MethodGet, "/projects/"+g.project+"/merge_requests", query, nil, &batch); err != nil {
			return out, err
		}
		out = append(out, batch...)
		if len(batch) < gitlabPerPage {
			break
		}
	}
	return out, nil
}

// getMergeRequest reads one MR by IID.
func (g *gitlabClient) getMergeRequest(iid int) (mergeRequest, error) {
	var mr mergeRequest
	err := g.request(http.MethodGet, fmt.Sprintf("/projects/%s/merge_requests/%d", g.project, iid), nil, nil, &mr)
	return mr, err
}

// pickOwnMergeRequest selects the authoritative MR for a source branch.
//
// An MR only counts when it was opened from the project we are delivering into:
// source_project_id must equal the project id GitLab reports for this path.
// Without that check a fork's identically named branch would be merged
// (review finding P1-2). Among the project's own MRs the newest IID wins, and a
// non-closed MR beats a closed one.
func pickOwnMergeRequest(items []mergeRequest, projectID int64, sourceBranch, targetBranch string) (mergeRequest, bool) {
	var best mergeRequest
	found, bestClosed := false, false
	for _, mr := range items {
		if mr.IID <= 0 {
			continue
		}
		if sourceBranch != "" && !domain.SameGitBranch(mr.SourceBranch, sourceBranch) {
			continue
		}
		if targetBranch != "" && !domain.SameGitBranch(mr.TargetBranch, targetBranch) {
			continue
		}
		// The MR must originate in this project. Zero means GitLab omitted the
		// field, which is treated as untrusted rather than assumed safe.
		if projectID <= 0 || mr.SourceProjectID != projectID {
			continue
		}
		closed := strings.EqualFold(mr.State, "closed") && mr.MergedAt == ""
		switch {
		case !found:
		case bestClosed && !closed:
		case bestClosed == closed && mr.IID > best.IID:
		default:
			continue
		}
		best, found, bestClosed = mr, true, closed
	}
	return best, found
}

// findOwnMergeRequest lists the project's MRs and picks the authoritative one.
func (g *gitlabClient) findOwnMergeRequest(projectID int64, sourceBranch, targetBranch string) (mergeRequest, bool, error) {
	items, err := g.listMergeRequests(sourceBranch, targetBranch, "all")
	if err != nil {
		return mergeRequest{}, false, err
	}
	mr, ok := pickOwnMergeRequest(items, projectID, sourceBranch, targetBranch)
	return mr, ok, nil
}

// createMergeRequest opens an MR from source to target.
func (g *gitlabClient) createMergeRequest(source, target, title, description string) (mergeRequest, error) {
	input := map[string]any{
		"source_branch": source,
		"target_branch": target,
		"title":         title,
	}
	if strings.TrimSpace(description) != "" {
		input["description"] = description
	}
	// A merge request that already exists for the branch pair answers 409; the
	// caller retries the lookup, so the error only matters when nothing is found.
	input["remove_source_branch"] = false
	var mr mergeRequest
	err := g.request(http.MethodPost, "/projects/"+g.project+"/merge_requests", nil, input, &mr)
	return mr, err
}

// mergeMergeRequest merges one MR. sha pins the merge to the commit that was
// reviewed: if the branch moved since the lookup, GitLab answers 409 instead of
// merging unreviewed commits. squash is left off so the work branch history is
// preserved, matching the GitHub adapter's merge-commit behaviour.
// mergeableStatuses are the detailed_merge_status values that allow the merge
// endpoint to proceed.
var mergeableStatuses = map[string]bool{
	"mergeable":    true,
	"not_approved": true, // approval rules are the instance's business, not ours
	"":             true, // older GitLab versions do not report the field
}

// waitMergeable polls the MR until GitLab finishes computing mergeability.
//
// A freshly created MR reports detailed_merge_status "preparing" (or
// "unchecked") for a short time while GitLab checks conflicts and CI. Calling
// merge during that window answers 405, which is not a failure: it is "try
// again". Without this wait every first delivery would report a spurious
// failure on a busy instance.
func (g *gitlabClient) waitMergeable(iid int) (mergeRequest, error) {
	deadline := time.Now().Add(gitLabMergeableWait)
	for {
		mr, err := g.getMergeRequest(iid)
		if err != nil {
			return mr, err
		}
		status := strings.ToLower(strings.TrimSpace(mr.DetailedMergeStatus))
		if mergeableStatuses[status] {
			return mr, nil
		}
		switch status {
		case "preparing", "unchecked", "checking", "approvals_syncing", "ci_still_running":
			// Still being computed: wait and look again.
		case "discussions_not_resolved", "draft_status", "ci_must_pass",
			"not_mergeable", "conflict", "need_rebase", "broken_status",
			"external_status_checks", "status_checks_must_pass", "blocked_status":
			return mr, fmt.Errorf("GitLab MR !%d 当前不可合入（%s）", iid, status)
		default:
			return mr, fmt.Errorf("GitLab MR !%d 当前不可合入（%s）", iid, status)
		}
		if time.Now().After(deadline) {
			return mr, fmt.Errorf("等待 GitLab MR !%d 可合并状态超时（最后状态：%s）", iid, status)
		}
		time.Sleep(gitLabMergeablePoll)
	}
}

// mergeMergeRequest merges one MR. sha pins the merge to the commit that was
// reviewed: if the branch moved since the lookup, GitLab answers 409 instead of
// merging unreviewed commits. squash is left off so the work branch history is
// preserved, matching the GitHub adapter's merge-commit behaviour.
func (g *gitlabClient) mergeMergeRequest(iid int, expectedSHA, message string) (mergeRequest, error) {
	// Wait for GitLab to finish its own mergeability computation first; a merge
	// attempted while it is still "preparing" answers 405.
	if _, err := g.waitMergeable(iid); err != nil {
		return mergeRequest{}, err
	}
	// should_remove_source_branch stays false: the work branch is reused by
	// later nodes and by the delivery MR lookup.
	//
	// Only keys with a real value are sent. GitLab rejects an explicitly null
	// merge_commit_message with 405 Method Not Allowed (its merge endpoint
	// reports validation failures that way), so nil values are omitted.
	input := map[string]any{"should_remove_source_branch": false}
	if msg := strings.TrimSpace(message); msg != "" {
		input["merge_commit_message"] = msg
	}
	if sha := strings.TrimSpace(expectedSHA); sha != "" {
		input["sha"] = sha
	}
	var mr mergeRequest
	err := g.request(http.MethodPut, fmt.Sprintf("/projects/%s/merge_requests/%d/merge", g.project, iid), nil, input, &mr)
	if err != nil && strings.Contains(err.Error(), "HTTP 405") {
		// 405 also means "not mergeable right now". Ask the MR itself for the
		// reason so the operator sees "CI must pass" or "discussions not
		// resolved" instead of a bare status code.
		if detail, lookupErr := g.getMergeRequest(iid); lookupErr == nil {
			reason := strings.TrimSpace(detail.DetailedMergeStatus)
			if reason == "" {
				reason = strings.TrimSpace(detail.State)
			}
			if reason != "" {
				return mr, fmt.Errorf("GitLab 拒绝合入（%s）：%w", reason, err)
			}
		}
	}
	return mr, err
}

// gitlabBranch is the subset of GET /repository/branches/{branch} used to tell
// whether a merged MR still covers the branch head.
type gitlabBranch struct {
	Name   string `json:"name"`
	Commit struct {
		ID string `json:"id"`
	} `json:"commit"`
}

// branchHead returns the current commit of a branch, or an empty string when
// the branch no longer exists (already deleted after a delivery).
func (g *gitlabClient) branchHead(branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return "", nil
	}
	var out gitlabBranch
	err := g.request(http.MethodGet, "/projects/"+g.project+"/repository/branches/"+url.PathEscape(branch), nil, nil, &out)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(out.Commit.ID), nil
}

// ensureAndMerge delivers source → target the way the GitHub adapter does, but
// never reports a historical MR as a fresh delivery (review finding P1-3).
//
// The decision table:
//
//	no MR                                  → create, then merge
//	open MR                                → merge it
//	merged MR whose sha == branch head     → report the existing merge
//	                                         (idempotent retry of the same work)
//	merged/closed MR, branch moved since   → the branch has new commits, so a
//	                                         new MR is created for them
//
// The third row is the actual fix for P1-3: "merged" alone is not evidence that
// this delivery happened. Only an unchanged branch head means the landed merge
// already covers everything on the branch.
func (s *Service) gitlabEnsureAndMerge(identity, baseURL, token, source, target, title, description string) (string, error) {
	source = strings.TrimSpace(source)
	target = strings.TrimSpace(target)
	if source == "" {
		return "", errors.New("source branch is required")
	}
	if target != "" && source == target {
		return "", nil
	}
	g, err := newGitLabClient(identity, baseURL, token)
	if err != nil {
		return "", err
	}
	project, err := g.getProject()
	if err != nil {
		return "", err
	}
	mr, found, err := g.findOwnMergeRequest(project.ID, source, target)
	if err != nil {
		return "", err
	}
	if found {
		landed := mr.MergedAt != "" || strings.EqualFold(mr.State, "merged")
		switch {
		case landed:
			// Does the landed merge already cover the current branch head? A
			// missing branch means the work is gone, so there is nothing new to
			// deliver and the existing merge stands.
			head, headErr := g.branchHead(source)
			if headErr != nil {
				return "", headErr
			}
			if head == "" || strings.EqualFold(head, strings.TrimSpace(mr.SHA)) {
				return "!" + strconv.Itoa(mr.IID), nil
			}
			// The branch advanced after that merge: this is new work and needs
			// its own merge request.
			mr, found = mergeRequest{}, false
		case strings.EqualFold(mr.State, "closed"):
			// A closed MR cannot be merged through the merge endpoint, so a new
			// one carries the delivery.
			mr, found = mergeRequest{}, false
		}
	}
	if !found {
		createTitle := strings.TrimSpace(title)
		if createTitle == "" {
			createTitle = fmt.Sprintf("LoopForge: %s → %s", source, target)
		}
		createDesc := strings.TrimSpace(description)
		if createDesc == "" {
			createDesc = fmt.Sprintf("定时 SOP 将 `%s` 合入 `%s`。", source, target)
		}
		created, createErr := g.createMergeRequest(source, target, createTitle, createDesc)
		if createErr != nil {
			// Another instance may have won the create race; look again before
			// failing so a 409 is not surfaced as a delivery failure.
			existing, ok, findErr := g.findOwnMergeRequest(project.ID, source, target)
			if findErr != nil {
				return "", findErr
			}
			if !ok {
				return "", fmt.Errorf("创建 GitLab MR %s → %s: %w", source, target, createErr)
			}
			mr = existing
		} else {
			mr = created
		}
		if mr.IID <= 0 {
			return "", fmt.Errorf("GitLab 未返回新建 MR 的 IID")
		}
	}
	if mr.HasConflicts {
		return "", fmt.Errorf("GitLab MR !%d 存在冲突，无法合入", mr.IID)
	}
	merged, mergeErr := g.mergeMergeRequest(mr.IID, mr.SHA, title)
	if mergeErr != nil {
		return "", fmt.Errorf("合入 GitLab MR !%d: %w", mr.IID, mergeErr)
	}
	if merged.MergedAt == "" && !strings.EqualFold(merged.State, "merged") {
		status := strings.TrimSpace(merged.DetailedMergeStatus)
		if status == "" {
			status = strings.TrimSpace(merged.State)
		}
		if status == "" {
			status = "unknown"
		}
		return "", fmt.Errorf("GitLab 未合并 MR !%d（状态：%s）", mr.IID, status)
	}
	return "!" + strconv.Itoa(mr.IID), nil
}

// gitlabFindBranch returns the authoritative MR of a source branch.
func (s *Service) gitlabFindBranch(identity, baseURL, token, sourceBranch string) (domain.DeliveryMR, bool, error) {
	g, err := newGitLabClient(identity, baseURL, token)
	if err != nil {
		return domain.DeliveryMR{}, false, err
	}
	project, err := g.getProject()
	if err != nil {
		return domain.DeliveryMR{}, false, err
	}
	mr, found, err := g.findOwnMergeRequest(project.ID, sourceBranch, "")
	if err != nil || !found {
		return domain.DeliveryMR{}, false, err
	}
	return mr.delivery(identity), true, nil
}

// refreshGitLabDelivery re-reads one MR and writes its state back.
func (s *Service) refreshGitLabDelivery(store MRDeliveryStore, identity, baseURL, token string, iid int) error {
	g, err := newGitLabClient(identity, baseURL, token)
	if err != nil {
		return err
	}
	mr, err := g.getMergeRequest(iid)
	if err != nil {
		return err
	}
	d := mr.delivery(identity)
	return store.UpdateMRDeliveryState(identity, iid, d.GongfengState, d.TargetBranch, mr.mergedAt(), time.Now().UTC())
}

// pullGitLabIssues imports project issues into the local work item table.
// Issues already imported keep their local lifecycle (status, assignee,
// clarification) through applyExistingIssue, exactly like the GitHub path.
func (s *Service) pullGitLabIssues(store IssueStoreAdapter, identity, baseURL, token string) (int, error) {
	g, err := newGitLabClient(identity, baseURL, token)
	if err != nil {
		return 0, err
	}
	var issues []gitlabIssue
	for page := 1; page <= gitlabMaxPages; page++ {
		query := url.Values{
			"per_page": {strconv.Itoa(gitlabPerPage)},
			"page":     {strconv.Itoa(page)},
			"state":    {"all"},
			"order_by": {"updated_at"},
			"sort":     {"desc"},
		}
		var batch []gitlabIssue
		if err := g.request(http.MethodGet, "/projects/"+g.project+"/issues", query, nil, &batch); err != nil {
			return len(issues), err
		}
		issues = append(issues, batch...)
		if len(batch) < gitlabPerPage {
			break
		}
	}
	repo, ok := repositoryByIdentity(store, identity)
	if !ok {
		return 0, fmt.Errorf("GitLab 仓库 %s 未绑定到本地项目", identity)
	}
	count := 0
	for _, item := range issues {
		if item.IID <= 0 {
			continue
		}
		id := fmt.Sprintf("gl-%s-%d", repo.ID, item.IID)
		issue := domain.Issue{
			ID:           id,
			Number:       item.IID,
			ProjectID:    repo.ProjectID,
			RepositoryID: repo.ID,
			BaseBranch:   repo.DefaultBranch,
			Kind:         "issue",
			Title:        item.Title,
			Description:  item.Description,
			Priority:     "medium",
			Source:       "gitlab",
			Status:       gitlabStateToStatus(item.State),
			CreatedAt:    parseGitLabTime(item.CreatedAt),
			UpdatedAt:    time.Now().UTC(),
			ExternalRef: &domain.ExternalRef{
				Source:        "gitlab",
				ExternalID:    strconv.FormatInt(item.ID, 10),
				ExternalIID:   item.IID,
				ExternalURL:   item.WebURL,
				ExternalState: item.State,
				ExternalType:  "issue",
				SyncedAt:      time.Now().UTC(),
			},
		}
		issue.ExternalRef.SourceContentHash = gitlabBodyHash(item.Description)
		if strings.EqualFold(item.State, "opened") {
			issue.Status = "todo"
		}
		if strings.EqualFold(item.State, "closed") {
			issue.CloseSource = "external"
		}
		if old, exists := store.Issue(id); exists {
			applyExistingIssue(&issue, old)
			// A locally clarified description must not be overwritten by the
			// remote body while the clarification is in effect.
			if old.Clarification.Status != "" || (old.ExternalRef != nil && gitlabBodyHash(old.Description) != old.ExternalRef.SourceContentHash) {
				issue.Description = old.Description
			}
		}
		if err := store.PutIssue(issue); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

type gitlabIssue struct {
	ID          int64  `json:"id"`
	IID         int    `json:"iid"`
	Title       string `json:"title"`
	Description string `json:"description"`
	State       string `json:"state"`
	WebURL      string `json:"web_url"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func gitlabStateToStatus(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "opened":
		return "todo"
	case "closed":
		return "closed"
	default:
		return "backlog"
	}
}

func gitlabBodyHash(body string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
}

func parseGitLabTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Now().UTC()
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

// repositoryByIdentity finds the bound local repository for a remote identity.
func repositoryByIdentity(store IssueStoreAdapter, identity string) (domain.Repository, bool) {
	for _, repo := range store.Repositories() {
		if strings.EqualFold(strings.TrimSpace(repo.GongfengProjectID), identity) {
			return repo, true
		}
	}
	return domain.Repository{}, false
}
