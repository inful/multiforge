package multiforge

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestKindString(t *testing.T) {
	tests := []struct {
		k    Kind
		want string
	}{
		{KindConfig, "config"},
		{KindAuth, "auth"},
		{KindNotFound, "not-found"},
		{KindConflict, "conflict"},
		{KindTransient, "transient"},
		{KindInternal, "internal"},
		{KindUnknown, "unknown"},
		{Kind(99), "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.k.String(); got != tt.want {
				t.Errorf("Kind(%d).String() = %q, want %q", tt.k, got, tt.want)
			}
		})
	}
}

func TestClassifyStatus(t *testing.T) {
	tests := []struct {
		status int
		want   Kind
	}{
		{http.StatusOK, KindUnknown},
		{http.StatusBadRequest, KindConfig},
		{http.StatusUnauthorized, KindAuth},
		{http.StatusForbidden, KindAuth},
		{http.StatusNotFound, KindNotFound},
		{http.StatusConflict, KindConflict},
		{http.StatusUnprocessableEntity, KindConflict},
		{http.StatusTooManyRequests, KindTransient},
		{http.StatusInternalServerError, KindTransient},
		{http.StatusBadGateway, KindTransient},
		{http.StatusServiceUnavailable, KindTransient},
		{399, KindUnknown},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("status_%d", tt.status), func(t *testing.T) {
			if got := ClassifyStatus(tt.status); got != tt.want {
				t.Errorf("ClassifyStatus(%d) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestErrorFormat(t *testing.T) {
	// Without Hint: <Op>: <underlying err>
	e := NewError(KindNotFound, "GetFile", errors.New("missing"))
	if got, want := e.Error(), "GetFile: missing"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}

	// With Hint: <Op>: <Hint>
	e = NewError(KindAuth, "GetFile", errors.New("bad token"))
	e.Hint = "token expired"
	if got, want := e.Error(), "GetFile: token expired"; got != want {
		t.Errorf("Error() with hint = %q, want %q", got, want)
	}

	// Empty Op falls through cleanly.
	e = NewError(KindConfig, "", errors.New("boom"))
	if got, want := e.Error(), "boom"; got != want {
		t.Errorf("Error() with empty Op = %q, want %q", got, want)
	}
}

func TestErrorIs(t *testing.T) {
	// errors.Is should match by Kind regardless of Op/Err.
	a := NewError(KindAuth, "GetFile", errors.New("bad token"))
	b := NewError(KindAuth, "GetRepository", errors.New("nope"))
	c := NewError(KindNotFound, "GetFile", errors.New("404"))

	if !errors.Is(a, b) {
		t.Error("expected a and b to match by Kind")
	}
	if errors.Is(a, c) {
		t.Error("expected a and c NOT to match (different Kinds)")
	}

	// Pointer identity should also work.
	if !errors.Is(a, a) {
		t.Error("expected a to match itself")
	}
}

func TestErrorUnwrap(t *testing.T) {
	inner := errors.New("inner cause")
	e := NewError(KindInternal, "Decode", inner)
	if got := errors.Unwrap(e); got != inner {
		t.Errorf("Unwrap() = %v, want %v", got, inner)
	}
}

func TestAs(t *testing.T) {
	inner := NewError(KindAuth, "Test", errors.New("bad"))
	wrapped := fmt.Errorf("op failed: %w", inner)

	got := As(wrapped)
	if got == nil {
		t.Fatal("As returned nil for wrapped *Error")
	}
	if got.Kind != KindAuth {
		t.Errorf("As().Kind = %v, want KindAuth", got.Kind)
	}

	if As(errors.New("plain")) != nil {
		t.Error("As should return nil for non-multiforge errors")
	}
}

func TestRetryAfterFromHeader(t *testing.T) {
	tests := []struct {
		name   string
		header http.Header
		want   string // string for human-readable comparison
	}{
		{
			name:   "missing header",
			header: http.Header{},
			want:   "0s",
		},
		{
			name:   "delta seconds",
			header: http.Header{"Retry-After": []string{"30"}},
			want:   "30s",
		},
		{
			name:   "delta seconds above cap",
			header: http.Header{"Retry-After": []string{"3600"}},
			want:   MaxRetryAfter.String(),
		},
		{
			name:   "negative delta",
			header: http.Header{"Retry-After": []string{"-5"}},
			want:   "0s",
		},
		{
			name:   "garbage value",
			header: http.Header{"Retry-After": []string{"not-a-number"}},
			want:   "0s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RetryAfterFromHeader(tt.header)
			if got.String() != tt.want {
				t.Errorf("RetryAfterFromHeader() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAuthHint(t *testing.T) {
	tests := []struct {
		status int
		want   string // empty string = no hint
	}{
		{http.StatusUnauthorized, "token rejected"},         // contains expected phrase
		{http.StatusForbidden, "lacks the access required"}, // ditto
		{http.StatusBadRequest, ""},
		{http.StatusOK, ""},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("status_%d", tt.status), func(t *testing.T) {
			hint := AuthHint("TestForge", tt.status)
			if tt.want == "" {
				if hint != "" {
					t.Errorf("AuthHint() = %q, want empty", hint)
				}
				return
			}
			if !contains(hint, tt.want) {
				t.Errorf("AuthHint() = %q, want to contain %q", hint, tt.want)
			}
		})
	}
}

// contains is a tiny strings.Contains replacement to avoid an import
// just for one test helper.
func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
