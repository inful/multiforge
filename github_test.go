package multiforge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGitHubServer returns an httptest.Server that responds to the
// GitHub REST API endpoints multiforge uses, with realistic payloads.
// Tests can mutate the handler before calling methods to drive
// specific scenarios (e.g. error responses).
type fakeGitHubServer struct {
	*httptest.Server
	repos    []ghRepo
	contents map[string]ghContents               // path -> contents response
	failNext func(r *http.Request) (int, string) // optional: returns status + body for the next request
}

func newFakeGitHub(t *testing.T) *fakeGitHubServer {
	t.Helper()
	f := &fakeGitHubServer{
		repos: []ghRepo{
			{
				ID:            1,
				Name:          "dockdeps",
				FullName:      "inful/dockdeps",
				Description:   "Track Docker dependencies across repos",
				DefaultBranch: "main",
				CloneURL:      "https://github.com/inful/dockdeps.git",
				SSHURL:        "git@github.com:inful/dockdeps.git",
				Topics:        []string{"docker", "dependencies"},
				Language:      "Go",
				Owner:         ghOwner{Login: "inful", Type: "User"},
			},
			{
				ID:            2,
				Name:          "postgres-containers-groonga",
				FullName:      "inful/postgres-containers-groonga",
				Description:   "Postgres with groonga",
				DefaultBranch: "master",
				CloneURL:      "https://github.com/inful/postgres-containers-groonga.git",
				Language:      "Dockerfile",
				Owner:         ghOwner{Login: "inful", Type: "User"},
			},
		},
		contents: make(map[string]ghContents),
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

func (f *fakeGitHubServer) serve(w http.ResponseWriter, r *http.Request) {
	// Allow tests to inject failures.
	if f.failNext != nil {
		status, body := f.failNext(r)
		if status != 0 {
			http.Error(w, body, status)
			return
		}
	}

	// The GitHub client always appends "/api/v3" to non-empty
	// baseURLs (treating them as Enterprise). For the test we
	// pass the httptest server's URL as baseURL, so the real path
	// has the prefix; strip it before matching.
	path := strings.TrimPrefix(r.URL.Path, "/api/v3")

	switch path {
	case "/users/inful/repos":
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, f.repos)
	case "/orgs/inful/repos":
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, f.repos)
	case "/repos/inful/dockdeps":
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, &f.repos[0])
	case "/repos/inful/dockdeps/contents/Dockerfile":
		if c, ok := f.contents["Dockerfile"]; ok {
			w.Header().Set("Content-Type", "application/json")
			writeJSON(w, &c)
		} else {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		}
	case "/repos/inful/dockdeps/contents/":
		// ListFiles on the root — return two entries.
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, []ghContents{
			{Name: "Dockerfile", Path: "Dockerfile", SHA: "abc", Size: 42, Type: "file"},
			{Name: "src", Path: "src", SHA: "def", Size: 0, Type: "dir"},
		})
	default:
		http.Error(w, `{"message":"unhandled: `+path+`"}`, http.StatusNotFound)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	_ = json.NewEncoder(w).Encode(v)
}

