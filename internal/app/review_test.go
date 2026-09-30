package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/report"
)

// The helper is a hostile protocol fixture. It deliberately produces a valid
// terminal JSON prefix followed by a process-level bounded-output failure.
func init() {
	if os.Getenv("HAYAKU_REVIEW_PROCESS") != "" && len(os.Args) > 1 && os.Args[1] == "env" {
		cwd, _ := os.Getwd()
		payload, _ := json.Marshal(map[string]string{"GOVERSION": "go1.27.1", "GOMOD": filepath.Join(cwd, "go.mod"), "GOWORK": "off", "GOFLAGS": "", "GOOS": runtime.GOOS, "GOARCH": runtime.GOARCH})
		os.Stdout.Write(payload)
		os.Exit(0)
	}

	if os.Getenv("HAYAKU_REVIEW_PROCESS") == "sleeper" {
		time.Sleep(3 * time.Second)
		os.Exit(0)
	}
	if os.Getenv("HAYAKU_REVIEW_PROCESS") == "waitdelay" {
		_, _ = os.Stdout.Write([]byte("{\"Action\":\"start\",\"Package\":\"example.test/pkg\"}\n{\"Action\":\"pass\",\"Package\":\"example.test/pkg\"}\n"))
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "HAYAKU_REVIEW_PROCESS=sleeper")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if child.Start() != nil {
			os.Exit(7)
		}
		os.Exit(0)
	}
	if os.Getenv("HAYAKU_REVIEW_PROCESS") != "overflow" {
		if os.Getenv("HAYAKU_REVIEW_PROCESS") == "pause" {
			_ = os.WriteFile(os.Getenv("HAYAKU_REVIEW_MARKER"), []byte("started"), 0600)
			time.Sleep(200 * time.Millisecond)
			os.Exit(0)
		}
		return
	}
	_, _ = os.Stdout.Write([]byte("{\"Action\":\"start\",\"Package\":\"example.test/pkg\"}\n{\"Action\":\"pass\",\"Package\":\"example.test/pkg\"}\n"))
	_, _ = os.Stderr.Write(bytes.Repeat([]byte("x"), 64<<20))
	os.Exit(0)
}

