// Package multiforge provides a unified Go client for GitHub, GitLab
// (cloud and self-hosted), and Forgejo/Gitea forges.
//
// The Client interface is intentionally small — it exposes only the
// operations multiple consumers actually need: discover repositories
// owned by a user or org, fetch a file's contents at a given ref, and
// list files in a tree. Additional operations (branch creation, file
// write, MR/PR creation, webhooks) are deliberately deferred so the
// v0.1 surface stays small and easy to evolve.
//
// All errors returned by a Client are wrapped in a typed *Error
// carrying a Kind so callers can branch on the failure category.
// KindTransient errors (429, 5xx, network) are retried automatically
// when the client is wrapped with WithRetry.
package multiforge

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Kind classifies an error so the caller can map it to a process exit
// code, a UI state, or a retry decision.
type Kind int

const (
	// KindUnknown is the zero value; treat as "no classification available".
	KindUnknown Kind = iota
	// KindConfig covers 400-class errors that aren't auth/not-found/conflict
	// (e.g. a bare 400 with a "branch is empty" or "invalid ref" body).
	// Use for input / request-shape mistakes.
	KindConfig
	// KindAuth covers 401 and 403.
	KindAuth
	// KindNotFound covers 404.
	KindNotFound
	// KindConflict covers 409 (e.g. stale branch head on UpdateFile) AND
	// 422, which GitHub and Gitea/Forgejo use to signal the same
	// condition that GitLab reports as 409. See ClassifyStatus for the
	// mapping rationale.
	KindConflict
	// KindTransient covers 429, 5xx, and network errors — all worth retrying.
	KindTransient
	// KindInternal covers programmer errors / decoding failures / unexpected.
	KindInternal
)

// String returns a stable name for k, useful for structured logging.
func (k Kind) String() string {
	switch k {
	case KindConfig:
		return "config"
	case KindAuth:
		return "auth"
	case KindNotFound:
		return "not-found"
	case KindConflict:
		return "conflict"
	case KindTransient:
		return "transient"
	case KindInternal:
		return "internal"
	default:
		return "unknown"
	}
}

// Error is a typed error carrying a Kind so callers can branch on the
// failure category and map it to a process exit code.
type Error struct {
	Kind       Kind          // category of the failure
	Op         string        // the operation that failed (e.g. "GetFile")
	Err        error         // underlying cause
	StatusCode int           // HTTP status code, 0 if not applicable
	RetryAfter time.Duration // server-supplied wait hint (parsed from Retry-After header); 0 means no hint
	Hint       string        // operator-friendly diagnostic for this error; overrides Error() when non-empty
}

// Error returns "<Op>: <Hint>" when Hint is set (so the operator reads
// the actionable diagnostic directly), otherwise the original
// "<Op>: <underlying err>" format. Tests and log-grep pipelines that
// look for "<Op>: ..." continue to work because Hint defaults to empty.
func (e *Error) Error() string {
	if e.Hint != "" {
		if e.Op == "" {
			return e.Hint
		}
		return fmt.Sprintf("%s: %s", e.Op, e.Hint)
	}
	if e.Op == "" {
		return e.Err.Error()
	}
	return fmt.Sprintf("%s: %s", e.Op, e.Err.Error())
}

func (e *Error) Unwrap() error { return e.Err }

// Is allows errors.Is(err, &Error{Kind: KindAuth}) to match any error with
// the same Kind, regardless of Op / Err / StatusCode.
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	if !ok {
		return false
	}
	return e.Kind == other.Kind
}

// NewError constructs a *Error with the given kind and underlying cause.
// Named to avoid clashing with the package-level New factory in forge.go.
func NewError(kind Kind, op string, err error) *Error {
	return &Error{Kind: kind, Op: op, Err: err}
}

