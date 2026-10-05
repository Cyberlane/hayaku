package native

import (
	"encoding/json"
	"errors"
)

// XcodeCases reads the independently generated flat `-enumerate-tests` output.
// A single enabled plan is supported. Explicitly disabled cases are not runnable.
func XcodeCases(data []byte, unit string) ([]Case, error) {
	var raw map[string]json.RawMessage
	if err := DecodeJSON(data, &raw); err != nil {
		return nil, err
	}
	var inventory struct {
		Errors []json.RawMessage `json:"errors"`
		Values []struct {
			Enabled []struct {
				Identifier string `json:"identifier"`
			} `json:"enabledTests"`
			Disabled []json.RawMessage `json:"disabledTests"`
		} `json:"values"`
	}
	if json.Unmarshal(data, &inventory) != nil || len(inventory.Errors) > 0 || len(inventory.Values) != 1 || len(inventory.Values[0].Enabled) == 0 || !safe(unit) {
		return nil, errors.New("incomplete or multi-plan Xcode enumeration")
	}
	var cases []Case
	seen := map[string]bool{}
	for _, test := range inventory.Values[0].Enabled {
		if !safe(test.Identifier) || seen[test.Identifier] {
			return nil, errors.New("invalid or duplicate Xcode case")
		}
		seen[test.Identifier] = true
		cases = append(cases, Case{Unit: unit, ID: test.Identifier})
	}
	return cases, nil
}

type xcodeNode struct {
	Type     string      `json:"nodeType"`
	Name     string      `json:"name"`
	ID       string      `json:"nodeIdentifier"`
	Result   string      `json:"result"`
	Children []xcodeNode `json:"children"`
}

// ReconcileXCResult consumes xcresulttool schema 0.4.0 exports. All diagnostic
// text, device identifiers and source locations are discarded before reporting.
func ReconcileXCResult(tests, summary []byte, expected []Case) (Result, error) {
	a, err := newCases(expected)
	if err != nil {
		return a.result, err
	}
	for _, data := range [][]byte{tests, summary} {
		var raw map[string]json.RawMessage
		if err := DecodeJSON(data, &raw); err != nil {
			return a.result, err
		}
	}
	var tree struct {
		Nodes          []xcodeNode       `json:"testNodes"`
		Devices        []json.RawMessage `json:"devices"`
		Configurations []json.RawMessage `json:"testPlanConfigurations"`
	}
	var totals struct {
		Result   string   `json:"result"`
		Total    *int     `json:"totalTestCount"`
		Passed   *int     `json:"passedTests"`
		Failed   *int     `json:"failedTests"`
		Skipped  *int     `json:"skippedTests"`
		Expected *int     `json:"expectedFailures"`
		Start    *float64 `json:"startTime"`
		Finish   *float64 `json:"finishTime"`
	}
	if json.Unmarshal(tests, &tree) != nil || json.Unmarshal(summary, &totals) != nil || len(tree.Devices) != 1 || len(tree.Configurations) != 1 || totals.Total == nil || totals.Passed == nil || totals.Failed == nil || totals.Skipped == nil || totals.Expected == nil || totals.Start == nil || totals.Finish == nil || *totals.Finish < *totals.Start {
		return a.result, errors.New("incomplete or unsupported Xcode run context")
	}
	counts := map[string]int{}
	count := 0
	var visit func(xcodeNode, string, int) error
	visit = func(n xcodeNode, bundle string, depth int) error {
		count++
		if depth > 32 || count > 100000 {
			return errors.New("Xcode result tree exceeds limits")
		}
		switch n.Type {
		case "Unit test bundle", "UI test bundle":
			if bundle != "" || !safe(n.Name) {
				return errors.New("ambiguous Xcode bundle")
			}
			bundle = n.Name
		case "Test Plan", "Test Suite":
		case "Test Case":
			if bundle == "" || !safe(n.ID) {
				return errors.New("invalid Xcode case identity")
			}
			action := ""
			switch n.Result {
			case "Passed", "Expected Failure":
				action = "pass"
			case "Failed":
				action = "fail"
			case "Skipped":
				action = "skip"
			default:
				return errors.New("nonterminal Xcode case")
			}
			if err := a.add(bundle+"/"+n.ID, action); err != nil {
				return err
			}
			counts[n.Result]++
		case "Skip Message", "Failure Message", "Source Code Reference", "Attachment", "Expression", "Test Value", "Runtime Warning", "Expected Failure":
			if len(n.Children) != 0 {
				return errors.New("unsupported nested Xcode diagnostic")
			}
			return nil
		default:
			return errors.New("unsupported Xcode repetition, parameterization or node type")
		}
		for _, child := range n.Children {
			if err := visit(child, bundle, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	for _, n := range tree.Nodes {
		if err := visit(n, "", 0); err != nil {
			return a.result, err
		}
	}
	if counts["Passed"] != *totals.Passed || counts["Failed"] != *totals.Failed || counts["Skipped"] != *totals.Skipped || counts["Expected Failure"] != *totals.Expected || len(a.seen) != *totals.Total {
		return a.result, errors.New("Xcode summary/case counters conflict")
	}
	if totals.Result != "Passed" && totals.Result != "Failed" {
		return a.result, errors.New("nonterminal Xcode run")
	}
	if (totals.Result == "Failed") != a.result.Failed {
		return a.result, errors.New("Xcode run/case outcomes conflict")
	}
	result, err := a.finish()
	result.Complete = err == nil
	return result, err
}
