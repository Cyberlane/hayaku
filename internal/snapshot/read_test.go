package snapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

func TestReadFileHealthyRawBytesAndEmpty(t *testing.T) {
	for _, data := range [][]byte{[]byte("abc\x00def\n"), {}} {
		name := filepath.Join(t.TempDir(), "raw")
		if err := os.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
		got, err := ReadFile(t.Context(), name, 1024)
		if err != nil || got == nil || !bytes.Equal(got, data) || len(got) != cap(got) {
			t.Fatalf("exact admitted bytes: length=%d capacity=%d error=%v", len(got), cap(got), err)
		}
	}
}

func TestReadFileCallerAndCommonLimits(t *testing.T) {
	name, _ := adversarialDigestFile(t, 3)
	if data, err := ReadFile(t.Context(), name, 3); err != nil || string(data) != "abc" {
		t.Fatalf("healthy exact caller limit: %v", err)
	}
	for _, limit := range []int64{0, -1, 2} {
		if data, err := ReadFile(t.Context(), name, limit); err == nil || data != nil {
			t.Fatalf("invalid/smaller caller limit admitted bytes: %d %v", len(data), err)
		}
	}
	large, _ := adversarialDigestFile(t, maxBlob+1)
	for _, limit := range []int64{maxBlob, maxBlob + 1, maxTree} {
		if data, err := ReadFile(t.Context(), large, limit); err == nil || data != nil {
			t.Fatal("caller limit enlarged the common materialized byte budget")
		}
	}
	for _, fault := range []string{"symlink", "directory", "missing", "canceled"} {
		t.Run(fault, func(t *testing.T) {
			path := name
			ctx := t.Context()
			switch fault {
			case "symlink":
				path = filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(name, path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				path = t.TempDir()
			case "missing":
				path = filepath.Join(t.TempDir(), "missing")
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			data, err := ReadFile(ctx, path, 1024)
			if err == nil || data != nil || (fault == "canceled" && !errors.Is(err, context.Canceled)) {
				t.Fatalf("read veto retained data or lost cancellation: %d %v", len(data), err)
			}
		})
	}
}

func TestReadFileAdmittedAllocation(t *testing.T) {
	name, _ := adversarialDigestFile(t, maxBlob)
	var start, finish runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&start)
	data, err := ReadFile(t.Context(), name, maxTree)
	runtime.ReadMemStats(&finish)
	if err != nil || int64(len(data)) != maxBlob || len(data) != cap(data) || string(data[:3]) != "abc" || data[len(data)-1] != 0 {
		t.Fatalf("real maximum sparse-file control failed: size=%d error=%v", len(data), err)
	}
	// One admitted 64 MiB result plus generous runtime overhead distinguishes
	// the fixed buffer from a growing bytes.Buffer and a copied final result.
	if allocated := finish.TotalAlloc - start.TotalAlloc; allocated > uint64(maxBlob+(8<<20)) {
		t.Fatalf("materialized reader exceeded one admitted allocation: %d", allocated)
	}
	runtime.KeepAlive(data)
}

func TestReadFileSharedGuardRejectsAfterActualRead(t *testing.T) {
	for _, fault := range []string{"growth", "replacement", "mode", "mtime", "alias", "canceled"} {
		t.Run(fault, func(t *testing.T) {
			name, before := adversarialDigestFile(t, 96<<10)
			if data, err := ReadFile(t.Context(), name, 128<<10); err != nil || int64(len(data)) != before.Size() {
				t.Fatalf("healthy public reader control: %v", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			mutated := false
			probe := &adversarialDigestWriter{once: func() {
				mutated = true
				switch fault {
				case "growth":
					if err := os.Truncate(name, maxTree); err != nil {
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
				case "alias":
					if err := os.Rename(name, name+".old"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(name+".old", name); err != nil {
						t.Fatal(err)
					}
				case "canceled":
					cancel()
				}
			}}
			buffer := &admittedBytes{bytes: make([]byte, int(before.Size()))}
			read, err := digestFileWithLimit(ctx, name, before, io.MultiWriter(buffer, probe), 128<<10, 128<<10)
			if err == nil || !mutated || probe.bytes == 0 || read != probe.bytes || read > before.Size()+1 || int64(len(buffer.bytes)) != before.Size() || cap(buffer.bytes) != len(buffer.bytes) {
				t.Fatalf("bounded actual-read fault: triggered=%v count=%d read=%d error=%v", mutated, probe.bytes, read, err)
			}
			if fault == "growth" && read != before.Size()+1 {
				t.Fatal("growth sentinel was not included in consumed-count rejection")
			}
			if fault == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal("shared final cancellation lost its typed cause")
			}
		})
	}
}

func TestReadFileSharedGuardPreservesWriterErrorAndCancellation(t *testing.T) {
	name, before := adversarialDigestFile(t, 3)
	if data, err := ReadFile(t.Context(), name, 3); err != nil || string(data) != "abc" {
		t.Fatalf("healthy control: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fault := errors.New("independent destination failure")
	writer := &readErrorWriter{cancel: cancel, failure: fault}
	_, err := digestFileWithLimit(ctx, name, before, writer, 3, 3)
	if !writer.actualWrite || !errors.Is(err, fault) || !errors.Is(err, context.Canceled) {
		t.Fatalf("final shared guard discarded typed read/destination/cancellation causes: %v", err)
	}
}

type readErrorWriter struct {
	cancel      context.CancelFunc
	failure     error
	actualWrite bool
}

func (w *readErrorWriter) Write(data []byte) (int, error) {
	w.actualWrite = len(data) != 0
	w.cancel()
	return 0, w.failure
}

func TestReadFileFIFOReplacement(t *testing.T) {
	const probeRoot = "HAYAKU_SNAPSHOT_READ_FIFO_ROOT"
	if root := os.Getenv(probeRoot); root != "" {
		name := filepath.Join(root, "input")
		if err := os.WriteFile(name, []byte("abc"), 0600); err != nil {
			t.Fatal(err)
		}
		before, err := os.Lstat(name)
		if err != nil {
			t.Fatal(err)
		}
		if data, err := ReadFile(t.Context(), name, 3); err != nil || string(data) != "abc" {
			t.Fatalf("healthy public read control: %v", err)
		}
		fmt.Fprintln(os.Stdout, "read-regular-control-ok")
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(name, 0600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(name)
		if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
			t.Fatal("actual FIFO control unavailable")
		}
		if data, err := ReadFile(t.Context(), name, 3); err == nil || data != nil {
			t.Fatal("public reader admitted FIFO bytes")
		}
		buffer := &admittedBytes{bytes: make([]byte, 3)}
		if _, err := digestFileWithLimit(t.Context(), name, before, buffer, 3, 3); err == nil || buffer.written != 0 {
			t.Fatal("stale regular-file preflight admitted or blocked on substituted FIFO")
		}
		fmt.Fprintln(os.Stdout, "read-fifo-rejected")
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	out, err := process.RunEnvironmentLimits(ctx, model.Command{Executable: os.Args[0], Args: []string{"-test.run=^TestReadFileFIFOReplacement$", "-test.v", "-test.count=1"}}, "", process.Environment(map[string]string{probeRoot: t.TempDir()}), 1024, 2048)
	if err != nil || !out.Completed || out.ExitCode != 0 || !strings.Contains(string(out.Stdout), "read-regular-control-ok\n") || !strings.Contains(string(out.Stdout), "read-fifo-rejected\n") {
		t.Fatalf("bounded FIFO child did not exit/join healthy and fault markers: completed=%v exit=%d error=%v", out.Completed, out.ExitCode, err)
	}
}
