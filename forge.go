// Package multiforge top-level types: Client interface, Backend
// constants, and Config. The factory function New lives here too.
//
// The Client interface exposes only the operations multiple consumers
// need in v0.1: discover repositories and fetch file contents. New
// methods will be added in v0.2+ as additive extensions — existing
// implementations must not be broken.

package multiforge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"time"

	"github.com/inful/multiforge/internal/httpclient"
)

// Backend identifies which forge a Client talks to.
type Backend string

const (
	// GitHub is github.com and GitHub Enterprise Server.
	GitHub Backend = "github"

	// GitLab is gitlab.com and self-hosted GitLab.
	GitLab Backend = "gitlab"

	// Forgejo covers both Forgejo and Gitea — the APIs are
	// identical. The constant is named Forgejo because Forgejo is
	// the actively-developed fork.
	Forgejo Backend = "forgejo"
)

// Client is the unified interface every backend implements. The
// methods are intentionally small; consumers compose them rather than
// rely on per-platform SDKs.
//
// All methods take a context.Context as the first argument and are
// safe for concurrent use across goroutines (the underlying *http.Client
// is reusable).
type Client interface {
	// ListUserRepos returns every repository owned by the given user
	// across their personal namespace and any organisations the token
	// can read. The result is deduplicated by FullName. Implementations
	// may return archived repositories; filter at the call site if
	// needed.
	ListUserRepos(ctx context.Context, user string) ([]Repository, error)

	// ListOrgRepos returns every repository in the given organisation
	// (GitHub/GitLab) or owner namespace (Forgejo/Gitea) visible to
	// the token. The org argument is the forge-native identifier:
	// "myorg" for GitHub/Forgejo, the numeric ID or full path for
	// GitLab (the official client's GetGroup supports both).
	ListOrgRepos(ctx context.Context, org string) ([]Repository, error)

	// GetRepository returns metadata for a single repository.
	// owner/repo together form the repository's canonical handle
	// within its forge ("inful/dockdeps").
	GetRepository(ctx context.Context, owner, repo string) (*Repository, error)

	// GetDefaultBranch is a convenience wrapper that returns the
	// repository's default branch name without requiring callers to
	// fetch the full Repository struct. Equivalent to
	// GetRepository(...).DefaultBranch but cheaper on the wire
	// when the caller doesn't need the rest of the metadata.
	GetDefaultBranch(ctx context.Context, owner, repo string) (string, error)

	// GetFile returns the raw bytes of the file at path on the given
	// ref (branch, tag, or commit SHA). For binary files the result
	// is the raw bytes as served by the forge API. A KindNotFound
	// error is returned for missing files and missing refs.
	GetFile(ctx context.Context, owner, repo, path, ref string) ([]byte, error)

	// ListFiles returns the immediate children of the directory at
	// path on the given ref. Use path="" for the repository root.
	// Files and directories are both returned; consult FileInfo.IsDir.
	// Implementations may cap the result at a forge-specific page
	// size; if you need recursive tree walks, call repeatedly with
	// each directory's path. (Future versions may add a recursive
	// variant; v0.1 keeps the surface non-recursive.)
	ListFiles(ctx context.Context, owner, repo, path, ref string) ([]FileInfo, error)
}

// Config holds the parameters needed to construct a Client.
//
// ${ENV_VAR} interpolation is performed on Token at New() time so
// the resolved token never appears in any logged Config.
type Config struct {
	// Backend selects which forge implementation to construct.
	// Required.
	Backend Backend

	// Token is a personal access token. Required for all backends.
	// ${ENV_VAR} syntax is supported via os.ExpandEnv at New() time.
	Token string

	// BaseURL is the API root for self-hosted forges.
	//   GitLab:   "https://gitlab.example.com/api/v4"
	//   Forgejo:  "https://git.example.com"
	//   GitHub:   leave empty (api.github.com is hardcoded); for
	//             GitHub Enterprise, set this to the base URL
	//             without the "/api/v3" suffix (the GitHub client
	//             appends it).
	BaseURL string

	// UserAgent overrides the default User-Agent header. Leave
	// empty for the default "multiforge/0.1".
	UserAgent string

	// HTTPClient overrides the shared 30-second-timeout client. Used
	// by tests and operators who need proxy, instrumentation, or
	// transport-level customisation. nil falls back to the default
	// client which respects HTTP_PROXY / HTTPS_PROXY / NO_PROXY.
	HTTPClient *http.Client

	// Retry configures automatic retry of KindTransient errors.
	// Leave zero-valued to disable retry (RetryConfig.MaxAttempts
	// defaults to 1 in that case).
	Retry RetryConfig
}

