package multiforge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNew_RequiresBackend(t *testing.T) {
	_, err := New(context.Background(), Config{Token: "x"})
	if err == nil || !strings.Contains(err.Error(), "Backend is required") {
		t.Errorf("expected Backend required error, got %v", err)
	}
}

func TestNew_RequiresToken(t *testing.T) {
	_, err := New(context.Background(), Config{Backend: GitHub})
	if err == nil || !strings.Contains(err.Error(), "Token is required") {
		t.Errorf("expected Token required error, got %v", err)
	}
}

func TestNew_InterpolatesEnv(t *testing.T) {
	t.Setenv("MULTIFORGE_TEST_TOKEN", "secret-from-env")

	// Fake GitHub server so the factory can construct a real client.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return one repo so ListUserRepos has something to return.
		// The URL has /api/v3 because GitHub client always appends.
		path := strings.TrimPrefix(r.URL.Path, "/api/v3")
		if path == "/users/test/repos" {
			w.Header().Set("Content-Type", "application/json")
			writeJSON(w, []ghRepo{{
				ID: 1, Name: "r", FullName: "test/r",
				DefaultBranch: "main", Owner: ghOwner{Login: "test", Type: "User"},
			}})
			return
		}
		http.Error(w, "unhandled", http.StatusNotFound)
	}))
	defer srv.Close()

	c, err := New(context.Background(), Config{
		Backend: GitHub,
		Token:   "${MULTIFORGE_TEST_TOKEN}",
		BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	repos, err := c.ListUserRepos(context.Background(), "test")
	if err != nil {
		t.Fatalf("ListUserRepos: %v", err)
	}
	if len(repos) != 1 {
		t.Errorf("expected 1 repo, got %d", len(repos))
	}
}

func TestNew_UnknownBackend(t *testing.T) {
	_, err := New(context.Background(), Config{
		Backend: Backend("nope"),
		Token:   "x",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown backend") {
		t.Errorf("expected unknown backend error, got %v", err)
	}
}

func TestNew_NoRetryWhenMaxAttemptsIsZero(t *testing.T) {
	// A Client constructed with cfg.Retry.MaxAttempts=0 should
	// not be wrapped with WithRetry. We verify by ensuring the
	// returned Client's concrete type isn't a *retryClient — but
	// that's an internal detail. Instead we test behaviour: the
	// fake server returns 503 once and the call should NOT be
	// retried (because MaxAttempts=0 → 1 → no retry).
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c, err := New(context.Background(), Config{
		Backend: GitHub,
		Token:   "x",
		BaseURL: srv.URL,
		// no Retry
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, _ = c.ListUserRepos(context.Background(), "u")
	if calls != 1 {
		t.Errorf("expected 1 call (no retry), got %d", calls)
	}
}

func TestNew_RetriesWhenConfigured(t *testing.T) {
	// Counter-test: with cfg.Retry.MaxAttempts=3, a 503 then 200
	// should yield 2 calls.
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v3")
		calls++
		if calls == 1 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if path == "/users/u/repos" {
			w.Header().Set("Content-Type", "application/json")
			writeJSON(w, []ghRepo{{
				ID: 1, Name: "r", FullName: "u/r",
				DefaultBranch: "main", Owner: ghOwner{Login: "u", Type: "User"},
			}})
			return
		}
		http.Error(w, "unhandled", http.StatusNotFound)
	}))
	defer srv.Close()

	c, err := New(context.Background(), Config{
		Backend: GitHub,
		Token:   "x",
		BaseURL: srv.URL,
		Retry: RetryConfig{
			MaxAttempts:    3,
			InitialBackoff: 1, // nanosecond
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	repos, err := c.ListUserRepos(context.Background(), "u")
	if err != nil {
		t.Fatalf("ListUserRepos: %v", err)
	}
	if len(repos) != 1 {
		t.Errorf("expected 1 repo after retry, got %d", len(repos))
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (1 fail + 1 success), got %d", calls)
	}
}
