package pilot

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/metrics"
)

func TestPilotRejectsUnboundedPackageAndImplicitCache(t *testing.T) {
	for _, pkg := range []string{"./...", "../outside", "./a/../b", "-flag", "./", "./a\\b"} {
		if _, err := Run(context.Background(), t.TempDir(), pkg, "cache"); err == nil {
			t.Fatalf("accepted %q", pkg)
		}
	}
	if _, err := Run(context.Background(), t.TempDir(), "./internal/graph", ""); err == nil {
		t.Fatal("implicit cache accepted")
	}
}

func pilotShellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func pilotWrite(t *testing.T, name, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func pilotGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture Git failed: %v: %s", err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestPilotCompilerPreflightCannotAutoFetch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("compiler instrumentation requires a POSIX launcher")
	}
	private := t.TempDir()
	log := filepath.Join(private, "compiler-env")
	// No Git executable is available in this PATH and root is not a repository.
	// A failed version preflight must stop before snapshotting or compilation.
	launcher := "#!/bin/sh\nprintf '%s\\n' \"$*\" \"$GOTOOLCHAIN\" \"$GOPROXY\" \"$GOSUMDB\" \"$GOWORK\" > " + pilotShellLiteral(log) + "\nexit 9\n"
	pilotWrite(t, filepath.Join(private, "go"), launcher, 0700)
	t.Setenv("PATH", private)
	t.Setenv("GOTOOLCHAIN", "auto")
	t.Setenv("GOPROXY", "https://invalid.example.test")
	t.Setenv("GOSUMDB", "sum.golang.org")
	t.Setenv("GOWORK", filepath.Join(private, "ambient.go.work"))
	_, err := Run(context.Background(), t.TempDir(), "./pure", filepath.Join(private, "passes"))
	if err == nil || !strings.Contains(err.Error(), "compiler version is unavailable") {
		t.Fatalf("compiler preflight did not fail early: %v", err)
	}
	got, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if want := "version\nlocal\noff\noff\noff\n"; string(got) != want {
		t.Fatalf("version invocation retained ambient fetch/workspace settings: %q", got)
	}
	if _, err := os.Stat(filepath.Join(private, "passes")); !os.IsNotExist(err) {
		t.Fatal("failed preflight created a reuse cache")
	}
}

