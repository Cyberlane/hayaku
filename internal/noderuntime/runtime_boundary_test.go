package noderuntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

func boundaryFile(t *testing.T) (string, os.FileInfo, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input")
	payload := bytes.Repeat([]byte("bounded-runtime-input\n"), 4000)
	if err := os.WriteFile(path, payload, 0640); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, info, payload
}

func TestRuntimeBoundaryHealthyHashAndCopy(t *testing.T) {
	path, info, payload := boundaryFile(t)
	h := sha256.New()
	b := &budget{}
	if err := digestFile(context.Background(), path, info, h, b); err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256(append([]byte(fmt.Sprintf("file:%o:%d\n", info.Mode(), info.Size())), payload...))
	if !bytes.Equal(h.Sum(nil), expected[:]) || b.bytes != int64(len(payload)) {
		t.Fatal("healthy hash or byte accounting changed")
	}
	target := filepath.Join(t.TempDir(), "copy")
	if err := copyFile(context.Background(), path, target, info); err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(copied, payload) {
		t.Fatal("healthy copy differs")
	}
	copiedInfo, err := os.Stat(target)
	if err != nil || copiedInfo.Mode().Perm() != info.Mode().Perm() {
		t.Fatal("copy permissions differ")
	}
	if err := copyFile(context.Background(), path, target, info); err == nil {
		t.Fatal("existing destination overwritten")
	}
}

// Each child first admits a real regular input and then replaces it with an
// unwritten FIFO. The parent deadline makes a blocking regression observable.
func TestRuntimeBoundaryFIFOReplacement(t *testing.T) {
	if kind := os.Getenv("HAYAKU_RUNTIME_FIFO_CHILD"); kind != "" {
		path, info, _ := boundaryFile(t)
		if err := digestFile(context.Background(), path, info, sha256.New(), &budget{}); err != nil {
			t.Fatal(err)
		}
		if err := copyFile(context.Background(), path, filepath.Join(t.TempDir(), "healthy-copy"), info); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
		var err error
		if kind == "digest" {
			err = digestFile(context.Background(), path, info, sha256.New(), &budget{})
		} else {
			err = copyFile(context.Background(), path, filepath.Join(t.TempDir(), "copy"), info)
		}
		if err == nil {
			t.Fatal("FIFO accepted as regular input")
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"digest", "copy"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, err := process.Run(ctx, model.Command{Executable: executable, Args: []string{"-test.run=^TestRuntimeBoundaryFIFOReplacement$", "-test.count=1"}}, "", map[string]string{"HAYAKU_RUNTIME_FIFO_CHILD": kind, "TMPDIR": t.TempDir()})
			if err != nil || ctx.Err() != nil || !out.Completed || out.ExitCode != 0 {
				t.Fatalf("FIFO child failed or blocked: complete=%v exit=%d error=%v context=%v", out.Completed, out.ExitCode, err, ctx.Err())
			}
		})
	}
}

type boundaryWriter struct {
	buffer bytes.Buffer
	mutate func()
	once   bool
}

func (w *boundaryWriter) Write(p []byte) (int, error) {
	n, err := w.buffer.Write(p)
	if n > 0 && !w.once {
		w.once = true
		w.mutate()
	}
	return n, err
}

func TestRuntimeBoundaryDriftAfterConsumedBytes(t *testing.T) {
	for _, kind := range []string{"growth", "mode", "mtime", "replacement", "cancel", "cancel-and-growth"} {
		t.Run(kind, func(t *testing.T) {
			path, info, payload := boundaryFile(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var mutationErr error
			destination := &boundaryWriter{mutate: func() {
				switch kind {
				case "growth", "cancel-and-growth":
					f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
					if err != nil {
						mutationErr = err
						return
					}
					_, mutationErr = f.Write([]byte("growth"))
					mutationErr = errors.Join(mutationErr, f.Close())
				case "mode":
					mutationErr = os.Chmod(path, 0600)
				case "mtime":
					mutationErr = os.Chtimes(path, info.ModTime(), info.ModTime().Add(time.Second))
				case "replacement":
					replacement := filepath.Join(filepath.Dir(path), "replacement")
					mutationErr = os.WriteFile(replacement, payload, info.Mode().Perm())
					if mutationErr == nil {
						mutationErr = os.Chtimes(replacement, info.ModTime(), info.ModTime())
					}
					if mutationErr == nil {
						mutationErr = os.Rename(replacement, path)
					}
					if mutationErr == nil {
						current, statErr := os.Stat(path)
						content, readErr := os.ReadFile(path)
						if statErr != nil || readErr != nil || os.SameFile(info, current) || current.Size() != info.Size() || current.Mode() != info.Mode() || !current.ModTime().Equal(info.ModTime()) || !bytes.Equal(content, payload) {
							mutationErr = errors.New("replacement control did not preserve bytes and metadata while changing inode")
						}
					}
				}
				if strings.HasPrefix(kind, "cancel") {
					cancel()
				}
			}}
			err := readFile(ctx, path, info, destination)
			if mutationErr != nil {
				t.Fatal(mutationErr)
			}
			if !destination.once || destination.buffer.Len() == 0 {
				t.Fatal("mutation did not follow consumed healthy bytes")
			}
			if err == nil {
				t.Fatal("changed input accepted")
			}
			if destination.buffer.Len() > len(payload)+1 {
				t.Fatal("read exceeded admitted size plus sentinel")
			}
			if strings.HasPrefix(kind, "cancel") && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost")
			}
			if kind == "cancel-and-growth" && !strings.Contains(err.Error(), "runtime input changed during read") {
				t.Fatal("joined input drift lost on cancellation")
			}
		})
	}
}

