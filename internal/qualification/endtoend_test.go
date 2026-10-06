package qualification

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/testutil/acceptance"
)

// These workloads are full-command oracles, not runtime qualification proofs.
// Inventories and mutation failure sets are authored separately from discovery.
type endToEndManifest struct {
	Schema      int             `json:"schema"`
	Kind        string          `json:"kind"`
	Runner      string          `json:"runner"`
	FailureExit int             `json:"failure_exit"`
	Inventory   []string        `json:"inventory"`
	Faults      []endToEndFault `json:"faults"`
}

type endToEndFault struct {
	ID                 string   `json:"id"`
	Operation          string   `json:"operation"`
	Path               string   `json:"path"`
	Before             string   `json:"before"`
	After              string   `json:"after"`
	Failures           []string `json:"failures"`
	Stage              string   `json:"stage"`
	AddedInventory     []string `json:"added_inventory"`
	DiagnosticContains string   `json:"diagnostic_contains"`
	ForbiddenTerminal  []string `json:"forbidden_terminal"`
	UnqualifiedEffect  bool     `json:"unqualified_effect"`
}

var endToEndRunners = []string{"go", "vitest", "worker", "unittest", "pytest", "cargo", "swift"}

func endToEndLoad(t *testing.T, runner string) (string, endToEndManifest) {
	t.Helper()
	source, err := filepath.Abs(filepath.Join("../../testdata/endtoend", runner))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(source, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest endToEndManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	if decoder.Decode(new(any)) != io.EOF || manifest.Schema != 1 || manifest.Kind != "independent-full-command-baseline" || manifest.Runner != runner || len(manifest.Inventory) == 0 || len(manifest.Faults) == 0 || manifest.FailureExit < 1 {
		t.Fatal("invalid independent full-command fixture contract")
	}
	return source, manifest
}

func endToEndMutate(t *testing.T, root string, mutation endToEndFault) {
	t.Helper()
	if !filepath.IsLocal(mutation.Path) || mutation.Path == "." {
		t.Fatal("fixture mutation escapes root")
	}
	path := filepath.Join(root, mutation.Path)
	switch mutation.Operation {
	case "replace":
		data, err := os.ReadFile(path)
		if err != nil || mutation.Before == "" || strings.Count(string(data), mutation.Before) != 1 {
			t.Fatal("mutation must match one source occurrence")
		}
		if err := os.WriteFile(path, []byte(strings.Replace(string(data), mutation.Before, mutation.After, 1)), 0600); err != nil {
			t.Fatal(err)
		}
	case "delete":
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	case "add":
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("mutation addition already exists")
		}
		if err := os.WriteFile(path, []byte(mutation.After), 0600); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("unknown fixture mutation operation")
	}
}

func TestEndToEndFixtureContracts(t *testing.T) {
	for _, runner := range endToEndRunners {
		t.Run(runner, func(t *testing.T) {
			source, manifest := endToEndLoad(t, runner)
			inventory := map[string]bool{}
			for _, id := range manifest.Inventory {
				if id == "" || inventory[id] {
					t.Fatal("duplicate or empty independent case identity")
				}
				inventory[id] = true
			}
			faults := map[string]bool{}
			for _, mutation := range manifest.Faults {
				if mutation.ID == "" || faults[mutation.ID] {
					t.Fatal("duplicate or empty fault identity")
				}
				faults[mutation.ID] = true
				for _, id := range mutation.Failures {
					if !inventory[id] {
						t.Fatalf("fault oracle references unknown case %q", id)
					}
				}
				added := map[string]bool{}
				for _, id := range mutation.AddedInventory {
					if id == "" || inventory[id] || added[id] {
						t.Fatal("new-test oracle must introduce a unique new case")
					}
					added[id] = true
				}
				forbidden := map[string]bool{}
				for _, id := range mutation.ForbiddenTerminal {
					if !inventory[id] || forbidden[id] {
						t.Fatal("forbidden terminal oracle must name unique independently known cases")
					}
					forbidden[id] = true
				}
				switch mutation.Stage {
				case "", "build", "discovery", "startup":
				default:
					t.Fatal("unknown pre-test failure stage")
				}
				if mutation.Stage != "" && (mutation.DiagnosticContains == "" || len(mutation.ForbiddenTerminal) == 0) {
					t.Fatal("pre-test faults require independent diagnostic and forbidden-case oracles")
				}
				endToEndMutate(t, faultFixture(t, source), mutation)
			}
		})
	}
}

