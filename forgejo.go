// Package multiforge's Forgejo/Gitea implementation. The two forges
// are API-compatible so a single client backs both. Hand-rolled
// net/http calls (no SDK) because the surface area we need is small.
//
// Auth header: "Authorization: token <token>" — note the literal
// word "token", not "Bearer". That's how Gitea distinguishes personal
// access tokens from OAuth bearer tokens.
//
// All endpoints are relative to the API root which defaults to
// "/api/v1". The newForgejo constructor accepts a baseURL like
// "https://git.example.com" and appends "/api/v1" internally; callers
// who pass a baseURL already containing "/api/v1" get it stripped so
// we never end up with "/api/v1/api/v1".

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

// newForgejo constructs a Client for a Forgejo or Gitea instance.
//
// baseURL is the API root, e.g. "https://git.example.com". The
// "/api/v1" suffix is appended internally; pass it without the
// suffix to avoid surprises if the operator includes it.
//
// token is a personal access token with at least read access to the
// repositories you want to discover.
//
// userAgent is sent on every request. Pass "" for the caller to use
// the multiforge default.
func newForgejo(baseURL, token string, httpClient *http.Client, userAgent string) (Client, error) {
	root := strings.TrimRight(baseURL, "/")
	root = strings.TrimSuffix(root, "/api/v1")
	if root == "" {
		return nil, fmt.Errorf("forgejo: base URL is required")
	}
	if token == "" {
		return nil, fmt.Errorf("forgejo: token is required")
	}
	if userAgent == "" {
		userAgent = "multiforge/0.1"
	}
	return &forgejoClient{
		baseURL:    root,
		token:      token,
		httpClient: httpClient,
		userAgent:  userAgent,
	}, nil
}

type forgejoClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
	userAgent  string
}

// do executes a request and returns the response body bytes on
// success (status 200/201/204) or a typed *Error otherwise.
func (c *forgejoClient) do(ctx context.Context, method, path string, body, v any) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return NewError(KindConfig, method+" "+path, fmt.Errorf("marshal: %w", err))
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return NewError(KindConfig, method+" "+path, fmt.Errorf("new request: %w", err))
	}
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", "application/json")
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
		if err := decodeResponse(resp.Body, v); err != nil {
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
		out.Hint = AuthHint("Gitea/Forgejo", resp.StatusCode)
	}
	return out
}

// forgejoUser is the owner block embedded in forgejoRepo responses.
type forgejoUser struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	FullName string `json:"full_name"`
}

// forgejoRepo is the JSON shape returned by Forgejo's repository
// endpoints. Fields are tagged to match the upstream response; we
// only decode the fields we actually use, so adding new fields on the
// server side doesn't break us.
type forgejoRepo struct {
	ID            int         `json:"id"`
	Name          string      `json:"name"`
	FullName      string      `json:"full_name"`
	Description   string      `json:"description"`
	Private       bool        `json:"private"`
	Fork          bool        `json:"fork"`
	CloneURL      string      `json:"clone_url"`
	SSHURL        string      `json:"ssh_url"`
	DefaultBranch string      `json:"default_branch"`
	Language      string      `json:"language"`
	Archived      bool        `json:"archived"`
	UpdatedAt     time.Time   `json:"updated_at"`
	Topics        []string    `json:"topics"`
	Owner         forgejoUser `json:"owner"`
}

// convertRepo maps the forge-native shape to Repository.
func (c *forgejoClient) convertRepo(r *forgejoRepo) Repository {
	return Repository{
		ID:            fmt.Sprintf("%d", r.ID),
		Forge:         Forgejo,
		Owner:         r.Owner.Username,
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
			"forgejo_id": fmt.Sprintf("%d", r.ID),
			"owner_name": r.Owner.FullName,
		},
	}
}

// ListUserRepos lists all repositories the user owns and any they have
// access to via organisations. Forgejo has a single /user/repos
// endpoint that returns both, paginated.
func (c *forgejoClient) ListUserRepos(ctx context.Context, user string) ([]Repository, error) {
	var all []Repository
	page := 1
	limit := 50
	for {
		path := fmt.Sprintf("/api/v1/users/%s/repos?page=%d&limit=%d", url.PathEscape(user), page, limit)
		var repos []forgejoRepo
		if err := c.do(ctx, http.MethodGet, path, nil, &repos); err != nil {
			return nil, PublicOp("ListUserRepos", err)
		}
		if len(repos) == 0 {
			break
		}
		for i := range repos {
			all = append(all, c.convertRepo(&repos[i]))
		}
		if len(repos) < limit {
			break
		}
		page++
	}
	return all, nil
}

