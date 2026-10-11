package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/passcache"
)

func coordinatorFixture(t *testing.T) (string, RunConfig) {
	t.Helper()
	root := t.TempDir()
	return root, RunConfig{Config: Config{Schema: 1, Platform: "darwin", Architecture: "arm64"}, Workspaces: []Workspace{
		{Workspace: model.Workspace{ID: "first", Root: ".", Adapter: "command", Command: model.Command{Dir: ".", Executable: "/usr/bin/true"}}},
		{Workspace: model.Workspace{ID: "second", Root: ".", Adapter: "command", Command: model.Command{Dir: ".", Executable: "/usr/bin/true"}}},
	}}
}

func TestWorkspaceValidatesAllScopesBeforeExecution(t *testing.T) {
	for _, mutation := range []func(*RunConfig){
		func(c *RunConfig) { c.Workspaces[1].ID = c.Workspaces[0].ID },
		func(c *RunConfig) { c.Workspaces[1].Root = "../escape" },
		func(c *RunConfig) { c.Workspaces[1].Command.Dir = "../escape" },
		func(c *RunConfig) { c.Workspaces[1].Command.Executable = "" },
		func(c *RunConfig) { c.Packages = []string{"."} },
		func(c *RunConfig) { c.Workspaces[1].Vitest = &VitestRuntimeInputs{} },
		func(c *RunConfig) { c.Environment = map[string]string{"broken=key": "value"} },
	} {
		root, c := coordinatorFixture(t)
		mutation(&c)
		called := false
		providers := map[string]workspaceProvider{"command": func(context.Context, string, Workspace, RunConfig, ExecutionOptions) (Report, error) {
			called = true
			return Report{}, nil
		}}
		report, err := runWorkspaces(context.Background(), root, c, ExecutionOptions{}, providers)
		if err == nil || called || report.Complete || report.Passed {
			t.Fatal("invalid later scope admitted execution", err, called, report)
		}
	}
}

func TestWorkspaceContinuesAfterAssertionFailureAndRetainsOwners(t *testing.T) {
	root, c := coordinatorFixture(t)
	var events []string
	providers := map[string]workspaceProvider{"command": func(_ context.Context, _ string, w Workspace, _ RunConfig, _ ExecutionOptions) (Report, error) {
		events = append(events, "run:"+w.ID)
		passed := w.ID != "first"
		var err error
		if !passed {
			err = errors.New("assertion failed")
		}
		return Report{Complete: true, Passed: passed, Groups: []Result{{Name: w.ID + "::suite", Executed: true, Complete: true, Passed: passed}}, finalize: func(ctx context.Context) error {
			if ctx.Err() != nil {
				t.Fatal("finalizer inherited canceled invocation")
			}
			events = append(events, "finish:"+w.ID)
			return nil
		}}, err
	}}
	report, err := runWorkspaces(context.Background(), root, c, ExecutionOptions{}, providers)
	if err == nil || !report.Complete || report.Passed || len(report.Groups) != 2 {
		t.Fatal("failed scope prevented independent execution or changed aggregate", report, err)
	}
	if !reflect.DeepEqual(events, []string{"run:first", "run:second", "finish:first", "finish:second"}) {
		t.Fatal("owners released before last workspace", events)
	}
}

func TestWorkspaceLaterDriftAndCleanupFailureInvalidateEarlierPasses(t *testing.T) {
	root, c := coordinatorFixture(t)
	var changed, closed bool
	providers := map[string]workspaceProvider{"command": func(_ context.Context, _ string, w Workspace, _ RunConfig, _ ExecutionOptions) (Report, error) {
		if w.ID == "second" {
			changed = true
		}
		return Report{Complete: true, Passed: true, Groups: []Result{{Name: w.ID + "::group", Key: identity(w.ID), Qualified: true, Executed: true, Complete: true, Passed: true}}, finalize: func(context.Context) error {
			closed = true
			if w.ID == "first" && changed {
				return errors.New("earlier producer input changed")
			}
			return nil
		}}, nil
	}}
	report, err := runWorkspaces(context.Background(), root, c, ExecutionOptions{}, providers)
	if err == nil || report.Complete || report.Passed || !closed || len(report.Groups) != 2 {
		t.Fatal("late drift retained aggregate success", report, err)
	}
	for _, group := range report.Groups {
		if group.Complete || group.Passed || group.Qualified {
			t.Fatal("late drift retained qualified pass", group)
		}
	}
}

