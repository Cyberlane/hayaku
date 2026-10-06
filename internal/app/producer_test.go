package app

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
)

func generatedAppFixture(t *testing.T) (string, string, string, model.Config) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows producer qualification is deferred")
	}
	root := t.TempDir()
	c := model.Config{Schema: model.Schema, Context: model.Context{ID: "generated-fixture", OS: runtime.GOOS, Arch: runtime.GOARCH},
		Workspaces: []model.Workspace{
			{ID: "left", Root: "left", Adapter: "command", Command: model.Command{Dir: ".", Executable: "/bin/sh", Args: []string{"check.sh"}}},
			{ID: "right", Root: "right", Adapter: "command", Command: model.Command{Dir: ".", Executable: "/bin/sh", Args: []string{"check.sh"}}},
		},
		Producers: []model.ProducerStage{{ID: "schema", Output: "generated", Command: model.Command{Dir: ".", Executable: "/bin/sh", Args: []string{"{source}/generate.sh", "{source}/schema", "{output}"}}}},
		Contracts: []model.Contract{{Input: "generated/value", Consumers: []string{"left", "right"}, Reason: "shared generated contract"}},
	}
	configuration, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	nativeAppWrite(t, root, "hayaku.json", string(configuration))
	nativeAppWrite(t, root, "schema", "base\n")
	nativeAppWrite(t, root, "generate.sh", "set -eu\ncat \"$1\" > \"$2/value\"\n")
	for _, workspace := range c.Workspaces {
		nativeAppWrite(t, root, workspace.Root+"/check.sh", "set -eu\ncmp ../schema ../generated/value\n")
	}
	nativeAppGit(t, root, "init", "-q")
	nativeAppGit(t, root, "config", "user.name", "Fixture")
	nativeAppGit(t, root, "config", "user.email", "fixture@example.test")
	nativeAppGit(t, root, "config", "commit.gpgsign", "false")
	base := nativeAppCommit(t, root, "base generation")
	nativeAppWrite(t, root, "schema", "candidate\n")
	candidate := nativeAppCommit(t, root, "candidate generation")
	return root, base, candidate, c
}

