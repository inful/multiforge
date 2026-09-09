// Package multiforge's GitLab implementation. Hand-rolled net/http
// calls (no SDK) because the surface area we need is small.
//
// Auth header: "Authorization: Bearer <token>" — Bearer works for
// both classic session cookies and personal access tokens. (PRIVATE-TOKEN
// also works but Bearer is the modern form.)
//
// GitLab returns X-Total-Pages and X-Per-Page headers on paginated
// responses. We use the simpler "got full page → maybe more" heuristic
// instead, which is good enough for the user/org listing endpoints we
// hit. If exact-stop semantics are ever needed, swap in a header-aware
// loop.

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
	// glDefaultAPIURL is the public gitlab.com API root. GitLab's API
	// lives under /api/v4 so the default here includes it.
	glDefaultAPIURL = "https://gitlab.com/api/v4"
)

// newGitLab constructs a Client for GitLab.
//
// baseURL is the API root including /api/v4. For gitlab.com pass ""
// (the default). For self-hosted pass e.g.
// "https://gitlab.example.com/api/v4".
//
// token is a personal access token with the "api" or "read_api" scope
// depending on what you intend to do. (For multiforge's read-only
// surface, read_api is sufficient.)
//
// userAgent is sent on every request. Pass "" for the multiforge
// default.
func newGitLab(baseURL, token string, httpClient *http.Client, userAgent string) (Client, error) {
	if token == "" {
		return nil, fmt.Errorf("gitlab: token is required")
	}
	if userAgent == "" {
		userAgent = "multiforge/0.1"
	}
	apiURL := strings.TrimRight(baseURL, "/")
	if apiURL == "" {
		apiURL = glDefaultAPIURL
	}
	return &glClient{
		apiURL:     apiURL,
		token:      token,
		httpClient: httpClient,
		userAgent:  userAgent,
	}, nil
}

type glClient struct {
	apiURL     string
	token      string
	httpClient *http.Client
	userAgent  string
}

// do executes a request and returns the response body bytes on
// success (status 200/201/204) or a typed *Error otherwise.
func (c *glClient) do(ctx context.Context, method, path string, body, v any) error {
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
		out.Hint = AuthHint("GitLab", resp.StatusCode)
	}
	return out
}

// glNamespace is the namespace block embedded in project responses.
type glNamespace struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

// glProject is the JSON shape returned by GitLab's project endpoints.
type glProject struct {
	ID                int                `json:"id"`
	Name              string             `json:"name"`
	Path              string             `json:"path"`
	NameWithNamespace string             `json:"name_with_namespace"`
	PathWithNamespace string             `json:"path_with_namespace"`
	Description       string             `json:"description"`
	DefaultBranch     string             `json:"default_branch"`
	HTTPURLToRepo     string             `json:"http_url_to_repo"`
	SSHURLToRepo      string             `json:"ssh_url_to_repo"`
	Visibility        string             `json:"visibility"`
	Archived          bool               `json:"archived"`
	LastActivityAt    time.Time          `json:"last_activity_at"`
	Topics            []string           `json:"topics"`
	Languages         map[string]float64 `json:"languages,omitempty"`
	Namespace         glNamespace        `json:"namespace"`
}

// convertRepo maps the GitLab-native shape to Repository.
// Primary language is picked as the language with the highest
// percentage in the Languages map; falls back to "" if Languages is
// empty.
func (c *glClient) convertRepo(p *glProject) Repository {
	primaryLanguage := ""
	maxPercentage := 0.0
	for lang, pct := range p.Languages {
		if pct > maxPercentage {
			maxPercentage = pct
			primaryLanguage = lang
		}
	}
	return Repository{
		ID:            fmt.Sprintf("%d", p.ID),
		Forge:         GitLab,
		Owner:         p.Namespace.Path,
		Name:          p.Path,
		FullName:      p.PathWithNamespace,
		CloneURL:      p.HTTPURLToRepo,
		SSHURL:        p.SSHURLToRepo,
		DefaultBranch: p.DefaultBranch,
		Description:   p.Description,
		Private:       p.Visibility != "public",
		Archived:      p.Archived,
		LastUpdated:   p.LastActivityAt,
		Topics:        p.Topics,
		Language:      primaryLanguage,
		Metadata: map[string]string{
			"gitlab_id":           fmt.Sprintf("%d", p.ID),
			"visibility":          p.Visibility,
			"name_with_namespace": p.NameWithNamespace,
			"namespace_kind":      p.Namespace.Kind,
		},
	}
}