// Opt-in permits provisioned, potentially expensive compiler/runtime workloads.
// Missing tools are explicit skips; this test never fetches or installs tools.
func TestEndToEndProvisionedFullCommandOracles(t *testing.T) {
	if os.Getenv("HAYAKU_ENDTOEND") != "1" {
		t.Skip("set HAYAKU_ENDTOEND=1 to run installed full-command baseline/fault contexts; no runtime skipping is qualified by these observations")
	}
	for _, runner := range endToEndRunners {
		t.Run(runner, func(t *testing.T) {
			source, manifest := endToEndLoad(t, runner)
			command, modules := endToEndCommand(t, runner)
			baseline := faultFixture(t, source)
			endToEndLinkModules(t, baseline, modules)
			for _, phase := range []string{"cold", "warm"} {
				output, outcomes := endToEndObserve(t, runner, baseline, command)
				endToEndAssert(t, output, outcomes, manifest.Inventory, nil, manifest.FailureExit)
				// Wall duration includes build, startup and execution; warm runs
				// retain runner build caches but execute the original full scope.
				record, err := json.Marshal(struct {
					Runner         string        `json:"runner"`
					Phase          string        `json:"phase"`
					Command        model.Command `json:"command"`
					Execution      time.Duration `json:"full_command_wall_ns"`
					Cases          int           `json:"cases"`
					ReuseQualified bool          `json:"reuse_qualified"`
				}{runner, phase, command, output.Duration, len(outcomes), false})
				if err != nil {
					t.Fatal(err)
				}
				t.Log(string(record))
			}
			for _, mutation := range manifest.Faults {
				t.Run(mutation.ID, func(t *testing.T) {
					root := faultFixture(t, source)
					endToEndMutate(t, root, mutation)
					endToEndLinkModules(t, root, modules)
					output, outcomes := endToEndObserve(t, runner, root, command)
					if mutation.Stage != "" {
						if output.ExitCode == 0 {
							t.Fatalf("%s fault became successful", mutation.Stage)
						}
						diagnostic := string(output.Stdout) + string(output.Stderr)
						if data, err := os.ReadFile(filepath.Join(root, "hayaku-results.json")); err == nil {
							diagnostic += string(data)
						}
						if !strings.Contains(diagnostic, mutation.DiagnosticContains) {
							t.Fatalf("missing independently expected %s diagnostic %q", mutation.Stage, mutation.DiagnosticContains)
						}
						for _, id := range mutation.ForbiddenTerminal {
							if _, exists := outcomes[id]; exists {
								t.Fatalf("%s failure unexpectedly produced terminal case %s", mutation.Stage, id)
							}
						}
						t.Logf("pre-test %s sentinel verified; expected cases did not reach assertions", mutation.Stage)
						return
					}
					inventory := append(append([]string{}, manifest.Inventory...), mutation.AddedInventory...)
					endToEndAssert(t, output, outcomes, inventory, mutation.Failures, manifest.FailureExit)
					if mutation.UnqualifiedEffect {
						t.Log("dormant external-input effect was not executed; clean full results do not authorize reuse")
					}
				})
			}
		})
	}
}

