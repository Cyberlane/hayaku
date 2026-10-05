package golang

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
)

func TestASTResourceEffectsAndUnresolvedOperations(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "pkg")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		source string
		effect model.Effect
	}{
		"literal-read":       {"package p; import resource \"os\"; func f(){resource.ReadFile(\"../data.txt\")}", model.Effect{Owner: "p", Kind: "read", Path: "data.txt"}},
		"directory":          {"package p; import \"os\"; func f(){os.ReadDir(\"fixtures\")}", model.Effect{Owner: "p", Kind: "directory", Path: "pkg/fixtures"}},
		"dynamic-read":       {"package p; import \"os\"; func f(name string){os.ReadFile(name)}", model.Effect{Owner: "p", Kind: "unknown"}},
		"captured-reference": {"package p; import \"os\"; var read=os.ReadFile", model.Effect{Owner: "p", Kind: "unknown"}},
		"escape":             {"package p; import \"os\"; func f(){os.ReadFile(\"../../secret\")}", model.Effect{Owner: "p", Kind: "unknown"}},
		"network":            {"package p; import \"net/http\"; func f(){http.Get(\"example\")}", model.Effect{Owner: "p", Kind: "network"}},
		"subprocess":         {"package p; import \"os/exec\"; func f(){exec.Command(\"command\")}", model.Effect{Owner: "p", Kind: "subprocess"}},
		"clock":              {"package p; import \"time\"; func f(){time.Now()}", model.Effect{Owner: "p", Kind: "clock"}},
		"random":             {"package p; import \"math/rand\"; func f(){rand.Int()}", model.Effect{Owner: "p", Kind: "random"}},
		"goroutine":          {"package p; func f(){go f()}", model.Effect{Owner: "p", Kind: "shared-state"}},
		"reflection":         {"package p; import \"reflect\"; func f(){reflect.ValueOf(1)}", model.Effect{Owner: "p", Kind: "native"}},
	} {
		t.Run(name, func(t *testing.T) {
			filename := filepath.Join(directory, "fixture.go")
			if err := os.WriteFile(filename, []byte(c.source), 0600); err != nil {
				t.Fatal(err)
			}
			effects, err := sourceEffects(context.Background(), root, filename, "p")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, e := range effects {
				if e == c.effect {
					found = true
				}
			}
			if !found {
				t.Fatalf("AST effect missing: want=%+v got=%+v", c.effect, effects)
			}
		})
	}
}