// ListUserRepos lists repositories owned by user. GitLab exposes this
// via /users/{user}/projects. We accept both usernames and numeric
// IDs (the official client accepts both).
func (c *glClient) ListUserRepos(ctx context.Context, user string) ([]Repository, error) {
	var all []Repository
	page := 1
	perPage := 100
	for {
		path := fmt.Sprintf("/users/%s/projects?per_page=%d&page=%d&order_by=last_activity_at",
			url.PathEscape(user), perPage, page)
		var projects []glProject
		if err := c.do(ctx, http.MethodGet, path, nil, &projects); err != nil {
			return nil, PublicOp("ListUserRepos", err)
		}
		if len(projects) == 0 {
			break
		}
		for i := range projects {
			all = append(all, c.convertRepo(&projects[i]))
		}
		if len(projects) < perPage {
			break
		}
		page++
	}
	return all, nil
}

// ListOrgRepos lists repositories in the given group. GitLab's API
// only accepts numeric group IDs in /groups/:id/projects — passing a
// path returns 404. The official GitLab client accepts both path and
// numeric ID; we follow that convention.
func (c *glClient) ListOrgRepos(ctx context.Context, org string) ([]Repository, error) {
	var all []Repository
	page := 1
	perPage := 100
	for {
		// include_subgroups=true so a query against a top-level
		// group returns repos from subgroups too. That's the usual
		// expectation when "list org repos" is invoked.
		path := fmt.Sprintf("/groups/%s/projects?per_page=%d&page=%d&include_subgroups=true&order_by=last_activity_at",
			url.PathEscape(org), perPage, page)
		var projects []glProject
		if err := c.do(ctx, http.MethodGet, path, nil, &projects); err != nil {
			return nil, PublicOp("ListOrgRepos", err)
		}
		if len(projects) == 0 {
			break
		}
		for i := range projects {
			all = append(all, c.convertRepo(&projects[i]))
		}
		if len(projects) < perPage {
			break
		}
		page++
	}
	return all, nil
}

// GetRepository returns metadata for a single repository.
func (c *glClient) GetRepository(ctx context.Context, owner, repo string) (*Repository, error) {
	projectPath := fmt.Sprintf("%s/%s", owner, repo)
	path := fmt.Sprintf("/projects/%s", url.PathEscape(projectPath))
	var p glProject
	if err := c.do(ctx, http.MethodGet, path, nil, &p); err != nil {
		return nil, PublicOp("GetRepository", err)
	}
	out := c.convertRepo(&p)
	return &out, nil
}

// GetDefaultBranch returns just the default branch for a repo. A
// convenience wrapper around GetRepository.
func (c *glClient) GetDefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	r, err := c.GetRepository(ctx, owner, repo)
	if err != nil {
		return "", PublicOp("GetDefaultBranch", err)
	}
	if r.DefaultBranch == "" {
		return "", NewError(KindConfig, "GetDefaultBranch", fmt.Errorf("repository %s/%s has no default branch", owner, repo))
	}
	return r.DefaultBranch, nil
}

// glFile is the JSON shape returned by the repository-files API for
// a single file.
type glFile struct {
	FilePath     string `json:"file_path"`
	Content      string `json:"content"`
	Encoding     string `json:"encoding"`
	LastCommitID string `json:"last_commit_id"`
}

