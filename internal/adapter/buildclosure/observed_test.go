package buildclosure

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

func TestObservedStageHelper(t *testing.T) {
	if os.Getenv("HAYAKU_OBSERVED_STAGE_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) != 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) != 3 {
		os.Exit(2)
	}
	source, build := args[1], args[2]
	data, err := os.ReadFile(filepath.Join(source, "input"))
	if err != nil || os.WriteFile(filepath.Join(build, "report"), []byte("native product"), 0600) != nil {
		os.Exit(3)
	}
	control := string(data)
	stdout, stderr := "12345678", "abcdefgh"
	if control == "stdout" {
		stdout = "87654321"
	}
	if control == "stderr" {
		stderr = "hgfedcba"
	}
	if control == "stdout-overflow" {
		stdout += "x"
	}
	if control == "stderr-overflow" {
		stderr += "x"
	}
	fmt.Fprint(os.Stdout, stdout)
	fmt.Fprint(os.Stderr, stderr)
	switch control {
	case "fail":
		os.Exit(7)
	case "drift", "cancel-drift":
		if os.WriteFile(filepath.Join(source, "input"), []byte("changed"), 0600) != nil {
			os.Exit(4)
		}
		if control == "cancel-drift" {
			if os.WriteFile(filepath.Join(build, "ready"), nil, 0600) != nil {
				os.Exit(5)
			}
			time.Sleep(time.Minute)
		}
		os.Exit(7)
	case "tool-alias":
		target, err := filepath.EvalSymlinks(os.Args[0])
		if err != nil || os.Remove(os.Args[0]) != nil || os.Symlink(target, os.Args[0]) != nil {
			os.Exit(6)
		}
		os.Exit(7)
	case "pass", "stdout", "stderr", "stdout-overflow", "stderr-overflow":
		os.Exit(0)
	default:
		os.Exit(9)
	}
}

func observedSession(t *testing.T, control string, alias bool) (*Session, model.Command) {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "input"), []byte(control), 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if alias {
		link := filepath.Join(t.TempDir(), "tool")
		if err := os.Symlink(exe, link); err != nil {
			t.Fatal(err)
		}
		exe = link
	}
	s, err := New(t.Context(), Request{Source: source, Revision: "observed-fixture", Environment: []string{"HAYAKU_OBSERVED_STAGE_HELPER=1"}, Tools: []string{exe}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		err := s.Close()
		wantGuard := control == "drift" || control == "cancel-drift" || control == "tool-alias"
		if wantGuard {
			if err == nil || s.private != "" {
				t.Error("deliberate input drift lost its final guard or owned cleanup", err)
			}
		} else if err != nil {
			t.Error(err)
		}
	})
	return s, model.Command{Executable: exe, Dir: "{build}", Args: []string{"-test.run=^TestObservedStageHelper$", "--", "{source}", "{build}"}}
}

func TestObservedNativeFailurePreservesExactExitAndSealedReports(t *testing.T) {
	for _, control := range []string{"pass", "fail"} {
		t.Run(control, func(t *testing.T) {
			s, cmd := observedSession(t, control, false)
			originalSource, originalBuild := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(originalSource, "input"), []byte(control), 0600); err != nil {
				t.Fatal(err)
			}
			originalCmd := cmd
			originalCmd.Dir = originalBuild
			originalCmd.Args = []string{"-test.run=^TestObservedStageHelper$", "--", originalSource, originalBuild}
			want, originalErr := process.RunEnvironmentLimits(t.Context(), originalCmd, originalBuild, []string{"HAYAKU_OBSERVED_STAGE_HELPER=1"}, 8, 8)
			if !want.Completed || (originalErr != nil) != (control == "fail") {
				t.Fatal("original native exit control unavailable", originalErr)
			}
			for attempt, id := range []string{"first", "retry"} {
				out, err := s.ObserveNative(t.Context(), id, cmd, 8, 8)
				if !out.Completed || out.ExitCode != want.ExitCode || string(out.Stdout) != string(want.Stdout) || string(out.Stderr) != string(want.Stderr) || (control == "fail" && err != ErrNativeFailure) || (control == "pass" && err != nil) {
					t.Fatal("observed exit/streams differ from actual original or failure lost exact sentinel", err)
				}
				data, err := s.ReadFile(t.Context(), "build", "report", 32)
				if err != nil || string(data) != "native product" {
					t.Fatal("completed native report generation unavailable", err)
				}
				records, _, err := s.Records(t.Context())
				if err != nil || len(records) != attempt+1 || records[attempt].ID != id || records[attempt].InvocationDigest == "" {
					t.Fatal("completed failure did not retain independently guarded stage record", err)
				}
			}
		})
	}
}