func TestReviewShadowRejectsInheritedContextDrift(t *testing.T) {
	root, base, _, c := gitFixture(t)
	checkout := exec.Command("git", "checkout", "-q", "--detach", base)
	checkout.Dir = root
	if output, err := checkout.CombinedOutput(); err != nil {
		t.Fatalf("fixture checkout: %v %s", err, output)
	}
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "started")
	c.Context.Env = map[string]string{"HAYAKU_REVIEW_PROCESS": "pause", "HAYAKU_REVIEW_MARKER": marker}
	c.Workspaces[0].Prerequisites = []model.Command{{Dir: ".", Executable: current}}
	t.Setenv("HAYAKU_REVIEW_ENV_DRIFT", "before")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := Build(ctx, root, base, base, c, false)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() {
		for ctx.Err() == nil {
			if _, err := os.Stat(marker); err == nil {
				done <- os.Setenv("HAYAKU_REVIEW_ENV_DRIFT", "after") == nil
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		done <- false
	}()
	result, err := Shadow(ctx, root, c, p)
	cancel()
	if !<-done {
		t.Fatal("fixture did not change inherited environment")
	}
	if err == nil || result.ComparisonValid {
		t.Fatalf("context drift produced a valid shadow comparison: valid=%v err=%v", result.ComparisonValid, err)
	}
}

func TestReviewRelativeExecutableUsesCommandDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture shell script uses Unix executable naming")
	}
	root, base, _, c := gitFixture(t)
	if err := os.Mkdir(filepath.Join(root, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "test"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = root
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git fixture failed: %v %s", err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("add", "scripts/test")
	git("commit", "-qm", "add original suite wrapper")
	candidate := git("rev-parse", "HEAD")
	c.Workspaces = []model.Workspace{{ID: "suite", Root: ".", Adapter: "command", Command: model.Command{Dir: "scripts", Executable: "./test"}}}
	if _, err := Build(context.Background(), root, base, candidate, c, false); err != nil {
		t.Fatalf("valid command-relative suite wrapper cannot be planned: %v", err)
	}
}

func TestReviewExplicitGoFlagsCannotBeSilentlyDiscarded(t *testing.T) {
	root, base, candidate, c := gitFixture(t)
	c.Context.Env = map[string]string{"GOFLAGS": "-tags=integration"}
	_, err := Build(context.Background(), root, base, candidate, c, false)
	if err == nil {
		t.Fatal("nonempty GOFLAGS accepted while effective suite flags are silently cleared")
	}
	if !strings.Contains(err.Error(), "GOFLAGS") {
		t.Fatalf("hidden scope flags need an actionable diagnostic: %v", err)
	}
}

func TestReviewPersistedGoFlagsCannotBeSilentlyDiscarded(t *testing.T) {
	root, base, candidate, c := gitFixture(t)
	goenv := filepath.Join(t.TempDir(), "goenv")
	if err := os.WriteFile(goenv, []byte("GOFLAGS=-tags=integration\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c.Context.Env = map[string]string{"GOENV": goenv}
	_, err := Build(context.Background(), root, base, candidate, c, false)
	if err == nil {
		t.Fatal("persisted GOFLAGS accepted while GOENV is silently disabled")
	}
	if !strings.Contains(err.Error(), "GOFLAGS") && !strings.Contains(err.Error(), "GOENV") {
		t.Fatalf("persisted scope flags need an actionable diagnostic: %v", err)
	}
}

func TestReviewSavedPlanPositiveRoundTrip(t *testing.T) {
	root, base, _, _ := gitFixture(t)
	root, canonicalErr := filepath.EvalSymlinks(root)
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}
	checkout := exec.Command("git", "checkout", "-q", "--detach", base)
	checkout.Dir = root
	if output, err := checkout.CombinedOutput(); err != nil {
		t.Fatalf("fixture checkout: %v %s", err, output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	planfile := filepath.Join(t.TempDir(), "plan.json")
	var out, errout bytes.Buffer
	if status := Run(ctx, []string{"plan", "--root", root, "--base", base, "--candidate", base, "--output", planfile}, &out, &errout); status != 0 {
		t.Fatalf("valid plan generation failed: %s", errout.String())
	}
	f, err := os.Open(planfile)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := report.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	c, err := readConfig(root, "hayaku.json")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := Build(ctx, root, base, base, c, false)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Equal(fresh, saved) {
		t.Fatalf("saved plan wire contract differs from fresh plan (%v)", report.Differences(fresh, saved))
	}
	out.Reset()
	errout.Reset()
	if status := Run(ctx, []string{"run", "--root", root, "--plan", planfile}, &out, &errout); status != 0 {
		t.Fatalf("valid saved plan rejected or full suite failed: %s", errout.String())
	}
	var result Execution
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Passed || len(result.Commands) != 1 || !result.Commands[0].Complete {
		t.Fatal("valid saved plan did not execute complete full suite")
	}
}

func TestReviewBuildRootAliasesPreservePlanIdentity(t *testing.T) {
	root, base, candidate, c := gitFixture(t)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "repository")
	if err := os.Symlink(realRoot, alias); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, realRoot)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	reference, err := Build(ctx, realRoot, base, candidate, c, false)
	if err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]string{"symlink": alias, "relative": relative} {
		t.Run(name, func(t *testing.T) {
			plan, err := Build(ctx, input, base, candidate, c, false)
			if err != nil {
				t.Fatal(err)
			}
			if !report.Equal(reference, plan) {
				t.Fatalf("same physical repository produced different plan identity (%v)", report.Differences(reference, plan))
			}
		})
	}
}

func TestReviewPinnedLauncherEnvironmentPreservesContextIdentity(t *testing.T) {
	root, _, _, c := gitFixture(t)
	c.Context.Env = map[string]string{"SHLVL": "1", "XPC_SERVICE_NAME": "0"}
	t.Setenv("SHLVL", "2")
	t.Setenv("XPC_SERVICE_NAME", "launcher-before")
	t.Setenv("HAYAKU_REVIEW_CONTEXT_INPUT", "before")
	before := ContextDigest(c.Context)
	nativeBefore, err := EffectiveContextDigest(context.Background(), root, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHLVL", "9")
	t.Setenv("XPC_SERVICE_NAME", "launcher-after")
	if before != ContextDigest(c.Context) {
		t.Fatal("explicit launcher environment overrides did not stabilize ambient context")
	}
	nativeAfter, err := EffectiveContextDigest(context.Background(), root, c)
	if err != nil {
		t.Fatal(err)
	}
	if nativeBefore != nativeAfter {
		t.Fatal("explicit launcher overrides did not stabilize native context")
	}
	t.Setenv("HAYAKU_REVIEW_CONTEXT_INPUT", "after")
	if before == ContextDigest(c.Context) {
		t.Fatal("unconfigured input drift was omitted from ambient binding")
	}
	nativeChanged, err := EffectiveContextDigest(context.Background(), root, c)
	if err != nil {
		t.Fatal(err)
	}
	if nativeBefore == nativeChanged {
		t.Fatal("unconfigured input drift was omitted from native binding")
	}
}

func TestReviewOutputOverflowCannotBeCompleteGoOutcome(t *testing.T) {
	reviewIncompleteProcess(t, "overflow")
}

func TestReviewWaitDelayCannotBeCompleteGoOutcome(t *testing.T) {
	reviewIncompleteProcess(t, "waitdelay")
}

func reviewIncompleteProcess(t *testing.T, mode string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("helper uses Unix executable naming")
	}
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(t.TempDir(), "go")
	if err := os.Link(current, tool); err != nil {
		t.Fatal(err)
	}
	c := model.Config{Schema: model.Schema, Context: model.Context{OS: runtime.GOOS, Arch: runtime.GOARCH, Env: map[string]string{"HAYAKU_REVIEW_PROCESS": mode}}, Workspaces: []model.Workspace{{ID: "go", Root: ".", Adapter: "go", Command: model.Command{Dir: ".", Executable: tool, Args: []string{"test", "."}}}}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var result Execution
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/pkg\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err = runWorkspaces(ctx, root, c, []model.Unit{{ID: "package", Workspace: "go", Selector: "example.test/pkg", Kind: "go-package"}}, false, true, &result, nil)
	if err == nil {
		t.Fatalf("bounded-output failure became a successful run: %+v", result.Commands)
	}
	if len(result.Commands) != 1 || result.Commands[0].Complete {
		t.Fatalf("failed native process presented as complete outcome: %+v", result.Commands)
	}
}
