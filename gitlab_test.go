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