// GetFile fetches the raw contents of path at ref. GitLab returns
// base64-encoded content; we decode it before returning.
func (c *glClient) GetFile(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	projectPath := fmt.Sprintf("%s/%s", owner, repo)
	apiPath := fmt.Sprintf("/projects/%s/repository/files/%s?ref=%s",
		url.PathEscape(projectPath), url.PathEscape(path), url.QueryEscape(ref))
	var f glFile
	if err := c.do(ctx, http.MethodGet, apiPath, nil, &f); err != nil {
		return nil, PublicOp("GetFile", err)
	}
	if f.Encoding != "base64" {
		return nil, NewError(KindInternal, "GetFile", fmt.Errorf("unexpected content encoding %q (only base64 supported)", f.Encoding))
	}
	content, err := base64.StdEncoding.DecodeString(f.Content)
	if err != nil {
		return nil, NewError(KindInternal, "GetFile", fmt.Errorf("decode base64: %w", err))
	}
	return content, nil
}

// glTreeEntry is the JSON shape returned by the repository-tree API
// for one entry.
type glTreeEntry struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Type string `json:"type"` // "blob" or "tree"
	Size int64  `json:"size"`
	ID   string `json:"id"` // SHA for blobs, tree SHA for trees
}

// ListFiles returns the immediate children of the directory at path.
// GitLab's tree API recursively walks the tree by default; passing
// path returns all entries under that path, not just immediate
// children. We post-filter to immediate children so the behaviour
// matches the other backends.
func (c *glClient) ListFiles(ctx context.Context, owner, repo, path, ref string) ([]FileInfo, error) {
	projectPath := fmt.Sprintf("%s/%s", owner, repo)

	// Collect all matching entries across pages, then post-filter to
	// immediate children.
	var entries []glTreeEntry
	page := 1
	for {
		q := url.Values{}
		q.Set("ref", ref)
		q.Set("per_page", "100")
		if path != "" {
			q.Set("path", path)
		}
		q.Set("page", fmt.Sprintf("%d", page))
		pagedPath := fmt.Sprintf("/projects/%s/repository/tree?%s",
			url.PathEscape(projectPath), q.Encode())
		var pageEntries []glTreeEntry
		if err := c.do(ctx, http.MethodGet, pagedPath, nil, &pageEntries); err != nil {
			return nil, PublicOp("ListFiles", err)
		}
		if len(pageEntries) == 0 {
			break
		}
		entries = append(entries, pageEntries...)
		if len(pageEntries) < 100 {
			break
		}
		page++
	}

	// Filter to immediate children of path. For an empty path,
	// every entry is an immediate child of the root.
	prefix := path
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	out := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		if !isImmediateChild(e.Path, prefix) {
			continue
		}
		out = append(out, FileInfo{
			Path:  e.Path,
			Size:  e.Size,
			SHA:   e.ID,
			IsDir: e.Type == "tree",
		})
	}
	return out, nil
}

// isImmediateChild reports whether childPath is exactly one level
// below prefixPath. The caller is responsible for ensuring
// prefixPath ends with "/" when non-empty (so "src" matches "src/"
// and "srcfoo" doesn't).
//
// Examples (prefixPath → childPath → result):
//
//	""           → "Dockerfile"   → true   (top-level entry)
//	""           → "src/foo.go"   → false  (nested under src, not root)
//	"src/"       → "src/foo.go"   → true   (immediate child of src)
//	"src/"       → "src/sub/bar"  → false  (one level too deep)
//	"src/"       → "README.md"    → false  (not under src)
func isImmediateChild(childPath, prefixPath string) bool {
	if prefixPath != "" && !strings.HasPrefix(childPath, prefixPath) {
		return false
	}
	rest := strings.TrimPrefix(childPath, prefixPath)
	// "Immediate child" means exactly one level below the prefix,
	// which is equivalent to "the path after stripping the prefix
	// contains no slash". With prefixPath="" and childPath="Dockerfile"
	// that's "Dockerfile" (no slash → true). With childPath="src/foo.go"
	// and prefixPath="" that's "src/foo.go" (slash present → false).
	return !strings.Contains(rest, "/")
}
