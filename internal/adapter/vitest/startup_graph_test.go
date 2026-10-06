package vitest

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/planner"
	"github.com/Cyberlane/hayaku/internal/process"
)

// Two independent files challenge startup ownership: only A imports the helper
// directly, but setup also consumes it for B. Assert the proposal, since required
// full execution could otherwise conceal a missing descriptive dependency.
func TestInstalledVitestTransitiveStartupInfluence(t *testing.T) {
	for _, stage := range []string{"setup", "global-setup", "configuration", "unresolved-virtual"} {
		t.Run(stage, func(t *testing.T) {
			configuration := "export default { test: { globals: true, pool: 'forks'"
			files := map[string]string{
				"helper.ts":         "export const value = 7;\n",
				"a.test.ts":         "import { value } from './helper'; test('A',()=>{expect(value).toBe(7)});\n",
				"b.test.ts":         "test('B',()=>{expect(globalThis.setupValue).toBe(7)});\n",
				"setup.ts":          "import { value } from './helper'; globalThis.setupValue = value;\n",
				"global.mjs":        "import { value } from './helper'; export default function(){ if(value!==7)throw new Error('startup'); }\n",
				"config-helper.mjs": "export const timeout = 5000;\n",
			}
			changed := "helper.ts"
			switch stage {
			case "setup":
				configuration += ", setupFiles: ['./setup.ts'] } };\n"
			case "global-setup":
				configuration += ", globalSetup: ['./global.mjs'] } };\n"
			case "configuration":
				configuration = "import { timeout } from './config-helper.mjs'; " + configuration + ", testTimeout: timeout } };\n"
				changed = "config-helper.mjs"
			case "unresolved-virtual":
				configuration += ", setupFiles: ['./setup.ts'] }, plugins: [{ name: 'fixture-virtual', resolveId(id){if(id==='virtual:fixture')return '\\0fixture';}, load(id){if(id==='\\0fixture')return 'export const value = 7';} }] };\n"
				files["setup.ts"] = "import { value } from 'virtual:fixture'; globalThis.setupValue = value;\n"
			}
			root, workspace := boundContextFixture(t, configuration)
			if err := os.Remove(filepath.Join(root, "value.test.ts")); err != nil {
				t.Fatal(err)
			}
			for name, contents := range files {
				if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
			}
			evidence, err := Discover(context.Background(), root, workspace, model.Context{Env: map[string]string{"CI": "true"}})
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"js::a.test.ts", "js::b.test.ts"}
			if len(evidence.Units) != 2 || !reflect.DeepEqual(evidence.Inputs[changed], want) {
				t.Fatalf("shared startup helper lost an owner: units=%v owners=%v", evidence.Units, evidence.Inputs[changed])
			}
			var changes []model.Change
			if stage != "unresolved-virtual" {
				changes = []model.Change{{Path: changed, Status: "M"}}
			}
			plan, err := planner.Build([]model.Evidence{evidence}, []model.Evidence{evidence}, changes, model.Config{Schema: model.Schema, Workspaces: []model.Workspace{workspace}})
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Proposed) != 2 || len(plan.Selected) != 2 || plan.Mode != "full-fallback" {
				t.Fatalf("startup/unknown influence omitted a proposal: %+v", plan)
			}
			if stage == "unresolved-virtual" && (len(evidence.Effects) != 1 || evidence.Effects[0].Kind != "unknown") {
				t.Fatal("unresolved virtual startup did not retain an explicit unknown effect")
			}
		})
	}
}

func TestUnresolvedGraphEffectBroadensWithoutChangedInput(t *testing.T) {
	for _, startup := range []bool{false, true} {
		data := graphData()
		if startup {
			data.Global = append(data.Global, "*")
		} else {
			data.Scopes[0].Incomplete = true
		}
		root, workspace := graphFixture(t, data)
		workspace.Adapter = "vitest"
		evidence, err := Discover(context.Background(), root, workspace, model.Context{})
		if err != nil {
			t.Fatal(err)
		}
		plan, err := planner.Build([]model.Evidence{evidence}, []model.Evidence{evidence}, nil, model.Config{Schema: model.Schema, Workspaces: []model.Workspace{workspace}})
		if err != nil || len(plan.Proposed) != 2 || len(plan.Selected) != 2 {
			t.Fatalf("unknown transform hid unchanged input influence: %+v err=%v", plan, err)
		}
	}
}

