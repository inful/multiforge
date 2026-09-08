# multiforge

`multiforge` is a unified Go client for talking to GitHub, GitLab (cloud or self-hosted), and Forgejo/Gitea instances. It is intentionally small — only the operations multiple consumers actually need: discover repositories owned by a user or org, fetch a file's contents at a given ref, and list files in a tree.

It is **not** a full SDK. It does not handle MR/PR creation, branch mutation, webhook registration, or file edits — those are out of v0.1 scope. The interface is designed to grow into those needs without breaking v0.1 callers.

## Backends

| Backend constant | Targets |
|---|---|
| `multiforge.GitHub` | `github.com` and GitHub Enterprise Server |
| `multiforge.GitLab` | `gitlab.com` and self-hosted GitLab |
| `multiforge.Forgejo` | Forgejo (covers Gitea too — the APIs are identical) |

## Install

```bash
go get github.com/inful/multiforge
```

## Usage

```go
package main

import (
    "context"
    "fmt"
    "log"
    "time"

    "github.com/inful/multiforge"
)

func main() {
    client, err := multiforge.New(context.Background(), multiforge.Config{
        Backend: multiforge.GitHub,
        Token:   "${GITHUB_TOKEN}", // ${VAR} interpolation happens at New() time
        Retry: multiforge.RetryConfig{
            MaxAttempts:    3,
            InitialBackoff: 500 * time.Millisecond,
        },
    })
    if err != nil {
        log.Fatal(err)
    }

    repos, err := client.ListUserRepos(context.Background(), "inful")
    if err != nil {
        log.Fatal(err)
    }
    for _, r := range repos {
        fmt.Printf("%s  %s\n", r.FullName, r.Description)
    }

    content, err := client.GetFile(context.Background(), r.Owner, r.Name, "Dockerfile", r.DefaultBranch)
    // ...
}
```

## Configuration

`Config` is a struct. Environment variables in any string field are interpolated via `os.ExpandEnv` (so `"${GITHUB_TOKEN}"` becomes the value of `$GITHUB_TOKEN`).

| Field | Purpose |
|---|---|
| `Backend` | One of `GitHub`, `GitLab`, `Forgejo` |
| `Token` | Personal access token (PAT) |
| `BaseURL` | API root; required for `GitLab`/`Forgejo`, defaults for `GitHub` |
| `UserAgent` | Optional; defaults to `multiforge/0.1` |
| `HTTPClient` | Optional; defaults to a 30s-timeout client honouring `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` |
| `Retry` | Optional `RetryConfig`; zero value disables retry |

## Auth

| Backend | Header |
|---|---|
| GitHub | `Authorization: Bearer <token>` (plus `Accept` and `X-GitHub-Api-Version` headers) |
| GitLab | `Authorization: Bearer <token>` (works for both classic and PAT) |
| Forgejo/Gitea | `Authorization: token <token>` (literal word "token", not "Bearer") |

## Errors

All errors returned by a `Client` are wrapped in a typed `*multiforge.Error` carrying a `Kind` so callers can branch on the failure category:

```go
e := multiforge.As(err)
switch e.Kind {
case multiforge.KindAuth:        // 401/403
case multiforge.KindNotFound:    // 404
case multiforge.KindConflict:    // 409/422
case multiforge.KindTransient:   // 429/5xx/network — retryable
case multiforge.KindConfig:      // other 4xx
case multiforge.KindInternal:    // decoding failures
}
```

`KindTransient` errors are retried automatically when `Config.Retry.MaxAttempts > 1`. All other kinds fail immediately. The `Hint` field on `*Error`, when set, is an operator-friendly diagnostic (e.g. "token expired, verify at https://github.com/settings/tokens").

## License

GPL-3 — same as the rest of the `inful/*` Go tool family.
