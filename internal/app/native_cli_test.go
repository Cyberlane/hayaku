package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/native"
)

func TestNativeCLIFullFallbackAndStrictConfiguration(t *testing.T) {
	root := t.TempDir()
	nativeAppWrite(t, root, "go.mod", "module sample\ngo 1.26\n")
	nativeAppWrite(t, root, "sample_test.go", `package sample;import("testing";"time");func TestClock(t *testing.T){if time.Now().IsZero(){t.Fatal("zero")}}`)
	nativeAppWrite(t, root, "native.json", `{"schema":1,"architecture":"arm64","packages":["."]}`)
	for i := 0; i < 2; i++ {
		var out, errout bytes.Buffer
		status := Run(context.Background(), []string{"native", "--root", root}, &out, &errout)
		var report native.Report
		if status != 0 || json.Unmarshal(out.Bytes(), &report) != nil || !report.Passed || !report.Groups[0].Executed || report.Groups[0].Qualified || report.Groups[0].Reused {
			t.Fatalf("native CLI status=%d report=%s error=%s", status, &out, &errout)
		}
	}
	nativeAppWrite(t, root, "native.json", `{"schema":1,"architecture":"arm64","packages":["."],"qualified":true}`)
	var out, errout bytes.Buffer
	if status := Run(context.Background(), []string{"native", "--root", root}, &out, &errout); status == 0 {
		t.Fatal("configuration assertion qualified native reuse")
	}
	nativeAppWrite(t, root, "native.json", `{"schema":1,"architecture":"arm64","packages":["./../outside"]}`)
	out.Reset()
	errout.Reset()
	if status := Run(context.Background(), []string{"native", "--root", root}, &out, &errout); status == 0 {
		t.Fatal("escaping package accepted")
	}
	var report native.Report
	if json.Unmarshal(out.Bytes(), &report) == nil && report.Passed {
		t.Fatal("invalid native config emitted a green report")
	}
	// Linked source is refused before qualification/building.
	if err := os.Symlink(filepath.Join(root, "sample_test.go"), filepath.Join(root, "linked.go")); err != nil {
		t.Fatal(err)
	}
	nativeAppWrite(t, root, "native.json", `{"schema":1,"architecture":"arm64","packages":["."]}`)
	out.Reset()
	errout.Reset()
	if status := Run(context.Background(), []string{"native", "--root", root}, &out, &errout); status == 0 {
		t.Fatal("linked input qualified")
	}
}
func TestNativeCLIRejectsLiveConfigurationDrift(t *testing.T) {
	root := t.TempDir()
	nativeAppWrite(t, root, "go.mod", "module sample\ngo 1.26\n")
	nativeAppGit(t, root, "init", "-q")
	nativeAppWrite(t, root, ".git/native.json", `{"schema":1,"architecture":"arm64","packages":["."]}`)
	// Policy inside .git is outside source capture; CLI policy revalidation must
	// still detect its mutation independently of the source digest.
	nativeAppWrite(t, root, "sample_test.go", `package sample;import("testing";"os");func TestPolicy(t *testing.T){if err:=os.WriteFile(".git/native.json",[]byte("{\"schema\":1,\"architecture\":\"arm64\",\"packages\":[\"./other\"]}"),0600);err!=nil{t.Fatal(err)}}`)
	var out, errout bytes.Buffer
	status := Run(context.Background(), []string{"native", "--root", root, "--config", ".git/native.json"}, &out, &errout)
	var report native.Report
	if status == 0 || json.Unmarshal(out.Bytes(), &report) != nil || report.Complete || report.Passed || !strings.Contains(errout.String(), "native configuration changed") {
		t.Fatalf("drift status=%d report=%s error=%s", status, &out, &errout)
	}
}
