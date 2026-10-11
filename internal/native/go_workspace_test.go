package native

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/passcache"
)

func canonicalGoWorkspace() (Workspace, RunConfig) {
	w := Workspace{Workspace: model.Workspace{ID: "go-scope", Root: ".", Adapter: "go", Command: model.Command{Dir: ".", Executable: "go", Args: []string{"test", "-count=1", "./a", "./b"}}, Patterns: []string{"./a", "./b"}}}
	return w, RunConfig{Config: Config{Schema: 1, Platform: "darwin", Architecture: "arm64"}}
}

func TestGoOriginalContextRejectsAmbientTargetAndToolchainDrift(t *testing.T) {
	for _, key := range []string{"GOFLAGS", "GOEXPERIMENT", "GOOS", "GOARCH", "GOAMD64", "GOARM64", "GOFIPS140", "GODEBUG"} {
		t.Setenv(key, "")
	}
	t.Setenv("GOENV", "off")
	t.Setenv("GOWORK", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	root := t.TempDir()
	write(t, root, "go.mod", "module originalcontext\ngo 1.26\n")
	w, c := canonicalGoWorkspace()
	c.Platform, c.Architecture = runtime.GOOS, runtime.GOARCH
	baseline, err := goOriginalContext(context.Background(), root, w, c)
	if err != nil || baseline == "" {
		t.Fatal("canonical original context unavailable", err)
	}
	for _, key := range []string{"GOFLAGS", "GOENV", "GOWORK", "GOTOOLCHAIN", "GOEXPERIMENT", "GOOS", "GOARCH", "GOAMD64", "GOARM64", "GOFIPS140", "GODEBUG"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "unsupported")
			if _, err := goOriginalContext(context.Background(), root, w, c); err == nil {
				t.Fatal("unknown original environment admitted", key)
			}
		})
	}
	t.Run("build-location-drift", func(t *testing.T) {
		t.Setenv("GOROOT", "/unused-original-location")
		changed, err := goOriginalContext(context.Background(), root, w, c)
		if err != nil || changed == baseline {
			t.Fatal("original compiler location absent from context", err)
		}
	})
	changed := c
	if runtime.GOOS == "linux" {
		changed.Platform = "darwin"
	} else {
		changed.Platform = "linux"
	}
	if _, err := goOriginalContext(context.Background(), root, w, changed); err == nil {
		t.Fatal("foreign native target substituted for original host")
	}
	write(t, root, "go.mod", "module originalcontext\ngo 1.26\ntoolchain go1.27.1\n")
	if _, err := goOriginalContext(context.Background(), root, w, c); err == nil {
		t.Fatal("toolchain directive admitted")
	}
	write(t, root, "go.mod", "module originalcontext\ngo 1.26\n")
	write(t, root, "go.work", "go 1.26\nuse .\n")
	t.Setenv("GOWORK", "")
	if _, err := goOriginalContext(context.Background(), root, w, c); err == nil {
		t.Fatal("implicit original workspace admitted")
	}
}

