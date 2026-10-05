// Package runner validates normalized outcomes independently of runner syntax.
package runner

import (
	"errors"
	"strings"

	"github.com/Cyberlane/hayaku/internal/model"
)

// Validate requires an exact terminal unit inventory, valid test ownership and
// successful process completion. A complete failing run remains a failure.
func Validate(result model.Outcomes, expected []model.Unit, completed bool, exit int) error {
	if !completed || !result.Complete || result.Errors != 0 || len(expected) == 0 || len(expected) > 50000 || len(result.Tests) > 100000 {
		return errors.New("runner results incomplete or contain unhandled errors")
	}
	want, seen := map[string]bool{}, map[string]bool{}
	for _, unit := range expected {
		if !identity(unit.ID) || unit.Workspace == "" || want[unit.ID] {
			return errors.New("invalid expected runner inventory")
		}
		want[unit.ID] = true
	}
	failed := result.Failed
	for _, outcome := range result.Units {
		if !want[outcome.Unit] || seen[outcome.Unit] || outcome.Test != "" || !terminal(outcome.Action) {
			return errors.New("unexpected, duplicate or nonterminal runner unit")
		}
		seen[outcome.Unit] = true
		failed = failed || outcome.Action == "fail"
	}
	if len(seen) != len(want) {
		return errors.New("missing runner unit terminal outcomes")
	}
	cases := map[[2]string]bool{}
	for _, outcome := range result.Tests {
		key := [2]string{outcome.Unit, outcome.Test}
		if !want[outcome.Unit] || !identity(outcome.Test) || cases[key] || !terminal(outcome.Action) {
			return errors.New("unexpected, duplicate or nonterminal runner test")
		}
		cases[key] = true
		failed = failed || outcome.Action == "fail"
	}
	if exit < 0 || (exit != 0 && !failed) {
		return errors.New("runner exit status has no reconciled failure")
	}
	return nil
}

func Failed(result model.Outcomes) bool {
	if result.Failed || result.Errors != 0 {
		return true
	}
	for _, items := range [][]model.Outcome{result.Units, result.Tests} {
		for _, item := range items {
			if item.Action == "fail" {
				return true
			}
		}
	}
	return false
}

func identity(value string) bool {
	return value != "" && len(value) <= 8192 && !strings.ContainsAny(value, "\x00\r\n")
}
func terminal(value string) bool {
	return value == "pass" || value == "fail" || value == "skip" || value == "bench"
}
