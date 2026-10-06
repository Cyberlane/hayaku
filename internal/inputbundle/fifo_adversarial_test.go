package inputbundle

import (
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

func TestInputBundleFIFOAfterRegularPreflightRejectsInBoundedChild(t *testing.T) {
	const probe = "HAYAKU_INPUT_FIFO_PROBE"
	if os.Getenv(probe) == "1" {
		name := filepath.Join(t.TempDir(), "input")
		if err := os.WriteFile(name, []byte("abc"), 0600); err != nil {
			t.Fatal(err)
		}
		before, err := os.Lstat(name)
		if err != nil || !before.Mode().IsRegular() {
			t.Fatal("regular preflight control missing")
		}
		capture := &collector{ctx: context.Background(), b: &Bundle{request: Request{Limits: Limits{FileBytes: 16, Bytes: 32}}}}
		data, err := capture.readFile(name, before)
		if err != nil || string(data) != "abc" || capture.b.manifest.Bytes != 3 {
			t.Fatal("healthy regular bytes were not captured exactly")
		}
		fmt.Fprintln(os.Stdout, "regular-capture-ok")
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(name, 0600); err != nil {
			t.Fatal(err)
		}
		fifo, err := os.Lstat(name)
		if err != nil || fifo.Mode()&os.ModeNamedPipe == 0 {
			t.Fatal("actual FIFO replacement missing")
		}
		// The retained preflight is still the old regular file. No process opens
		// the actual replacement FIFO for writing; blocking open cannot finish.
		data, err = capture.readFile(name, before)
		if err == nil || data != nil || capture.b.manifest.Bytes != 3 {
			t.Fatal("FIFO replacement contributed capture bytes or succeeded")
		}
		fmt.Fprintln(os.Stdout, "fifo-capture-rejected")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// All child files stay under a parent-owned directory, including on timeout.
	root := t.TempDir()
	out, err := process.RunEnvironmentLimits(ctx, model.Command{
		Executable: os.Args[0], Args: []string{"-test.run=^TestInputBundleFIFOAfterRegularPreflightRejectsInBoundedChild$", "-test.v", "-test.count=1"},
	}, "", process.Environment(map[string]string{probe: "1", "TMPDIR": root}), 1024, 2048)
	if err != nil || !out.Completed || out.ExitCode != 0 || !strings.Contains(string(out.Stdout), "regular-capture-ok\n") || !strings.Contains(string(out.Stdout), "fifo-capture-rejected\n") {
		t.Fatalf("bounded capture child failed controls: completed=%t exit=%d error=%v", out.Completed, out.ExitCode, err)
	}
	t.Logf("actual stale-preflight FIFO rejected without a producer; isolated child exited in %s", out.Duration)
}
