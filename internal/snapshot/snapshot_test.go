package snapshot

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
)

func gitTest(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root, "-c", "user.name=Hayaku Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func repository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitTest(t, root, "init", "--initial-branch=main")
	return root
}

func write(t *testing.T, root, path, text string) {
	t.Helper()
	destination := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, root string) string {
	t.Helper()
	gitTest(t, root, "add", "--all")
	gitTest(t, root, "commit", "--allow-empty", "-m", "fixture")
	return gitTest(t, root, "rev-parse", "HEAD")
}

func TestCaptureImmutableTreesAndWeirdPaths(t *testing.T) {
	root := repository(t)
	write(t, root, "deleted.go", "deleted\n")
	write(t, root, "renamed.go", strings.Repeat("rename\n", 30))
	write(t, root, "literal.go", "jpeg\n")
	write(t, root, "tab\tand\nnewline.go", "base\n")
	write(t, root, ".gitattributes", "exported.go export-ignore\n")
	write(t, root, "exported.go", "archive must not omit this\n")
	base := commit(t, root)
	if err := os.Remove(filepath.Join(root, "deleted.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "renamed.go"), filepath.Join(root, "new name.go")); err != nil {
		t.Fatal(err)
	}
	write(t, root, "literal.go", "avif\n")
	write(t, root, "--argument.go", "added\n")
	write(t, root, "tab\tand\nnewline.go", "candidate\n")
	candidate := commit(t, root)
	write(t, root, "literal.go", "dirty bytes must not be materialized\n")
	indexBefore := gitTest(t, root, "status", "--porcelain=v1")
	pair, err := Capture(context.Background(), root, base, candidate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pair.Close() })
	want := []model.Change{{Path: "--argument.go", Status: "A"}, {Path: "deleted.go", Status: "D"}, {Path: "literal.go", Status: "M"}, {Path: "new name.go", OldPath: "renamed.go", Status: "R"}, {Path: "tab\tand\nnewline.go", Status: "M"}}
	if !reflect.DeepEqual(pair.Changes, want) {
		t.Fatalf("changes = %#v, want %#v", pair.Changes, want)
	}
	for _, check := range []struct{ dir, path, text string }{{pair.BaseDir, "literal.go", "jpeg\n"}, {pair.CandidateDir, "literal.go", "avif\n"}, {pair.CandidateDir, "exported.go", "archive must not omit this\n"}} {
		data, err := os.ReadFile(filepath.Join(check.dir, check.path))
		if err != nil || string(data) != check.text {
			t.Fatalf("materialized %q = %q, %v", check.path, data, err)
		}
	}
	if got := gitTest(t, root, "status", "--porcelain=v1"); got != indexBefore {
		t.Fatalf("checkout mutated: before %q after %q", indexBefore, got)
	}
	dir := pair.CandidateDir
	if err := pair.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private snapshot retained: %v", err)
	}
	if err := pair.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestCaptureRejectsMissingRevisionsAndCancellation(t *testing.T) {
	root := repository(t)
	write(t, root, "a.go", "base")
	id := commit(t, root)
	for _, revision := range []string{"missing-parent", "--all", "", "HEAD\x00evil"} {
		if _, err := Capture(context.Background(), root, revision, id); err == nil {
			t.Fatalf("accepted invalid base %q", revision)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Capture(ctx, root, id, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestCaptureRejectsSymlinksAndGitlinks(t *testing.T) {
	for _, kind := range []string{"symlink", "gitlink"} {
		t.Run(kind, func(t *testing.T) {
			root := repository(t)
			write(t, root, "a", "base")
			base := commit(t, root)
			if kind == "symlink" {
				if err := os.Symlink("../../escape", filepath.Join(root, "escape")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				gitTest(t, root, "add", "escape")
			} else {
				gitTest(t, root, "update-index", "--add", "--cacheinfo", "160000,"+base+",submodule")
			}
			gitTest(t, root, "commit", "-m", "unsupported entry")
			id := gitTest(t, root, "rev-parse", "HEAD")
			if _, err := Capture(context.Background(), root, base, id); err == nil {
				t.Fatal("unsupported entry accepted")
			}
		})
	}
}

func TestCaptureRejectsUnmergedIndex(t *testing.T) {
	root := repository(t)
	write(t, root, "a", "base\n")
	base := commit(t, root)
	gitTest(t, root, "checkout", "-b", "other")
	write(t, root, "a", "other\n")
	commit(t, root)
	gitTest(t, root, "checkout", "main")
	write(t, root, "a", "main\n")
	candidate := commit(t, root)
	cmd := exec.Command("git", "-C", root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "merge", "other")
	if err := cmd.Run(); err == nil {
		t.Fatal("fixture merge must conflict")
	}
	if _, err := Capture(context.Background(), root, base, candidate); err == nil || !strings.Contains(err.Error(), "unmerged") {
		t.Fatalf("unmerged capture error = %v", err)
	}
}

func TestCaptureMissingShallowBaseDoesNotFetch(t *testing.T) {
	root := repository(t)
	write(t, root, "a", "base\n")
	base := commit(t, root)
	write(t, root, "a", "candidate\n")
	candidate := commit(t, root)
	parent := t.TempDir()
	clone := filepath.Join(parent, "clone")
	gitTest(t, parent, "clone", "--depth=1", "file://"+filepath.ToSlash(root), clone)
	if _, err := Capture(context.Background(), clone, base, candidate); err == nil {
		t.Fatal("missing shallow base accepted")
	}
	if shallow := gitTest(t, clone, "rev-parse", "--is-shallow-repository"); shallow != "true" {
		t.Fatal("capture modified repository depth")
	}
}

func TestValidateCandidateDetectsAllCheckoutInputs(t *testing.T) {
	root := repository(t)
	write(t, root, "a", "base\n")
	write(t, root, ".gitignore", "ignored\n")
	id := commit(t, root)
	if err := ValidateCandidate(context.Background(), root, id); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"a", "untracked", "ignored"} {
		write(t, root, path, "different\n")
		if err := ValidateCandidate(context.Background(), root, id); err == nil {
			t.Fatalf("accepted checkout input %s", path)
		}
		if path == "a" {
			write(t, root, path, "base\n")
		} else if err := os.Remove(filepath.Join(root, path)); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateCandidate(context.Background(), root, strings.Repeat("0", 40)); err == nil {
		t.Fatal("accepted different candidate")
	}
}

func TestValidateCandidateRejectsNormalizedButDifferentBytes(t *testing.T) {
	root := repository(t)
	write(t, root, ".gitattributes", "a text eol=crlf\n")
	write(t, root, "a", "base\n")
	id := commit(t, root)
	if err := os.Remove(filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	gitTest(t, root, "checkout-index", "--force", "a")
	gitTest(t, root, "add", "--", "a")
	if status := gitTest(t, root, "status", "--porcelain=v1"); status != "" {
		data, _ := os.ReadFile(filepath.Join(root, "a"))
		t.Fatalf("fixture should be Git-clean after normalization: %s, bytes %q, diff %s", status, data, gitTest(t, root, "diff", "--", "a"))
	}
	if err := ValidateCandidate(context.Background(), root, id); err == nil {
		t.Fatal("accepted raw bytes differing from commit")
	}
}

func TestGitEnvironmentCannotRedirectSnapshots(t *testing.T) {
	root := repository(t)
	write(t, root, "a", "real\n")
	id := commit(t, root)
	other := repository(t)
	write(t, other, "a", "redirected\n")
	commit(t, other)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	pair, err := Capture(context.Background(), root, id, id)
	if err != nil {
		t.Fatal(err)
	}
	defer pair.Close()
	data, _ := os.ReadFile(filepath.Join(pair.CandidateDir, "a"))
	if string(data) != "real\n" {
		t.Fatalf("redirected source = %q", data)
	}
}

func TestBoundedOutputAndMalformedInventories(t *testing.T) {
	buffer := &boundedBuffer{limit: 2}
	if _, err := buffer.Write([]byte("abc")); err == nil || !buffer.full || buffer.buf.Len() != 0 {
		t.Fatal("oversized output accepted")
	}
	for _, input := range [][]byte{[]byte("M\x00path"), []byte("R100\x00old\x00"), []byte("M\x00../escape\x00"), []byte("Q\x00path\x00"), []byte("M\x00a\\b\x00"), []byte("M\x00.git/config\x00"), {'M', 0, 255, 0}} {
		if _, err := parseChanges(input); err == nil {
			t.Fatalf("accepted malformed inventory %q", input)
		}
	}
}

func FuzzParseChanges(f *testing.F) {
	f.Add([]byte("M\x00a.go\x00"))
	f.Add([]byte("R100\x00a.go\x00b.go\x00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		changes, err := parseChanges(data)
		if err != nil {
			return
		}
		for _, change := range changes {
			if validPath(change.Path) != nil || (change.OldPath != "" && validPath(change.OldPath) != nil) {
				t.Fatal("accepted unsafe path")
			}
		}
		if bytes.Contains(data, []byte("../")) && len(changes) == 0 && len(data) > 0 {
			t.Fatal("malformed nonempty inventory became empty")
		}
	})
}
