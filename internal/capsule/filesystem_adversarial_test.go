package capsule

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

func TestCapsuleFileCaptureStalePreflight(t *testing.T) {
	for _, fault := range []string{"growth", "replacement", "mode", "mtime", "budget", "cancelled"} {
		t.Run(fault, func(t *testing.T) {
			dir := t.TempDir()
			name := filepath.Join(dir, "input")
			if err := os.WriteFile(name, []byte("abc"), 0600); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			before, err := root.Lstat("input")
			if err != nil {
				t.Fatal(err)
			}
			data, err := loadRegularFile(context.Background(), root, "input", before, MaxInputBytes)
			if err != nil || string(data) != "abc" {
				t.Fatal("known healthy capture unavailable", err)
			}
			ctx := context.Background()
			remaining := int64(MaxInputBytes)
			switch fault {
			case "growth":
				if err := os.Truncate(name, 64<<20); err != nil {
					t.Fatal(err)
				}
			case "replacement":
				if err := os.WriteFile(name+".new", []byte("abc"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(name+".new", name); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(name, 0640); err != nil {
					t.Fatal(err)
				}
			case "mtime":
				if err := os.Chtimes(name, before.ModTime(), before.ModTime().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			case "budget":
				remaining = 2
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if data, err := loadRegularFile(ctx, root, "input", before, remaining); err == nil || data != nil {
				t.Fatal("stale preflight retained captured bytes")
			}
		})
	}
}

func TestCapsuleFileCaptureFIFOReplacement(t *testing.T) {
	const fixture = "HAYAKU_CAPSULE_FIFO_ROOT"
	if dir := os.Getenv(fixture); dir != "" {
		name := filepath.Join(dir, "input")
		if err := os.WriteFile(name, []byte("abc"), 0600); err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		before, err := root.Lstat("input")
		if err != nil {
			t.Fatal(err)
		}
		data, err := loadRegularFile(context.Background(), root, "input", before, MaxInputBytes)
		if err != nil || string(data) != "abc" {
			t.Fatal("regular confined control unavailable", err)
		}
		fmt.Fprintln(os.Stdout, "regular-control-ok")
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(name, 0600); err != nil {
			t.Fatal(err)
		}
		// Retain the original regular FileInfo. No writer can unblock a bad open.
		if data, err := loadRegularFile(context.Background(), root, "input", before, MaxInputBytes); err == nil || data != nil {
			t.Fatal("FIFO substitution retained captured bytes")
		}
		fmt.Fprintln(os.Stdout, "fifo-rejected")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := process.RunEnvironmentLimits(ctx, model.Command{Executable: os.Args[0], Args: []string{"-test.run=^TestCapsuleFileCaptureFIFOReplacement$", "-test.count=1", "-test.v"}}, "", process.Environment(map[string]string{fixture: t.TempDir(), "TMPDIR": t.TempDir()}), 1024, 2048)
	if err != nil || !out.Completed || out.ExitCode != 0 || !strings.Contains(string(out.Stdout), "regular-control-ok\n") || !strings.Contains(string(out.Stdout), "fifo-rejected\n") {
		t.Fatal("bounded confined FIFO child failed", err)
	}
	t.Logf("actual no-writer FIFO child finished in %s", out.Duration)
}

type captureFaultWriter struct{ write func([]byte) (int, error) }

func (w captureFaultWriter) Write(data []byte) (int, error) { return w.write(data) }
func TestCapsuleFileCaptureAfterHealthyRead(t *testing.T) {
	for _, fault := range []string{"bytes", "growth", "replacement", "mode", "mtime", "removal"} {
		t.Run(fault, func(t *testing.T) {
			dir := t.TempDir()
			name := filepath.Join(dir, "input")
			if err := os.WriteFile(name, []byte("abc"), 0600); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			before, err := root.Lstat("input")
			if err != nil {
				t.Fatal(err)
			}
			data, err := loadRegularFile(context.Background(), root, "input", before, MaxInputBytes)
			if err != nil || string(data) != "abc" {
				t.Fatal("healthy read control unavailable", err)
			}
			mutated := false
			var captured bytes.Buffer
			mutate := func() {
				mutated = true
				switch fault {
				case "bytes":
					if err := os.WriteFile(name, []byte("xyz"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Chtimes(name, before.ModTime(), before.ModTime().Add(time.Second)); err != nil {
						t.Fatal(err)
					}
				case "growth":
					if err := os.Truncate(name, 64<<20); err != nil {
						t.Fatal(err)
					}
				case "replacement":
					if err := os.WriteFile(name+".new", []byte("abc"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(name+".new", name); err != nil {
						t.Fatal(err)
					}
				case "mode":
					if err := os.Chmod(name, 0640); err != nil {
						t.Fatal(err)
					}
				case "mtime":
					if err := os.Chtimes(name, before.ModTime(), before.ModTime().Add(time.Second)); err != nil {
						t.Fatal(err)
					}
				case "removal":
					if err := os.Remove(name); err != nil {
						t.Fatal(err)
					}
				}
			}
			writer := captureFaultWriter{write: func(part []byte) (int, error) {
				n, err := captured.Write(part)
				if !mutated {
					if captured.String() != "abc" {
						t.Fatal("fault preceded the independently expected healthy read")
					}
					mutate()
				}
				return n, err
			}}
			err = copyRegularFile(context.Background(), root, "input", before, MaxInputBytes, writer)
			if !mutated || err == nil {
				t.Fatal("post-read source drift retained bytes or fault never occurred")
			}
		})
	}
}
