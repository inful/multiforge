// Package multiforge's GitHub implementation. Hand-rolled net/http
// calls (no SDK) because the subset of GitHub's API multiforge needs
// is small, and avoiding the dependency cuts the module's transitive
// surface significantly.
//
// Auth header: "Authorization: Bearer <token>". GitHub also requires
// the Accept and X-GitHub-Api-Version headers for stable behaviour;
// we set both unconditionally.

package multiforge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// ghDefaultAPIURL is the public github.com API root.
	ghDefaultAPIURL = "https://api.github.com"

	// ghAPIVersion is the GitHub REST API version header value.
	ghAPIVersion = "2022-11-28"
)

// newGitHub constructs a Client for GitHub.
//
// baseURL is the API root. For github.com pass "". For GitHub
// Enterprise Server pass "https://github.example.com" without the
// "/api/v3" suffix; the "/api/v3" path is appended internally.
//
// token is a personal access token (classic PAT or fine-grained PAT
// with appropriate repository permissions).
//
// userAgent is sent on every request. GitHub rate-limits
// unidentified clients aggressively, so passing a meaningful value is
// encouraged.
func newGitHub(baseURL, token string, httpClient *http.Client, userAgent string) (Client, error) {
	if token == "" {
		return nil, fmt.Errorf("github: token is required")
	}
	if userAgent == "" {
		userAgent = "multiforge/0.1"
	}
	apiURL := strings.TrimRight(baseURL, "/")
	if apiURL == "" {
		apiURL = ghDefaultAPIURL
	} else {
		// Enterprise: callers pass "https://host" without "/api/v3";
		// we append it. If they accidentally included it, strip it
		// first to avoid "/api/v3/api/v3".
		apiURL = strings.TrimSuffix(apiURL, "/api/v3")
		apiURL = apiURL + "/api/v3"
	}
	return &ghClient{
		apiURL:     apiURL,
		token:      token,
		httpClient: httpClient,
		userAgent:  userAgent,
	}, nil
}

type ghClient struct {
	apiURL     string
	token      string
	httpClient *http.Client
	userAgent  string
}

// do executes a request and returns the response body bytes on
// success (status 200/201/204) or a typed *Error otherwise.
func (c *ghClient) do(ctx context.Context, method, path string, body, v any) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return NewError(KindConfig, method+" "+path, fmt.Errorf("marshal: %w", err))
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.apiURL+path, bodyReader)
	if err != nil {
		return NewError(KindConfig, method+" "+path, fmt.Errorf("new request: %w", err))
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", ghAPIVersion)
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &Error{Kind: KindTransient, Op: method + " " + path, Err: err}
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if v == nil {
			return nil
		}
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			return NewError(KindInternal, method+" "+path, fmt.Errorf("decode: %w", err))
		}
		return nil
	}

	// Non-2xx: classify by status.
	respBody, _ := io.ReadAll(resp.Body)
	kind := ClassifyStatus(resp.StatusCode)
	out := &Error{
		Kind:       kind,
		Op:         method + " " + path,
		Err:        fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody)),
		StatusCode: resp.StatusCode,
		RetryAfter: RetryAfterFromHeader(resp.Header),
	}
	if kind == KindAuth {
		out.Hint = githubAuthHint(resp.StatusCode, respBody, resp.Header)
	}
	return out
}