func TestPilotRealGoWASICompilesTwiceAndMeasuresWholeContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("compiler instrumentation requires a POSIX launcher")
	}
	compiler, err := exec.LookPath("go")
	if err != nil {
		t.Fatal("pilot fixture requires the installed Go compiler")
	}
	compiler, err = filepath.Abs(compiler)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	pilotWrite(t, filepath.Join(root, "go.mod"), "module example.test/hayaku-pilot\n\ngo 1.24.0\n", 0600)
	pilotWrite(t, filepath.Join(root, "pure", "sum.go"), "package pure\nfunc Sum(n int) int { result := 0; for i := 0; i <= n; i++ { result += i }; return result }\n", 0600)
	pilotWrite(t, filepath.Join(root, "pure", "sum_test.go"), "package pure\nimport \"testing\"\nfunc TestPureArithmetic(t *testing.T) { if got := Sum(10000); got != 50005000 { t.Fatalf(\"sum = %d\", got) } }\n", 0600)
	pilotGit(t, root, "init", "-q")
	pilotGit(t, root, "config", "user.name", "Fixture")
	pilotGit(t, root, "config", "user.email", "fixture@example.test")
	pilotGit(t, root, "config", "commit.gpgsign", "false")
	pilotGit(t, root, "add", ".")
	pilotGit(t, root, "commit", "-qm", "pure WASI contract")

	private := t.TempDir()
	log := filepath.Join(private, "compiler-calls")
	// This transparent launcher observes cache state, then invokes the actual
	// installed compiler. Its log is outside both the source and pass cache.
	launcher := fmt.Sprintf(`#!/bin/sh
if [ "$1" = version ]; then
  printf 'version\n' >> %s
  exec %s "$@"
fi
before=absent
if [ -e "$GOCACHE" ]; then before=present; fi
%s "$@"
status=$?
after=absent
if [ -d "$GOCACHE" ]; then after=present; fi
printf 'build\t%%s\t%%s\t%%s\t%%s\t%%s\n' "$GOCACHE" "$before" "$after" "$status" "$(pwd)" >> %s
exit "$status"
`, pilotShellLiteral(log), pilotShellLiteral(compiler), pilotShellLiteral(compiler), pilotShellLiteral(log))
	pilotWrite(t, filepath.Join(private, "go"), launcher, 0700)
	ambientCache := filepath.Join(private, "ambient-cache")
	pilotWrite(t, filepath.Join(ambientCache, "sentinel"), "must remain untouched", 0600)
	t.Setenv("GOCACHE", ambientCache)
	t.Setenv("PATH", private+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	started := time.Now()
	result, err := Run(ctx, root, "./pure", filepath.Join(private, "passes"))
	elapsed := time.Since(started).Nanoseconds()
	if err != nil {
		t.Fatalf("real Go/WASI pilot failed: %v", err)
	}
	if result.Contract != "separate-deterministic-go-wasi-suite-v1" || result.CompilerCache != "independent-fresh" {
		t.Fatalf("native equivalence or upstream cache accounting claimed: %+v", result)
	}
	if !result.Comparison.Valid || result.Baseline.Context != result.Candidate.Context {
		t.Fatalf("fresh independent compilation changed experiment identity: %+v", result.Comparison)
	}
	if result.BaselineResult.Mode != "executed" || result.CandidateResult.Mode != "reused" || result.BaselineResult.Key != result.CandidateResult.Key {
		t.Fatalf("expected one fresh guest and one authenticated reuse: before=%+v after=%+v", result.BaselineResult, result.CandidateResult)
	}
	for _, guest := range []struct{ complete, qualified, passed bool }{
		{result.BaselineResult.Complete, result.BaselineResult.Qualified, result.BaselineResult.Passed},
		{result.CandidateResult.Complete, result.CandidateResult.Qualified, result.CandidateResult.Passed},
	} {
		if !guest.complete || !guest.qualified || !guest.passed {
			t.Fatal("pure separate WASI suite did not qualify")
		}
	}
	if before, after := result.Comparison.Baseline, result.Comparison.Candidate; before.Executed != 1 || before.HayakuReused != 0 || after.Executed != 0 || after.HayakuReused != 1 || before.UpstreamCached != 0 || after.UpstreamCached != 0 {
		t.Fatalf("incorrect executed/reused/cache counts: before=%+v after=%+v", before, after)
	}
	if before, after := result.Comparison.Baseline, result.Comparison.Candidate; before.RunnerNanos <= 0 || after.RunnerNanos != 0 || before.HayakuOverheadNanos+before.RunnerNanos != before.WallNanos || after.HayakuOverheadNanos != after.WallNanos {
		t.Fatalf("compilation, validation or reuse overhead was excluded or credited as guest savings: before=%+v after=%+v", before, after)
	}
	for _, measurement := range []metrics.Run{result.Baseline, result.Candidate} {
		var end int64
		for _, phase := range measurement.Phases {
			if phase.StartNanos != end || phase.DurationNanos <= 0 {
				t.Fatalf("measurement lost or overlapped a wall interval: %+v", measurement)
			}
			end += phase.DurationNanos
		}
		last := measurement.Phases[len(measurement.Phases)-1]
		if end != measurement.WallNanos || last.ID != "final-validation-and-cleanup" || last.Kind != "hayaku" {
			t.Fatalf("final source validation or cleanup excluded from cost: %+v", measurement)
		}
	}
	if result.Baseline.WallNanos+result.Candidate.WallNanos > elapsed || result.Comparison.NetSavingsNanos != result.Baseline.WallNanos-result.Candidate.WallNanos {
		t.Fatal("comparison substituted summed test time or clamped wall costs")
	}
	observations, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var caches, snapshots []string
	versions := 0
	for _, line := range strings.Split(strings.TrimSpace(string(observations)), "\n") {
		if line == "version" {
			versions++
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 6 || fields[0] != "build" || fields[2] != "absent" || fields[3] != "present" || fields[4] != "0" {
			t.Fatalf("compiler did not perform a real build from a fresh cache: %q", line)
		}
		caches = append(caches, fields[1])
		snapshots = append(snapshots, fields[5])
	}
	if versions != 2 || len(caches) != 2 || caches[0] == caches[1] || snapshots[0] == snapshots[1] {
		t.Fatalf("measurements shared compiler preflight, cache or snapshot: %q", observations)
	}
	for _, name := range append(caches, snapshots...) {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatalf("temporary compiler cache or immutable snapshot survived measurement: %s", name)
		}
	}
	entries, err := os.ReadDir(ambientCache)
	if err != nil || len(entries) != 1 || entries[0].Name() != "sentinel" {
		t.Fatal("ambient upstream compilation cache was used")
	}
	if got := pilotGit(t, root, "status", "--porcelain=v1", "--untracked-files=all", "--ignored=matching"); got != "" {
		t.Fatalf("pilot modified original source tree: %s", got)
	}

	// A reuse can still cost more overall. Preserve that regression when the
	// same actual measurement has additional final validation/cleanup overhead.
	costly := result.Candidate
	costly.Phases = append([]metrics.Phase(nil), costly.Phases...)
	extra := result.Baseline.WallNanos + int64(time.Second)
	costly.WallNanos += extra
	costly.Phases[len(costly.Phases)-1].DurationNanos += extra
	comparison, err := metrics.Compare(result.Baseline, costly)
	if err != nil || !comparison.Valid || comparison.NetSavingsNanos >= 0 || comparison.NetSavingsNanos != result.Baseline.WallNanos-costly.WallNanos {
		t.Fatalf("reuse hid a net wall-cost regression: %+v %v", comparison, err)
	}
}
