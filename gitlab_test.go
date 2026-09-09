package multiforge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeGitLabServer struct {
	*httptest.Server
	projects []glProject
	files    map[string]glFile
	rootTree []glTreeEntry
}

func newFakeGitLab(t *testing.T) *fakeGitLabServer {
	t.Helper()
	f := &fakeGitLabServer{
		projects: []glProject{
			{
				ID:                100,
				Name:              "myproject",
				Path:              "myproject",
				NameWithNamespace: "inful / myproject",
				PathWithNamespace: "inful/myproject",
				Description:       "A test project",
				DefaultBranch:     "main",
				HTTPURLToRepo:     "https://gitlab.com/inful/myproject.git",
				Visibility:        "public",
				Namespace:         glNamespace{Kind: "user", Path: "inful"},
			},
		},
		files: make(map[string]glFile),
		rootTree: []glTreeEntry{
			{Path: "Dockerfile", Name: "Dockerfile", Type: "blob", Size: 25, ID: "abc"},
			{Path: "src", Name: "src", Type: "tree", Size: 0, ID: "def"},
		},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

func (f *fakeGitLabServer) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case strings.HasPrefix(path, "/api/v4/users/inful/projects"):
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, f.projects)
	case strings.HasPrefix(path, "/api/v4/groups/inful/projects"):
		// ListOrgRepos hits /groups/:id/projects.
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, f.projects)
	case path == "/api/v4/projects/inful/myproject":
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, &f.projects[0])
	case strings.HasPrefix(path, "/api/v4/projects/inful/myproject/repository/files/Dockerfile"):
		if file, ok := f.files["Dockerfile"]; ok {
			w.Header().Set("Content-Type", "application/json")
			writeJSON(w, &file)
		} else {
			http.Error(w, `{"message":"404 File Not Found"}`, http.StatusNotFound)
		}
	case strings.HasPrefix(path, "/api/v4/projects/inful/myproject/repository/tree"):
		// ListFiles uses the tree API; empty path = root.
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, f.rootTree)
	default:
		http.Error(w, `{"message":"unhandled: `+path+`"}`, http.StatusNotFound)
	}
}

func TestGitLabClient_ListUserRepos(t *testing.T) {
	f := newFakeGitLab(t)
	defer f.Close()

	c, err := newGitLab(f.URL+"/api/v4", "token", f.Client(), "test")
	if err != nil {
		t.Fatalf("newGitLab: %v", err)
	}

	repos, err := c.ListUserRepos(context.Background(), "inful")
	if err != nil {
		t.Fatalf("ListUserRepos: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(repos))
	}
	if repos[0].Forge != GitLab {
		t.Errorf("Forge = %v, want GitLab", repos[0].Forge)
	}
	if repos[0].FullName != "inful/myproject" {
		t.Errorf("FullName = %q", repos[0].FullName)
	}
}

func TestGitLabClient_GetFile(t *testing.T) {
	f := newFakeGitLab(t)
	defer f.Close()

	f.files["Dockerfile"] = glFile{
		FilePath:     "Dockerfile",
		Content:      "RlJPTSBnb2xhbmc6MS4yMgo=", // base64 of "FROM golang:1.22\n"
		Encoding:     "base64",
		LastCommitID: "abc123",
	}

	c, err := newGitLab(f.URL+"/api/v4", "token", f.Client(), "test")
	if err != nil {
		t.Fatalf("newGitLab: %v", err)
	}

	content, err := c.GetFile(context.Background(), "inful", "myproject", "Dockerfile", "main")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if string(content) != "FROM golang:1.22\n" {
		t.Errorf("content = %q", string(content))
	}
}

func TestGitLabClient_ListOrgRepos(t *testing.T) {
	f := newFakeGitLab(t)
	defer f.Close()

	c, err := newGitLab(f.URL+"/api/v4", "token", f.Client(), "test")
	if err != nil {
		t.Fatalf("newGitLab: %v", err)
	}

	repos, err := c.ListOrgRepos(context.Background(), "inful")
	if err != nil {
		t.Fatalf("ListOrgRepos: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(repos))
	}
	if repos[0].FullName != "inful/myproject" {
		t.Errorf("FullName = %q", repos[0].FullName)
	}
}

func TestGitLabClient_GetRepository(t *testing.T) {
	f := newFakeGitLab(t)
	defer f.Close()

	c, err := newGitLab(f.URL+"/api/v4", "token", f.Client(), "test")
	if err != nil {
		t.Fatalf("newGitLab: %v", err)
	}

	r, err := c.GetRepository(context.Background(), "inful", "myproject")
	if err != nil {
		t.Fatalf("GetRepository: %v", err)
	}
	if r.FullName != "inful/myproject" {
		t.Errorf("FullName = %q", r.FullName)
	}
	if r.DefaultBranch != "main" {
		t.Errorf("DefaultBranch = %q", r.DefaultBranch)
	}
}

func TestGitLabClient_GetDefaultBranch(t *testing.T) {
	f := newFakeGitLab(t)
	defer f.Close()

	c, err := newGitLab(f.URL+"/api/v4", "token", f.Client(), "test")
	if err != nil {
		t.Fatalf("newGitLab: %v", err)
	}

	branch, err := c.GetDefaultBranch(context.Background(), "inful", "myproject")
	if err != nil {
		t.Fatalf("GetDefaultBranch: %v", err)
	}
	if branch != "main" {
		t.Errorf("branch = %q, want main", branch)
	}
}

func TestGitLabClient_ListFiles(t *testing.T) {
	f := newFakeGitLab(t)
	defer f.Close()

	c, err := newGitLab(f.URL+"/api/v4", "token", f.Client(), "test")
	if err != nil {
		t.Fatalf("newGitLab: %v", err)
	}

	entries, err := c.ListFiles(context.Background(), "inful", "myproject", "", "main")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Find the directory entry to confirm IsDir round-trips.
	var dirs []FileInfo
	for _, e := range entries {
		if e.IsDir {
			dirs = append(dirs, e)
		}
	}
	if len(dirs) != 1 {
		t.Errorf("expected 1 directory, got %d", len(dirs))
	}
	if len(dirs) > 0 && dirs[0].Path != "src" {
		t.Errorf("dir path = %q, want src", dirs[0].Path)
	}
}

// TestIsImmediateChild pins the post-filter behaviour that
// ListFiles relies on. The previous implementation rejected
// top-level entries when prefixPath was empty (it required a "/"
// in the path), so callers listing at the repo root got nothing.
func TestIsImmediateChild(t *testing.T) {
	tests := []struct {
		child  string
		prefix string
		want   bool
	}{
		// Empty prefix: only top-level entries (no slash) are children.
		{"Dockerfile", "", true},
		{"src", "", true},
		{"src/foo.go", "", false},
		{"a/b/c", "", false},

		// Non-empty prefix: must start with prefix and have no
		// further slash.
		{"src/foo.go", "src/", true},
		{"src/sub/bar", "src/", false},
		{"README.md", "src/", false},
		{"srcfoo.go", "src/", false},
		{"src/foo", "src/", true},
	}
	for _, tt := range tests {
		t.Run(tt.child+"_under_"+tt.prefix, func(t *testing.T) {
			if got := isImmediateChild(tt.child, tt.prefix); got != tt.want {
				t.Errorf("isImmediateChild(%q, %q) = %v, want %v", tt.child, tt.prefix, got, tt.want)
			}
		})
	}
}