func TestGoOriginalContextBindsExecutableSelectionsAndRetargets(t *testing.T) {
	for _, key := range []string{"GOFLAGS", "GOEXPERIMENT", "GOOS", "GOARCH", "GOAMD64", "GOARM64", "GOFIPS140", "GODEBUG"} {
		t.Setenv(key, "")
	}
	t.Setenv("GOENV", "off")
	t.Setenv("GOWORK", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	for _, fault := range []string{"declared-alias", "path-entry", "compiler-bytes"} {
		t.Run(fault, func(t *testing.T) {
			root, bin, sdk, temporary := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
			write(t, root, "go.mod", "module aliascontrol\ngo 1.26\n")
			first, second := filepath.Join(sdk, "first"), filepath.Join(sdk, "second")
			// Executable files are selection controls only and are never run.
			for _, name := range []string{first, second} {
				if err := os.WriteFile(name, []byte("identical fake compiler bytes"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			goPath, alias := filepath.Join(bin, "go"), filepath.Join(bin, "declared")
			for _, name := range []string{goPath, alias} {
				if err := os.Symlink(first, name); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin)
			w, c := canonicalGoWorkspace()
			c.Platform, c.Architecture = runtime.GOOS, runtime.GOARCH
			if fault == "declared-alias" {
				w.Command.Executable = alias
			}
			baseline, err := goOriginalContext(context.Background(), root, w, c)
			if err != nil || baseline == "" {
				t.Fatal("fake executable selection unavailable", err)
			}
			cacheDir, key := filepath.Join(t.TempDir(), "cache"), strings.Repeat("c", 64)
			cache, err := passcache.Open(cacheDir, "hayaku-native-pure-pass-v1")
			if err != nil {
				t.Fatal(err)
			}
			// Authenticated synthetic history tests revocation, not Go authority.
			if err := cache.Store(context.Background(), key, true); err != nil {
				t.Fatal(err)
			}
			if err := cache.Close(); err != nil {
				t.Fatal(err)
			}
			owner := &goBuildOwner{temporary: temporary, options: ExecutionOptions{CacheDir: cacheDir}, report: Report{Groups: []Result{{Key: key}}}, verifyOriginal: func(ctx context.Context) error {
				current, err := goOriginalContext(ctx, root, w, c)
				if err != nil || current != baseline {
					return errors.Join(errors.New("original command selection changed"), err)
				}
				return nil
			}}
			if err := owner.verifyOriginal(context.Background()); err != nil {
				t.Fatal("healthy original selection drifted", err)
			}
			if fault == "compiler-bytes" {
				if err := os.WriteFile(first, []byte("changed fake compiler bytes"), 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				retarget := goPath
				if fault == "declared-alias" {
					retarget = alias
				}
				if err := os.Remove(retarget); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(second, retarget); err != nil {
					t.Fatal(err)
				}
				if data, err := os.ReadFile(first); err != nil || string(data) != "identical fake compiler bytes" {
					t.Fatal("retarget control changed original physical compiler", err)
				}
			}
			if owner.verifyOriginal(context.Background()) == nil || owner.finish(context.Background()) == nil {
				t.Fatal("retarget or compiler drift retained authority")
			}
			if _, err := os.Stat(filepath.Join(cacheDir, key+".json")); !os.IsNotExist(err) {
				t.Fatal("original executable drift retained pass", err)
			}
			if _, err := os.Stat(temporary); !os.IsNotExist(err) {
				t.Fatal("original executable drift missed cleanup", err)
			}
		})
	}
}

func TestGoIncompleteAttemptFallbackRetainsAndRevokesReceiptHistory(t *testing.T) {
	for _, failRevocation := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-original", true: "retry-at-finalization"}[failRevocation], func(t *testing.T) {
			cacheDir, temporary := filepath.Join(t.TempDir(), "cache"), t.TempDir()
			earlier, current := strings.Repeat("a", 64), strings.Repeat("b", 64)
			cache, err := passcache.Open(cacheDir, "hayaku-native-pure-pass-v1")
			if err != nil {
				t.Fatal(err)
			}
			// Both are authenticated synthetic revocation controls, not native proof.
			for _, key := range []string{earlier, current} {
				if err := cache.Store(context.Background(), key, true); err != nil {
					t.Fatal(err)
				}
			}
			if err := cache.Close(); err != nil {
				t.Fatal(err)
			}
			owner := &goBuildOwner{temporary: temporary, options: ExecutionOptions{CacheDir: cacheDir}, report: Report{Groups: []Result{{Name: "go-scope::./a", Key: earlier, Complete: true, Passed: true}, {Name: "go-scope::./b", Key: current, Complete: false}}}}
			retained := cacheDir + "-retained"
			if failRevocation {
				if err := os.Rename(cacheDir, retained); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(cacheDir, []byte("unavailable cache directory"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			group, err := owner.original(context.Background(), func(context.Context, string) (Result, error) {
				calls++
				if !failRevocation {
					for _, key := range []string{earlier, current} {
						if _, err := os.Stat(filepath.Join(cacheDir, key+".json")); !os.IsNotExist(err) {
							t.Fatal("original ran before all attempted history was revoked", err)
						}
					}
				}
				return Result{Name: "go-scope::suite", Executed: true, Complete: true, Passed: true}, nil
			}, "incomplete current execution")
			public := Report{Groups: []Result{group}, Complete: group.Complete, Passed: group.Passed}
			if calls != 1 || len(public.Groups) != 1 || public.Groups[0].Key != "" || len(owner.report.Groups) != 2 {
				t.Fatal("full scope replacement changed attempted ownership")
			}
			if failRevocation {
				if err == nil || public.Complete || public.Passed {
					t.Fatal("revocation failure was hidden by successful original scope")
				}
				if err := os.Remove(cacheDir); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(retained, cacheDir); err != nil {
					t.Fatal(err)
				}
				if owner.finish(context.Background()) == nil {
					t.Fatal("finalization forgot failed attempted-history revocation")
				}
			} else if err != nil || !public.Complete || !public.Passed || owner.finish(context.Background()) != nil {
				t.Fatal("healthy full scope replacement failed", err)
			}
			for _, key := range []string{earlier, current} {
				if _, err := os.Stat(filepath.Join(cacheDir, key+".json")); !os.IsNotExist(err) {
					t.Fatal("discarded native attempt retained a pass", err)
				}
			}
			if _, err := os.Stat(temporary); !os.IsNotExist(err) {
				t.Fatal("attempted-history finalization missed cleanup", err)
			}
		})
	}
}

func TestGoWorkspaceCanonicalAdmissionAndPrivateMetadata(t *testing.T) {
	w, c := canonicalGoWorkspace()
	config, ok := goWorkspaceConfig(w, c)
	if !ok || config.workspace != w.ID || !config.deferCleanup || !slices.Equal(config.Packages, w.Patterns) {
		t.Fatal("canonical scope rejected")
	}
	config.Packages[0] = "./changed"
	if w.Patterns[0] != "./a" || w.Command.Args[2] != "./a" {
		t.Fatal("provider retained caller slice")
	}
	for _, mutation := range []func(*Workspace, *RunConfig){
		func(w *Workspace, _ *RunConfig) { w.Command.Args = []string{"test", "./a"} },
		func(w *Workspace, _ *RunConfig) {
			w.Command.Args = []string{"test", "-count=1", "./..."}
			w.Patterns = []string{"./..."}
		},
		func(w *Workspace, _ *RunConfig) { w.Patterns = []string{"./b", "./a"} },
		func(w *Workspace, _ *RunConfig) {
			w.Command.Args = []string{"test", "-count=1", "./a", "./a"}
			w.Patterns = []string{"./a", "./a"}
		},
		func(w *Workspace, _ *RunConfig) { w.Command.Dir = "cwd" },
		func(w *Workspace, _ *RunConfig) { w.Command.Executable = "custom-go" },
		func(w *Workspace, _ *RunConfig) { w.Prerequisites = []model.Command{{Executable: "go"}} },
		func(w *Workspace, _ *RunConfig) { w.BuildFlags = []string{"-race"} },
		func(w *Workspace, _ *RunConfig) { w.Inputs = []string{"fixture"} },
		func(w *Workspace, _ *RunConfig) { w.NodeRuntime = &model.NodeRuntime{} },
		func(w *Workspace, _ *RunConfig) { w.SwiftRuntime = &model.SwiftRuntime{} },
		func(_ *Workspace, c *RunConfig) { c.Environment = map[string]string{"SETTING": "value"} },
	} {
		w, c := canonicalGoWorkspace()
		mutation(&w, &c)
		if _, admitted := goWorkspaceConfig(w, c); admitted {
			t.Fatal("unsupported invocation admitted")
		}
	}
	var decoded Config
	if err := json.Unmarshal([]byte(`{"schema":1,"packages":["./a"],"workspace":"forged","deferCleanup":true}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.workspace != "" || decoded.original != nil || decoded.deferCleanup {
		t.Fatal("JSON supplied private lifecycle authority")
	}
}

func TestGoWorkspaceAMD64AlwaysRunsExactOriginalWithoutQualification(t *testing.T) {
	root, bin := t.TempDir(), t.TempDir()
	marker, cacheDir := filepath.Join(root, "trace"), filepath.Join(t.TempDir(), "native-cache")
	command := "#!/bin/sh\n[ \"$#\" = 4 ] && [ \"$1\" = test ] && [ \"$2\" = -count=1 ] && [ \"$3\" = ./a ] && [ \"$4\" = ./b ] || exit 91\nprintf run >> \"$HAYAKU_AMD64_CONTROL_MARKER\"\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(command), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HAYAKU_AMD64_CONTROL_MARKER", marker)
	t.Setenv("GOARCH", "amd64")
	w, c := canonicalGoWorkspace()
	c.Platform, c.Architecture = runtime.GOOS, "amd64"
	// This direct assertion is independent of original-host mismatch. On an
	// AMD64 host, the original and requested native target also match.
	if _, admitted := goWorkspaceConfig(w, c); admitted {
		t.Fatal("canonical AMD64 workspace admitted without reviewed acceptance")
	}
	c.Workspaces = []Workspace{w}
	for attempt := 0; attempt < 2; attempt++ {
		report, err := RunWithOptions(context.Background(), root, c, ExecutionOptions{CacheDir: cacheDir})
		if err != nil || !report.Complete || !report.Passed || len(report.Groups) != 1 || report.finalize != nil {
			t.Fatal("canonical AMD64 scope did not run original", report, err)
		}
		group := report.Groups[0]
		if group.Name != w.ID+"::suite" || !group.Executed || group.Reused || group.Qualified || group.Key != "" || group.Context != "original-host" {
			t.Fatal("AMD64 original fallback acquired native qualification", group)
		}
		if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
			t.Fatal("AMD64 original fallback created native cache", err)
		}
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "runrun" {
		t.Fatal("AMD64 original command changed, reused or repeated", string(data), err)
	}
}

func TestGoWorkspaceUnsupportedInvocationRunsExactOriginalOnce(t *testing.T) {
	root := t.TempDir()
	write(t, root, "scope/cwd/input", "fixture")
	w, c := canonicalGoWorkspace()
	w.Root = "scope"
	w.Command = model.Command{Dir: "cwd", Executable: "/bin/sh", Args: []string{"-c", `test "$SETTING" = declared && test "$1" = 'literal ; $(false)' && test "${PWD##*/}" = cwd && printf command >> trace`, "fixture", "literal ; $(false)"}}
	w.Prerequisites = []model.Command{{Dir: "cwd", Executable: "/bin/sh", Args: []string{"-c", "printf prerequisite >> trace"}}}
	c.Environment = map[string]string{"SETTING": "declared"}
	c.Workspaces = []Workspace{w}
	for attempt := 0; attempt < 2; attempt++ {
		report, err := RunWithOptions(context.Background(), root, c, ExecutionOptions{})
		if err != nil || !report.Complete || !report.Passed || len(report.Groups) != 1 {
			t.Fatal("original configured scope failed", err)
		}
		group := report.Groups[0]
		if group.Name != w.ID+"::suite" || !group.Executed || group.Reused || group.Qualified || group.Key != "" || group.Context != "original-host" {
			t.Fatal("fallback invented qualification", group)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "scope/cwd/trace"))
	if err != nil || string(data) != "prerequisitecommandprerequisitecommand" {
		t.Fatal("original invocation was changed or repeated", string(data), err)
	}
}

func TestGoWorkspaceAuditRevokesHistoryThenRunsExactOriginalOnce(t *testing.T) {
	root, bin := t.TempDir(), t.TempDir()
	write(t, root, "go.mod", "module auditcontrol\ngo 1.26\n")
	cacheDir, marker := filepath.Join(t.TempDir(), "cache"), filepath.Join(t.TempDir(), "marker")
	key := strings.Repeat("a", 64)
	cache, err := passcache.Open(cacheDir, "hayaku-native-pure-pass-v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Store(context.Background(), key, true); err != nil {
		t.Fatal(err)
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	// This fake command proves ordering and exact argv, supplies no compiler
	// authority, and must never receive discovery or build arguments.
	command := "#!/bin/sh\n[ \"$1\" = test ] && [ ! -e \"$HAYAKU_AUDIT_CONTROL_KEY\" ] || exit 91\nprintf '%s' \"$*\" >> \"$HAYAKU_AUDIT_CONTROL_MARKER\"\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(command), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HAYAKU_AUDIT_CONTROL_KEY", filepath.Join(cacheDir, key+".json"))
	t.Setenv("HAYAKU_AUDIT_CONTROL_MARKER", marker)
	w, c := canonicalGoWorkspace()
	c.Platform, c.Architecture = runtime.GOOS, runtime.GOARCH
	c.Workspaces = []Workspace{w}
	report, err := RunWithOptions(context.Background(), root, c, ExecutionOptions{CacheDir: cacheDir, Audit: true})
	if err != nil || !report.Complete || !report.Passed || len(report.Groups) != 1 {
		t.Fatal("typed audit did not complete original scope", err)
	}
	g := report.Groups[0]
	if !g.Executed || g.Reused || g.Qualified || g.Key != "" || g.Name != w.ID+"::suite" {
		t.Fatal("original audit acquired qualification")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "test -count=1 ./a ./b" {
		t.Fatal("audit ran narrowed or repeated command", string(data), err)
	}
}

func TestGoRetainedOwnerDriftRevokesPassesAndCleansArtifacts(t *testing.T) {
	root, temporary := t.TempDir(), t.TempDir()
	write(t, root, "source.go", "first source")
	source := filepath.Join(temporary, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	before, err := capture(context.Background(), root, source)
	if err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(t.TempDir(), "cache")
	key := strings.Repeat("a", 64)
	cache, err := passcache.Open(cacheDir, "hayaku-native-pure-pass-v1")
	if err != nil {
		t.Fatal(err)
	}
	// Authenticated synthetic receipt is a revocation control, not native proof.
	if err := cache.Store(context.Background(), key, true); err != nil {
		t.Fatal(err)
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	owner := &goBuildOwner{root: root, source: source, temporary: temporary, sourceDigest: before, options: ExecutionOptions{CacheDir: cacheDir}, report: Report{Complete: true, Passed: true, Groups: []Result{{Key: key}}}}
	write(t, root, "source.go", "later workspace changed source")
	first := owner.finish(context.Background())
	if first == nil || owner.finish(context.Background()) != first {
		t.Fatal("drift or repeated lifecycle lost failure")
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatal("retained build tree not removed", err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, key+".json")); !os.IsNotExist(err) {
		t.Fatal("earlier owner receipt survived drift", err)
	}
}

func TestGoRetainedOwnerStaysLiveThroughLaterWorkspace(t *testing.T) {
	root, temporary := t.TempDir(), t.TempDir()
	write(t, root, "source.go", "first source")
	source := filepath.Join(temporary, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	before, err := capture(context.Background(), root, source)
	if err != nil {
		t.Fatal(err)
	}
	owner := &goBuildOwner{root: root, source: source, temporary: temporary, sourceDigest: before}
	w, c := canonicalGoWorkspace()
	c.Workspaces = []Workspace{w, {Workspace: model.Workspace{ID: "later", Root: ".", Adapter: "control", Command: model.Command{Dir: ".", Executable: "/usr/bin/true"}}}}
	providers := map[string]workspaceProvider{
		"go": func(context.Context, string, Workspace, RunConfig, ExecutionOptions) (Report, error) {
			return Report{Complete: true, Passed: true, Groups: []Result{{Name: w.ID + "::./a", Executed: true, Complete: true, Passed: true}}, finalize: owner.finish}, nil
		},
		"control": func(context.Context, string, Workspace, RunConfig, ExecutionOptions) (Report, error) {
			if _, err := os.Stat(source); err != nil {
				t.Fatal("earlier owner released before later scope", err)
			}
			write(t, root, "source.go", "later workspace changed source")
			return Report{Complete: true, Passed: true, Groups: []Result{{Name: "later::suite", Executed: true, Complete: true, Passed: true}}}, nil
		},
	}
	report, err := runWorkspaces(context.Background(), root, c, ExecutionOptions{}, providers)
	if err == nil || report.Complete || report.Passed || len(report.Groups) != 2 {
		t.Fatal("late workspace drift retained success", report, err)
	}
	for _, group := range report.Groups {
		if group.Complete || group.Passed || group.Qualified {
			t.Fatal("earlier pass survived finalization", group)
		}
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatal("coordinator missed cleanup", err)
	}
}

func TestGoRetainedOwnerCancelledFinalizationStillCleansArtifacts(t *testing.T) {
	root, temporary := t.TempDir(), t.TempDir()
	source := filepath.Join(temporary, "source")
	write(t, root, "source.go", "fixture")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	before, err := capture(context.Background(), root, source)
	if err != nil {
		t.Fatal(err)
	}
	owner := &goBuildOwner{root: root, source: source, temporary: temporary, sourceDigest: before}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if owner.finish(ctx) == nil {
		t.Fatal("cancelled guard reported completion")
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatal("cancelled finalizer lost cleanup", err)
	}
}
