package native

import (
	"strings"
	"testing"
)

func TestXcodeEnumerationAndResultCompleteness(t *testing.T) {
	enumeration := `{"errors":[],"values":[{"enabledTests":[{"identifier":"Tests/Class/testPass()"},{"identifier":"Tests/Class/testSkip()"}],"disabledTests":[]}]}`
	cases, err := XcodeCases([]byte(enumeration), "x:suite")
	if err != nil {
		t.Fatal(err)
	}
	tree := `{"devices":[{}],"testPlanConfigurations":[{}],"testNodes":[{"nodeType":"Unit test bundle","name":"Tests","children":[{"nodeType":"Test Case","nodeIdentifier":"Class/testPass()","result":"Passed"},{"nodeType":"Test Case","nodeIdentifier":"Class/testSkip()","result":"Skipped"}]}]}`
	summary := `{"result":"Passed","totalTestCount":2,"passedTests":1,"failedTests":0,"skippedTests":1,"expectedFailures":0,"startTime":1,"finishTime":2}`
	r, err := ReconcileXCResult([]byte(tree), []byte(summary), cases)
	if err != nil || !r.Complete || r.Failed || len(r.Tests) != 2 || r.Tests[1].Action != "skip" {
		t.Fatal("Xcode skip/full outcomes lost", r, err)
	}
	for _, bad := range []string{strings.Replace(tree, "testSkip()", "unknown()", 1), strings.Replace(tree, "Skipped", "unknown", 1), strings.Replace(tree, "[{}]", "[{},{}]", 1), strings.Replace(tree, "Test Case", "Repetition", 1), strings.Replace(tree, "testSkip()", "testPass()", 1), strings.Replace(tree, `"result":"Skipped"`, `"result":"Skipped","result":"Passed"`, 1)} {
		if r, err := ReconcileXCResult([]byte(bad), []byte(summary), cases); err == nil || r.Complete {
			t.Fatal("ambiguous Xcode report accepted")
		}
	}
	for _, bad := range []string{strings.Replace(summary, `"totalTestCount":2`, `"totalTestCount":1`, 1), strings.Replace(summary, `"finishTime":2`, `"finishTime":0`, 1), strings.Replace(summary, `"Passed"`, `"Failed"`, 1), `{}`} {
		if _, err := ReconcileXCResult([]byte(tree), []byte(bad), cases); err == nil {
			t.Fatal("partial/conflicting summary accepted")
		}
	}
	for _, bad := range []string{`{"errors":[{}],"values":[]}`, strings.Replace(enumeration, "testSkip()", "testPass()", 1), `{"errors":[],"values":[]}`} {
		if _, err := XcodeCases([]byte(bad), "x:suite"); err == nil {
			t.Fatal("incomplete enumeration accepted")
		}
	}
}