func TestGitHubClient_ListUserRepos(t *testing.T) {
	f := newFakeGitHub(t)
	defer f.Close()

	c, err := newGitHub(f.URL, "token", f.Client(), "test")
	if err != nil {
		t.Fatalf("newGitHub: %v", err)
	}

	repos, err := c.ListUserRepos(context.Background(), "inful")
	if err != nil {
		t.Fatalf("ListUserRepos: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(repos))
	}

	r := repos[0]
	if r.FullName != "inful/dockdeps" {
		t.Errorf("FullName = %q, want inful/dockdeps", r.FullName)
	}
	if r.DefaultBranch != "main" {
		t.Errorf("DefaultBranch = %q, want main", r.DefaultBranch)
	}
	if r.Forge != GitHub {
		t.Errorf("Forge = %v, want GitHub", r.Forge)
	}
	if !r.TopicsEqual([]string{"docker", "dependencies"}) {
		t.Errorf("Topics = %v", r.Topics)
	}
	if r.Language != "Go" {
		t.Errorf("Language = %q, want Go", r.Language)
	}
}

// TopicsEqual is a helper to compare string slices without pulling
// in reflect.DeepEqual everywhere.
func (r Repository) TopicsEqual(want []string) bool {
	if len(r.Topics) != len(want) {
		return false
	}
	for i, t := range want {
		if r.Topics[i] != t {
			return false
		}
	}
	return true
}

func TestGitHubClient_GetRepository(t *testing.T) {
	f := newFakeGitHub(t)
	defer f.Close()

	c, err := newGitHub(f.URL, "token", f.Client(), "test")
	if err != nil {
		t.Fatalf("newGitHub: %v", err)
	}

	r, err := c.GetRepository(context.Background(), "inful", "dockdeps")
	if err != nil {
		t.Fatalf("GetRepository: %v", err)
	}
	if r.FullName != "inful/dockdeps" {
		t.Errorf("FullName = %q", r.FullName)
	}
}

func TestGitHubClient_GetDefaultBranch(t *testing.T) {
	f := newFakeGitHub(t)
	defer f.Close()

	c, err := newGitHub(f.URL, "token", f.Client(), "test")
	if err != nil {
		t.Fatalf("newGitHub: %v", err)
	}

	branch, err := c.GetDefaultBranch(context.Background(), "inful", "dockdeps")
	if err != nil {
		t.Fatalf("GetDefaultBranch: %v", err)
	}
	if branch != "main" {
		t.Errorf("branch = %q, want main", branch)
	}
}

func TestGitHubClient_GetFile(t *testing.T) {
	f := newFakeGitHub(t)
	defer f.Close()

	f.contents["Dockerfile"] = ghContents{
		Name:    "Dockerfile",
		Path:    "Dockerfile",
		SHA:     "abc123",
		Size:    42,
		Type:    "file",
		Content: "RlJPTSBnbGFuZ19iYXNlCmFMTUVOVCBmcm9tID1iYXNlCg==", // base64 of the expected string below
	}

	c, err := newGitHub(f.URL, "token", f.Client(), "test")
	if err != nil {
		t.Fatalf("newGitHub: %v", err)
	}

	content, err := c.GetFile(context.Background(), "inful", "dockdeps", "Dockerfile", "main")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if string(content) != "FROM glang_base\naLMENT from =base\n" {
		t.Errorf("content = %q", string(content))
	}
}

func TestGitHubClient_GetFile_NotFound(t *testing.T) {
	f := newFakeGitHub(t)
	defer f.Close()

	c, err := newGitHub(f.URL, "token", f.Client(), "test")
	if err != nil {
		t.Fatalf("newGitHub: %v", err)
	}

	_, err = c.GetFile(context.Background(), "inful", "dockdeps", "missing.txt", "main")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	mfErr := As(err)
	if mfErr == nil || mfErr.Kind != KindNotFound {
		t.Errorf("expected KindNotFound, got %v", mfErr)
	}
}

func TestGitHubClient_AuthHint_BadCredentials(t *testing.T) {
	// Inject a 401 with "Bad credentials" body — the GitHub-specific
	// hint should fire.
	f := newFakeGitHub(t)
	defer f.Close()

	f.failNext = func(r *http.Request) (int, string) {
		return http.StatusUnauthorized, `{"message":"Bad credentials"}`
	}

	c, err := newGitHub(f.URL, "token", f.Client(), "test")
	if err != nil {
		t.Fatalf("newGitHub: %v", err)
	}

	_, err = c.ListUserRepos(context.Background(), "inful")
	if err == nil {
		t.Fatal("expected error")
	}
	mfErr := As(err)
	if mfErr == nil {
		t.Fatal("expected *Error")
	}
	if mfErr.Hint == "" {
		t.Error("expected Hint to be populated for 401 Bad credentials")
	}
	if !containsHint(mfErr.Hint, "Bad credentials") {
		t.Errorf("Hint doesn't mention Bad credentials: %q", mfErr.Hint)
	}
}

func TestGitHubClient_ListOrgRepos(t *testing.T) {
	f := newFakeGitHub(t)
	defer f.Close()

	c, err := newGitHub(f.URL, "token", f.Client(), "test")
	if err != nil {
		t.Fatalf("newGitHub: %v", err)
	}

	repos, err := c.ListOrgRepos(context.Background(), "inful")
	if err != nil {
		t.Fatalf("ListOrgRepos: %v", err)
	}
	if len(repos) != 2 {
		t.Errorf("expected 2 repos, got %d", len(repos))
	}
}

func TestGitHubClient_ListFiles(t *testing.T) {
	f := newFakeGitHub(t)
	defer f.Close()

	c, err := newGitHub(f.URL, "token", f.Client(), "test")
	if err != nil {
		t.Fatalf("newGitHub: %v", err)
	}

	entries, err := c.ListFiles(context.Background(), "inful", "dockdeps", "", "main")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Find the directory entry.
	var dirs []FileInfo
	for _, e := range entries {
		if e.IsDir {
			dirs = append(dirs, e)
		}
	}
	if len(dirs) != 1 {
		t.Errorf("expected 1 directory entry, got %d", len(dirs))
	}
	if len(dirs) > 0 && dirs[0].Path != "src" {
		t.Errorf("dir path = %q, want src", dirs[0].Path)
	}
}

// containsHint is a tiny helper that doesn't need a strings import.
func containsHint(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
