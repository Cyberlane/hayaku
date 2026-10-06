package native

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
)

func importedClosureFixture(t *testing.T) (string, model.Workspace, model.Context) {
	t.Helper()
	root := t.TempDir()
	write(t, root, "go.mod", "module captured.fixture\ngo 1.26.0\n")
	write(t, root, "lib/lib.go", "package lib;func Value()int{return 1}")
	write(t, root, "app/app_test.go", `package app;import("testing";"captured.fixture/lib");func TestImported(t *testing.T){if lib.Value()!=1{t.Fatal("wrong")}}`)
	w := model.Workspace{ID: "go", Root: ".", Adapter: "go", Command: model.Command{Dir: ".", Executable: "go", Args: []string{"test", "-count=1", "./app"}}}
	c := model.Context{OS: runtime.GOOS, Arch: runtime.GOARCH, Env: map[string]string{"GOFLAGS": "", "GOWORK": "off", "CGO_ENABLED": "0"}}
	return root, w, c
}

func TestCapturedImportedGoClosurePrivateLifecycle(t *testing.T) {
	root, w, execution := importedClosureFixture(t)
	write(t, root, ".git/private-metadata", "PRIVATE_METADATA_NOT_BUILD_SOURCE")
	c, err := CaptureGoClosure(context.Background(), root, w, execution)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	private := c.Directory()
	if private == "" || private == root || !c.Evidence.InventoryComplete || len(c.Evidence.Inventory.Cases) != 1 || c.Evidence.Inventory.Cases[0].ID != "TestImported" {
		t.Fatal("private imported build/case closure unavailable")
	}
	if _, err := os.Stat(filepath.Join(private, ".git")); !os.IsNotExist(err) {
		t.Fatal("repository authority metadata copied into build closure")
	}
	if err := c.Revalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	write(t, root, ".git/private-metadata", "CHANGED_REPOSITORY_METADATA")
	if err := c.Revalidate(context.Background()); err != nil {
		t.Fatal("excluded repository metadata changed semantic source closure")
	}
	write(t, root, "lib/lib.go", "package lib;func Value()int{return 2}")
	if c.Revalidate(context.Background()) == nil {
		t.Fatal("live imported dependency change retained capture")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(private); !os.IsNotExist(err) || c.Directory() != "" || c.Revalidate(context.Background()) == nil {
		t.Fatal("closed capture retained private authority")
	}
}

func TestCapturedGoClosureRejectsPrivateAndSummaryDrift(t *testing.T) {
	for _, kind := range []string{"materialization", "source-copy", "source-deletion", "whole-source-map", "summary"} {
		t.Run(kind, func(t *testing.T) {
			root, w, execution := importedClosureFixture(t)
			c, err := CaptureGoClosure(context.Background(), root, w, execution)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			switch kind {
			case "materialization":
				write(t, c.Directory(), "lib/lib.go", "package lib;func Value()int{return 3}")
			case "source-copy":
				for _, p := range c.Evidence.Packages {
					if p.ImportPath == "captured.fixture/lib" {
						p.Sources["lib.go"][0] = 'X'
					}
				}
			case "source-deletion":
				for _, p := range c.Evidence.Packages {
					if p.ImportPath == "captured.fixture/lib" {
						delete(p.Sources, "lib.go")
					}
				}
			case "whole-source-map":
				for i := range c.Evidence.Packages {
					if c.Evidence.Packages[i].ImportPath == "captured.fixture/lib" {
						c.Evidence.Packages[i].Sources = nil
					}
				}
			case "summary":
				c.Evidence.Inventory.Cases[0].ID = "FORGED_CASE"
			}
			if c.Revalidate(context.Background()) == nil {
				t.Fatal("tampered descriptive build closure retained evidence")
			}
		})
	}
}

func TestImportedMetadataCannotQualifyNativeSkipping(t *testing.T) {
	root, w, execution := importedClosureFixture(t)
	c, err := CaptureGoClosure(context.Background(), root, w, execution)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if len(c.Evidence.Gaps) == 0 {
		t.Fatal("compiler evidence silently became runtime proof")
	}
	for i := 0; i < 2; i++ {
		r, err := Go(context.Background(), root, Config{Schema: 1, Architecture: "arm64", Packages: []string{"./app"}}, "", false)
		if err != nil || !r.Passed || !r.Complete || len(r.Groups) != 1 || !r.Groups[0].Executed || r.Groups[0].Qualified || r.Groups[0].Reused {
			t.Fatal("new descriptive closure changed existing imported-package full fallback")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c, err := CaptureGoClosure(ctx, root, w, execution); err == nil || c != nil {
		t.Fatal("canceled source capture succeeded")
	}
}
