// Package multiforge is a unified Go client for GitHub, GitLab
// (cloud and self-hosted), and Forgejo/Gitea forges.
//
// The Client interface exposes only the operations multiple consumers
// need in v0.1: discover repositories owned by a user or org, fetch
// a file's contents at a given ref, and list files in a tree.
// Additional operations (branch creation, file write, MR/PR
// creation, webhooks) are deliberately deferred so the v0.1 surface
// stays small and easy to evolve.
//
// All errors returned by a Client are wrapped in a typed *Error
// carrying a Kind so callers can branch on the failure category.
// KindTransient errors (429, 5xx, network) are retried
// automatically when the Client is wrapped with WithRetry.
//
// Example:
//
//	client, err := multiforge.New(ctx, multiforge.Config{
//	    Backend: multiforge.GitHub,
//	    Token:   "${GITHUB_TOKEN}",
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	repos, err := client.ListUserRepos(ctx, "inful")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	for _, r := range repos {
//	    fmt.Println(r.FullName)
//	}
package multiforge
