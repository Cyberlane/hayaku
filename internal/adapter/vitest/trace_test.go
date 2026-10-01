package vitest

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDecodeTraceTypedDynamicInputs(t *testing.T) {
	data := []byte(`{"schema":1,"type":"setup","owner":"test/a.test.ts"}
{"schema":1,"type":"observation","operation":"read","path":"fixtures/value.json","owner":"test/a.test.ts"}
{"schema":1,"type":"observation","operation":"exists","path":"fixtures/missing.json","owner":"test/a.test.ts"}
{"schema":1,"type":"observation","operation":"directory","path":"fixtures","owner":"test/a.test.ts"}
{"schema":1,"type":"observation","operation":"directory","path":".","owner":"test/a.test.ts"}
{"schema":1,"type":"observation","operation":"read","path":"fixtures/value.json","owner":"test/a.test.ts"}
{"schema":1,"type":"gap","code":"subprocess","owner":"test/a.test.ts"}
`)
	trace, err := DecodeTrace(data, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !trace.Complete || trace.Schema != 1 || len(trace.Observations) != 4 || !reflect.DeepEqual(trace.Owners, []string{"test/a.test.ts"}) || !reflect.DeepEqual(trace.Gaps, []string{"subprocess"}) {
		t.Fatalf("unexpected trace: %#v", trace)
	}
}

func TestDecodeTraceRejectsIncompleteAndMalformedTransport(t *testing.T) {
	header := `{"schema":1,"type":"setup","owner":"*"}` + "\n"
	for name, data := range map[string]string{
		"empty": "", "missing newline": strings.TrimSuffix(header, "\n"),
		"missing setup":  `{"schema":1,"type":"gap","code":"network","owner":"*"}` + "\n",
		"duplicate keys": `{"schema":1,"schema":1,"type":"setup","owner":"*"}` + "\n",
		"nested values":  `{"schema":1,"type":"setup","owner":{"secret":"value"}}` + "\n",
		"trailing JSON":  strings.TrimSuffix(header, "\n") + "{}\n",
		"blank line":     header + "\n", "wrong schema": strings.Replace(header, `"schema":1`, `"schema":2`, 1),
		"unexpected fields": strings.Replace(header, `"owner":"*"`, `"owner":"*","raw":"secret"`, 1),
		"unknown operation": header + `{"schema":1,"type":"observation","operation":"shell","path":"safe","owner":"*"}` + "\n",
		"unknown gap":       header + `{"schema":1,"type":"gap","code":"raw secret","owner":"*"}` + "\n",
		"outside path":      header + `{"schema":1,"type":"observation","operation":"read","path":"../secret","owner":"*"}` + "\n",
		"absolute path":     header + `{"schema":1,"type":"observation","operation":"read","path":"/secret","owner":"*"}` + "\n",
		"noncanonical":      header + `{"schema":1,"type":"observation","operation":"read","path":"a/../secret","owner":"*"}` + "\n",
		"wrong owner":       `{"schema":1,"type":"setup","owner":"a.test.ts"}` + "\n" + `{"schema":1,"type":"observation","operation":"read","path":"safe","owner":"b.test.ts"}` + "\n",
		"oversize line":     header + strings.Repeat("x", 32769) + "\n",
		"too many records":  strings.Repeat(header, 100001),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeTrace([]byte(data), t.TempDir()); err == nil {
				t.Fatal("accepted invalid runtime observations")
			}
		})
	}
	if _, err := DecodeTrace([]byte(header), "relative"); err == nil {
		t.Fatal("accepted relative snapshot root")
	}
}

func TestValidatePersistedTrace(t *testing.T) {
	valid := Trace{Schema: 1, Complete: true, Owners: []string{"a.test.ts"}, Observations: []Observation{{Operation: "exists", Path: "missing.txt", Owner: "a.test.ts"}}, Gaps: []string{"clock"}}
	if err := ValidateTrace(valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Trace){
		"incomplete":   func(trace *Trace) { trace.Complete = false },
		"wrong schema": func(trace *Trace) { trace.Schema = 2 },
		"no workers":   func(trace *Trace) { trace.Owners = nil },
		"unsafe owner": func(trace *Trace) { trace.Owners = []string{"../unsafe"} },
		"unsafe path": func(trace *Trace) {
			trace.Observations = []Observation{{Operation: "read", Path: "../unsafe", Owner: "a.test.ts"}}
		},
		"unsupported operation": func(trace *Trace) {
			trace.Observations = []Observation{{Operation: "shell", Path: "safe", Owner: "a.test.ts"}}
		},
		"unexpected value": func(trace *Trace) { trace.Gaps = []string{"sensitive-value"} },
	} {
		t.Run(name, func(t *testing.T) {
			trace := valid
			mutate(&trace)
			if err := ValidateTrace(trace); err == nil {
				t.Fatal("accepted invalid persisted trace")
			}
		})
	}
}

