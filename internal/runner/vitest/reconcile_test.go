package vitest

import (
	"github.com/Cyberlane/hayaku/internal/model"
	"strings"
	"testing"
)

func expected() []model.Unit {
	return []model.Unit{{ID: "js:node:a.test.ts", Workspace: "js", Kind: "vitest-file", Selector: "a.test.ts"}, {ID: "js:browser:a.test.ts", Workspace: "js", Kind: "vitest-file", Selector: "a.test.ts"}}
}
func TestProjectAwareDuplicateNamesAndFailures(t *testing.T) {
	raw := `{"schema":1,"complete":true,"errors":0,"files":[{"file":"a.test.ts","project":"node","action":"failed"},{"file":"a.test.ts","project":"browser","action":"passed"}],"tests":[{"file":"a.test.ts","project":"node","test":"[\"suite > same\",0]","action":"passed"},{"file":"a.test.ts","project":"node","test":"[\"suite > same\",1]","action":"failed"},{"file":"a.test.ts","project":"browser","test":"[\"suite > same\",0]","action":"skipped"}]}`
	got, err := Reconcile(strings.NewReader(raw), expected())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Failed || len(got.Files) != 2 || len(got.Tests) != 3 || got.Tests[0].Action != "skip" {
		t.Fatalf("lost identities/outcomes: %+v", got)
	}
}
func TestRejectIncompleteOrUnexpectedProtocol(t *testing.T) {
	base := `{"schema":1,"complete":true,"errors":0,"files":[{"file":"a.test.ts","project":"node","action":"passed"},{"file":"a.test.ts","project":"browser","action":"skipped"}],"tests":[]}`
	cases := []string{
		base[:len(base)-1], base + ` {}`, strings.Replace(base, `"complete":true`, `"complete":false`, 1), strings.Replace(base, `"schema":1`, `"schema":2`, 1), strings.Replace(base, `"errors":0`, `"errors":-1`, 1), strings.Replace(base, `"project":"node"`, `"project":"other"`, 1), strings.Replace(base, `"action":"passed"`, `"action":"pending"`, 1), strings.Replace(base, `"project":"browser"`, `"project":"node"`, 1), strings.Replace(base, `"files":[`, `"files":[{"file":"b.test.ts","project":"node","action":"passed"},`, 1), strings.Replace(base, `,"tests":[]`, `,"unknown":true,"tests":[]`, 1),
	}
	for _, raw := range cases {
		if _, err := Reconcile(strings.NewReader(raw), expected()); err == nil {
			t.Fatalf("invalid protocol accepted: %s", raw)
		}
	}
	if _, err := Reconcile(strings.NewReader(base), nil); err == nil {
		t.Fatal("empty expected accepted")
	}
	if _, err := Reconcile(strings.NewReader(`{"schema":1,"complete":true,"errors":0,"files":[],"tests":[]}`), expected()); err == nil {
		t.Fatal("missing files accepted")
	}
}
func TestUnhandledErrorsAndSkippedFiles(t *testing.T) {
	raw := `{"schema":1,"complete":true,"errors":1,"files":[{"file":"a.test.ts","project":"node","action":"skipped"},{"file":"a.test.ts","project":"browser","action":"passed"}],"tests":[]}`
	got, err := Reconcile(strings.NewReader(raw), expected())
	if err != nil || !got.Failed || got.UnhandledErrors != 1 {
		t.Fatalf("unhandled error lost: %+v %v", got, err)
	}
}

func TestRejectMissingMalformedAndDuplicateTestIdentities(t *testing.T) {
	prefix := `{"schema":1,"complete":true,"errors":0,"files":[{"file":"a.test.ts","project":"node","action":"passed"},{"file":"a.test.ts","project":"browser","action":"passed"}],"tests":[`
	valid := `{"file":"a.test.ts","project":"node","test":"[\"same\",0]","action":"passed"}`
	cases := []string{
		valid + "," + valid,
		strings.Replace(valid, `[\"same\",0]`, `[\"same\",-1]`, 1),
		strings.Replace(valid, `[\"same\",0]`, `not-an-identity`, 1),
		strings.Replace(valid, `"test":"[\"same\",0]",`, "", 1),
		strings.Replace(valid, `"action":"passed"`, `"action":"pending"`, 1),
	}
	for _, item := range cases {
		if _, err := Reconcile(strings.NewReader(prefix+item+`]}`), expected()); err == nil {
			t.Fatalf("invalid test terminal accepted: %s", item)
		}
	}
}
