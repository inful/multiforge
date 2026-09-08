package multiforge

import "time"

// Repository is the cross-forge representation of a single repository.
// Fields are populated to the extent each forge exposes them; gaps are
// left as zero values rather than fabricating plausible data.
//
// The Metadata field carries forge-specific extras (visibility, fork
// count, etc.) so callers that need them can read them without us
// committing to a public API for every forge's quirks.
type Repository struct {
	// ID is a stable, forge-native identifier for the repository.
	// GitHub and GitLab use numeric IDs (stringified); Forgejo uses
	// the slug "owner/name" so it survives forge renames.
	ID string

	// Forge is the backend this repository was discovered on.
	Forge Backend

	// Owner is the user or organisation that owns the repository.
	Owner string

	// Name is the repository's short name (no namespace).
	Name string

	// FullName is "owner/name" — the canonical handle for the repo
	// within its forge.
	FullName string

	// CloneURL is the HTTPS clone URL.
	CloneURL string

	// SSHURL is the SSH clone URL (may be empty for forges that
	// don't expose it).
	SSHURL string

	// DefaultBranch is the branch HEAD points at (e.g. "main").
	DefaultBranch string

	// Description is the user-supplied short description.
	Description string

	// Private reports whether the repository requires authentication
	// to read.
	Private bool

	// Archived reports whether the repository is read-only.
	Archived bool

	// Fork reports whether this repository was forked from another.
	Fork bool

	// LastUpdated is the forge's last-activity timestamp.
	LastUpdated time.Time

	// Topics are the forge-managed topic tags.
	Topics []string

	// Language is the primary language as reported by the forge.
	Language string

	// Metadata carries forge-specific extras. Keys are
	// forge-prefixed: "github_*", "gitlab_*", "forgejo_*".
	Metadata map[string]string
}

// FileInfo describes a single file or directory entry returned by
// ListFiles. IsDir distinguishes files from directories.
type FileInfo struct {
	// Path is the path of the entry relative to the requested root.
	Path string

	// Size is the byte count for files; 0 for directories.
	Size int64

	// SHA is the blob SHA for files; empty for directories.
	SHA string

	// IsDir reports whether the entry is a directory.
	IsDir bool
}