// githubAuthHint distinguishes the four practical GitHub auth
// failure modes operators hit: expired/revoked PAT (401 Bad
// credentials), primary rate-limit (403 + X-RateLimit-Remaining: 0),
// fine-grained PAT scope mismatch (403 + "Resource not accessible
// by personal access token" message), and generic 403 insufficient
// permission.
func githubAuthHint(status int, body []byte, hdr http.Header) string {
	// Body is JSON: {"message":"..."}; we don't bother decoding since
	// the message strings we care about are stable substrings that
	// appear verbatim in the body.
	bs := string(body)
	switch status {
	case http.StatusUnauthorized:
		if strings.Contains(bs, "Bad credentials") {
			return "GitHub rejected the token (401 Bad credentials). The token may be expired or revoked; verify or regenerate at https://github.com/settings/tokens"
		}
		return "GitHub rejected the token (401 Unauthorized). The token is invalid or revoked; verify the token is correct and active at https://github.com/settings/tokens"
	case http.StatusForbidden:
		if hdr != nil && hdr.Get("X-RateLimit-Remaining") == "0" {
			reset := hdr.Get("X-RateLimit-Reset")
			hint := "GitHub API rate limit exceeded (403 Forbidden). Wait until the X-RateLimit-Reset time and retry"
			if reset != "" {
				hint += " (resets at epoch=" + reset + ")"
			}
			return hint
		}
		if strings.Contains(bs, "Resource not accessible by personal access token") {
			return "GitHub rejected the token with insufficient scope (403 Resource not accessible). For fine-grained tokens, grant repository contents read permission; for classic PATs, ensure the `repo` scope is selected"
		}
		return "GitHub rejected the token (403 Forbidden). The token is valid but lacks the access required for this operation; verify the token has the required scope and that you have access to the repository"
	default:
		return ""
	}
}

// ghOwner is the owner block embedded in repository responses.
type ghOwner struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

// ghRepo is the JSON shape returned by GitHub's repository endpoints.
type ghRepo struct {
	ID            int       `json:"id"`
	Name          string    `json:"name"`
	FullName      string    `json:"full_name"`
	Description   string    `json:"description"`
	Private       bool      `json:"private"`
	Fork          bool      `json:"fork"`
	CloneURL      string    `json:"clone_url"`
	SSHURL        string    `json:"ssh_url"`
	DefaultBranch string    `json:"default_branch"`
	Language      string    `json:"language"`
	Archived      bool      `json:"archived"`
	UpdatedAt     time.Time `json:"updated_at"`
	Topics        []string  `json:"topics"`
	Owner         ghOwner   `json:"owner"`
}

// convertRepo maps the GitHub-native shape to Repository.
func (c *ghClient) convertRepo(r *ghRepo) Repository {
	return Repository{
		ID:            fmt.Sprintf("%d", r.ID),
		Forge:         GitHub,
		Owner:         r.Owner.Login,
		Name:          r.Name,
		FullName:      r.FullName,
		CloneURL:      r.CloneURL,
		SSHURL:        r.SSHURL,
		DefaultBranch: r.DefaultBranch,
		Description:   r.Description,
		Private:       r.Private,
		Archived:      r.Archived,
		Fork:          r.Fork,
		LastUpdated:   r.UpdatedAt,
		Topics:        r.Topics,
		Language:      r.Language,
		Metadata: map[string]string{
			"github_id":  fmt.Sprintf("%d", r.ID),
			"owner_type": r.Owner.Type,
		},
	}
}

// ListUserRepos lists repositories owned by user. GitHub returns both
// personal repos and repos in organisations the token can read via
// /users/{user}/repos. We use the listing endpoint and paginate with
// page + per_page.
func (c *ghClient) ListUserRepos(ctx context.Context, user string) ([]Repository, error) {
	var all []Repository
	page := 1
	perPage := 100
	for {
		path := fmt.Sprintf("/users/%s/repos?page=%d&per_page=%d&sort=updated",
			url.PathEscape(user), page, perPage)
		var repos []ghRepo
		if err := c.do(ctx, http.MethodGet, path, nil, &repos); err != nil {
			return nil, err
		}
		if len(repos) == 0 {
			break
		}
		for i := range repos {
			all = append(all, c.convertRepo(&repos[i]))
		}
		if len(repos) < perPage {
			break
		}
		page++
	}
	return all, nil
}

