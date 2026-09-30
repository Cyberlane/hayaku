package snapshot

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

// MetadataDirectory resolves linked-worktree metadata using the same isolated
// Git environment as source capture. No path is inferred from a .git directory.
func MetadataDirectory(ctx context.Context, root string) (string, error) {
	root, err := repositoryRoot(ctx, root)
	if err != nil {
		return "", err
	}
	data, err := git(ctx, root, maxMetadata, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	path := strings.TrimSuffix(string(data), "\n")
	if !filepath.IsAbs(path) {
		return "", errors.New("Git metadata path is not absolute")
	}
	return filepath.EvalSymlinks(path)
}
