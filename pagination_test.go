package multiforge

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestPaginatedFetchHelper_SinglePage(t *testing.T) {
	pages := [][]int{
		{1, 2, 3},
	}
	calls := 0
	got, err := PaginatedFetchHelper(
		context.Background(),
		"/items",
		"page",
		"per_page",
		100,
		func(endpoint string) ([]int, bool, error) {
			calls++
			if endpoint != "/items?page=1&per_page=100" {
				return nil, false, fmt.Errorf("unexpected endpoint: %s", endpoint)
			}
			return pages[0], true, nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 fetch call, got %d", calls)
	}
	if len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Errorf("unexpected result: %v", got)
	}
}

func TestPaginatedFetchHelper_MultiPage(t *testing.T) {
	pages := [][]int{
		makeRange(1, 100),
		makeRange(101, 50),
	}
	calls := 0
	got, err := PaginatedFetchHelper(
		context.Background(),
		"/items",
		"page",
		"per_page",
		100,
		func(endpoint string) ([]int, bool, error) {
			if calls >= len(pages) {
				return nil, false, fmt.Errorf("too many calls")
			}
			page := pages[calls]
			calls++
			return page, true, nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 fetch calls, got %d", calls)
	}
	if len(got) != 150 {
		t.Errorf("expected 150 items, got %d", len(got))
	}
	if got[0] != 1 || got[149] != 150 {
		t.Errorf("first/last items wrong: got %d..%d", got[0], got[149])
	}
}

func TestPaginatedFetchHelper_StopsAtPartial(t *testing.T) {
	// Last page returns 5 items (less than pageSize=10). The loop
	// must stop without fetching another empty page.
	pages := [][]int{
		makeRange(1, 10),
		makeRange(11, 5),
	}
	calls := 0
	got, err := PaginatedFetchHelper(
		context.Background(),
		"/items",
		"page",
		"per_page",
		10,
		func(endpoint string) ([]int, bool, error) {
			page := pages[calls]
			calls++
			return page, true, nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 fetch calls, got %d", calls)
	}
	if len(got) != 15 {
		t.Errorf("expected 15 items, got %d", len(got))
	}
}

func TestPaginatedFetchHelper_StopsWhenNoMore(t *testing.T) {
	// hasMore=false on first page should stop immediately.
	calls := 0
	_, err := PaginatedFetchHelper(
		context.Background(),
		"/items",
		"page",
		"per_page",
		100,
		func(endpoint string) ([]int, bool, error) {
			calls++
			return []int{1, 2, 3}, false, nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 fetch call, got %d", calls)
	}
}

func TestPaginatedFetchHelper_StopsOnEmpty(t *testing.T) {
	// Empty page (0 items) with hasMore=true: stop because page is
	// shorter than pageSize.
	calls := 0
	_, err := PaginatedFetchHelper(
		context.Background(),
		"/items",
		"page",
		"per_page",
		100,
		func(endpoint string) ([]int, bool, error) {
			calls++
			return []int{}, true, nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 fetch call, got %d", calls)
	}
}

func TestPaginatedFetchHelper_PropagatesError(t *testing.T) {
	want := errors.New("boom")
	calls := 0
	_, err := PaginatedFetchHelper(
		context.Background(),
		"/items",
		"page",
		"per_page",
		100,
		func(endpoint string) ([]int, bool, error) {
			calls++
			return nil, false, want
		},
	)
	if !errors.Is(err, want) {
		t.Errorf("expected wrapped error, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 fetch call, got %d", calls)
	}
}

func TestPaginatedFetchHelper_AppendsExistingQuery(t *testing.T) {
	calls := 0
	_, err := PaginatedFetchHelper(
		context.Background(),
		"/items?sort=updated",
		"page",
		"per_page",
		100,
		func(endpoint string) ([]int, bool, error) {
			calls++
			if endpoint != "/items?sort=updated&page=1&per_page=100" {
				t.Errorf("endpoint malformed: %s", endpoint)
			}
			return []int{}, false, nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 fetch call, got %d", calls)
	}
}

func TestPaginatedFetchHelper_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, err := PaginatedFetchHelper(
		ctx,
		"/items",
		"page",
		"per_page",
		100,
		func(endpoint string) ([]int, bool, error) {
			calls++
			return makeRange(1, 100), true, nil
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if calls != 0 {
		t.Errorf("expected 0 fetch calls when ctx is already cancelled, got %d", calls)
	}
}

// makeRange builds a slice of ints from `start` of length `count`
// (so makeRange(1, 3) = [1, 2, 3]).
func makeRange(start, count int) []int {
	out := make([]int, count)
	for i := 0; i < count; i++ {
		out[i] = start + i
	}
	return out
}