// ListOrgRepos lists repositories in the given organisation.
func (c *ghClient) ListOrgRepos(ctx context.Context, org string) ([]Repository, error) {
	var all []Repository
	page := 1
	perPage := 100
	for {
		path := fmt.Sprintf("/orgs/%s/repos?page=%d&per_page=%d&sort=updated",
			url.PathEscape(org), page, perPage)
		var repos []ghRepo
		if err := c.do(ctx, http.MethodGet, path, nil, &repos); err != nil {
			return nil, err
		}
		if len(repos) == 0 {
			break
		}
		for i := range repos {
			all = append(all, c.convertRepo(&repos[i]))
		}
		if len(repos) < perPage {
			break
		}
		page++
	}
	return all, nil
}

// GetRepository returns metadata for a single repository.
func (c *ghClient) GetRepository(ctx context.Context, owner, repo string) (*Repository, error) {
	path := fmt.Sprintf("/repos/%s/%s", url.PathEscape(owner), url.PathEscape(repo))
	var r ghRepo
	if err := c.do(ctx, http.MethodGet, path, nil, &r); err != nil {
		return nil, err
	}
	out := c.convertRepo(&r)
	return &out, nil
}

// GetDefaultBranch returns just the default branch for a repo. A
// convenience wrapper around GetRepository; if callers want to avoid
// the extra request they can call GetRepository themselves and read
// DefaultBranch from the result.
func (c *ghClient) GetDefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	r, err := c.GetRepository(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	if r.DefaultBranch == "" {
		return "", NewError(KindConfig, "GetDefaultBranch", fmt.Errorf("repository %s/%s has no default branch", owner, repo))
	}
	return r.DefaultBranch, nil
}

// ghContents is the JSON shape returned by the contents API for a
// file. For directory listings, the same shape is returned for each
// entry.
type ghContents struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	SHA     string `json:"sha"`
	Size    int64  `json:"size"`
	Type    string `json:"type"` // "file" or "dir"
	Content string `json:"content"`
}

// GetFile fetches the raw contents of path at ref. GitHub returns
// base64-encoded content; we decode it before returning. For binary
// files, callers receive the raw bytes (not base64).
func (c *ghClient) GetFile(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	apiPath := fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s",
		url.PathEscape(owner), url.PathEscape(repo), path, url.QueryEscape(ref))
	var r ghContents
	if err := c.do(ctx, http.MethodGet, apiPath, nil, &r); err != nil {
		return nil, err
	}
	if r.Content == "" {
		// Empty file: return zero-length slice without trying to
		// base64-decode an empty string (which would succeed but is
		// wasteful and confusing).
		return []byte{}, nil
	}
	content, err := base64.StdEncoding.DecodeString(r.Content)
	if err != nil {
		return nil, NewError(KindInternal, "GetFile", fmt.Errorf("decode base64: %w", err))
	}
	return content, nil
}

// ListFiles returns the immediate children of path on ref. For a
// directory, the contents endpoint returns one element per child; for
// a file, it returns a single element (which we surface so callers
// can detect "this is a file, not a directory").
func (c *ghClient) ListFiles(ctx context.Context, owner, repo, path, ref string) ([]FileInfo, error) {
	apiPath := fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s",
		url.PathEscape(owner), url.PathEscape(repo), path, url.QueryEscape(ref))
	var entries []ghContents
	if err := c.do(ctx, http.MethodGet, apiPath, nil, &entries); err != nil {
		return nil, err
	}
	// GitHub's contents API returns a single object (not array) when
	// path refers to a file. To normalise that, decode the response
	// into a single object too and wrap it in a slice.
	if len(entries) == 0 {
		var single ghContents
		if err := c.do(ctx, http.MethodGet, apiPath, nil, &single); err != nil {
			return nil, err
		}
		if single.Path == "" {
			return nil, NewError(KindNotFound, "ListFiles", fmt.Errorf("path %q not found in %s/%s", path, owner, repo))
		}
		entries = []ghContents{single}
	}
	out := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		out = append(out, FileInfo{
			Path:  e.Path,
			Size:  e.Size,
			SHA:   e.SHA,
			IsDir: e.Type == "dir",
		})
	}
	return out, nil
}
