package native

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
)

// Case is a caller-bound complete native test inventory. Report names alone
// never establish ownership, source completeness or authority to omit tests.
type Case struct {
	Unit string `json:"unit"`
	ID   string `json:"id"`
}

// JUnitID avoids ambiguous concatenation of class and parameterized test names.
func JUnitID(class, name string) string {
	data, _ := json.Marshal([]string{class, name})
	return string(data)
}

type xmlNode struct {
	name     xml.Name
	attrs    map[string]string
	children []*xmlNode
}

// XML diagnostics and properties are bounded while parsing and discarded.
// DTDs, external entities, duplicate attributes and multiple roots are rejected.
func readXML(r io.Reader) (*xmlNode, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxResultBytes+1))
	if err != nil || len(data) > MaxResultBytes {
		return nil, errors.New("XML report read or size limit")
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	var root *xmlNode
	stack := []*xmlNode{}
	count := 0
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("invalid XML report")
		}
		switch t := token.(type) {
		case xml.StartElement:
			count++
			if count > 200000 || len(stack) >= 48 {
				return nil, errors.New("XML report complexity exceeds limit")
			}
			n := &xmlNode{name: t.Name, attrs: map[string]string{}}
			for _, a := range t.Attr {
				key := a.Name.Space + "\x00" + a.Name.Local
				if _, ok := n.attrs[key]; ok || len(a.Value) > 65536 {
					return nil, errors.New("ambiguous XML report attribute")
				}
				n.attrs[key] = a.Value
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, errors.New("multiple XML report roots")
				}
				root = n
			} else {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, errors.New("unbalanced XML report")
			}
			stack = stack[:len(stack)-1]
		case xml.Directive:
			return nil, errors.New("XML directives are unsupported")
		case xml.ProcInst:
			if t.Target != "xml" || root != nil {
				return nil, errors.New("XML processing instruction unsupported")
			}
		case xml.CharData:
			if len(stack) == 0 && strings.TrimSpace(string(t)) != "" {
				return nil, errors.New("trailing XML report text")
			}
		}
	}
	if root == nil || len(stack) != 0 {
		return nil, errors.New("incomplete XML report")
	}
	return root, nil
}
func (n *xmlNode) attr(name string) string { return n.attrs["\x00"+name] }
func (n *xmlNode) childrenNamed(name string) []*xmlNode {
	result := []*xmlNode{}
	for _, child := range n.children {
		if child.name.Local == name {
			result = append(result, child)
		}
	}
	return result
}

type caseAccumulator struct {
	result  Result
	want    map[string]string
	seen    map[string]bool
	actions map[string]string
}

func newCases(expected []Case) (*caseAccumulator, error) {
	a := &caseAccumulator{result: Result{Units: []Outcome{}, Tests: []Outcome{}, Missing: []string{}}, want: map[string]string{}, seen: map[string]bool{}, actions: map[string]string{}}
	if len(expected) == 0 || len(expected) > 100000 {
		return a, errors.New("invalid complete expected case inventory")
	}
	for _, c := range expected {
		if !safe(c.Unit) || !safe(c.ID) || a.want[c.ID] != "" {
			return a, errors.New("invalid or duplicate expected case")
		}
		a.want[c.ID] = c.Unit
	}
	return a, nil
}
func (a *caseAccumulator) add(id, action string) error {
	unit := a.want[id]
	if unit == "" || a.seen[id] || !terminal(action) {
		return errors.New("unexpected, duplicate or non-terminal native case")
	}
	a.seen[id] = true
	a.result.Tests = append(a.result.Tests, Outcome{Unit: unit, Test: id, Action: action})
	prior := a.actions[unit]
	if action == "fail" || prior == "fail" {
		a.actions[unit] = "fail"
		a.result.Failed = true
	} else if action == "pass" || prior == "pass" {
		a.actions[unit] = "pass"
	} else {
		a.actions[unit] = "skip"
	}
	return nil
}
func (a *caseAccumulator) finish() (Result, error) {
	missingUnits := map[string]bool{}
	for id, unit := range a.want {
		if !a.seen[id] {
			missingUnits[unit] = true
		}
	}
	for unit := range missingUnits {
		a.result.Missing = append(a.result.Missing, unit)
	}
	for unit, action := range a.actions {
		if !missingUnits[unit] {
			a.result.Units = append(a.result.Units, Outcome{Unit: unit, Action: action})
		}
	}
	sortResult(&a.result)
	if len(a.result.Missing) > 0 {
		return a.result, errors.New("missing native case terminal outcomes")
	}
	return a.result, nil
}

