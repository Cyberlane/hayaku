package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

func adversarialDigestFile(t *testing.T, size int64) (string, fs.FileInfo) {
	t.Helper()
	name := filepath.Join(t.TempDir(), "input")
	file, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0640)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("abc"); err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, 0640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	return name, before
}

func replaceAdversarialDigestFile(t *testing.T, name string, before fs.FileInfo) {
	t.Helper()
	// Keep the old inode alive so replacement cannot reuse its identity.
	if err := os.Rename(name, name+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("abc"), before.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(name, before.Size()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, before.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(name, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(name)
	if err != nil || os.SameFile(before, after) || after.Size() != before.Size() || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("replacement control did not preserve metadata and change identity: %v", err)
	}
}

func TestDigestAdversarialHealthyBytesAndStreamingAllocation(t *testing.T) {
	name, before := adversarialDigestFile(t, 3)
	hash := sha256.New()
	n, err := digestFile(context.Background(), name, before, hash, maxTree)
	if err != nil || n != 3 || hex.EncodeToString(hash.Sum(nil)) != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("healthy known-byte digest: %d %v", n, err)
	}

	large, largeInfo := adversarialDigestFile(t, maxBlob)
	var start, finish runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&start)
	n, err = digestFile(context.Background(), large, largeInfo, io.Discard, maxTree)
	runtime.ReadMemStats(&finish)
	if err != nil || n != maxBlob {
		t.Fatalf("healthy maximum-size sparse file: %d %v", n, err)
	}
	// The helper hashes a real 64 MiB file. An 8 MiB allowance tolerates runtime
	// bookkeeping while distinguishing streaming from whole-file allocation.
	if allocated := finish.TotalAlloc - start.TotalAlloc; allocated > 8<<20 {
		t.Fatalf("bounded streaming allocated %d bytes for a %d-byte file", allocated, maxBlob)
	}
}