// New constructs a Client for the configured backend.
//
// ${ENV_VAR} interpolation is applied to the Token field at this
// point so the resolved value never appears in any logged Config.
//
// ctx is currently unused (reserved for future token side-effects);
// the returned Client is independent of ctx after New returns.
func New(ctx context.Context, cfg Config) (Client, error) {
	_ = ctx // reserved
	if cfg.Backend == "" {
		return nil, fmt.Errorf("multiforge: Backend is required")
	}

	// Interpolate ${ENV} in the token. os.ExpandEnv substitutes
	// both $VAR and ${VAR}. Missing variables expand to the empty
	// string, which the per-backend constructor rejects as a
	// missing token.
	token := os.ExpandEnv(cfg.Token)
	if token == "" {
		return nil, fmt.Errorf("multiforge: Token is required (set the env var referenced in the config or pass an explicit value for backend %s)", cfg.Backend)
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = httpclient.New(30 * time.Second)
	}
	userAgent := cfg.UserAgent
	if userAgent == "" {
		userAgent = "multiforge/0.1"
	}

	// Hand the resolved token to the per-backend constructor.
	// Each constructor applies its own auth scheme and
	// forge-specific headers.
	var (
		c   Client
		err error
	)
	switch cfg.Backend {
	case GitHub:
		c, err = newGitHub(cfg.BaseURL, token, httpClient, userAgent)
	case GitLab:
		c, err = newGitLab(cfg.BaseURL, token, httpClient, userAgent)
	case Forgejo:
		c, err = newForgejo(cfg.BaseURL, token, httpClient, userAgent)
	default:
		return nil, fmt.Errorf("multiforge: unknown backend %q", cfg.Backend)
	}
	if err != nil {
		return nil, err
	}

	// Wrap with retry if configured. MaxAttempts <= 1 (the zero
	// value) disables retry, so we don't double-wrap in that case.
	if cfg.Retry.MaxAttempts > 1 {
		c = WithRetry(c, cfg.Retry)
	}
	return c, nil
}

// decodeResponse decodes body into v with a single quirk: when v is a
// pointer to a slice and body parses as a JSON object (or is empty,
// or is `null`), the slice is left as nil instead of returning
// "cannot unmarshal object into ...".
//
// GitLab emits `{}` with a 200 status on some endpoints when the
// requested entity exists but the API token can't see any of its
// child resources — e.g. /users/{user}/projects when the user has no
// projects visible to the token. That's semantically "no projects",
// not "decode error", and the upstream caller treats a nil slice as
// "empty result" naturally.
//
// Real decode failures — body into struct, object into *Repository,
// etc. — still surface because we only relax the rule for slice
// targets fed a non-array body.
func decodeResponse(body io.Reader, v any) error {
	if v == nil {
		_, err := io.Copy(io.Discard, body)
		return err
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if sliceTargetMismatch(data, v) {
		return nil
	}
	return json.Unmarshal(data, v)
}

// isSlicePtr reports whether v's dynamic type is a pointer to a slice.
// Used by decodeResponse to relax the JSON unmarshal step for the
// GitLab quirk where some endpoints return `{}` with a 200 when they
// should return a JSON array.
//
// The reflect.Kind() calls below intentionally defeat govet's
// "inlinable reflect" check — we genuinely need the runtime kind
// because the call sites pass a heterogeneous mix of *[]T types.
//nolint:govet
func isSlicePtr(v any) bool {
	if v == nil {
		return false
	}
	rv := reflect.ValueOf(v)
	return rv.IsValid() && rv.Kind() == reflect.Ptr && rv.Elem().Kind() == reflect.Slice
}

// sliceTargetMismatch reports whether `data` is a JSON document that
// cannot match a slice target (and therefore should be treated as
// "empty"). Returns true for empty bodies, the literal `null`, or any
// JSON object — only JSON arrays can satisfy a `*[]T` target.
func sliceTargetMismatch(data []byte, v any) bool {
	if !isSlicePtr(v) {
		return false
	}
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) == 0 {
		return true // empty body
	}
	switch trimmed[0] {
	case '[':
		return false // proper array — let the normal decoder run
	case 'n':
		return len(trimmed) >= 4 && bytes.Equal(trimmed[:4], []byte("null"))
	default:
		return true // object, primitive, or anything non-array
	}
}
