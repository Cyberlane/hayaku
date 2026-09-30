// Package golang reconciles native go test -json outcomes without storing output.
package golang

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
)

const maxResultBytes = 64 << 20

type Outcome struct {
	Package string `json:"package"`
	Test    string `json:"test,omitempty"`
	Action  string `json:"action"`
	Cached  bool   `json:"cached,omitempty"`
}
type Result struct {
	Packages []Outcome `json:"packages"`
	Tests    []Outcome `json:"tests"`
	Missing  []string  `json:"missing"`
	Failed   bool      `json:"failed"`
}
type event struct{ Action, Package, Test, Output, ImportPath string }

// Reconcile requires a terminal outcome for every expected package. Individual
// test skips remain skips. Raw diagnostics are intentionally never returned.
// The caller must additionally require the native process to exit successfully.
func Reconcile(r io.Reader, expected []string) (Result, error) {
	result := Result{}
	if len(expected) == 0 {
		return result, errors.New("Go reconciliation requires expected packages")
	}
	want := map[string]bool{}
	for _, p := range expected {
		if p == "" || want[p] {
			return result, errors.New("invalid expected package inventory")
		}
		want[p] = true
	}
	packages, tests, cached := map[string]Outcome{}, map[string]Outcome{}, map[string]bool{}
	started := map[string]bool{}
	scanner := bufio.NewScanner(io.LimitReader(r, maxResultBytes+1))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	consumed := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		consumed += len(line) + 1
		if consumed > maxResultBytes {
			return result, errors.New("Go result output limit exceeded")
		}
		var e event
		if err := json.Unmarshal(line, &e); err != nil {
			return result, errors.New("invalid Go test JSON")
		}
		if e.Action == "build-output" || e.Action == "build-fail" {
			if e.ImportPath == "" && e.Package == "" {
				return result, errors.New("Go build event lacks package identity")
			}
			if e.Action == "build-fail" {
				result.Failed = true
			}
			continue
		}
		if e.Package == "" {
			return result, errors.New("Go test event lacks package")
		}
		switch e.Action {
		case "output":
			if e.Test == "" && strings.HasPrefix(e.Output, "ok  \t"+e.Package+"\t(cached)") {
				cached[e.Package] = true
			}
		case "run":
			if e.Test == "" {
				return result, errors.New("Go run event lacks test identity")
			}
			started[e.Package+"\x00"+e.Test] = true
		case "start", "pause", "cont":
		case "pass", "fail", "skip", "bench":
			action := e.Action
			o := Outcome{Package: e.Package, Test: e.Test, Action: action}
			key := e.Package + "\x00" + e.Test
			dest := tests
			if e.Test == "" {
				dest = packages
				key = e.Package
			}
			if old, ok := dest[key]; ok && old.Action != o.Action {
				return result, errors.New("conflicting Go terminal outcomes")
			}
			dest[key] = o
			if action == "fail" {
				result.Failed = true
			}
		default:
			return result, errors.New("unsupported Go event action")
		}
	}
	if scanner.Err() != nil {
		return result, errors.New("Go result read failed")
	}
	for key := range started {
		if _, ok := tests[key]; !ok {
			return result, errors.New("missing Go test terminal outcomes")
		}
	}
	for p := range want {
		if _, ok := packages[p]; !ok {
			result.Missing = append(result.Missing, p)
		}
	}
	for p, o := range packages {
		o.Cached = cached[p]
		result.Packages = append(result.Packages, o)
	}
	for _, o := range tests {
		result.Tests = append(result.Tests, o)
	}
	sort.Strings(result.Missing)
	sort.Slice(result.Packages, func(i, j int) bool { return result.Packages[i].Package < result.Packages[j].Package })
	sort.Slice(result.Tests, func(i, j int) bool {
		a, b := result.Tests[i], result.Tests[j]
		if a.Package != b.Package {
			return a.Package < b.Package
		}
		return a.Test < b.Test
	})
	if len(result.Missing) > 0 {
		return result, errors.New("missing Go package terminal outcomes")
	}
	return result, nil
}