func TestDigestAdversarialPreflightDriftAndBudget(t *testing.T) {
	for _, fault := range []string{"growth", "replacement", "mode", "mtime", "removed", "budget", "canceled"} {
		t.Run(fault, func(t *testing.T) {
			name, before := adversarialDigestFile(t, 3)
			if _, err := digestFile(context.Background(), name, before, io.Discard, maxTree); err != nil {
				t.Fatalf("healthy control rejected: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			remaining := int64(maxTree)
			switch fault {
			case "growth":
				if err := os.Truncate(name, maxBlob+1); err != nil {
					t.Fatal(err)
				}
			case "replacement":
				replaceAdversarialDigestFile(t, name, before)
			case "mode":
				if err := os.Chmod(name, 0600); err != nil {
					t.Fatal(err)
				}
			case "mtime":
				changed := before.ModTime().Add(time.Hour)
				if err := os.Chtimes(name, changed, changed); err != nil {
					t.Fatal(err)
				}
			case "removed":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
			case "budget":
				remaining = 2
			case "canceled":
				cancel()
			}
			writer := &adversarialDigestWriter{}
			_, err := digestFile(ctx, name, before, writer, remaining)
			if err == nil || writer.bytes != 0 {
				t.Fatalf("preflight fault consumed bytes or succeeded: %d %v", writer.bytes, err)
			}
			if fault == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation identity lost: %v", err)
			}
		})
	}
}

type adversarialDigestWriter struct {
	bytes int64
	once  func()
}

func (w *adversarialDigestWriter) Write(data []byte) (int, error) {
	w.bytes += int64(len(data))
	if w.once != nil {
		mutate := w.once
		w.once = nil
		mutate()
	}
	return len(data), nil
}

func TestDigestAdversarialDriftAfterActualRead(t *testing.T) {
	for _, fault := range []string{"growth", "replacement", "mode", "mtime", "canceled"} {
		t.Run(fault, func(t *testing.T) {
			name, before := adversarialDigestFile(t, 96<<10)
			if _, err := digestFile(context.Background(), name, before, io.Discard, maxTree); err != nil {
				t.Fatalf("healthy control rejected: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			mutated := false
			writer := &adversarialDigestWriter{once: func() {
				mutated = true
				switch fault {
				case "growth":
					if err := os.Truncate(name, maxBlob+1); err != nil {
						t.Fatal(err)
					}
				case "replacement":
					replaceAdversarialDigestFile(t, name, before)
				case "mode":
					if err := os.Chmod(name, 0600); err != nil {
						t.Fatal(err)
					}
				case "mtime":
					changed := before.ModTime().Add(time.Hour)
					if err := os.Chtimes(name, changed, changed); err != nil {
						t.Fatal(err)
					}
				case "canceled":
					cancel()
				}
			}}
			_, err := digestFile(ctx, name, before, writer, maxTree)
			if !mutated || writer.bytes == 0 || writer.bytes > before.Size()+1 || err == nil {
				t.Fatalf("actual-read mutation was not rejected within read bound: mutated=%t bytes=%d error=%v", mutated, writer.bytes, err)
			}
			if fault == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("mid-read cancellation identity lost: %v", err)
			}
		})
	}
}

func TestDigestAdversarialDirectoryDriftAfterHealthyFileRead(t *testing.T) {
	for _, fault := range []string{"addition", "deletion", "mode"} {
		t.Run(fault, func(t *testing.T) {
			name, before := adversarialDigestFile(t, 3)
			directory := filepath.Join(filepath.Dir(name), "nested")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			control := filepath.Join(directory, "existing")
			if err := os.WriteFile(control, []byte("control"), 0600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(directory)
			if err != nil {
				t.Fatal(err)
			}
			directories := map[string]fs.FileInfo{directory: info}
			if err := verifyDigestEntries(context.Background(), directories); err != nil {
				t.Fatalf("healthy nested directory rejected: %v", err)
			}
			writer := &adversarialDigestWriter{once: func() {
				var err error
				switch fault {
				case "addition":
					err = os.WriteFile(filepath.Join(directory, "added"), []byte("new"), 0600)
				case "deletion":
					err = os.Remove(control)
				case "mode":
					err = os.Chmod(directory, 0750)
				}
				if err != nil {
					t.Fatal(err)
				}
			}}
			n, err := digestFile(context.Background(), name, before, writer, maxTree)
			if err != nil || n != 3 || writer.bytes != 3 || writer.once != nil {
				t.Fatalf("unchanged file control failed during directory mutation: %d %v", n, err)
			}
			if err := verifyDigestEntries(context.Background(), directories); err == nil {
				t.Fatal("detectable nested directory drift accepted")
			}
		})
	}
}

func TestDigestAdversarialEarlierFileDriftDuringLaterHealthyRead(t *testing.T) {
	for _, fault := range []string{"bytes", "inode", "size", "mode", "mtime"} {
		t.Run(fault, func(t *testing.T) {
			first, firstInfo := adversarialDigestFile(t, 3)
			directory := filepath.Dir(first)
			later := filepath.Join(directory, "later")
			if err := os.WriteFile(later, []byte("def"), 0600); err != nil {
				t.Fatal(err)
			}
			laterInfo, err := os.Lstat(later)
			if err != nil {
				t.Fatal(err)
			}
			directoryInfo, err := os.Lstat(directory)
			if err != nil {
				t.Fatal(err)
			}
			var replacement string
			if fault == "inode" {
				replacement = filepath.Join(t.TempDir(), "replacement")
				if err := os.WriteFile(replacement, []byte("abc"), firstInfo.Mode().Perm()); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(replacement, firstInfo.Mode().Perm()); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(replacement, firstInfo.ModTime(), firstInfo.ModTime()); err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(replacement)
				if err != nil || os.SameFile(firstInfo, info) {
					t.Fatalf("replacement does not have a distinct inode: %v", err)
				}
			}
			admitted := map[string]fs.FileInfo{first: firstInfo, later: laterInfo, directory: directoryInfo}
			firstHash, laterHash := sha256.New(), sha256.New()
			if n, err := digestFile(context.Background(), first, firstInfo, firstHash, maxTree); err != nil || n != 3 {
				t.Fatalf("first healthy read: %d %v", n, err)
			}
			if n, err := digestFile(context.Background(), later, laterInfo, laterHash, maxTree); err != nil || n != 3 {
				t.Fatalf("later healthy read: %d %v", n, err)
			}
			if hex.EncodeToString(firstHash.Sum(nil)) != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" || verifyDigestEntries(context.Background(), admitted) != nil {
				t.Fatal("healthy two-file baseline failed")
			}
			laterDigest := hex.EncodeToString(laterHash.Sum(nil))
			laterHash.Reset()
			writer := &adversarialDigestWriter{once: func() {
				var err error
				switch fault {
				case "bytes":
					err = os.WriteFile(first, []byte("xyz"), firstInfo.Mode().Perm())
				case "inode":
					err = os.Rename(replacement, first)
					if err == nil {
						err = os.Chtimes(directory, directoryInfo.ModTime(), directoryInfo.ModTime())
					}
				case "size":
					err = os.Truncate(first, 4)
				case "mode":
					err = os.Chmod(first, 0600)
				case "mtime":
					changed := firstInfo.ModTime().Add(time.Hour)
					err = os.Chtimes(first, changed, changed)
				}
				if err != nil {
					t.Fatal(err)
				}
			}}
			n, err := digestFile(context.Background(), later, laterInfo, io.MultiWriter(laterHash, writer), maxTree)
			if err != nil || n != 3 || writer.bytes != 3 || writer.once != nil || hex.EncodeToString(laterHash.Sum(nil)) != laterDigest {
				t.Fatalf("later file stopped being a healthy independent control: %d %v", n, err)
			}
			if err := verifyDigestEntries(context.Background(), map[string]fs.FileInfo{directory: directoryInfo}); err != nil {
				t.Fatalf("parent directory changed and masked the earlier-file fault: %v", err)
			}
			if err := verifyDigestEntries(context.Background(), admitted); err == nil {
				t.Fatal("earlier file drift accepted after a later healthy read")
			}
		})
	}
}

func TestDigestAdversarialFIFOReplacement(t *testing.T) {
	const probeRoot = "HAYAKU_DIGEST_FIFO_PROBE_ROOT"
	if root := os.Getenv(probeRoot); root != "" {
		name := filepath.Join(root, "input")
		if err := os.WriteFile(name, []byte("abc"), 0600); err != nil {
			t.Fatal(err)
		}
		before, err := os.Lstat(name)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.New()
		if n, err := digestFile(context.Background(), name, before, hash, maxTree); err != nil || n != 3 || hex.EncodeToString(hash.Sum(nil)) != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
			t.Fatalf("regular-file control failed: %d %v", n, err)
		}
		fmt.Fprintln(os.Stdout, "regular-control-ok")
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(name, 0600); err != nil {
			t.Fatal(err)
		}
		fifo, err := os.Lstat(name)
		if err != nil || fifo.Mode()&os.ModeNamedPipe == 0 {
			t.Fatalf("actual FIFO fixture missing: %v", err)
		}
		// No process opens the FIFO for writing. Blocking open would therefore
		// hang until the parent cancels and kills this isolated process group.
		if _, err := digestFile(context.Background(), name, before, io.Discard, maxTree); err == nil {
			t.Fatal("FIFO substituted for a regular file was accepted")
		}
		fmt.Fprintln(os.Stdout, "fifo-rejected")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := process.RunEnvironmentLimits(ctx, model.Command{
		Executable: os.Args[0], Args: []string{"-test.run=^TestDigestAdversarialFIFOReplacement$", "-test.v", "-test.count=1"},
	}, "", process.Environment(map[string]string{probeRoot: t.TempDir()}), 1024, 2048)
	if err != nil || !out.Completed || out.ExitCode != 0 || !strings.Contains(string(out.Stdout), "regular-control-ok\n") || !strings.Contains(string(out.Stdout), "fifo-rejected\n") {
		t.Fatalf("bounded FIFO child did not finish both controls: completed=%t exit=%d error=%v", out.Completed, out.ExitCode, err)
	}
	t.Logf("actual FIFO rejected without a producer; isolated child exited in %s", out.Duration)
}