func TestObservedStageProvenanceBindsStatusStreamsAndLimits(t *testing.T) {
	seen := map[string]bool{}
	for _, fixture := range []struct {
		control string
		limit   int
	}{{"pass", 8}, {"fail", 8}, {"stdout", 8}, {"stderr", 8}, {"pass", 9}} {
		s, cmd := observedSession(t, fixture.control, false)
		out, err := s.ObserveNative(t.Context(), "same-command", cmd, fixture.limit, fixture.limit)
		if !out.Completed || (fixture.control == "fail" && err != ErrNativeFailure) || (fixture.control != "fail" && err != nil) {
			t.Fatal("provenance fixture did not complete original native status", err)
		}
		records, _, err := s.Records(t.Context())
		if err != nil || len(records) != 1 || seen[records[0].InvocationDigest] {
			t.Fatal("identical command/report products hid changed native status, streams or limits", err)
		}
		seen[records[0].InvocationDigest] = true
	}
}

func TestObservedStageAdmissionAndDuplicateIdentity(t *testing.T) {
	s, cmd := observedSession(t, "pass", false)
	for _, limits := range [][2]int{{0, 8}, {8, -1}, {32<<20 + 1, 8}, {8, 32<<20 + 1}} {
		if out, err := s.ObserveNative(t.Context(), "invalid", cmd, limits[0], limits[1]); err == nil || out.Completed {
			t.Fatal("invalid output bounds started usable native observation")
		}
	}
	if out, err := s.ObserveNative(t.Context(), "", cmd, 8, 8); err == nil || out.Completed {
		t.Fatal("unrecorded native observation admitted")
	}
	if out, err := s.ObserveNative(t.Context(), "once", cmd, 8, 8); err != nil || !out.Completed {
		t.Fatal("invalid admission consumed or broke the healthy session", err)
	}
	if out, err := s.ObserveNative(t.Context(), "once", cmd, 8, 8); err == nil || out.Completed {
		t.Fatal("same stage identity observed twice")
	}
	if records, _, err := s.Records(t.Context()); err != nil || len(records) != 1 {
		t.Fatal("duplicate preflight destroyed completed observed evidence", err)
	}
}

func TestObservedStageOverflowAndPostFailureDriftInvalidate(t *testing.T) {
	for _, control := range []string{"stdout-overflow", "stderr-overflow", "drift", "tool-alias"} {
		t.Run(control, func(t *testing.T) {
			s, cmd := observedSession(t, control, control == "tool-alias")
			out, err := s.ObserveNative(t.Context(), "invalid", cmd, 8, 8)
			if err == nil || err == ErrNativeFailure || out.Completed {
				t.Fatal("overflow or postguard error became usable native failure", err)
			}
			if (control == "drift" || control == "tool-alias") && (out.ExitCode != 7 || !errors.Is(err, ErrNativeFailure)) {
				t.Fatal("postguard discarded the independently completed native failure", err)
			}
			if _, _, err := s.Records(t.Context()); err == nil {
				t.Fatal("unsafe observed reports remained usable")
			}
		})
	}
	// A report sealed after a completed failure remains subject to fresh guards.
	s, cmd := observedSession(t, "fail", false)
	if out, err := s.ObserveNative(t.Context(), "failed", cmd, 8, 8); err != ErrNativeFailure || !out.Completed {
		t.Fatal("fresh native failure control unavailable", err)
	}
	if err := os.WriteFile(filepath.Join(s.Build(), "report"), []byte("forged product"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Records(t.Context()); err == nil {
		t.Fatal("native failure exempted report bytes from final verification")
	}
}

func TestObservedStageCancellationRetainsActualSourceDrift(t *testing.T) {
	s, cmd := observedSession(t, "cancel-drift", false)
	build := s.Build()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	joined := false
	type result struct {
		completed bool
		err       error
	}
	done := make(chan result, 1)
	go func() {
		out, err := s.ObserveNative(ctx, "canceled", cmd, 8, 8)
		done <- result{completed: out.Completed, err: err}
	}()
	defer func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Error("canceled observed stage did not join before fixture cleanup")
			}
		}
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
ready:
	for {
		select {
		case <-deadline.C:
			t.Fatal("real observed child did not reach mutation checkpoint")
		case <-tick.C:
			if _, err := os.Stat(filepath.Join(build, "ready")); err == nil {
				break ready
			}
		}
	}
	cancel()
	select {
	case got := <-done:
		joined = true
		if got.completed || !errors.Is(got.err, context.Canceled) || !strings.Contains(got.err.Error(), "input") {
			t.Fatal("observed cancellation masked actual source drift", got.completed, got.err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("observed canceled child exceeded bounded join")
	}
	if _, _, err := s.Records(t.Context()); err == nil {
		t.Fatal("interrupted observed generation remained usable")
	}
}

func TestObservedStageDoesNotWeakenSuccessfulBuildContract(t *testing.T) {
	s, cmd := observedSession(t, "fail", false)
	out, err := s.RunProducer(t.Context(), "build", cmd)
	if err == nil || out.Completed || errors.Is(err, ErrNativeFailure) {
		t.Fatal("ordinary build API accepted native failure as a valid stage", err)
	}
	if _, _, err := s.Records(t.Context()); err == nil {
		t.Fatal("ordinary unsuccessful build supplied observed report authority")
	}
}