func TestWorkspaceFallbackRejectsChangedDirectoryAndFailedPrerequisite(t *testing.T) {
	for _, fault := range []string{"redirect", "failure"} {
		t.Run(fault, func(t *testing.T) {
			root, c := coordinatorFixture(t)
			outside := t.TempDir()
			cwd := filepath.Join(root, "cwd")
			if err := os.Mkdir(cwd, 0700); err != nil {
				t.Fatal(err)
			}
			c.Workspaces = c.Workspaces[:1]
			w := &c.Workspaces[0]
			w.Command = model.Command{Dir: "cwd", Executable: "/bin/sh", Args: []string{"-c", "printf ran > main-marker"}}
			if fault == "redirect" {
				w.Prerequisites = []model.Command{{Dir: ".", Executable: "/bin/sh", Args: []string{"-c", "mv \"$1\" \"$1.original\" && ln -s \"$2\" \"$1\"", "fixture", cwd, outside}}}
			} else {
				w.Prerequisites = []model.Command{{Dir: ".", Executable: "/usr/bin/false"}}
			}
			report, err := RunWithOptions(context.Background(), root, c, ExecutionOptions{})
			if err == nil || report.Complete || report.Passed {
				t.Fatal("redirected or failed prerequisite passed", report, err)
			}
			for _, path := range []string{filepath.Join(cwd, "main-marker"), filepath.Join(outside, "main-marker")} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("main ran after prerequisite fault", path, err)
				}
			}
		})
	}
}

func TestWorkspaceLateFailureRevokesOwnReceiptsAndRetainsSharedAudit(t *testing.T) {
	for _, audit := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "audit"}[audit], func(t *testing.T) {
			root, c := coordinatorFixture(t)
			cacheDir := filepath.Join(t.TempDir(), "passes")
			issuer, verifier := nativePortableAuthority(t)
			oldKey, newKey, foreignKey := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
			// Signed synthetic records exercise common cleanup, not qualification.
			store := func(domain, key string) {
				cache, err := passcache.OpenWithAuthority(cacheDir, domain, issuer)
				if err != nil {
					t.Fatal(err)
				}
				if err := cache.Store(context.Background(), key, true); err != nil {
					t.Fatal(err)
				}
				if _, status := cache.Load(key); status != "hit" {
					t.Fatal("signed cleanup control unavailable", status)
				}
				if err := cache.Close(); err != nil {
					t.Fatal(err)
				}
			}
			store("hayaku-native-pure-pass-v1", oldKey)
			store("foreign-workspace-control", foreignKey)
			foreignPath := filepath.Join(cacheDir, foreignKey+".json")
			before, err := os.ReadFile(foreignPath)
			if err != nil {
				t.Fatal(err)
			}
			var shared *passcache.Audit
			calls := 0
			providers := map[string]workspaceProvider{"command": func(_ context.Context, _ string, w Workspace, _ RunConfig, options ExecutionOptions) (Report, error) {
				calls++
				if audit {
					if options.AuditHandle == nil {
						t.Fatal("workspace lost invocation audit")
					}
					if shared == nil {
						shared = options.AuditHandle
					} else if shared != options.AuditHandle {
						t.Fatal("workspaces created independent audits")
					}
					if _, err := os.Stat(filepath.Join(cacheDir, oldKey+".json")); !os.IsNotExist(err) {
						t.Fatal("audit retained previous own history", err)
					}
				}
				if w.ID == "first" {
					store("hayaku-native-pure-pass-v1", newKey)
				}
				key := identity(w.ID)
				if w.ID == "first" {
					key = newKey
				}
				return Report{Complete: true, Passed: true, Groups: []Result{{Name: w.ID + "::group", Key: key, Executed: true, Qualified: true, Complete: true, Passed: true}}, finalize: func(context.Context) error {
					if w.ID == "first" {
						return errors.New("retained producer cleanup failed")
					}
					return nil
				}}, nil
			}}
			report, err := runWorkspaces(context.Background(), root, c, ExecutionOptions{CacheDir: cacheDir, Authority: verifier, Audit: audit}, providers)
			if err == nil || report.Complete || report.Passed || calls != 2 {
				t.Fatal("late cleanup retained aggregate authority", report, err, calls)
			}
			if _, err := os.Stat(filepath.Join(cacheDir, newKey+".json")); !os.IsNotExist(err) {
				t.Fatal("late failure retained own issued receipt", err)
			}
			after, err := os.ReadFile(foreignPath)
			if err != nil || string(after) != string(before) {
				t.Fatal("late failure changed foreign receipt", err)
			}
		})
	}
}

func TestWorkspaceCancellationJoinsEarlierOwnerCleanup(t *testing.T) {
	root, c := coordinatorFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls, cleanups int
	providers := map[string]workspaceProvider{"command": func(context.Context, string, Workspace, RunConfig, ExecutionOptions) (Report, error) {
		calls++
		cancel()
		return Report{Complete: true, Passed: true, Groups: []Result{{Name: "first::group", Executed: true, Complete: true, Passed: true}}, finalize: func(ctx context.Context) error {
			cleanups++
			return ctx.Err()
		}}, nil
	}}
	report, err := runWorkspaces(ctx, root, c, ExecutionOptions{}, providers)
	if err == nil || report.Complete || report.Passed || calls != 1 || cleanups != 1 {
		t.Fatal("cancelled batch lost cleanup or executed later scope", report, err, calls, cleanups)
	}
}

