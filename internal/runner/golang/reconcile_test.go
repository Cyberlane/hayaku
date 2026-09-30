package golang

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutcomesAndCache(t *testing.T) {
	input := `{"Action":"start","Package":"a"}
{"Action":"run","Package":"a","Test":"TestOne"}
{"Action":"skip","Package":"a","Test":"TestOne"}
{"Action":"output","Package":"a","Output":"ok  \ta\t(cached)\n"}
{"Action":"pass","Package":"a"}
{"Action":"build-fail","ImportPath":"b"}
{"Action":"fail","Package":"b","FailedBuild":"b"}
`
	result, err := Reconcile(strings.NewReader(input), []string{"b", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Failed || len(result.Packages) != 2 || !result.Packages[0].Cached || len(result.Tests) != 1 || result.Tests[0].Action != "skip" {
		t.Fatalf("outcomes %+v", result)
	}
}
func TestMissingInvalidAndConflicting(t *testing.T) {
	for _, input := range []string{`{"Action":"pass","Package":"other"}`, `not json`, `{"Action":"mystery","Package":"a"}`, "{\"Action\":\"pass\",\"Package\":\"a\"}\n{\"Action\":\"fail\",\"Package\":\"a\"}\n", `{"Action":"pass"}`} {
		if _, err := Reconcile(strings.NewReader(input), []string{"a"}); err == nil {
			t.Errorf("accepted incomplete input: %s", input)
		}
	}
}

func TestNativeBuildEvents(t *testing.T) {
	input := `{"Action":"build-output","ImportPath":"a [a.test]","Output":"compiler error"}
{"Action":"build-fail","ImportPath":"a [a.test]"}
{"Action":"fail","Package":"a","FailedBuild":"a [a.test]"}
`
	result, err := Reconcile(strings.NewReader(input), []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Failed {
		t.Fatal("build failure lost")
	}
}

func TestUnfinishedTestDoesNotBecomeGreen(t *testing.T) {
	input := `{"Action":"run","Package":"a","Test":"TestMissing"}
{"Action":"pass","Package":"a"}
`
	if _, err := Reconcile(strings.NewReader(input), []string{"a"}); err == nil {
		t.Fatal("unfinished test accepted")
	}
}

func TestInstalledGoResults(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod":      "module example.test/results\n\ngo 1.26.0\n",
		"lib.go":      "package results\nfunc Value() int {return 1}\n",
		"lib_test.go": "package results\nimport \"testing\"\nfunc TestPass(t *testing.T) {}\nfunc TestSkip(t *testing.T) {t.Skip(\"deliberate\")}\n",
	}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run := func() Result {
		t.Helper()
		cmd := exec.Command("go", "test", "-json", ".")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOENV=off", "GOFLAGS=", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOWORK=off")
		output, processErr := cmd.Output()
		result, err := Reconcile(bytes.NewReader(output), []string{"example.test/results"})
		if err != nil {
			t.Fatalf("reconcile: %v; process failure: %v", err, processErr)
		}
		if (processErr != nil) != result.Failed {
			t.Fatal("native exit status and reconciled failure disagree")
		}
		return result
	}
	first := run()
	if len(first.Tests) != 2 || first.Tests[1].Action != "skip" {
		t.Fatalf("native tests %+v", first.Tests)
	}
	cached := run()
	if !cached.Packages[0].Cached {
		t.Fatal("native cache reuse not reported")
	}
	if err := os.WriteFile(filepath.Join(root, "lib.go"), []byte("package results\nfunc Value() int {return missing}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	failed := run()
	if !failed.Failed || failed.Packages[0].Action != "fail" {
		t.Fatalf("native compile failure lost: %+v", failed)
	}
}
