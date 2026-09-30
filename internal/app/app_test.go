package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/model"
)

func gitFixture(t *testing.T) (string, string, string, model.Config) {
	t.Helper()
	root := t.TempDir()
	write := func(path, contents string) {
		t.Helper()
		path = filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c := model.Config{Schema: 1, Context: model.Context{ID: "fixture", OS: runtime.GOOS, Arch: runtime.GOARCH}, Workspaces: []model.Workspace{{ID: "go", Root: ".", Adapter: "go", Command: model.Command{Dir: ".", Executable: "go", Args: []string{"test", "./..."}}}}}
	b, _ := json.Marshal(c)
	write("hayaku.json", string(b))
	write("go.mod", "module example.test/fixture\n\ngo 1.26\n")
	write("a/a.go", "package a\nfunc Value() int { return 1 }\n")
	write("a/a_test.go", "package a\nimport \"testing\"\nfunc TestValue(t *testing.T){if Value()<1{t.Fatal(\"bad\")}}\n")
	// This independent package reads another package's source at runtime. The
	// native graph omits that edge, so the shadow proposal must expose a miss.
	write("b/b.go", "package b\nconst Value=1\n")
	write("b/b_test.go", `package b
import("testing";"os";"bytes")
func TestRuntimeSource(t *testing.T){data,err:=os.ReadFile("../a/a.go");if err!=nil{t.Fatal(err)};if bytes.Contains(data,[]byte("return 2")){t.Fatal("runtime dependency changed")}}
`)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	git("init", "-q")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.test")
	git("config", "commit.gpgsign", "false")
	git("add", ".")
	git("commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	write("a/a.go", "package a\nfunc Value() int { return 2 }\n")
	git("add", ".")
	git("commit", "-qm", "change literal")
	candidate := git("rev-parse", "HEAD")
	return root, base, candidate, c
}

func TestEndToEndNeverPromotesGraphProposal(t *testing.T) {
	root, base, candidate, c := gitFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := Build(ctx, root, base, candidate, c, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Proposed) != 1 || p.Proposed[0].Selector != "example.test/fixture/a" || len(p.Selected) != 2 || p.Mode != "full-fallback" {
		t.Fatalf("bad envelope %+v", p)
	}
	if len(p.Commands) != 1 || strings.Join(p.Commands[0].Args, " ") != "test ./..." {
		t.Fatal("required command narrowed")
	}
	result, err := Shadow(ctx, root, c, p)
	if err == nil || len(result.ObservedMisses) == 0 {
		t.Fatalf("miss not detected: %+v err=%v", result, err)
	}
	resultRun, err := Execute(ctx, root, c, p)
	if err == nil || resultRun.Passed {
		t.Fatal("failing full suite became green")
	}
	b, _ := json.Marshal(p)
	planfile := filepath.Join(root, ".git", "plan.json")
	os.WriteFile(planfile, b, 0600)
	// Changed command in an untrusted plan must be rejected before execution.
	p.Commands[0].Args = []string{"test", "./a"}
	b, _ = json.Marshal(p)
	os.WriteFile(planfile, b, 0600)
	var out, errout bytes.Buffer
	status := Run(ctx, []string{"run", "--root", root, "--plan", planfile}, &out, &errout)
	if status == 0 || !strings.Contains(errout.String(), "differs from fresh") {
		t.Fatalf("tampered plan accepted: %d %s", status, errout.String())
	}
}

func TestConfigDriftAndInitPreservesPolicy(t *testing.T) {
	root, base, candidate, c := gitFixture(t)
	ctx := context.Background()
	p, err := Build(ctx, root, base, candidate, c, false)
	if err != nil {
		t.Fatal(err)
	}
	c.Context.Env = map[string]string{"HYK_FIXTURE": "drift"}
	if _, err := Execute(ctx, root, c, p); err == nil {
		t.Fatal("context drift accepted")
	}
	var out, errout bytes.Buffer
	if Run(ctx, []string{"init", "--root", root}, &out, &errout) == 0 {
		t.Fatal("existing configuration overwritten")
	}
	if config.Digest(c) == p.ConfigDigest {
		t.Fatal("environment not bound")
	}
}