// ReconcileJUnit supports the common testsuites/testsuite/testcase dialect.
// Unsupported retry/rerun extensions fail closed instead of changing results.
func ReconcileJUnit(r io.Reader, expected []Case) (Result, error) {
	a, err := newCases(expected)
	if err != nil {
		return a.result, err
	}
	root, err := readXML(r)
	if err != nil {
		return a.result, err
	}
	if root.name.Space != "" || (root.name.Local != "testsuite" && root.name.Local != "testsuites") {
		return a.result, errors.New("unsupported JUnit report root")
	}
	type counts struct{ tests, failures, errors, skips int }
	var visit func(*xmlNode) (counts, error)
	visit = func(suite *xmlNode) (counts, error) {
		c := counts{}
		for _, child := range suite.children {
			if child.name.Space != "" {
				return c, errors.New("unsupported JUnit XML namespace")
			}
			switch child.name.Local {
			case "testsuite":
				nested, err := visit(child)
				if err != nil {
					return c, err
				}
				c.tests += nested.tests
				c.failures += nested.failures
				c.errors += nested.errors
				c.skips += nested.skips
			case "testcase":
				if suite.name.Local == "testsuites" || !safe(child.attr("name")) {
					return c, errors.New("invalid JUnit case identity")
				}
				action := "pass"
				terminalCount := 0
				for _, outcome := range child.children {
					if outcome.name.Space != "" {
						return c, errors.New("unsupported JUnit case namespace")
					}
					switch outcome.name.Local {
					case "failure":
						action = "fail"
						terminalCount++
						c.failures++
					case "error":
						action = "fail"
						terminalCount++
						c.errors++
					case "skipped":
						action = "skip"
						terminalCount++
						c.skips++
					case "system-out", "system-err", "properties":
					default:
						return c, errors.New("unsupported JUnit case extension")
					}
				}
				if terminalCount > 1 {
					return c, errors.New("conflicting JUnit case terminal outcomes")
				}
				status := child.attr("status")
				if status != "" && status != "run" && status != "passed" && status != "failed" && status != "skipped" {
					return c, errors.New("non-terminal JUnit case status")
				}
				if status == "failed" && action != "fail" || status == "skipped" && action != "skip" {
					return c, errors.New("conflicting JUnit status")
				}
				if err := a.add(JUnitID(child.attr("classname"), child.attr("name")), action); err != nil {
					return c, err
				}
				c.tests++
			case "properties", "system-out", "system-err":
			default:
				return c, errors.New("unsupported JUnit suite element")
			}
		}
		for name, actual := range map[string]int{"tests": c.tests, "failures": c.failures, "errors": c.errors, "skipped": c.skips} {
			if value := suite.attr(name); value != "" {
				n, err := strconv.Atoi(value)
				if err != nil || n < 0 || n != actual {
					return c, errors.New("inconsistent JUnit suite counters")
				}
			}
		}
		return c, nil
	}
	if _, err := visit(root); err != nil {
		return a.result, err
	}
	return a.finish()
}

const trxNamespace = "http://microsoft.com/schemas/VisualStudio/TeamTest/2010"

// ReconcileTRX supports VSTest's terminal UnitTestResult dialect. Expected IDs
// bind testId to owners independently; omitted, duplicate and extra IDs fail.
func ReconcileTRX(r io.Reader, expected []Case) (Result, error) {
	a, err := newCases(expected)
	if err != nil {
		return a.result, err
	}
	root, err := readXML(r)
	if err != nil {
		return a.result, err
	}
	if root.name.Local != "TestRun" || root.name.Space != trxNamespace {
		return a.result, errors.New("unsupported TRX namespace or root")
	}
	var validate func(*xmlNode) error
	validate = func(n *xmlNode) error {
		if n.name.Space != trxNamespace {
			return errors.New("mixed TRX namespaces")
		}
		for _, c := range n.children {
			if err := validate(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err := validate(root); err != nil {
		return a.result, err
	}
	results, summaries := root.childrenNamed("Results"), root.childrenNamed("ResultSummary")
	if len(results) != 1 || len(summaries) != 1 {
		return a.result, errors.New("missing or duplicate TRX result sections")
	}
	counter := map[string]int{"total": 0, "executed": 0, "passed": 0, "failed": 0, "notExecuted": 0}
	for _, test := range results[0].children {
		if test.name.Local != "UnitTestResult" {
			return a.result, errors.New("unsupported TRX result type")
		}
		for _, c := range test.children {
			if c.name.Local != "Output" {
				return a.result, errors.New("unsupported nested TRX result")
			}
		}
		action := ""
		counter["total"]++
		switch test.attr("outcome") {
		case "Passed":
			action = "pass"
			counter["passed"]++
			counter["executed"]++
		case "Failed":
			action = "fail"
			counter["failed"]++
			counter["executed"]++
		case "NotExecuted":
			action = "skip"
			counter["notExecuted"]++
		default:
			return a.result, errors.New("unsupported or non-terminal TRX outcome")
		}
		if err := a.add(test.attr("testId"), action); err != nil {
			return a.result, err
		}
	}
	summary := summaries[0]
	if summary.attr("outcome") != "Completed" && summary.attr("outcome") != "Passed" && summary.attr("outcome") != "Failed" {
		return a.result, errors.New("non-terminal TRX run summary")
	}
	if summary.attr("outcome") == "Failed" {
		if !a.result.Failed {
			a.result.UnhandledErrors++
		}
		a.result.Failed = true
	}
	counters := summary.childrenNamed("Counters")
	if len(counters) != 1 {
		return a.result, errors.New("missing TRX counters")
	}
	for name, actual := range counter {
		value := counters[0].attr(name)
		n, err := strconv.Atoi(value)
		if err != nil || n != actual {
			return a.result, errors.New("inconsistent TRX counters")
		}
	}
	for _, name := range []string{"error", "timeout", "aborted", "inconclusive", "passedButRunAborted", "notRunnable", "disconnected", "inProgress", "pending"} {
		if value := counters[0].attr(name); value != "" && value != "0" {
			return a.result, errors.New("incomplete or unsupported TRX counter outcome")
		}
	}
	for _, infos := range summary.childrenNamed("RunInfos") {
		for _, info := range infos.children {
			if info.name.Local != "RunInfo" || info.attr("outcome") != "Passed" && info.attr("outcome") != "Completed" && info.attr("outcome") != "Warning" {
				a.result.Failed = true
				a.result.UnhandledErrors++
			}
		}
	}
	if a.result.UnhandledErrors > 0 {
		return a.result, errors.New("unhandled TRX errors invalidate reconciliation")
	}
	return a.finish()
}