func TestRuntimeHookSymlinkEscapeProducesGap(t *testing.T) {
	trace := executeTraceHook(t, `
import fs from 'node:fs';
try { fs.readFileSync('escape'); } catch {}
`, func(root, private string) {
		outside := filepath.Join(private, "outside.txt")
		if err := os.WriteFile(outside, []byte("private-source-value"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
			t.Skip("symlink fixture unavailable")
		}
	})
	found := false
	for _, gap := range trace.Gaps {
		if gap == "outside-snapshot" {
			found = true
		}
	}
	if !found {
		t.Fatal("symlink escape did not report external influence")
	}
	for _, observation := range trace.Observations {
		if observation.Path == "escape" {
			t.Fatal("outside symlink was treated as an ordinary snapshot input")
		}
	}
}

func TestRuntimeHookRecorderDoesNotTraceItsOwnWrites(t *testing.T) {
	trace := executeTraceHook(t, `import fs from 'node:fs'; fs.readFileSync('value.txt');`)
	for _, gap := range trace.Gaps {
		if gap == "filesystem-write" {
			t.Fatal("private trace recorder recursively observed its own writes")
		}
	}
}

func TestRuntimeHookWithinSnapshotSymlinkRetainsUncertainty(t *testing.T) {
	trace := executeTraceHook(t, `import fs from 'node:fs'; fs.readFileSync('alias.txt');`, func(root, private string) {
		if err := os.Symlink("value.txt", filepath.Join(root, "alias.txt")); err != nil {
			t.Skip("symlink fixture unavailable")
		}
	})
	found := false
	for _, gap := range trace.Gaps {
		if gap == "path-resolution" {
			found = true
		}
	}
	if !found {
		t.Fatal("within-snapshot alias lost its target influence")
	}
}

func TestRuntimeHookRecordsReadsMissingPathsAndDirectoryEntries(t *testing.T) {
	trace := executeTraceHook(t, `
import fs from 'node:fs';
import { readFileSync } from 'node:fs';
import { readFile } from 'node:fs/promises';
globalThis.__vitest_worker__ = { filepath: process.cwd() + '/a.test.ts' };
readFileSync('value.txt', 'utf8');
await readFile('value.txt', 'utf8');
await new Promise((resolve, reject) => fs.readFile('value.txt', (error) => error ? reject(error) : resolve()));
fs.existsSync('missing.txt');
try { await fs.promises.access('missing.txt'); } catch {}
fs.readdirSync('.');
const dir = await fs.promises.opendir('.'); await dir.close();
fs.statSync('value.txt');
fs.lstatSync('value.txt');
`)
	expected := map[Observation]bool{
		{"read", "value.txt", "a.test.ts"}:     true,
		{"exists", "missing.txt", "a.test.ts"}: true,
		{"directory", ".", "a.test.ts"}:        true,
		{"metadata", "value.txt", "a.test.ts"}: true,
	}
	for _, observation := range trace.Observations {
		delete(expected, observation)
	}
	if len(expected) != 0 {
		t.Fatalf("missing input observations: %#v in %#v", expected, trace)
	}
	if !trace.Complete {
		t.Fatalf("unexpected transport result: %#v", trace)
	}
}

func TestRuntimeHookMarksUnsupportedInputsWithoutPersistingValues(t *testing.T) {
	trace := executeTraceHook(t, `
import fs from 'node:fs';
import childProcess from 'node:child_process';
import net from 'node:net';
import { Worker } from 'node:worker_threads';
fs.readFileSync(Buffer.from('value.txt'));
const fd = fs.openSync('value.txt', 'r');
fs.readFileSync(fd); fs.closeSync(fd);
try { fs.readFileSync('/hayaku-private-secret-value-does-not-exist'); } catch {}
childProcess.execFileSync(process.execPath, ['-e', '']);
const server = net.createServer(); server.close();
try { process.dlopen({ exports: {} }, '/hayaku-private-secret-addon-does-not-exist'); } catch {}
const worker = new Worker('', { eval: true }); await worker.terminate();
fs.writeFileSync('result.txt', 'private-source-value');
Date.now(); new Date(); Math.random();
`)
	for _, gap := range []string{"unsupported-path", "file-descriptor", "outside-snapshot", "subprocess", "network", "native-addon", "worker", "filesystem-write", "clock", "random"} {
		found := false
		for _, recorded := range trace.Gaps {
			if recorded == gap {
				found = true
			}
		}
		if !found {
			t.Errorf("missing gap %q: %#v", gap, trace.Gaps)
		}
	}
	for _, observation := range trace.Observations {
		if strings.Contains(observation.Path, "private-secret") || strings.Contains(observation.Path, "private-source") {
			t.Fatal("sensitive runtime value recorded")
		}
	}
}

func executeTraceHook(t *testing.T, script string, prepare ...func(string, string)) Trace {
	t.Helper()
	node := os.Getenv("HAYAKU_VITEST_NODE")
	if node == "" {
		var err error
		node, err = exec.LookPath("node")
		if err != nil {
			t.Skip("installed Node required for observational hook fixture")
		}
	}
	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	root = resolved
	private := t.TempDir()
	hook, output := filepath.Join(private, "trace.mjs"), filepath.Join(private, "trace.jsonl")
	if err := os.WriteFile(hook, TraceHook(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fixture.mjs"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "value.txt"), []byte("private-source-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, setup := range prepare {
		setup(root, private)
	}
	command := exec.Command(node, "--import", hook, filepath.Join(root, "fixture.mjs"))
	command.Dir = root
	command.Env = append(os.Environ(), "HAYAKU_TRACE_ROOT="+root, "HAYAKU_TRACE_OUTPUT="+output)
	if logs, err := command.CombinedOutput(); err != nil {
		t.Fatalf("hook fixture failed: %v: %s", err, logs)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-secret") || strings.Contains(string(data), "private-source") {
		t.Fatal("trace persisted sensitive input values")
	}
	trace, err := DecodeTrace(data, root)
	if err != nil {
		t.Fatal(err)
	}
	return trace
}