// ClassifyStatus maps an HTTP status code to a Kind. 4xx codes other
// than 401/403/404/409/422/429 fall into KindConfig (input mistakes),
// and 5xx/429 fall into KindTransient (worth retrying).
//
// 422 is mapped to KindConflict (not KindConfig) because GitHub and
// Gitea/Forgejo use 422 to signal a stale-branch-head on file update —
// the same condition GitLab reports as 409. Mapping 422 to KindConflict
// keeps stale-branch conflicts on the same exit code across all
// platforms.
func ClassifyStatus(status int) Kind {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return KindAuth
	case status == http.StatusNotFound:
		return KindNotFound
	case status == http.StatusConflict, status == http.StatusUnprocessableEntity:
		return KindConflict
	case status == http.StatusTooManyRequests || status >= 500:
		return KindTransient
	case status >= 400:
		return KindConfig
	default:
		return KindUnknown
	}
}

// As extracts a *Error from an error chain, returning nil if none is found.
func As(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// MaxRetryAfter caps server-supplied Retry-After values at a value
// reasonable for a CLI tool's wall-clock budget. A misbehaving server
// that asks us to wait hours is treated as MaxRetryAfter (the upper
// bound for sane rate-limit responses from any platform we target).
const MaxRetryAfter = 60 * time.Second

// RetryAfterFromHeader extracts a Retry-After duration from an HTTP
// header. Supports both the delta-seconds form ("Retry-After: 30") and
// the HTTP-date form ("Retry-After: Wed, 21 Oct 2026 07:28:00 GMT")
// defined in RFC 7231 §7.1.3. The result is capped at MaxRetryAfter.
//
// Returns 0 for missing or malformed headers, for negative deltas, and
// for HTTP-dates that have already passed.
func RetryAfterFromHeader(h http.Header) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	// Delta-seconds form is the most common.
	if n, err := parseSeconds(v); err == nil {
		return capRetryAfter(time.Duration(n) * time.Second)
	}
	// Fall back to HTTP-date.
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d > 0 {
			return capRetryAfter(d)
		}
		return 0
	}
	return 0
}

// parseSeconds is a small helper around strconv.Atoi that returns a
// non-nil error for non-numeric strings so the caller can fall back to
// HTTP-date parsing. (Using Atoi directly would also return an error
// for empty strings — we want the same semantics — but we keep this as
// a single-line seam in case we ever want to support fractional
// seconds.)
func parseSeconds(v string) (int, error) {
	// Importing strconv just for this is fine; net/http's ParseTime
	// also imports it. Keeping parseSeconds unexported so callers
	// don't depend on its signature.
	var n int
	_, err := fmt.Sscanf(v, "%d", &n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// capRetryAfter clamps d at the [0, MaxRetryAfter] range. Negative
// values clamp to 0; values above MaxRetryAfter clamp to MaxRetryAfter.
func capRetryAfter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if d > MaxRetryAfter {
		return MaxRetryAfter
	}
	return d
}

// AuthHint returns an operator-friendly diagnostic for the given
// platform + status code. It's used by per-platform classifiers as
// the default fallback for KindAuth errors when no platform-specific
// body parsing is available (i.e. GitLab and Gitea/Forgejo, whose
// response bodies don't carry a structured signal).
//
// GitHub has its own richer hint logic and sets Hint on the typed
// error directly instead of calling this helper.
//
// Pass platform as the lowercase, user-facing name
// ("gitlab", "github", "gitea/forgejo").
func AuthHint(platform string, status int) string {
	switch status {
	case http.StatusUnauthorized:
		return fmt.Sprintf(
			"token rejected by %s (401 Unauthorized). The token may be expired, revoked, or invalid; verify the token is correct and active. "+
				"GitLab PATs are managed at https://gitlab.com/-/user_settings/personal_access_tokens",
			platform,
		)
	case http.StatusForbidden:
		return fmt.Sprintf(
			"token rejected by %s (403 Forbidden). The token is valid but lacks the access required for this operation "+
				"(typically the `api` scope and read access to the target repository)",
			platform,
		)
	default:
		return ""
	}
}