func TestWorkspaceInvalidEnvironmentOnlyAuditRevokesOwnedHistory(t *testing.T) {
	issuer, verifier := nativePortableAuthority(t)
	cacheDir := filepath.Join(t.TempDir(), "passes")
	own, foreign := strings.Repeat("a", 64), strings.Repeat("c", 64)
	for domain, key := range map[string]string{"hayaku-native-pure-pass-v1": own, "another-provider": foreign} {
		cache, err := passcache.OpenWithAuthority(cacheDir, domain, issuer)
		if err != nil {
			t.Fatal(err)
		}
		if err = cache.Store(context.Background(), key, map[string]bool{"synthetic": true}); err != nil {
			t.Fatal(err)
		}
		if err = cache.Close(); err != nil {
			t.Fatal(err)
		}
	}
	foreignPath := filepath.Join(cacheDir, foreign+".json")
	before, err := os.ReadFile(foreignPath)
	if err != nil {
		t.Fatal(err)
	}
	report, err := RunWithOptions(context.Background(), t.TempDir(), RunConfig{Config: Config{Schema: 1}, Environment: map[string]string{"CI": "true"}}, ExecutionOptions{CacheDir: cacheDir, Authority: verifier, Audit: true})
	if err == nil || report.Complete || report.Passed {
		t.Fatal("invalid scope passed", report, err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, own+".json")); !os.IsNotExist(err) {
		t.Fatal("invalid audit retained owned history", err)
	}
	after, err := os.ReadFile(foreignPath)
	if err != nil || string(after) != string(before) {
		t.Fatal("invalid audit changed foreign history", err)
	}
}

func TestWorkspaceRejectsContradictoryProviderCompletion(t *testing.T) {
	for _, bad := range []Report{
		{Complete: true, Passed: true},
		// Namespace admission is independent of the lifecycle controls below.
		{Complete: true, Passed: true, Groups: []Result{{Name: "unit", Complete: true, Passed: true}}},
		{Complete: true, Passed: true, Groups: []Result{{Name: "first::unit", Complete: true, Passed: true}}},
		{Complete: true, Passed: true, Groups: []Result{{Name: "first::unit", Executed: true, Reused: true, Complete: true, Passed: true}}},
		{Complete: true, Passed: true, Groups: []Result{{Name: "first::unit", Reused: true, Complete: true, Passed: true}}},
		{Complete: true, Passed: true, Groups: []Result{{Name: "first::unit", Executed: true, Complete: true, Passed: true}, {Name: "first::unit", Executed: true, Complete: true, Passed: true}}},
		{Complete: true, Passed: true, Groups: []Result{{Name: "first::unit", Qualified: true, Executed: true, Complete: true, Passed: true}}, finalize: func(context.Context) error { return nil }},
		{Complete: true, Passed: true, Groups: []Result{{Name: "first::unit", Key: identity("key"), Qualified: true, Executed: true, Complete: true, Passed: true}}},
		{Complete: true, Passed: true, Groups: []Result{{Name: "first::one", Key: identity("key"), Qualified: true, Executed: true, Complete: true, Passed: true}, {Name: "first::two", Key: identity("key"), Qualified: true, Executed: true, Complete: true, Passed: true}}, finalize: func(context.Context) error { return nil }},
	} {
		root, c := coordinatorFixture(t)
		c.Workspaces = c.Workspaces[:1]
		providers := map[string]workspaceProvider{"command": func(context.Context, string, Workspace, RunConfig, ExecutionOptions) (Report, error) { return bad, nil }}
		report, err := runWorkspaces(context.Background(), root, c, ExecutionOptions{}, providers)
		if err == nil || report.Complete || report.Passed {
			t.Fatal("contradictory provider completion passed", report, err)
		}
	}
}

func TestWorkspaceFullFallbackPreservesOriginalArgumentsDirectoryEnvironment(t *testing.T) {
	root, c := coordinatorFixture(t)
	if err := os.Mkdir(filepath.Join(root, "cwd"), 0700); err != nil {
		t.Fatal(err)
	}
	c.Workspaces = c.Workspaces[:1]
	c.Workspaces[0].Command = model.Command{Dir: "cwd", Executable: "/bin/sh", Args: []string{"-c", "test \"$HAYAKU_FIXTURE\" = declared && test \"$1\" = 'literal ; $(false)' && test \"${PWD##*/}\" = cwd && printf done > marker", "fixture", "literal ; $(false)"}}
	c.Environment = map[string]string{"HAYAKU_FIXTURE": "declared"}
	report, err := RunWithOptions(context.Background(), root, c, ExecutionOptions{})
	if err != nil || !report.Complete || !report.Passed || len(report.Groups) != 1 {
		t.Fatal("full original scope failed", report, err)
	}
	group := report.Groups[0]
	if group.Qualified || group.Reused || !group.Executed || group.Key != "" || group.Context != "original-host" {
		t.Fatal("full command acquired qualification", group)
	}
	data, err := os.ReadFile(filepath.Join(root, "cwd", "marker"))
	if err != nil || string(data) != "done" {
		t.Fatal("original command did not run in configured cwd", err)
	}
}