func TestRuntimeBoundaryPreflightAndCancellation(t *testing.T) {
	path, info, _ := boundaryFile(t)
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := digestFile(context.Background(), path, info, sha256.New(), &budget{}); err == nil {
		t.Fatal("pre-open metadata drift accepted")
	}
	if err := copyFile(context.Background(), path, filepath.Join(t.TempDir(), "copy"), info); err == nil {
		t.Fatal("copy accepted pre-open metadata drift")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := digestFile(ctx, path, info, sha256.New(), &budget{}); !errors.Is(err, context.Canceled) {
		t.Fatal("hash lost cancellation")
	}
	target := filepath.Join(t.TempDir(), "cancelled")
	if err := copyFile(ctx, path, target, info); !errors.Is(err, context.Canceled) {
		t.Fatal("copy lost cancellation")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("cancelled copy created target")
	}
	if err := readFile(context.Background(), path, nil, io.Discard); err == nil {
		t.Fatal("nil preflight accepted")
	}
	b := &budget{bytes: MaxBytes - info.Size() + 1}
	if err := digestFile(context.Background(), path, info, sha256.New(), b); err == nil || b.bytes != MaxBytes-info.Size()+1 {
		t.Fatal("byte budget overflow admitted")
	}
}

func TestRuntimeBoundaryPublicFingerprintGolden(t *testing.T) {
	r := fixture(t)
	for path, mode := range map[string]os.FileMode{r.Node: 0755, r.Modules: 0755, filepath.Join(r.Modules, "package"): 0755, filepath.Join(r.Modules, "package", "index.js"): 0644} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Identity(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	// Independent SHA-256 golden for the historical file/directory framing of
	// this fixed fixture, rather than comparing two implementations to each other.
	want := Fingerprint{Digest: "07010cbbe919d5b583de79ead449d26f378696523487ea7921cfe47eefa65b85", NodeDigest: "cc915aab54b9751cbde07508c5489fd2a60930dc0eb17b3d461fded44719af45", ModulesDigest: "66e706d2fc595ad4c9fdb04956c3913b3149e66dc5f4195a9d8a0d1cd44d765d", Files: 3, Bytes: 44}
	if got != want {
		t.Fatalf("stable public runtime fingerprint changed: %v", got)
	}
}

type boundaryCancellation struct {
	context.Context
	observe func()
}

func (c boundaryCancellation) Err() error { c.observe(); return c.Context.Err() }

func TestRuntimeBoundaryStagedCopyCancellationCleanup(t *testing.T) {
	r := fixture(t)
	fingerprint, err := Identity(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	destination := filepath.Join(parent, "modules")
	foreign := filepath.Join(parent, "foreign")
	if err := os.WriteFile(foreign, []byte("foreign-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := false
	var observationErr error
	ctx := boundaryCancellation{Context: base, observe: func() {
		if observed || observationErr != nil {
			return
		}
		entries, err := os.ReadDir(parent)
		if err != nil {
			observationErr = err
			cancel()
			return
		}
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".hayaku-modules-") {
				continue
			}
			copied := filepath.Join(parent, entry.Name(), "package", "index.js")
			content, err := os.ReadFile(copied)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				observationErr = err
				cancel()
				return
			}
			if string(content) == "export const value = 1;" {
				observed = true
				cancel()
				return
			}
		}
	}}
	err = CopyModules(ctx, r, destination, fingerprint)
	if observationErr != nil {
		t.Fatal(observationErr)
	}
	if !observed || !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation did not follow actual staged module bytes")
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatal("cancelled staging became visible")
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".hayaku-modules-") {
			t.Fatal("owned staging remained after cancellation")
		}
	}
	content, err := os.ReadFile(foreign)
	if err != nil || string(content) != "foreign-bytes" {
		t.Fatal("foreign sibling changed during cleanup")
	}
}
