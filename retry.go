package multiforge

import (
	"context"
	"log/slog"
	"math/rand"
	"sync"
	"time"
)

// RetryConfig configures the retry policy applied by WithRetry.
type RetryConfig struct {
	// MaxAttempts is the total number of attempts including the first.
	// 1 means "do not retry"; 3 means "up to 3 attempts".
	MaxAttempts int

	// InitialBackoff is the wait before the second attempt; subsequent
	// waits double this value (exponential).
	InitialBackoff time.Duration

	// Jitter is the ±fractional jitter applied to each backoff (0 =
	// none, 0.2 = ±20%). Defaults to 0.2 when zero.
	Jitter float64

	// Logger receives Debug-level "retrying" messages; nil disables
	// logging.
	Logger *slog.Logger

	// Rand is used for jitter. nil falls back to a package-private
	// source seeded lazily from time.Now().
	Rand *rand.Rand
}

// WithRetry wraps a Client so every method retries KindTransient
// errors up to cfg.MaxAttempts total attempts. Other kinds fail
// immediately.
//
// Defaults: MaxAttempts=1 (no retry) when <=0; Jitter=0.2 when 0.
func WithRetry(inner Client, cfg RetryConfig) Client {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 1
	}
	if cfg.Jitter == 0 {
		cfg.Jitter = 0.2
	}
	return &retryClient{inner: inner, cfg: cfg}
}

type retryClient struct {
	inner Client
	cfg   RetryConfig
}

func (r *retryClient) ListUserRepos(ctx context.Context, user string) ([]Repository, error) {
	return retryDo(ctx, r.cfg, "ListUserRepos", func(ctx context.Context) ([]Repository, error) {
		return r.inner.ListUserRepos(ctx, user)
	})
}

func (r *retryClient) ListOrgRepos(ctx context.Context, org string) ([]Repository, error) {
	return retryDo(ctx, r.cfg, "ListOrgRepos", func(ctx context.Context) ([]Repository, error) {
		return r.inner.ListOrgRepos(ctx, org)
	})
}

func (r *retryClient) GetRepository(ctx context.Context, owner, repo string) (*Repository, error) {
	return retryDo(ctx, r.cfg, "GetRepository", func(ctx context.Context) (*Repository, error) {
		return r.inner.GetRepository(ctx, owner, repo)
	})
}

func (r *retryClient) GetDefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	return retryDo(ctx, r.cfg, "GetDefaultBranch", func(ctx context.Context) (string, error) {
		return r.inner.GetDefaultBranch(ctx, owner, repo)
	})
}

func (r *retryClient) GetFile(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	return retryDo(ctx, r.cfg, "GetFile", func(ctx context.Context) ([]byte, error) {
		return r.inner.GetFile(ctx, owner, repo, path, ref)
	})
}

func (r *retryClient) ListFiles(ctx context.Context, owner, repo, path, ref string) ([]FileInfo, error) {
	return retryDo(ctx, r.cfg, "ListFiles", func(ctx context.Context) ([]FileInfo, error) {
		return r.inner.ListFiles(ctx, owner, repo, path, ref)
	})
}

// retryDo executes fn with retry on KindTransient errors. It returns
// the final value and error. The function is generic so it can wrap
// any method without per-method boilerplate.
func retryDo[T any](ctx context.Context, cfg RetryConfig, op string, fn func(context.Context) (T, error)) (T, error) {
	var zero T

	var lastErr error
	delay := cfg.InitialBackoff
	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		result, err := fn(ctx)
		if err == nil {
			return result, nil
		}
		e := As(err)
		if e == nil || e.Kind != KindTransient {
			return zero, err
		}
		lastErr = err
		if attempt == cfg.MaxAttempts {
			break
		}
		// Prefer the server-supplied Retry-After hint over our
		// backoff when present — servers often use it to communicate
		// quota resets that we cannot otherwise know. Skip jitter on
		// a hint since the server has stated a specific moment.
		actualDelay := applyJitter(delay, cfg.Jitter, cfg.Rand)
		if e.RetryAfter > 0 {
			actualDelay = e.RetryAfter // already capped at MaxRetryAfter
		}
		if cfg.Logger != nil {
			cfg.Logger.DebugContext(ctx, "retrying forge request",
				"op", op, "attempt", attempt, "delay", actualDelay, "err", err.Error())
		}
		select {
		case <-time.After(actualDelay):
		case <-ctx.Done():
			return zero, ctx.Err()
		}
		delay *= 2
	}
	return zero, lastErr
}

// applyJitter multiplies d by (1 + jitter*rand[-1,1]). Pure helper.
func applyJitter(d time.Duration, jitter float64, r *rand.Rand) time.Duration {
	if jitter <= 0 {
		return d
	}
	var f float64
	if r != nil {
		f = r.Float64()
	} else {
		f = defaultRandFloat()
	}
	factor := 1.0 + (f*2-1)*jitter
	return time.Duration(float64(d) * factor)
}

// ---------------------------------------------------------------------------
// Package-private RNG for jitter. The seed (defaultRandNow) is captured
// once at package init from time.Now(); the *rand.Rand source itself
// (defaultRandSrc) is created lazily on first use. Tests should pass
// their own *rand.Rand via RetryConfig.Rand for determinism.
// ---------------------------------------------------------------------------

var (
	defaultRandMu  sync.Mutex
	defaultRandSrc *rand.Rand
	defaultRandNow = time.Now().UnixNano()
)

func defaultRandFloat() float64 {
	defaultRandMu.Lock()
	defer defaultRandMu.Unlock()
	if defaultRandSrc == nil {
		defaultRandSrc = rand.New(rand.NewSource(defaultRandNow))
	}
	return defaultRandSrc.Float64()
}
