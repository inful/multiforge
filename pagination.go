package multiforge

import (
	"context"
	"fmt"
	"strings"
)

// PaginatedFetchHelper performs paginated API requests using a common
// pattern. The caller provides a callback that receives a fully-built
// endpoint (with page + limit query params appended) and returns the
// items for that page, a bool indicating whether more pages may
// exist, and any error.
//
// The loop stops when:
//   - the callback returns an error
//   - the callback reports no more pages (hasMore == false)
//   - the callback returns fewer items than pageSize
//   - the context is cancelled
//
// Most forges fit this pattern cleanly. Forges that need a different
// stop condition (e.g. GitHub's Link header, or an explicit
// X-Total-Pages) can implement their own loop and not use this helper.
//
// pageParam is the query parameter name for the page number ("page"
// for most forges). limitParam is the query parameter name for the
// per-page count ("per_page" for GitHub/GitLab, "limit" for
// Forgejo/Gitea).
func PaginatedFetchHelper[T any](
	ctx context.Context,
	baseEndpoint string,
	pageParam string,
	limitParam string,
	pageSize int,
	fetchPage func(endpoint string) ([]T, bool, error),
) ([]T, error) {
	var allResults []T
	page := 1

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Build paginated endpoint. We append "?a=1&b=2" if the
		// caller didn't already include a query string, "&a=1&b=2"
		// otherwise. This lets callers pass endpoints like
		// "/orgs/foo/repos?sort=updated" without breaking.
		sep := "?"
		if strings.Contains(baseEndpoint, "?") {
			sep = "&"
		}
		endpoint := fmt.Sprintf("%s%s%s=%d&%s=%d", baseEndpoint, sep, pageParam, page, limitParam, pageSize)

		pageResults, hasMore, err := fetchPage(endpoint)
		if err != nil {
			return nil, err
		}

		allResults = append(allResults, pageResults...)

		// Stop if the callback signals no more pages, or if this
		// page came back short (fewer items than pageSize means we
		// definitely hit the last page).
		if !hasMore || len(pageResults) < pageSize {
			break
		}

		page++
	}

	return allResults, nil
}