func endToEndCommand(t *testing.T, runner string) (model.Command, string) {
	t.Helper()
	lookup := func(name string) string {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("installed %s unavailable; no automatic install", name)
		}
		return path
	}
	switch runner {
	case "go":
		return model.Command{Executable: lookup("go"), Args: []string{"test", "-json", "-count=1", "./..."}}, ""
	case "cargo":
		return model.Command{Executable: lookup("cargo"), Args: []string{"test", "--offline", "--locked", "--features", "qualified-feature", "--", "--test-threads=1"}}, ""
	case "swift":
		return model.Command{Executable: lookup("swift"), Args: []string{"test", "--disable-swift-testing", "--disable-sandbox", "--jobs", "8", "-Xswiftc", "-sdk-module-cache-path", "-Xswiftc", t.TempDir()}}, ""
	case "unittest", "pytest":
		python := os.Getenv("HAYAKU_NATIVE_PYTHON")
		if runner == "unittest" {
			python = os.Getenv("HAYAKU_NATIVE_UNITTEST_PYTHON")
		}
		if python == "" {
			python = lookup("python3")
		}
		if runner == "unittest" {
			return model.Command{Executable: python, Args: []string{"-m", "unittest", "discover", "-v"}}, ""
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		probe, _ := process.Run(ctx, model.Command{Executable: python, Args: []string{"-c", "import pytest"}}, t.TempDir(), nil)
		if !probe.Completed || probe.ExitCode != 0 {
			t.Skip("pytest unavailable in explicitly provisioned interpreter; no automatic install")
		}
		return model.Command{Executable: python, Args: []string{"-m", "pytest", "-q", "-p", "no:cacheprovider", "--junitxml=hayaku-results.xml"}}, ""
	case "vitest", "worker":
		node, modules := os.Getenv("HAYAKU_VITEST_NODE"), os.Getenv("HAYAKU_VITEST_MODULES")
		if runner == "worker" {
			modules = os.Getenv("HAYAKU_WORKER_MODULES")
		}
		if node == "" || modules == "" {
			t.Skip("explicit installed Node/modules missing; set HAYAKU_VITEST_NODE and HAYAKU_VITEST_MODULES or HAYAKU_WORKER_MODULES")
		}
		if !filepath.IsAbs(node) || !filepath.IsAbs(modules) {
			t.Fatal("installed runtime/module paths must be absolute")
		}
		entry := filepath.Join(modules, "vitest", "vitest.mjs")
		if _, err := os.Stat(entry); err != nil {
			t.Fatal("explicit installed Vitest entrypoint unavailable")
		}
		if runner == "worker" {
			if _, err := os.Stat(filepath.Join(modules, "@cloudflare/vitest-plugin")); err != nil {
				t.Skip("provisioned Cloudflare worker plugin unavailable")
			}
		}
		return model.Command{Executable: node, Args: []string{entry, "run", "--config", "vitest.config.mjs", "--reporter=json", "--outputFile=hayaku-results.json"}}, modules
	}
	t.Fatal("unknown full-command runner")
	return model.Command{}, ""
}

func endToEndLinkModules(t *testing.T, root, modules string) {
	t.Helper()
	if modules != "" {
		if err := os.Symlink(modules, filepath.Join(root, "node_modules")); err != nil {
			t.Fatal(err)
		}
	}
}