func TestGeneratedInputsIndependentPlanningAndFullExecution(t *testing.T) {
	root, base, candidate, c := generatedAppFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	plan, err := Build(ctx, root, base, candidate, c, true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != "full-fallback" || len(plan.Selected) != 2 || len(plan.Proposed) != 2 || plan.Generated == nil || len(plan.Generated.Base) != 1 || len(plan.Generated.Candidate) != 1 {
		t.Fatal("generated plan omitted required full consumers", plan.Mode)
	}
	if plan.Generated.Base[0].Revision != base || plan.Generated.Candidate[0].Revision != candidate || plan.Generated.Base[0].OutputDigest == plan.Generated.Candidate[0].OutputDigest || plan.Generated.Base[0].InputsDigest == plan.Generated.Candidate[0].InputsDigest {
		t.Fatal("BASE generation used candidate inputs")
	}
	changed, unknown := false, false
	for _, change := range plan.Changes {
		if change.Path == "generated/value" && change.Status == "M" {
			changed = true
		}
	}
	for _, gap := range plan.Gaps {
		if gap.Code == "producer-unqualified" {
			unknown = true
		}
	}
	if !changed || !unknown {
		t.Fatal("generated diff or unclosed producer influence missing")
	}
	run, err := Execute(ctx, root, c, plan)
	if err != nil || !run.Passed || len(run.Commands) != 2 {
		t.Fatal("full consumers could not read fresh candidate outputs", err)
	}
	if _, err := os.Stat(filepath.Join(root, "generated")); !os.IsNotExist(err) {
		t.Fatal("planning/execution wrote generated outputs into checkout", err)
	}
	if _, err := Observe(ctx, root, c, plan); err == nil || !strings.Contains(err.Error(), "producer") {
		t.Fatal("unsupported observation silently admitted", err)
	}
	if _, err := Shadow(ctx, root, c, plan); err == nil || !strings.Contains(err.Error(), "producer") {
		t.Fatal("unsupported shadow silently admitted", err)
	}
}

func TestGeneratedInputsNoChangesStillBroadens(t *testing.T) {
	root, _, candidate, c := generatedAppFixture(t)
	plan, err := Build(context.Background(), root, candidate, candidate, c, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 || plan.Generated == nil || len(plan.Proposed) != 2 || len(plan.Selected) != 2 || plan.Mode != "full-fallback" {
		t.Fatal("unchanged host producers silently authorized skipping")
	}
}

func TestGeneratedInputsFreshOutputMismatchStopsConsumers(t *testing.T) {
	root, _, candidate, c := generatedAppFixture(t)
	external := filepath.Join(t.TempDir(), "ambient-input")
	if err := os.WriteFile(external, []byte("candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nativeAppWrite(t, root, "generate.sh", "set -eu\ncat \"$3\" > \"$2/value\"\n")
	c.Producers[0].Command.Args = append(c.Producers[0].Command.Args, external)
	configuration, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	nativeAppWrite(t, root, "hayaku.json", string(configuration))
	candidate = nativeAppCommit(t, root, "ambient generator example")
	plan, err := Build(context.Background(), root, candidate, candidate, c, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(external, []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run, err := Execute(context.Background(), root, c, plan)
	if err == nil || !strings.Contains(err.Error(), "producer") || run.Passed || len(run.Commands) != 0 {
		t.Fatal("fresh mismatched generation reached consumers", err)
	}
}

func TestGeneratedInputsRejectCandidateCheckoutExecutable(t *testing.T) {
	root, base, candidate, c := generatedAppFixture(t)
	local := filepath.Join(root, "local-tool")
	if err := os.WriteFile(local, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	c.Producers[0].Command.Executable = local
	if _, err := Build(context.Background(), root, base, candidate, c, false); err == nil || !strings.Contains(err.Error(), "external") {
		t.Fatal("BASE could execute a candidate checkout tool", err)
	}
}

func TestGeneratedInputsCleanupFailureRejectsSuccessfulPlan(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("cleanup permission oracle requires a non-root macOS/Linux user")
	}
	root, _, _, c := generatedAppFixture(t)
	owned := t.TempDir()
	t.Setenv("TMPDIR", owned)
	// Repair only this test's task-owned temporary trees so the deliberately
	// failed cleanup does not leave a leak after its error has been asserted.
	t.Cleanup(func() {
		if err := filepath.WalkDir(owned, func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return os.Chmod(name, 0700)
			}
			return nil
		}); err != nil {
			t.Error(err)
		}
	})
	nativeAppWrite(t, root, "generate.sh", "set -eu\ncat \"$1\" > \"$2/value\"\nmkdir \"$2/readonly\"\nprintf keep > \"$2/readonly/member\"\nchmod 500 \"$2/readonly\"\n")
	candidate := nativeAppCommit(t, root, "readonly owned generated member")
	plan, err := Build(context.Background(), root, candidate, candidate, c, true)
	if err == nil || plan.Generated != nil || plan.Candidate != "" {
		t.Fatal("owned cleanup failure returned a successful plan", err)
	}
	var cleanup *os.PathError
	if !errors.As(err, &cleanup) || filepath.Base(cleanup.Path) != "member" || filepath.Base(filepath.Dir(cleanup.Path)) != "readonly" || cleanup.Op != "unlinkat" && cleanup.Op != "unlink" && cleanup.Op != "remove" {
		t.Fatal("failure did not originate in owned member cleanup", err)
	}
	caches, cacheErr := filepath.Glob(filepath.Join(root, ".git", "hayaku-cache", "*.json"))
	if cacheErr != nil {
		t.Fatal(cacheErr)
	}
	discovered := map[string]bool{}
	for _, name := range caches {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Evidence model.Evidence `json:"evidence"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatal(err)
		}
		discovered[envelope.Evidence.Workspace] = true
	}
	if !discovered["left"] || !discovered["right"] {
		t.Fatal("cleanup oracle did not reach successful consumer discovery")
	}
	entries, readErr := os.ReadDir(owned)
	if readErr != nil {
		t.Fatal(readErr)
	}
	leak := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "hayaku-producer-") || strings.HasPrefix(entry.Name(), "hayaku-generated-consumer-") {
			leak = true
		}
	}
	if !leak {
		t.Fatal("cleanup failure oracle did not retain a known owned directory")
	}
}