// ListOrgRepos lists repositories in the given organisation.
func (c *forgejoClient) ListOrgRepos(ctx context.Context, org string) ([]Repository, error) {
	var all []Repository
	page := 1
	limit := 50
	for {
		path := fmt.Sprintf("/api/v1/orgs/%s/repos?page=%d&limit=%d", url.PathEscape(org), page, limit)
		var repos []forgejoRepo
		if err := c.do(ctx, http.MethodGet, path, nil, &repos); err != nil {
			return nil, PublicOp("ListOrgRepos", err)
		}
		if len(repos) == 0 {
			break
		}
		for i := range repos {
			all = append(all, c.convertRepo(&repos[i]))
		}
		if len(repos) < limit {
			break
		}
		page++
	}
	return all, nil
}

// GetRepository returns metadata for a single repository.
func (c *forgejoClient) GetRepository(ctx context.Context, owner, repo string) (*Repository, error) {
	path := fmt.Sprintf("/api/v1/repos/%s/%s", url.PathEscape(owner), url.PathEscape(repo))
	var r forgejoRepo
	if err := c.do(ctx, http.MethodGet, path, nil, &r); err != nil {
		return nil, PublicOp("GetRepository", err)
	}
	out := c.convertRepo(&r)
	return &out, nil
}

// GetDefaultBranch returns just the default branch for a repo. It's a
// convenience wrapper around GetRepository for callers that don't need
// the rest of the metadata. Implementing it directly (rather than via
// GetRepository) lets future versions cache the answer.
func (c *forgejoClient) GetDefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	r, err := c.GetRepository(ctx, owner, repo)
	if err != nil {
		return "", PublicOp("GetDefaultBranch", err)
	}
	if r.DefaultBranch == "" {
		return "", NewError(KindConfig, "GetDefaultBranch", fmt.Errorf("repository %s/%s has no default branch", owner, repo))
	}
	return r.DefaultBranch, nil
}

// GetFile fetches the raw contents of path at ref. Forgejo returns
// base64-encoded content in the JSON body; we decode it before
// returning.
func (c *forgejoClient) GetFile(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	apiPath := fmt.Sprintf("/api/v1/repos/%s/%s/contents/%s?ref=%s",
		url.PathEscape(owner), url.PathEscape(repo), path, url.QueryEscape(ref))
	var r struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := c.do(ctx, http.MethodGet, apiPath, nil, &r); err != nil {
		return nil, PublicOp("GetFile", err)
	}
	if r.Encoding != "base64" {
		return nil, NewError(KindInternal, "GetFile", fmt.Errorf("unexpected content encoding %q (only base64 supported)", r.Encoding))
	}
	content, err := base64.StdEncoding.DecodeString(r.Content)
	if err != nil {
		return nil, NewError(KindInternal, "GetFile", fmt.Errorf("decode base64: %w", err))
	}
	return content, nil
}

// forgejoContent is the JSON shape returned by the contents API for
// directory listings (the API returns one element per entry).
type forgejoContent struct {
	Name string `json:"name"`
	Path string `json:"path"`
	SHA  string `json:"sha"`
	Type string `json:"type"` // "file" or "dir"
	Size int64  `json:"size"`
}

// ListFiles returns the immediate children of the directory at path.
// Forgejo's contents API returns the same shape for files and
// directories; for a directory we get back one element per child.
func (c *forgejoClient) ListFiles(ctx context.Context, owner, repo, path, ref string) ([]FileInfo, error) {
	apiPath := fmt.Sprintf("/api/v1/repos/%s/%s/contents/%s?ref=%s",
		url.PathEscape(owner), url.PathEscape(repo), path, url.QueryEscape(ref))
	var entries []forgejoContent
	if err := c.do(ctx, http.MethodGet, apiPath, nil, &entries); err != nil {
		return nil, PublicOp("ListFiles", err)
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
