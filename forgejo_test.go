package multiforge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeForgejoServer struct {
	*httptest.Server
	repos       []forgejoRepo
	contents    map[string]forgejoContent // used by ListFiles
	fileContent map[string]string         // path -> base64 content (used by GetFile)
	rootEntries []forgejoContent          // used by ListFiles at the repo root
}

func newFakeForgejo(t *testing.T) *fakeForgejoServer {
	t.Helper()
	f := &fakeForgejoServer{
		repos: []forgejoRepo{
			{
				ID:            10,
				Name:          "internal-tool",
				FullName:      "inful/internal-tool",
				Description:   "Internal utility",
				DefaultBranch: "main",
				CloneURL:      "https://git.example.com/inful/internal-tool.git",
				Language:      "Go",
				Owner:         forgejoUser{Username: "inful", FullName: "Inful User"},
			},
		},
		contents: make(map[string]forgejoContent),
		fileContent: make(map[string]string),
		rootEntries: []forgejoContent{
			{Name: "Dockerfile", Path: "Dockerfile", SHA: "abc", Type: "file", Size: 20},
			{Name: "src", Path: "src", SHA: "def", Type: "dir", Size: 0},
		},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

func (f *fakeForgejoServer) serve(w http.ResponseWriter, r *http.Request) {
	// Forgejo's API lives under /api/v1.
	path := r.URL.Path
	switch {
	case strings.HasPrefix(path, "/api/v1/users/inful/repos"):
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, f.repos)
	case strings.HasPrefix(path, "/api/v1/orgs/inful/repos"):
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, f.repos)
	case path == "/api/v1/repos/inful/internal-tool":
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, &f.repos[0])
	case path == "/api/v1/repos/inful/internal-tool/contents/":
		// ListFiles at the repo root — return rootEntries.
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, f.rootEntries)
	case strings.HasPrefix(path, "/api/v1/repos/inful/internal-tool/contents/Dockerfile"):
		// GetFile and ListFiles share the same endpoint but
		// expect different response shapes. Detect which is being
		// asked for by query: ref= is set by GetFile, no ref=
		// means ListFiles.
		if content, ok := f.fileContent["Dockerfile"]; ok && r.URL.Query().Get("ref") != "" {
			w.Header().Set("Content-Type", "application/json")
			writeJSON(w, map[string]string{
				"content":  content,
				"encoding": "base64",
			})
			return
		}
		if c, ok := f.contents["Dockerfile"]; ok {
			w.Header().Set("Content-Type", "application/json")
			writeJSON(w, &c)
			return
		}
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	default:
		http.Error(w, `{"message":"unhandled: `+path+`"}`, http.StatusNotFound)
	}
}

func TestForgejoClient_ListUserRepos(t *testing.T) {
	f := newFakeForgejo(t)
	defer f.Close()

	c, err := newForgejo(f.URL, "token", f.Client(), "test")
	if err != nil {
		t.Fatalf("newForgejo: %v", err)
	}

	repos, err := c.ListUserRepos(context.Background(), "inful")
	if err != nil {
		t.Fatalf("ListUserRepos: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(repos))
	}
	if repos[0].Forge != Forgejo {
		t.Errorf("Forge = %v, want Forgejo", repos[0].Forge)
	}
	if repos[0].FullName != "inful/internal-tool" {
		t.Errorf("FullName = %q", repos[0].FullName)
	}
}

func TestForgejoClient_GetFile(t *testing.T) {
	f := newFakeForgejo(t)
	defer f.Close()

	f.fileContent["Dockerfile"] = "RlJPTSBhbHBpbmU6bGF0ZXN0Cg==" // base64 of "FROM alpine:latest\n"

	c, err := newForgejo(f.URL, "token", f.Client(), "test")
	if err != nil {
		t.Fatalf("newForgejo: %v", err)
	}

	content, err := c.GetFile(context.Background(), "inful", "internal-tool", "Dockerfile", "main")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if string(content) != "FROM alpine:latest\n" {
		t.Errorf("content = %q", string(content))
	}
}

func TestForgejoClient_ListOrgRepos(t *testing.T) {
	f := newFakeForgejo(t)
	defer f.Close()

	c, err := newForgejo(f.URL, "token", f.Client(), "test")
	if err != nil {
		t.Fatalf("newForgejo: %v", err)
	}

	repos, err := c.ListOrgRepos(context.Background(), "inful")
	if err != nil {
		t.Fatalf("ListOrgRepos: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(repos))
	}
	if repos[0].FullName != "inful/internal-tool" {
		t.Errorf("FullName = %q", repos[0].FullName)
	}
}

func TestForgejoClient_GetRepository(t *testing.T) {
	f := newFakeForgejo(t)
	defer f.Close()

	c, err := newForgejo(f.URL, "token", f.Client(), "test")
	if err != nil {
		t.Fatalf("newForgejo: %v", err)
	}

	r, err := c.GetRepository(context.Background(), "inful", "internal-tool")
	if err != nil {
		t.Fatalf("GetRepository: %v", err)
	}
	if r.FullName != "inful/internal-tool" {
		t.Errorf("FullName = %q", r.FullName)
	}
	if r.DefaultBranch != "main" {
		t.Errorf("DefaultBranch = %q", r.DefaultBranch)
	}
}

func TestForgejoClient_GetRepository_NotFound(t *testing.T) {
	f := newFakeForgejo(t)
	defer f.Close()

	c, err := newForgejo(f.URL, "token", f.Client(), "test")
	if err != nil {
		t.Fatalf("newForgejo: %v", err)
	}

	_, err = c.GetRepository(context.Background(), "inful", "does-not-exist")
	if err == nil {
		t.Fatal("expected error for missing repo")
	}
	mfErr := As(err)
	if mfErr == nil || mfErr.Kind != KindNotFound {
		t.Errorf("expected KindNotFound, got %v", mfErr)
	}
}

func TestForgejoClient_GetDefaultBranch(t *testing.T) {
	f := newFakeForgejo(t)
	defer f.Close()

	c, err := newForgejo(f.URL, "token", f.Client(), "test")
	if err != nil {
		t.Fatalf("newForgejo: %v", err)
	}

	branch, err := c.GetDefaultBranch(context.Background(), "inful", "internal-tool")
	if err != nil {
		t.Fatalf("GetDefaultBranch: %v", err)
	}
	if branch != "main" {
		t.Errorf("branch = %q, want main", branch)
	}
}

func TestForgejoClient_ListFiles(t *testing.T) {
	f := newFakeForgejo(t)
	defer f.Close()

	c, err := newForgejo(f.URL, "token", f.Client(), "test")
	if err != nil {
		t.Fatalf("newForgejo: %v", err)
	}

	entries, err := c.ListFiles(context.Background(), "inful", "internal-tool", "", "main")
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
