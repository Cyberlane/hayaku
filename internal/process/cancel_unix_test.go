//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package process

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
)

// The helper's descendant signals readiness, then attempts to write after its
// parent is cancelled. The independently scheduled write probes actual cleanup.
func TestProcessTreeHelper(t *testing.T) {
	mode := os.Getenv("HAYAKU_PROCESS_TREE_HELPER")
	if mode == "" {
		return
	}
	if strings.HasPrefix(mode, "child") {
		if strings.HasPrefix(mode, "child-orphan") {
			parent, _ := strconv.Atoi(os.Getenv("HAYAKU_PROCESS_TREE_PARENT"))
			for os.Getppid() == parent {
				time.Sleep(10 * time.Millisecond)
			}
		}
		if err := os.WriteFile(os.Getenv("HAYAKU_PROCESS_TREE_PID"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(4)
		}
		if mode == "child-orphan-stdout" || mode == "child-orphan-stderr" {
			for {
				if _, err := os.Stat(os.Getenv("HAYAKU_PROCESS_TREE_TRIGGER")); err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			stream := os.Stdout
			if mode == "child-orphan-stderr" {
				stream = os.Stderr
			}
			_, _ = fmt.Fprint(stream, strings.Repeat("x", 257))
		}
		if strings.HasPrefix(mode, "child-orphan") {
			time.Sleep(time.Second)
		}
		time.Sleep(2 * time.Second)
		_ = os.WriteFile(os.Getenv("HAYAKU_PROCESS_TREE_MARKER"), []byte("survived"), 0600)
		os.Exit(0)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestProcessTreeHelper$")
	childMode := "child"
	if strings.HasPrefix(mode, "orphan") {
		childMode = "child-" + mode
	}
	child.Env = Environment(map[string]string{"HAYAKU_PROCESS_TREE_HELPER": childMode, "HAYAKU_PROCESS_TREE_PARENT": strconv.Itoa(os.Getpid())})
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(3)
	}
	if strings.HasPrefix(mode, "orphan") {
		if mode == "orphan-failure" {
			os.Exit(2)
		}
		os.Exit(0)
	}
	if mode == "stdout" || mode == "stderr" {
		for {
			if _, err := os.Stat(os.Getenv("HAYAKU_PROCESS_TREE_TRIGGER")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		stream := os.Stdout
		if mode == "stderr" {
			stream = os.Stderr
		}
		_, _ = fmt.Fprint(stream, strings.Repeat("x", 257))
	}
	_ = child.Wait()
	os.Exit(0)
}

func TestCancellationKillsRunnerDescendants(t *testing.T) {
	for _, cause := range []string{"parent", "stdout", "stderr", "orphan-cancel", "orphan-stdout", "orphan-stderr", "orphan-pipes", "orphan-failure"} {
		t.Run(cause, func(t *testing.T) { probeRunnerDescendants(t, cause) })
	}
}

func probeRunnerDescendants(t *testing.T, cause string) {
	t.Helper()
	dir := t.TempDir()
	marker, pidfile := filepath.Join(dir, "marker"), filepath.Join(dir, "pid")
	trigger := filepath.Join(dir, "trigger")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var output Output
	var runErr error
	go func() {
		output, runErr = RunEnvironmentLimits(ctx, model.Command{Executable: os.Args[0], Args: []string{"-test.run=^TestProcessTreeHelper$"}}, "", Environment(map[string]string{
			"HAYAKU_PROCESS_TREE_HELPER": cause, "HAYAKU_PROCESS_TREE_MARKER": marker, "HAYAKU_PROCESS_TREE_PID": pidfile, "HAYAKU_PROCESS_TREE_TRIGGER": trigger,
		}), 256, 256)
		close(done)
	}()
	deadline := time.After(10 * time.Second)
	var pid int
	for pid == 0 {
		if data, err := os.ReadFile(pidfile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
		select {
		case <-deadline:
			t.Fatal("runner did not start its descendant")
		case <-done:
			t.Fatalf("runner ended before cancellation: %v", runErr)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if cause == "parent" || cause == "orphan-cancel" {
		cancel()
	} else if cause != "orphan-pipes" && cause != "orphan-failure" {
		if err := os.WriteFile(trigger, []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("process-tree cancellation did not return")
	}
	// os/exec preserves a direct nonzero exit over its later pipe timeout.
	// It still reports that direct exit; the failed group's descendants must die.
	if runErr == nil || (output.Completed && cause != "orphan-failure") {
		t.Fatal("cancelled tree was accepted as completed")
	}
	if cause == "orphan-failure" && output.ExitCode != 2 {
		t.Fatal("original runner failure was lost")
	}
	if strings.HasSuffix(cause, "stdout") || strings.HasSuffix(cause, "stderr") {
		if runErr.Error() != "process output exceeded limit" {
			t.Fatalf("overflow did not cancel the tree: %v", runErr)
		}
	}
	// A killed orphan can briefly be a zombie before the host reaps it; wait for
	// the independently scheduled write deadline rather than assuming kill(0)
	// distinguishes running from zombie descendants on every supported Unix.
	timer := time.NewTimer(3200 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("descendant survived cancellation and wrote its marker")
	}
}