func TestCollectionCompatibilityDoesNotInheritDiscoveryMatrix(t *testing.T) {
	for _, pair := range [][2]string{{"3.2.7", "6.4.3"}, {"4.1.11", "7.3.1"}, {"4.1.11", "8.1.5"}, {"4.1.11", "8.2.2"}} {
		if !supportedAPIVersions(pair[0], pair[1]) {
			t.Fatal("legacy discovery/execution compatibility lost")
		}
		data, evidence := collectionFixture()
		data.Version, data.ViteVersion = pair[0], pair[1]
		evidence.Version = "vitest-vite-graph-v1:" + pair[0] + ":vite:" + pair[1]
		encoded, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		_, err = decodeCollection(process.Output{Completed: true, Stdout: encoded}, "js", evidence)
		if (err == nil) != (pair == [2]string{"4.1.11", "8.2.2"}) {
			t.Fatalf("untested collection inherited compatibility: pair=%v err=%v", pair, err)
		}
	}
}

// Mutation happens after the path preflight and fd identity check. Source reads
// must stay bounded and reject the changed view even when the pathname returns
// the same bytes through a different inode.
func TestNativeGraphSourceReaderRejectsLiveMutation(t *testing.T) {
	node := os.Getenv("HAYAKU_VITEST_NODE")
	if node == "" {
		var err error
		node, err = exec.LookPath("node")
		if err != nil {
			t.Skip("installed Node required for bounded source reader probe")
		}
	}
	prefix, _, ok := strings.Cut(nativeBridge, "// This is bounded syntax evidence")
	if !ok {
		t.Fatal("source reader helper boundary missing")
	}
	for _, mutation := range []string{"growth", "replacement"} {
		t.Run(mutation, func(t *testing.T) {
			directory := t.TempDir()
			target := filepath.Join(directory, "source.mjs")
			if err := os.WriteFile(target, []byte("export const value = 7;\n"), 0600); err != nil {
				t.Fatal(err)
			}
			probe := `
const healthy = readSource(input.target);
if (healthy !== 'export const value = 7;\n') throw new Error('healthy source unavailable');
const admitted = fs.statSync(input.target).size;
const originalRead = fs.readSync, originalClose = fs.closeSync, originalAlloc = Buffer.alloc;
let allocated = 0, readingFd, closed = false;
Buffer.alloc = function(size, ...args) { allocated = Math.max(allocated, size); return originalAlloc.call(Buffer, size, ...args); };
fs.closeSync = function(fd) { if (fd === readingFd) closed = true; return originalClose(fd); };
fs.readSync = function(...args) {
  readingFd = args[0];
  fs.readSync = originalRead;
  if (input.mutation === 'growth') fs.appendFileSync(input.target, 'x'.repeat(65536));
  else { fs.renameSync(input.target, input.target + '.old'); fs.writeFileSync(input.target, healthy); }
  return originalRead(...args);
};
let rejected = false;
try { readSource(input.target); }
catch (error) { rejected = error.message === 'native graph source changed'; }
if (!rejected || !closed || allocated !== admitted + 1) throw new Error('source mutation escaped bounded fd verification');
`
			script := filepath.Join(directory, "probe.mjs")
			if err := os.WriteFile(script, []byte(prefix+probe), 0600); err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal(map[string]string{"target": target, "mutation": mutation})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, node, script, string(payload)).CombinedOutput()
			if err != nil {
				t.Fatalf("bounded reader failed %s probe: %v %s", mutation, err, output)
			}
		})
	}
}