func endToEndObserve(t *testing.T, runner, root string, command model.Command) (process.Output, map[string]string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	output, err := process.Run(ctx, command, root, map[string]string{"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOWORK": "off", "GOFLAGS": "", "CARGO_NET_OFFLINE": "true", "PYTHONDONTWRITEBYTECODE": "1", "PYTEST_DISABLE_PLUGIN_AUTOLOAD": "1", "CI": "true", "NO_COLOR": "1", "WRANGLER_SEND_METRICS": "false", "WRANGLER_LOG": "error"})
	if !output.Completed || (err != nil && output.ExitCode < 1) {
		t.Fatalf("full-command invocation incomplete: %v", err)
	}
	outcomes := map[string]string{}
	put := func(id, result string) {
		if _, exists := outcomes[id]; exists {
			t.Fatalf("duplicate terminal identity %s", id)
		}
		outcomes[id] = result
	}
	switch runner {
	case "go":
		decoder := json.NewDecoder(bytes.NewReader(output.Stdout))
		for {
			var event struct{ Package, Test, Action string }
			if err := decoder.Decode(&event); err != nil {
				if err != io.EOF {
					t.Fatal("invalid full-command Go JSON")
				}
				break
			}
			if event.Test != "" && (event.Action == "pass" || event.Action == "fail" || event.Action == "skip") {
				put(event.Package+"/"+event.Test, event.Action)
			}
		}
	case "vitest", "worker":
		data, err := os.ReadFile(filepath.Join(root, "hayaku-results.json"))
		if os.IsNotExist(err) && output.ExitCode != 0 {
			return output, outcomes
		}
		if err != nil {
			t.Fatal(err)
		}
		var report struct {
			TestResults []struct {
				Name             string
				AssertionResults []struct{ FullName, Status string }
			}
		}
		if err := json.Unmarshal(data, &report); err != nil {
			t.Fatal("invalid Vitest full-command report")
		}
		for _, file := range report.TestResults {
			canonicalRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			canonicalFile, err := filepath.EvalSymlinks(file.Name)
			if err != nil {
				t.Fatal(err)
			}
			path, err := filepath.Rel(canonicalRoot, canonicalFile)
			if err != nil || !filepath.IsLocal(path) {
				t.Fatal("Vitest result escaped fixture")
			}
			for _, test := range file.AssertionResults {
				result := map[string]string{"passed": "pass", "failed": "fail", "pending": "skip", "skipped": "skip"}[test.Status]
				if result == "" {
					t.Fatal("unknown Vitest terminal state")
				}
				put(filepath.ToSlash(path)+"::"+test.FullName, result)
			}
		}
	case "pytest":
		data, err := os.ReadFile(filepath.Join(root, "hayaku-results.xml"))
		if err != nil {
			t.Fatal(err)
		}
		var report struct {
			Suites []struct {
				Cases []struct {
					Class   string    `xml:"classname,attr"`
					Name    string    `xml:"name,attr"`
					Failure *struct{} `xml:"failure"`
					Error   *struct{} `xml:"error"`
					Skip    *struct{} `xml:"skipped"`
				} `xml:"testcase"`
			} `xml:"testsuite"`
		}
		if err := xml.Unmarshal(data, &report); err != nil {
			t.Fatal("invalid pytest XML")
		}
		for _, suite := range report.Suites {
			for _, test := range suite.Cases {
				parts := strings.Split(test.Class, ".")
				id := parts[0] + ".py"
				for _, part := range parts[1:] {
					id += "::" + part
				}
				result := "pass"
				if test.Failure != nil || test.Error != nil {
					result = "fail"
				}
				if test.Skip != nil {
					result = "skip"
				}
				put(id+"::"+test.Name, result)
			}
		}
	case "unittest":
		text := string(output.Stderr)
		for _, match := range regexp.MustCompile(`(?m)^\w+ \(([^)]+)\)(?:\s*\.{3}\s*|\n)(ok|FAIL|ERROR|skipped[^\n]*)`).FindAllStringSubmatch(text, -1) {
			result := "pass"
			if match[2] == "FAIL" || match[2] == "ERROR" {
				result = "fail"
			}
			if strings.HasPrefix(match[2], "skipped") {
				result = "skip"
			}
			put(match[1], result)
		}
	case "cargo":
		for _, match := range regexp.MustCompile(`(?m)^test ([^ ]+) \.\.\. (ok|FAILED|ignored)`).FindAllStringSubmatch(string(output.Stdout), -1) {
			result := map[string]string{"ok": "pass", "FAILED": "fail", "ignored": "skip"}[match[2]]
			put(match[1], result)
		}
	case "swift":
		text := string(output.Stdout) + string(output.Stderr)
		for _, match := range regexp.MustCompile(`Test Case '-\[[^ .]+\.([^ ]+) ([^]]+)\]' (passed|failed|skipped)`).FindAllStringSubmatch(text, -1) {
			put(match[1]+"."+match[2], map[string]string{"passed": "pass", "failed": "fail", "skipped": "skip"}[match[3]])
		}
	}
	return output, outcomes
}

func endToEndAssert(t *testing.T, output process.Output, outcomes map[string]string, inventory, failures []string, failureExit int) {
	t.Helper()
	unit := model.Unit{ID: "original-full-command", Workspace: "independent-fixture", Kind: "full-scope"}
	wantCases := map[string]string{}
	for _, id := range inventory {
		wantCases[id] = "pass"
	}
	for _, id := range failures {
		wantCases[id] = "fail"
	}
	normalize := func(cases map[string]string, failed bool) model.Outcomes {
		action := "pass"
		if failed {
			action = "fail"
		}
		result := model.Outcomes{Complete: true, Failed: failed, Units: []model.Outcome{{Unit: unit.ID, Action: action}}}
		for id, action := range cases {
			result.Tests = append(result.Tests, model.Outcome{Unit: unit.ID, Test: id, Action: action})
		}
		return result
	}
	got := acceptance.Observation{Outcomes: normalize(outcomes, output.ExitCode != 0), Completed: output.Completed, Passed: output.ExitCode == 0, ExitCode: output.ExitCode, Groups: []acceptance.Group{{Name: unit.ID, Mode: "execute", Qualified: false}}}
	expectedExit := 0
	if len(failures) != 0 {
		expectedExit = failureExit
	}
	want := acceptance.Expectation{Inventory: []model.Unit{unit}, Outcomes: normalize(wantCases, len(failures) != 0), Completed: true, Passed: len(failures) == 0, ExitCode: expectedExit, Groups: []acceptance.Group{{Name: unit.ID, Mode: "execute", Qualified: false}}}
	if issues := acceptance.Check(got, nil, want); len(issues) != 0 {
		t.Fatalf("independent full-command oracle mismatch: %v; observed %v expected %v (exit %d)", issues, outcomes, wantCases, output.ExitCode)
	}
}
