// Package nextest augments offline Cargo package evidence with the installed
// nextest's configured binary/case inventory. Runtime influence is unqualified.
package nextest

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Cyberlane/hayaku/internal/adapter/cargo"
	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	nativerunner "github.com/Cyberlane/hayaku/internal/runner/native"
)

const Version = "nextest-native-v1"
const NativeVersion = "0.9.146"

type Case struct {
	Name     string `json:"name"`
	Ignored  bool   `json:"ignored"`
	Selected bool   `json:"selected"`
}
type Binary struct {
	ID      string `json:"id"`
	Package string `json:"package"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Cases   []Case `json:"cases"`
}
type Inventory struct {
	Schema   int      `json:"schema"`
	Version  string   `json:"version"`
	Binaries []Binary `json:"binaries"`
}

// ExpectedJUnitCases binds selected native JUnit identities to whole package
// units. Filtered/ignored cases are retained in native discovery diagnostics;
// nextest's default JUnit report includes only cases selected for execution.
func (i Inventory) ExpectedJUnitCases(units []model.Unit) ([]nativerunner.Case, error) {
	owners := map[string]string{}
	for _, unit := range units {
		if unit.Kind != "nextest-package" || unit.ID == "" || !packageName(unit.Selector) || owners[unit.Selector] != "" {
			return nil, errors.New("invalid nextest result owner inventory")
		}
		owners[unit.Selector] = unit.ID
	}
	if i.Schema != 1 || i.Version != NativeVersion || len(owners) == 0 {
		return nil, errors.New("invalid nextest native result inventory")
	}
	result := []nativerunner.Case{}
	for _, binary := range i.Binaries {
		if owners[binary.Package] == "" {
			continue
		}
		for _, test := range binary.Cases {
			if test.Selected {
				result = append(result, nativerunner.Case{Unit: owners[binary.Package], ID: nativerunner.JUnitID(binary.ID, test.Name)})
			}
		}
	}
	if len(result) == 0 {
		return nil, errors.New("empty selected nextest result inventory")
	}
	sort.Slice(result, func(a, b int) bool { return result[a].ID < result[b].ID })
	return result, nil
}

type arguments struct {
	cargo, list, preserved []string
	profile                string
}

// Collect returns only stable native identities/statuses. Absolute build and
// source paths, process logs and raw environment values are never returned.
func Collect(ctx context.Context, root string, w model.Workspace, c model.Context) (Inventory, error) {
	a, err := parse(w)
	if err != nil {
		return Inventory{}, err
	}
	result, _, err := collect(ctx, root, w, c, a)
	return result, err
}

// Discover rounds every native binary to its whole configured Cargo package.
func Discover(ctx context.Context, root string, w model.Workspace, c model.Context) (model.Evidence, error) {
	a, err := parse(w)
	if err != nil {
		return model.Evidence{}, err
	}
	list, e, err := collect(ctx, root, w, c, a)
	if err != nil {
		return e, err
	}
	e.Adapter = "nextest"
	e.Version = Version + ";nextest=" + list.Version
	selected := map[string]bool{}
	for _, b := range list.Binaries {
		for _, test := range b.Cases {
			if test.Selected {
				selected[b.Package] = true
			}
		}
	}
	units := []model.Unit{}
	for _, unit := range e.Units {
		if selected[unit.Selector] {
			unit.Kind = "nextest-package"
			units = append(units, unit)
		}
	}
	if len(units) == 0 {
		return e, errors.New("native nextest inventory has no selected packages")
	}
	e.Units = units
	// Native build/profile/filter settings and generated artifacts influence all
	// selected packages until the shared complete-input boundary is qualified.
	owners := []string{}
	for _, unit := range units {
		owners = append(owners, unit.ID)
	}
	sort.Strings(owners)
	for input := range e.Inputs {
		e.Inputs[input] = append([]string(nil), owners...)
	}
	base := filepath.Join(root, filepath.FromSlash(w.Root), filepath.FromSlash(w.Command.Dir), ".config")
	if _, err := os.Lstat(base); err == nil {
		err = filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() {
				return errors.New("unsupported nextest configuration input type")
			}
			rel, err := filepath.Rel(root, path)
			if err != nil || !config.Relative(filepath.ToSlash(rel)) {
				return errors.New("unsafe nextest configuration input")
			}
			if len(e.Inputs) >= 50000 || (len(e.Inputs)+1)*len(owners) > 1000000 {
				return errors.New("nextest configuration inventory exceeds limit")
			}
			e.Inputs[filepath.ToSlash(rel)] = append([]string(nil), owners...)
			return nil
		})
		if err != nil {
			return e, err
		}
	} else if !os.IsNotExist(err) {
		return e, errors.New("nextest configuration inventory unavailable")
	}
	e.Gaps = append(e.Gaps, model.Gap{Code: "nextest-native-inventory", Workspace: w.ID, Detail: "Installed version-qualified nextest binary/case discovery is complete for its configured filters; this is not runtime influence proof"}, model.Gap{Code: "collection-side-effects", Workspace: w.ID, Detail: "Native listing compiles build scripts/procedural macros and executes trusted test binaries; no sandbox or network enforcement is claimed"}, model.Gap{Code: "collection-context-modified", Workspace: w.ID, Detail: "Discovery builds in a private target directory with offline locked Cargo; nextest may create empty profile store directories; original required execution remains unchanged"})
	return e, nil
}

func collect(ctx context.Context, root string, w model.Workspace, c model.Context, a arguments) (Inventory, model.Evidence, error) {
	result := Inventory{Schema: 1, Version: NativeVersion, Binaries: []Binary{}}
	if err := ctx.Err(); err != nil {
		return result, model.Evidence{}, err
	}
	if c.OS != "" && c.OS != runtime.GOOS || c.Arch != "" && c.Arch != runtime.GOARCH {
		return result, model.Evidence{}, errors.New("nextest native listing requires host context")
	}
	if path, ok := c.Env["PATH"]; ok && path != os.Getenv("PATH") {
		return result, model.Evidence{}, errors.New("nextest PATH overrides are unsupported; bind installed tools explicitly")
	}
	var err error
	root, err = filepath.Abs(root)
	if err != nil {
		return result, model.Evidence{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return result, model.Evidence{}, err
	}
	if !config.Relative(w.Root) || !config.Relative(w.Command.Dir) {
		return result, model.Evidence{}, errors.New("invalid nextest workspace directory")
	}
	dir := filepath.Join(root, filepath.FromSlash(w.Root), filepath.FromSlash(w.Command.Dir))
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil || !within(root, dir) {
		return result, model.Evidence{}, errors.New("nextest cwd escapes snapshot")
	}
	cargoWorkspace := w
	cargoWorkspace.Adapter = "cargo"
	cargoWorkspace.Command = model.Command{Dir: w.Command.Dir, Executable: "cargo", Args: append([]string{"test"}, a.cargo...)}
	tmp, err := os.MkdirTemp("", "hayaku-nextest-list-")
	if err != nil {
		return result, model.Evidence{}, err
	}
	defer os.RemoveAll(tmp)
	bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	env := map[string]string{}
	for k, v := range c.Env {
		env[k] = v
	}
	env["CARGO_NET_OFFLINE"] = "true"
	env["RUSTUP_AUTO_INSTALL"] = "0"
	env["CARGO_TERM_COLOR"] = "never"
	env["CARGO_TARGET_DIR"] = filepath.Join(tmp, "target")
	collectionContext := c
	collectionContext.Env = env
	e, err := cargo.Discover(bounded, root, cargoWorkspace, collectionContext)
	if err != nil {
		if ctx.Err() != nil {
			return result, e, ctx.Err()
		}
		return result, e, err
	}
	packages := map[string]bool{}
	for _, unit := range e.Units {
		packages[unit.Selector] = true
	}
	version, err := process.Run(bounded, model.Command{Executable: w.Command.Executable, Args: []string{"nextest", "--version"}}, dir, env)
	if err != nil || !strings.HasPrefix(string(version.Stdout), "cargo-nextest "+NativeVersion+" ") {
		return result, e, errors.New("installed nextest version is unsupported or unavailable")
	}
	args := append([]string{"nextest", "list", "--message-format=json", "--offline", "--locked", "--target-dir", filepath.Join(tmp, "target")}, a.list...)
	out, err := process.Run(bounded, model.Command{Executable: w.Command.Executable, Args: args}, dir, env)
	if bounded.Err() != nil {
		return result, e, bounded.Err()
	}
	if err != nil {
		return result, e, errors.New("offline locked nextest listing failed; diagnostics withheld")
	}
	var raw map[string]json.RawMessage
	if err := nativerunner.DecodeJSON(out.Stdout, &raw); err != nil {
		return result, e, err
	}
	var payload struct {
		TestCount int `json:"test-count"`
		Suites    map[string]struct {
			Package string `json:"package-name"`
			ID      string `json:"binary-id"`
			Name    string `json:"binary-name"`
			Kind    string `json:"kind"`
			Status  string `json:"status"`
			Cases   map[string]struct {
				Ignored bool `json:"ignored"`
				Filter  struct {
					Status string `json:"status"`
				} `json:"filter-match"`
			} `json:"testcases"`
		} `json:"rust-suites"`
	}
	if json.Unmarshal(out.Stdout, &payload) != nil || payload.TestCount <= 0 || payload.TestCount > 100000 || len(payload.Suites) == 0 || len(payload.Suites) > 50000 {
		return result, e, errors.New("invalid nextest native list inventory")
	}
	count := 0
	for key, suite := range payload.Suites {
		if key != suite.ID || !identity(suite.ID) || !identity(suite.Name) || !packages[suite.Package] || suite.Status != "listed" || suite.Kind == "" {
			return result, e, errors.New("nextest binary identity does not match configured Cargo packages")
		}
		binary := Binary{ID: suite.ID, Package: suite.Package, Name: suite.Name, Kind: suite.Kind, Cases: []Case{}}
		for name, test := range suite.Cases {
			count++
			if count > 100000 || !identity(name) || test.Filter.Status != "matches" && test.Filter.Status != "mismatch" {
				return result, e, errors.New("invalid nextest native case/filter identity")
			}
			binary.Cases = append(binary.Cases, Case{Name: name, Ignored: test.Ignored, Selected: test.Filter.Status == "matches"})
		}
		sort.Slice(binary.Cases, func(i, j int) bool { return binary.Cases[i].Name < binary.Cases[j].Name })
		result.Binaries = append(result.Binaries, binary)
	}
	if count != payload.TestCount {
		return result, e, errors.New("incomplete nextest native case count")
	}
	sort.Slice(result.Binaries, func(i, j int) bool { return result.Binaries[i].ID < result.Binaries[j].ID })
	return result, e, nil
}

// Proposal retains native nextest profile, Cargo build profile, filters, targets
// and runner semantics; only package scope is replaced. No omission is authorized.
func Proposal(w model.Workspace, units []model.Unit) (model.Command, error) {
	a, err := parse(w)
	if err != nil {
		return model.Command{}, err
	}
	packages := map[string]bool{}
	for _, unit := range units {
		if unit.Workspace != w.ID || unit.Kind != "nextest-package" || !packageName(unit.Selector) {
			return model.Command{}, errors.New("invalid nextest proposal unit")
		}
		packages[unit.Selector] = true
	}
	if len(packages) == 0 {
		return model.Command{}, errors.New("empty nextest proposal")
	}
	names := []string{}
	for name := range packages {
		names = append(names, name)
	}
	sort.Strings(names)
	cmd := w.Command
	cmd.Args = append([]string{"nextest", "run"}, a.preserved...)
	for _, name := range names {
		cmd.Args = append(cmd.Args, "--package", name)
	}
	return cmd, nil
}
func parse(w model.Workspace) (arguments, error) {
	a := arguments{}
	name := filepath.Base(w.Command.Executable)
	if name != "cargo" && name != "cargo.exe" || len(w.Command.Args) < 2 || w.Command.Args[0] != "nextest" || w.Command.Args[1] != "run" {
		return a, errors.New("nextest discovery requires cargo nextest run")
	}
	if len(w.Patterns) > 0 || len(w.BuildFlags) > 0 {
		return a, errors.New("nextest context must be encoded in command argv")
	}
	buildBoolean := map[string]bool{"--workspace": true, "--all": true, "--no-default-features": true, "--all-features": true, "--offline": true, "--locked": true, "--frozen": true, "--release": true, "-r": true}
	buildValue := map[string]bool{"--package": true, "-p": true, "--exclude": true, "--features": true, "-F": true, "--target": true, "--cargo-profile": true, "--build-jobs": true}
	listBoolean := map[string]bool{"--lib": true, "--bins": true, "--examples": true, "--tests": true, "--benches": true, "--all-targets": true, "--ignore-default-filter": true}
	listValue := map[string]bool{"--bin": true, "--example": true, "--test": true, "--bench": true, "--run-ignored": true, "--profile": true, "-P": true, "--config-file": true, "--user-config-file": true, "--filterset": true, "-E": true, "--partition": true}
	runBoolean := map[string]bool{"--no-fail-fast": true, "--fail-fast": true, "--no-capture": true}
	runValue := map[string]bool{"--test-threads": true, "-j": true, "--retries": true, "--flaky-result": true, "--max-fail": true, "--failure-output": true, "--success-output": true, "--status-level": true, "--final-status-level": true}
	for i := 2; i < len(w.Command.Args); i++ {
		arg := w.Command.Args[i]
		if !strings.HasPrefix(arg, "-") {
			if !identity(arg) {
				return a, errors.New("invalid nextest positional filter")
			}
			a.list = append(a.list, arg)
			a.preserved = append(a.preserved, arg)
			continue
		}
		key, value, has := strings.Cut(arg, "=")
		boolean := buildBoolean[key] || listBoolean[key] || runBoolean[key]
		valued := buildValue[key] || listValue[key] || runValue[key]
		if !boolean && !valued {
			return a, errors.New("unsupported nextest argv flag")
		}
		if boolean && has {
			return a, errors.New("nextest boolean flag has value")
		}
		items := []string{arg}
		if valued && !has {
			i++
			if i >= len(w.Command.Args) {
				return a, errors.New("missing nextest flag value")
			}
			value = w.Command.Args[i]
			items = append(items, value)
		}
		if valued && !identity(value) {
			return a, errors.New("invalid nextest flag value")
		}
		if buildBoolean[key] || buildValue[key] {
			cargoItems := append([]string(nil), items...)
			switch key {
			case "-r":
				cargoItems[0] = "--release"
			case "--cargo-profile":
				cargoItems[0] = "--profile"
				if has {
					cargoItems[0] += "=" + value
				}
			case "--build-jobs":
				cargoItems[0] = "--jobs"
				if has {
					cargoItems[0] += "=" + value
				}
			}
			a.cargo = append(a.cargo, cargoItems...)
		}
		if !runBoolean[key] && !runValue[key] && key != "--offline" && key != "--locked" {
			a.list = append(a.list, items...)
		}
		if key != "--workspace" && key != "--all" && key != "--package" && key != "-p" && key != "--exclude" {
			a.preserved = append(a.preserved, items...)
		}
	}
	return a, nil
}
func identity(s string) bool { return s != "" && len(s) <= 8192 && !strings.ContainsAny(s, "\x00\r\n") }
func packageName(s string) bool {
	if !identity(s) {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
