package native

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNativePlatformDefaultsAndValidation(t *testing.T) {
	if (Backend{}).platform() != "linux" || (Backend{Platform: "darwin"}).platform() != "darwin" {
		t.Fatal("native platform default changed")
	}
	for _, c := range []Config{
		{Schema: 1, Platform: "windows", Architecture: "arm64", Packages: []string{"."}},
		{Schema: 1, Platform: "darwin", Image: "unused-image", Architecture: "arm64", Packages: []string{"."}},
	} {
		if _, err := Go(context.Background(), t.TempDir(), c, "", false); err == nil {
			t.Fatal("invalid native platform admitted")
		}
	}
}

func darwinFixtureBackend(t *testing.T) (Backend, string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("actual macOS enforcement required")
	}
	tool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	worker, err := prepareDarwinWorker(context.Background(), tool, dir, buildEnvironmentForPlatform("darwin", runtime.GOARCH))
	if err != nil {
		t.Fatal("private installed Darwin probe build failed", err)
	}
	b := Backend{Platform: "darwin", Sandbox: "/usr/bin/sandbox-exec", Worker: worker, Architecture: runtime.GOARCH}
	if err := b.Bind(context.Background()); err != nil {
		t.Fatal("actual Darwin boundary unavailable", err)
	}
	return b, dir
}

func TestInstalledDarwinBoundaryBinding(t *testing.T) {
	b, dir := darwinFixtureBackend(t)
	if b.Identity == "" {
		t.Fatal("missing actual Darwin context")
	}
	output, err := b.execute(context.Background(), Artifact{Program: b.Worker, Args: []string{"--probe"}, Env: []string{"GOMAXPROCS=1"}})
	if err != nil || !output.Completed || output.ExitCode != 0 || string(output.Stdout) != darwinBoundaryVersion+"\n" || len(output.Stderr) != 0 {
		t.Fatalf("actual Darwin probe failed %+v %v", output, err)
	}
	other := b
	other.Platform = "linux"
	if err := other.unchanged(context.Background()); err == nil {
		t.Fatal("platform retarget retained Darwin identity")
	}
	other = b
	other.Worker = filepath.Join(dir, "missing-probe")
	if err := other.unchanged(context.Background()); err == nil {
		t.Fatal("probe retarget retained Darwin identity")
	}
	if _, err := b.execute(context.Background(), Artifact{Program: b.Worker, Envelope: &Envelope{}}); err == nil {
		t.Fatal("unqualified Darwin envelope admitted")
	}
}

func TestInstalledDarwinPureGoReuse(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("actual macOS enforcement required")
	}
	root := t.TempDir()
	cache := filepath.Join(t.TempDir(), "cache")
	write(t, root, "go.mod", "module darwinfixture\ngo 1.26\n")
	for _, p := range []string{"a", "b"} {
		write(t, root, p+"/value.go", "package value;func Value()int{return 7}")
		write(t, root, p+"/value_test.go", "package value;import \"testing\";func TestValue(t *testing.T){if Value()!=7{t.Fatal(\"value\")}}")
	}
	c := Config{Schema: 1, Platform: "darwin", Architecture: runtime.GOARCH, Packages: []string{"./a", "./b"}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for step := 0; step < 3; step++ {
		if step == 2 {
			write(t, root, "a/value.go", "package value;func Value()int{return 6+1}")
		}
		r, err := Go(ctx, root, c, cache, false)
		if err != nil || !r.Complete || !r.Passed || len(r.Groups) != 2 {
			t.Fatalf("Darwin lifecycle step%d %+v %v", step, r, err)
		}
		for _, g := range r.Groups {
			wantReuse := step == 1 || (step == 2 && g.Name == "./b")
			if g.Context != "darwin-"+runtime.GOARCH || !g.Qualified || g.Reused != wantReuse || g.Executed == wantReuse {
				t.Fatalf("Darwin lifecycle group %+v", g)
			}
		}
	}
}

func TestDarwinBuildEnvironmentBindsExplicitPlatform(t *testing.T) {
	darwin, linux := buildEnvironmentForPlatform("darwin", "arm64"), buildEnvironment("arm64")
	if !strings.Contains(strings.Join(darwin, "\n"), "GOOS=darwin\n") || identity(darwin) == identity(linux) {
		t.Fatal("Darwin compiler target not bound")
	}
	if _, err := captureGoBuildForPlatform(context.Background(), "missing", "windows", "arm64"); err == nil {
		t.Fatal("unsupported compiler target admitted")
	}
	if _, err := os.Stat(darwinDyldProfile); err != nil && runtime.GOOS == "darwin" {
		t.Fatal("installed loader policy missing", err)
	}
}
