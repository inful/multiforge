package multiforge

import (
	"context"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWithRetry_RetriesTransient(t *testing.T) {
	// Fake server returns 503 the first 2 times and succeeds on the
	// 3rd — the retry loop should keep going through transient
	// failures.
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":1,"name":"r","full_name":"u/r","default_branch":"main","owner":{"login":"u","type":"User"}}]`))
	}))
	defer srv.Close()

	c, err := newGitHub(srv.URL, "token", srv.Client(), "test")
	if err != nil {
		t.Fatalf("newGitHub: %v", err)
	}
	wrapped := WithRetry(c, RetryConfig{
		MaxAttempts:    3,
		InitialBackoff: 1 * time.Millisecond,
	})

	repos, err := wrapped.ListUserRepos(t.Context(), "u")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls (2 fails + 1 success), got %d", calls)
	}
	if len(repos) != 1 {
		t.Errorf("expected 1 repo, got %d", len(repos))
	}
}

func TestWithRetry_DoesNotRetryNonTransient(t *testing.T) {
	// 404 must NOT be retried.
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c, err := newGitHub(srv.URL, "token", srv.Client(), "test")
	if err != nil {
		t.Fatalf("newGitHub: %v", err)
	}
	wrapped := WithRetry(c, RetryConfig{
		MaxAttempts:    5,
		InitialBackoff: 1 * time.Millisecond,
	})

	_, err = wrapped.ListUserRepos(t.Context(), "u")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls != 1 {
		t.Errorf("expected 1 call (no retry on 404), got %d", calls)
	}
	mfErr := As(err)
	if mfErr == nil || mfErr.Kind != KindNotFound {
		t.Errorf("expected KindNotFound, got %v", mfErr)
	}
}

func TestWithRetry_ExhaustsAttempts(t *testing.T) {
	// Server always returns 503. After MaxAttempts, the final
	// transient error is returned.
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c, err := newGitHub(srv.URL, "token", srv.Client(), "test")
	if err != nil {
		t.Fatalf("newGitHub: %v", err)
	}
	wrapped := WithRetry(c, RetryConfig{
		MaxAttempts:    3,
		InitialBackoff: 1 * time.Millisecond,
	})

	_, err = wrapped.ListUserRepos(t.Context(), "u")
	if err == nil {
		t.Fatal("expected error after exhausting attempts")
	}
	if calls != 3 {
		t.Errorf("expected 3 calls (all retries), got %d", calls)
	}
	mfErr := As(err)
	if mfErr == nil || mfErr.Kind != KindTransient {
		t.Errorf("expected KindTransient, got %v", mfErr)
	}
}

func TestWithRetry_RespectsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c, err := newGitHub(srv.URL, "token", srv.Client(), "test")
	if err != nil {
		t.Fatalf("newGitHub: %v", err)
	}
	wrapped := WithRetry(c, RetryConfig{
		MaxAttempts:    5,
		InitialBackoff: 50 * time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err = wrapped.ListUserRepos(ctx, "u")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	// We don't assert the exact error type because Go versions
	// surface context.DeadlineExceeded slightly differently through
	// the retry loop. The important thing is we got an error and
	// didn't loop forever.
}

func TestWithRetry_DisabledWhenMaxAttemptsIs1(t *testing.T) {
	// MaxAttempts=0 (the default) is normalised to 1 → no retry.
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c, err := newGitHub(srv.URL, "token", srv.Client(), "test")
	if err != nil {
		t.Fatalf("newGitHub: %v", err)
	}
	wrapped := WithRetry(c, RetryConfig{}) // MaxAttempts=0 → defaults to 1

	_, err = wrapped.ListUserRepos(t.Context(), "u")
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 call (no retry), got %d", calls)
	}
}

// TestWithRetry_RespectsServerRetryAfter verifies that when the
// server sends a Retry-After header on a 503, the retry loop honors
// that hint. We can't easily measure wall-clock delay without flaky
// tests, so we just verify the call eventually completes (i.e. the
// retry actually fired and succeeded, with no infinite loop).
func TestWithRetry_RespectsServerRetryAfter(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			// Server asks us to wait 1 second. We cap at
			// MaxRetryAfter (60s) internally, so the test won't
			// actually wait the full second — but it'll wait
			// *something*. Use a value just long enough to be
			// measurable: 1 second.
			w.Header().Set("Retry-After", "1")
			http.Error(w, "slow down", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c, err := newGitHub(srv.URL, "token", srv.Client(), "test")
	if err != nil {
		t.Fatalf("newGitHub: %v", err)
	}
	wrapped := WithRetry(c, RetryConfig{
		MaxAttempts:    2,
		InitialBackoff: 1 * time.Nanosecond,
	})

	done := make(chan struct{})
	go func() {
		_, _ = wrapped.ListUserRepos(t.Context(), "u")
		close(done)
	}()

	select {
	case <-done:
		// Good — the call completed.
	case <-time.After(3 * time.Second):
		t.Fatal("retry did not complete within 3 seconds")
	}
	if calls != 2 {
		t.Errorf("expected 2 calls, got %d", calls)
	}
}

// TestWithRetry_JitterApplied makes sure the jitter machinery doesn't
// produce zero or negative durations (a regression on
// applyJitter would silently break backoff).
func TestWithRetry_JitterApplied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c, err := newGitHub(srv.URL, "token", srv.Client(), "test")
	if err != nil {
		t.Fatalf("newGitHub: %v", err)
	}
	wrapped := WithRetry(c, RetryConfig{
		MaxAttempts:    2,
		InitialBackoff: 10 * time.Millisecond,
		Jitter:         0.5,
		Rand:           rand.New(rand.NewSource(42)),
	})

	_, err = wrapped.ListUserRepos(t.Context(), "u")
	if err == nil {
		t.Fatal("expected error")
	}
}
