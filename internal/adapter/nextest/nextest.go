// Package nextest augments offline Cargo package evidence with the installed
// nextest's configured binary/case inventory. Runtime influence is unqualified.
package nextest

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Cyberlane/hayaku/internal/model"
	nativerunner "github.com/Cyberlane/hayaku/internal/runner/native"
)

const Version = "nextest-build-closure-v2"
const NativeVersion = "0.9.146"

type Case struct {
	Name     string `json:"name"`
	Ignored  bool   `json:"ignored"`
	Selected bool   `json:"selected"`
}
type Binary struct {
	Owner          string `json:"owner"`
	ArtifactDigest string `json:"artifact_digest"`
	BuildPlatform  string `json:"build_platform"`
	ID             string `json:"id"`
	Package        string `json:"package"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Cases          []Case `json:"cases"`
}
type Inventory struct {
	Digest              string                   `json:"digest"`
	BuildMetadataDigest string                   `json:"build_metadata_digest"`
	ToolchainDigest     string                   `json:"toolchain_digest"`
	ContextDigest       string                   `json:"context_digest"`
	Execution           model.ExecutionInventory `json:"execution"`
	InventoryComplete   bool                     `json:"inventory_complete"`
	Schema              int                      `json:"schema"`
	Version             string                   `json:"version"`
	Binaries            []Binary                 `json:"binaries"`
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
func identity(s string) bool {
	return s != "" && len(s) <= 8192 && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}
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
