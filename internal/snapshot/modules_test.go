package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBoundModulesDoNotExemptOtherCheckoutInputs(t *testing.T) {
	root := repository(t)
	write(t, root, "tracked.txt", "original")
	commit(t, root)
	write(t, root, ".gitignore", "node_modules/\nother-cache/\n")
	gitTest(t, root, "add", ".gitignore")
	gitTest(t, root, "commit", "-qm", "declare ignored dependencies")
	commit := gitTest(t, root, "rev-parse", "HEAD")
	write(t, root, "node_modules/package/index.js", "export const value = 1\n")
	ctx := context.Background()
	if err := ValidateCandidate(ctx, root, commit); err == nil {
		t.Fatal("default validation accepted ignored installation")
	}
	if err := ValidateCandidateWithModules(ctx, root, commit, []string{"node_modules"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"untracked.txt", "other-cache/input"} {
		write(t, root, name, "unexpected input")
		if err := ValidateCandidateWithModules(ctx, root, commit, []string{"node_modules"}); err == nil {
			t.Fatalf("bound modules exempted unrelated input %s", name)
		}
		if err := os.RemoveAll(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
	write(t, root, "tracked.txt", "changed")
	if err := ValidateCandidateWithModules(ctx, root, commit, []string{"node_modules"}); err == nil {
		t.Fatal("tracked/untracked source mutation accepted")
	}
}

func TestSeparateModuleDigestLeavesSourceBindingStrict(t *testing.T) {
	root := t.TempDir()
	write(t, root, "node_modules/package/index.js", "one")
	write(t, root, "source.ts", "source")
	ctx := context.Background()
	base, err := DigestWithModules(ctx, root, []string{"node_modules"})
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "node_modules/package/index.js", "two")
	current, err := DigestWithModules(ctx, root, []string{"node_modules"})
	if err != nil || current != base {
		t.Fatalf("separately bound module bytes leaked into source digest: %v", err)
	}
	write(t, root, "source.ts", "different")
	current, err = DigestWithModules(ctx, root, []string{"node_modules"})
	if err != nil || current == base {
		t.Fatal("source bytes stopped being bound")
	}
	for _, name := range []string{".", "../node_modules", "source.ts"} {
		if _, err := DigestWithModules(ctx, root, []string{name}); err == nil {
			t.Fatalf("arbitrary source exclusion accepted: %s", name)
		}
	}
}
