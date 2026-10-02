// Package native implements version-gated discovery and whole-file proposals
// for additional installed runners. Every input conservatively owns every unit.
package native

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Cyberlane/hayaku/internal/adapter/pytest"
	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	nativerunner "github.com/Cyberlane/hayaku/internal/runner/native"
)

//go:embed node.mjs
var nodeBridge string

//go:embed unittest.py
var unittestBridge string

//go:embed playwright.cjs
var playwrightReporter string

//go:embed pytest.py
var pytestBridge string

type Descriptor struct {
	ID                    string
	Tool                  string
	Versions              []string
	Collection            string
	ProtocolCompatibility string
	FixtureVersions       []string
}

func Descriptors() []Descriptor {
	return []Descriptor{
		{ID: "node-test", Tool: "node", Versions: []string{"22.18.0", "25.2.1"}, Collection: "explicit files via node:test; test bodies suppressed", ProtocolCompatibility: "exact native version gate", FixtureVersions: []string{"22.18.0", "25.2.1"}},
		{ID: "unittest", Tool: "python3", Versions: []string{"3.11", "3.12", "3.13", "3.14"}, Collection: "native TestLoader, including load_tests hooks", ProtocolCompatibility: "Python3.11-3.14 stable unittest APIs; contexts remain unqualified", FixtureVersions: []string{"3.14.7"}},
		{ID: "pytest", Tool: "python3", Versions: []string{"7", "8", "9"}, Collection: "existing native collection plus terminal fixture-aware execution", ProtocolCompatibility: "pytest7-9 hook protocol; contexts remain unqualified", FixtureVersions: []string{"9.0.2"}},
		{ID: "jest", Tool: "jest", Versions: []string{"30"}, Collection: "installed Jest --listTests --json", ProtocolCompatibility: "Jest30 CLI JSON; contexts remain unqualified", FixtureVersions: []string{"30.5.2"}},
		{ID: "playwright", Tool: "playwright", Versions: []string{"1.51-1.63"}, Collection: "native --list and private Reporter", ProtocolCompatibility: "Playwright1.51-1.63 Reporter API; contexts remain unqualified", FixtureVersions: []string{"1.63.0"}},
	}
}

type item struct {
	Selector string   `json:"selector"`
	Path     string   `json:"path"`
	Tests    []string `json:"tests"`
}
type inventory struct {
	Schema   int    `json:"schema"`
	Runner   string `json:"runner"`
	Version  string `json:"version"`
	Complete bool   `json:"complete"`
	Items    []item `json:"items"`
}
type parsed struct {
	flags, selectors []string
	options          map[string]any
}

// Discover executes trusted native collection, never an enforcement boundary.
func Discover(ctx context.Context, root string, w model.Workspace, c model.Context) (model.Evidence, error) {
	e := model.Evidence{Adapter: w.Adapter, Version: "native-files-v1", Workspace: w.ID, Nodes: []string{}, Units: []model.Unit{}, Inputs: map[string][]string{}, Edges: []model.Edge{}, Gaps: []model.Gap{
		{Code: "runtime-unqualified", Workspace: w.ID, Detail: "Native collection has no enforced complete runtime influence boundary"},
		{Code: "dependency-unqualified", Workspace: w.ID, Detail: "Every workspace input owns every collected file; no source dependency narrowing is claimed"},
		{Code: "collection-side-effects", Workspace: w.ID, Detail: "Collection imports trusted project configuration and modules; it does not isolate network, subprocesses or other external effects"},
	}}
	p, err := parse(w)
	if err != nil {
		return e, err
	}
	root, dir, err := locations(root, w, c)
	if err != nil {
		return e, err
	}
	collected, _, err := invoke(ctx, root, dir, w, c, p, "discover", nil)
	if err != nil {
		return e, err
	}
	if collected.Schema != 1 || !collected.Complete || collected.Runner != w.Adapter || !supportedVersion(w.Adapter, collected.Version) || len(collected.Items) == 0 || len(collected.Items) > 50000 {
		return e, errors.New("incomplete or unsupported native collection")
	}
	e.Version += ";" + w.Adapter + "=" + collected.Version
	seen, tests := map[string]bool{}, map[string]bool{}
	workspace := filepath.Join(root, filepath.FromSlash(w.Root))
	for _, entry := range collected.Items {
		if !validSelector(w.Adapter, entry.Selector) || seen[entry.Selector] || !filepath.IsAbs(entry.Path) || len(entry.Tests) > 100000 {
			return e, errors.New("invalid native collection identity")
		}
		resolved, err := filepath.EvalSymlinks(entry.Path)
		if err != nil || !within(workspace, resolved) {
			return e, errors.New("native test file escapes workspace or is missing")
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.Mode().IsRegular() {
			return e, errors.New("native test file is not regular")
		}
		if w.Adapter != "unittest" {
			expected := filepath.Join(dir, filepath.FromSlash(entry.Selector))
			expected, err = filepath.EvalSymlinks(expected)
			if err != nil || expected != resolved {
				return e, errors.New("native selector does not identify its file")
			}
		}
		seen[entry.Selector] = true
		id := w.ID + ":" + w.Adapter + ":" + entry.Selector
		e.Nodes = append(e.Nodes, id)
		e.Units = append(e.Units, model.Unit{ID: id, Workspace: w.ID, Selector: entry.Selector, Kind: w.Adapter + "-file"})
		for _, test := range entry.Tests {
			key := entry.Selector + "\x00" + test
			if test == "" || len(test) > 8192 || strings.ContainsAny(test, "\x00\r\n") || tests[key] || len(tests) >= 100000 {
				return e, errors.New("invalid or duplicate native case identity")
			}
			tests[key] = true
		}
	}
	sort.Strings(e.Nodes)
	sort.Slice(e.Units, func(i, j int) bool { return e.Units[i].ID < e.Units[j].ID })
	err = filepath.WalkDir(workspace, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("unsupported native snapshot input type")
		}
		if len(e.Inputs) >= 50000 || (len(e.Inputs)+1)*len(e.Nodes) > 1000000 {
			return errors.New("native ownership inventory exceeds limit")
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !config.Relative(rel) {
			return errors.New("unsafe native input")
		}
		e.Inputs[rel] = append([]string(nil), e.Nodes...)
		return nil
	})
	if w.Adapter == "node-test" || w.Adapter == "unittest" {
		e.Gaps = append(e.Gaps, model.Gap{Code: "collection-context-modified", Workspace: w.ID, Detail: "Discovery suppresses test bodies or interpreter bytecode; original full execution remains mandatory"})
	}
	return e, err
}

// Proposal preserves supported context flags and replaces only whole-file scope.
func Proposal(w model.Workspace, units []model.Unit) (model.Command, error) {
	p, err := parse(w)
	if err != nil {
		return model.Command{}, err
	}
	seen := map[string]bool{}
	for _, u := range units {
		if u.Workspace != w.ID || u.Kind != w.Adapter+"-file" || !validSelector(w.Adapter, u.Selector) {
			return model.Command{}, errors.New("invalid native proposal unit")
		}
		seen[u.Selector] = true
	}
	if len(seen) == 0 {
		return model.Command{}, errors.New("empty native proposal")
	}
	if w.Adapter == "unittest" && (p.options["discover"] == true || len(p.selectors) == 0) {
		// Preserve native discover/sys.path/load_tests semantics. -k patterns are
		// a native OR list; adding owners to an existing filter could broaden its
		// original scope, so that case does not receive a narrowed proposal.
		for _, flag := range p.flags {
			key, _, _ := strings.Cut(flag, "=")
			if key == "-k" {
				return model.Command{}, errors.New("unittest module proposals cannot preserve existing -k OR filters")
			}
		}
		cmd := w.Command
		cmd.Args = append([]string(nil), w.Command.Args...)
		modules := []string{}
		for module := range seen {
			modules = append(modules, module)
		}
		sort.Strings(modules)
		for _, module := range modules {
			cmd.Args = append(cmd.Args, "-k", module+".*")
		}
		return cmd, nil
	}
	selectors := []string{}
	for s := range seen {
		selectors = append(selectors, s)
	}
	sort.Strings(selectors)
	cmd := w.Command
	switch w.Adapter {
	case "node-test":
		cmd.Args = append([]string{"--test"}, p.flags...)
	case "unittest":
		cmd.Args = append([]string{"-m", "unittest"}, unittestExecutionFlags(p.flags)...)
	case "jest":
		cmd.Args = append(append([]string{}, p.flags...), "--runTestsByPath")
	case "playwright":
		// Playwright positional selectors are regular expressions; exact escaped
		// suffixes are emitted below rather than raw filenames with metacharacters.
		cmd.Args = append([]string{"test"}, p.flags...)
		for i, s := range selectors {
			selectors[i] = regexpPath(s)
		}
	}
	cmd.Args = append(cmd.Args, selectors...)
	return cmd, nil
}

// Execute returns only normalized result metadata. Nil units keeps configured
// full scope; non-nil units are experimental proposal scopes.
func Execute(ctx context.Context, root string, w model.Workspace, c model.Context, units []model.Unit) (process.Output, error) {
	if w.Adapter == "pytest" {
		return executePytest(ctx, root, w, c, units)
	}
	p, err := parse(w)
	if err != nil {
		return process.Output{}, err
	}
	root, dir, err := locations(root, w, c)
	if err != nil {
		return process.Output{}, err
	}
	if units != nil {
		proposal, err := Proposal(w, units)
		if err != nil {
			return process.Output{}, err
		}
		if w.Adapter != "playwright" {
			copy := w
			copy.Command = proposal
			p, err = parse(copy)
			if err != nil {
				return process.Output{}, err
			}
		}
		// Playwright regex selectors are emitted only for the external proposal;
		// private execution reuses their validated literal whole-file selectors.
		if w.Adapter == "playwright" {
			p.selectors = nil
			for _, u := range units {
				p.selectors = append(p.selectors, u.Selector)
			}
		}
	}
	_, out, err := invoke(ctx, root, dir, w, c, p, "run", units)
	return out, err
}

func locations(root string, w model.Workspace, c model.Context) (string, string, error) {
	if c.OS != "" && c.OS != runtime.GOOS || c.Arch != "" && c.Arch != runtime.GOARCH {
		return "", "", errors.New("native discovery requires host context")
	}
	var err error
	root, err = filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	if !config.Relative(w.Root) || !config.Relative(w.Command.Dir) {
		return "", "", errors.New("invalid native workspace directory")
	}
	dir := filepath.Join(root, filepath.FromSlash(w.Root), filepath.FromSlash(w.Command.Dir))
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil || !within(root, dir) {
		return "", "", errors.New("native working directory escapes snapshot")
	}
	return root, dir, nil
}
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func validSelector(runner, s string) bool {
	if s == "" || len(s) > 4096 || strings.ContainsAny(s, "\x00\r\n") || strings.HasPrefix(s, "-") {
		return false
	}
	if runner == "unittest" {
		for _, part := range strings.Split(s, ".") {
			if part == "" {
				return false
			}
			for i, r := range part {
				if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
					return false
				}
			}
		}
		return true
	}
	return config.Relative(s) && s != "." && !strings.ContainsAny(s, "\\*?[]")
}
func regexpPath(s string) string {
	var b strings.Builder
	b.WriteString("(^|[/\\\\])")
	for _, r := range s {
		if r == '/' {
			b.WriteString("[/\\\\]")
		} else {
			if strings.ContainsRune(".+*?()|[]{}^$\\", r) {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('$')
	return b.String()
}
func unittestExecutionFlags(flags []string) []string {
	result := []string{}
	for i := 0; i < len(flags); i++ {
		key, _, has := strings.Cut(flags[i], "=")
		switch key {
		case "-s", "--start-directory", "-p", "--pattern", "-t", "--top-level-directory":
			if !has {
				i++
			}
		default:
			result = append(result, flags[i])
		}
	}
	return result
}
func supportedVersion(runner, v string) bool {
	switch runner {
	case "node-test":
		return v == "22.18.0" || v == "25.2.1"
	case "unittest":
		parts := strings.Split(v, ".")
		if len(parts) != 3 || parts[0] != "3" {
			return false
		}
		minor, err := strconv.Atoi(parts[1])
		_, patchErr := strconv.Atoi(parts[2])
		return err == nil && patchErr == nil && minor >= 11 && minor <= 14
	case "jest":
		return strings.HasPrefix(v, "30.") && semver(v)
	case "pytest":
		major, _, _ := strings.Cut(v, ".")
		return (major == "7" || major == "8" || major == "9") && semver(v)
	case "playwright":
		parts := strings.Split(v, ".")
		if len(parts) != 3 || parts[0] != "1" {
			return false
		}
		minor, err := strconv.Atoi(parts[1])
		return err == nil && minor >= 51 && minor <= 63 && semver(v)
	}
	return false
}
func semver(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || strconv.Itoa(n) != p {
			return false
		}
	}
	return true
}

func parse(w model.Workspace) (parsed, error) {
	p := parsed{options: map[string]any{}}
	args := w.Command.Args
	name := filepath.Base(w.Command.Executable)
	if len(w.Patterns) > 0 || len(w.BuildFlags) > 0 {
		return p, errors.New("native runner scope and flags must be in original argv")
	}
	boolean, valued := map[string]bool{}, map[string]bool{}
	switch w.Adapter {
	case "node-test":
		if name != "node" && name != "node.exe" || len(args) == 0 || args[0] != "--test" {
			return p, errors.New("Node native discovery requires node --test with explicit files")
		}
		args = args[1:]
		valued = map[string]bool{"--test-concurrency": true, "--test-timeout": true, "--test-name-pattern": true, "--test-skip-pattern": true}
	case "unittest":
		if name != "python" && name != "python3" && !strings.HasPrefix(name, "python3.") || len(args) < 2 || args[0] != "-m" || args[1] != "unittest" {
			return p, errors.New("unittest requires python -m unittest")
		}
		args = args[2:]
		boolean = map[string]bool{"-v": true, "--verbose": true, "-q": true, "--quiet": true, "-f": true, "--failfast": true, "-b": true, "--buffer": true}
		valued = map[string]bool{"-s": true, "--start-directory": true, "-p": true, "--pattern": true, "-t": true, "--top-level-directory": true, "-k": true}
	case "jest":
		if name != "jest" && name != "jest.cmd" {
			return p, errors.New("Jest requires a direct installed jest command")
		}
		boolean = map[string]bool{"--runInBand": true, "--ci": true, "--silent": true, "--verbose": true, "--passWithNoTests": true, "--runTestsByPath": true}
		valued = map[string]bool{"--config": true, "--testNamePattern": true, "--maxWorkers": true, "--testTimeout": true, "--env": true, "--selectProjects": true}
	case "playwright":
		if name != "playwright" && name != "playwright.cmd" || len(args) == 0 || args[0] != "test" {
			return p, errors.New("Playwright requires a direct installed playwright test command")
		}
		args = args[1:]
		boolean = map[string]bool{"--forbid-only": true, "--fully-parallel": true, "--quiet": true, "--pass-with-no-tests": true}
		valued = map[string]bool{"--config": true, "-c": true, "--project": true, "--grep": true, "-g": true, "--grep-invert": true, "--workers": true, "-j": true, "--retries": true, "--timeout": true, "--global-timeout": true}
	default:
		return p, errors.New("unsupported native runner")
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if w.Adapter == "unittest" && arg == "discover" {
			if len(p.selectors) != 0 || p.options["discover"] != nil {
				return p, errors.New("invalid unittest discovery command")
			}
			p.options["discover"] = true
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			if !validSelector(w.Adapter, arg) {
				return p, errors.New("invalid native file or module selector")
			}
			p.selectors = append(p.selectors, arg)
			continue
		}
		key, value, has := strings.Cut(arg, "=")
		if boolean[key] {
			if has {
				return p, errors.New("boolean native flag has value")
			}
			if key != "--runTestsByPath" {
				p.flags = append(p.flags, arg)
			}
			continue
		}
		if !valued[key] {
			return p, fmt.Errorf("unsupported %s flag %q", w.Adapter, key)
		}
		p.flags = append(p.flags, arg)
		if !has {
			i++
			if i >= len(args) {
				return p, errors.New("missing native flag value")
			}
			value = args[i]
			p.flags = append(p.flags, value)
		}
		if value == "" || strings.ContainsAny(value, "\x00\r\n") {
			return p, errors.New("invalid native flag value")
		}
		if w.Adapter == "node-test" {
			if p.options[key] != nil {
				return p, errors.New("duplicate Node context flag")
			}
			if key == "--test-concurrency" || key == "--test-timeout" {
				n, err := strconv.Atoi(value)
				if err != nil || n <= 0 || n > 86400000 {
					return p, errors.New("invalid Node numeric flag")
				}
				p.options[key] = n
			} else {
				p.options[key] = value
			}
		}
	}
	if w.Adapter == "node-test" && len(p.selectors) == 0 {
		return p, errors.New("Node discovery requires explicit files; implicit scans are unqualified")
	}
	if w.Adapter == "unittest" && p.options["discover"] == true && len(p.selectors) > 0 {
		return p, errors.New("unittest discover positional directories must use -s")
	}
	return p, nil
}

func invoke(ctx context.Context, root, dir string, w model.Workspace, c model.Context, p parsed, mode string, units []model.Unit) (inventory, process.Output, error) {
	var collected inventory
	var out process.Output
	bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	tmp, err := os.MkdirTemp("", "hayaku-native-runner-")
	if err != nil {
		return collected, out, err
	}
	defer os.RemoveAll(tmp)
	output := filepath.Join(tmp, "metadata.json")
	env := map[string]string{}
	for k, v := range c.Env {
		env[k] = v
	}
	var cmd model.Command
	switch w.Adapter {
	case "node-test":
		version, err := process.Run(bounded, model.Command{Executable: w.Command.Executable, Args: []string{"--version"}}, dir, env)
		if err != nil {
			return collected, out, errors.New("installed Node unavailable")
		}
		v := strings.TrimPrefix(strings.TrimSpace(string(version.Stdout)), "v")
		if !supportedVersion(w.Adapter, v) {
			return collected, out, errors.New("unsupported Node test runtime version")
		}
		files := []string{}
		for _, selector := range p.selectors {
			path := filepath.Join(dir, filepath.FromSlash(selector))
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || !within(root, path) {
				return collected, out, errors.New("Node explicit test selector is not a regular snapshot file")
			}
			files = append(files, path)
		}
		request, _ := json.Marshal(map[string]any{"mode": mode, "version": v, "files": files, "selectors": p.selectors, "options": p.options, "output": output})
		script := filepath.Join(tmp, "node.mjs")
		if err := os.WriteFile(script, []byte(nodeBridge), 0600); err != nil {
			return collected, out, err
		}
		cmd = model.Command{Executable: w.Command.Executable, Args: []string{script, string(request)}}
	case "unittest":
		request, _ := json.Marshal(map[string]any{"mode": mode, "args": w.Command.Args[2:], "selected": p.selectors, "flags": p.flags, "output": output, "discover": p.options["discover"] == true, "cwd": dir})
		if units != nil {
			proposal, err := Proposal(w, units)
			if err != nil {
				return collected, out, err
			}
			request, _ = json.Marshal(map[string]any{"mode": mode, "args": proposal.Args[2:], "output": output, "cwd": dir})
		}
		script := filepath.Join(tmp, "bridge.py")
		if err := os.WriteFile(script, []byte(unittestBridge), 0600); err != nil {
			return collected, out, err
		}
		if mode == "discover" {
			env["PYTHONDONTWRITEBYTECODE"] = "1"
		}
		cmd = model.Command{Executable: w.Command.Executable, Args: []string{script, string(request)}}
	case "jest", "playwright":
		if w.NodeRuntime == nil || !filepath.IsAbs(w.NodeRuntime.Node) || !filepath.IsAbs(w.NodeRuntime.Modules) {
			return collected, out, errors.New("native JS runner requires bound installed Node and modules")
		}
		pkg := w.Adapter
		if pkg == "playwright" {
			pkg = "playwright"
		}
		modules := w.NodeRuntime.Modules
		if info, err := os.Stat(filepath.Join(dir, "node_modules", pkg, "package.json")); err == nil && info.Mode().IsRegular() {
			modules = filepath.Join(dir, "node_modules")
		}
		data, err := readMetadata(filepath.Join(modules, pkg, "package.json"))
		if err != nil {
			return collected, out, errors.New("installed runner package unavailable")
		}
		var manifest struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal(data, &manifest); err != nil || !supportedVersion(w.Adapter, manifest.Version) {
			return collected, out, errors.New("unsupported installed runner package version")
		}
		if w.Adapter == "jest" {
			args := append([]string{filepath.Join(modules, "jest", "bin", "jest.js")}, p.flags...)
			if mode == "discover" {
				args = append(args, "--listTests", "--json")
				args = append(args, p.selectors...)
			} else {
				args = append(args, "--json", "--outputFile", output, "--runTestsByPath")
				args = append(args, p.selectors...)
			}
			cmd = model.Command{Executable: w.NodeRuntime.Node, Args: args}
			out, err = process.Run(bounded, cmd, dir, env)
			if mode == "discover" {
				if err != nil {
					return collected, out, errors.New("native Jest collection failed; output withheld")
				}
				return jestInventory(out.Stdout, dir, manifest.Version)
			}
			data, readErr := readMetadata(output)
			if readErr != nil {
				return collected, out, readErr
			}
			report, parseErr := jestReport(data, dir, manifest.Version)
			if parseErr != nil {
				return collected, out, parseErr
			}
			out.Stdout, _ = json.Marshal(report)
			out.Stderr = nil
			return collected, out, err
		}
		reporter := filepath.Join(tmp, "playwright.cjs")
		if err := os.WriteFile(reporter, []byte(playwrightReporter), 0600); err != nil {
			return collected, out, err
		}
		env["HAYAKU_NATIVE_OUTPUT"] = output
		env["HAYAKU_NATIVE_MODE"] = mode
		env["HAYAKU_NATIVE_VERSION"] = manifest.Version
		env["HAYAKU_NATIVE_CWD"] = dir
		args := append([]string{filepath.Join(modules, "playwright", "cli.js"), "test"}, p.flags...)
		args = append(args, "--reporter", reporter)
		if mode == "discover" {
			args = append(args, "--list")
		}
		for _, s := range p.selectors {
			args = append(args, regexpPath(s))
		}
		cmd = model.Command{Executable: w.NodeRuntime.Node, Args: args}
	}
	out, err = process.Run(bounded, cmd, dir, env)
	if bounded.Err() != nil {
		return collected, out, bounded.Err()
	}
	data, readErr := readMetadata(output)
	if readErr != nil {
		return collected, out, readErr
	}
	if mode == "discover" {
		if err != nil {
			return collected, out, errors.New("native collection failed; output withheld")
		}
		if decodeErr := nativerunner.DecodeJSON(data, &collected); decodeErr != nil {
			return collected, out, decodeErr
		}
		out.Stdout = nil
		out.Stderr = nil
		return collected, out, nil
	}
	var report nativerunner.Report
	if decodeErr := nativerunner.DecodeJSON(data, &report); decodeErr != nil {
		return collected, out, decodeErr
	}
	if report.Runner != w.Adapter || !supportedVersion(w.Adapter, report.Version) {
		return collected, out, errors.New("unsupported native result version")
	}
	out.Stdout, _ = json.Marshal(report)
	out.Stderr = nil
	return collected, out, err
}
func readMetadata(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("native metadata must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("native metadata unavailable")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, nativerunner.MaxResultBytes+1))
	if err != nil || len(data) > nativerunner.MaxResultBytes {
		return nil, errors.New("native metadata exceeds limit")
	}
	return data, nil
}

func jestInventory(data []byte, dir, version string) (inventory, process.Output, error) {
	result := inventory{Schema: 1, Runner: "jest", Version: version, Complete: true, Items: []item{}}
	var files []string
	if err := nativerunner.DecodeJSON(data, &files); err != nil {
		return result, process.Output{}, err
	}
	for _, path := range files {
		if !filepath.IsAbs(path) {
			return result, process.Output{}, errors.New("Jest listed non-absolute path")
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || !validSelector("jest", filepath.ToSlash(rel)) {
			return result, process.Output{}, errors.New("Jest file outside cwd")
		}
		result.Items = append(result.Items, item{Selector: filepath.ToSlash(rel), Path: path, Tests: []string{}})
	}
	return result, process.Output{Completed: true, ExitCode: 0}, nil
}
func jestReport(data []byte, dir, version string) (nativerunner.Report, error) {
	report := nativerunner.Report{Schema: 1, Runner: "jest", Version: version, Units: []nativerunner.Terminal{}, Tests: []nativerunner.Terminal{}}
	// Native Jest reports include diagnostics. Only selected identity/status
	// fields survive normalization, never messages, source or assertion values.
	var raw struct {
		WasInterrupted            bool `json:"wasInterrupted"`
		NumTotalTestSuites        int  `json:"numTotalTestSuites"`
		NumRuntimeErrorTestSuites int  `json:"numRuntimeErrorTestSuites"`
		TestResults               []struct {
			Name             string `json:"name"`
			Status           string `json:"status"`
			AssertionResults []struct {
				FullName string `json:"fullName"`
				Status   string `json:"status"`
			} `json:"assertionResults"`
		} `json:"testResults"`
	}
	var metadata map[string]json.RawMessage
	if nativerunner.DecodeJSON(data, &metadata) != nil || json.Unmarshal(data, &raw) != nil || raw.NumTotalTestSuites <= 0 || len(raw.TestResults) != raw.NumTotalTestSuites || raw.NumRuntimeErrorTestSuites < 0 {
		return report, errors.New("invalid or incomplete Jest result")
	}
	report.Errors = raw.NumRuntimeErrorTestSuites
	for _, file := range raw.TestResults {
		rel, err := filepath.Rel(dir, file.Name)
		selector := filepath.ToSlash(rel)
		if err != nil || !filepath.IsAbs(file.Name) || !validSelector("jest", selector) {
			return report, errors.New("invalid Jest result file")
		}
		action, err := jestAction(file.Status)
		if file.Status == "focused" {
			action, err = "skip", nil
			for _, test := range file.AssertionResults {
				if test.Status == "passed" {
					action = "pass"
				}
			}
		}
		if err != nil {
			return report, err
		}
		report.Units = append(report.Units, nativerunner.Terminal{Selector: selector, Action: action})
		counts := map[string]int{}
		for _, test := range file.AssertionResults {
			a, err := jestAction(test.Status)
			if err != nil || test.FullName == "" {
				return report, errors.New("invalid Jest test result")
			}
			identity, _ := json.Marshal([]any{test.FullName, counts[test.FullName]})
			counts[test.FullName]++
			report.Tests = append(report.Tests, nativerunner.Terminal{Selector: selector, Test: string(identity), Action: a})
		}
	}
	report.Complete = !raw.WasInterrupted
	return report, nil
}
func jestAction(status string) (string, error) {
	switch status {
	case "passed":
		return "pass", nil
	case "failed":
		return "fail", nil
	case "pending", "todo", "disabled", "skipped":
		return "skip", nil
	}
	return "", errors.New("non-terminal Jest outcome")
}

func executePytest(ctx context.Context, root string, w model.Workspace, c model.Context, units []model.Unit) (process.Output, error) {
	// Reuse the existing adapter's strict argument contract and proposal logic.
	if _, err := pytest.Proposal(w, []model.Unit{{Workspace: w.ID, Kind: "pytest-file", Selector: "__hayaku_validation__.py"}}); err != nil {
		return process.Output{}, err
	}
	var err error
	root, dir, err := locations(root, w, c)
	if err != nil {
		return process.Output{}, err
	}
	cmd := w.Command
	if units != nil {
		cmd, err = pytest.Proposal(w, units)
		if err != nil {
			return process.Output{}, err
		}
	}
	tmp, err := os.MkdirTemp("", "hayaku-pytest-result-")
	if err != nil {
		return process.Output{}, err
	}
	defer os.RemoveAll(tmp)
	script := filepath.Join(tmp, "bridge.py")
	if err := os.WriteFile(script, []byte(pytestBridge), 0600); err != nil {
		return process.Output{}, err
	}
	output := filepath.Join(tmp, "metadata.json")
	request, _ := json.Marshal(map[string]any{"args": cmd.Args[2:], "output": output, "cwd": dir})
	bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, runErr := process.Run(bounded, model.Command{Executable: cmd.Executable, Args: []string{script, string(request)}}, dir, c.Env)
	if bounded.Err() != nil {
		return out, bounded.Err()
	}
	data, err := readMetadata(output)
	if err != nil {
		return out, err
	}
	var report nativerunner.Report
	if err := nativerunner.DecodeJSON(data, &report); err != nil {
		return out, err
	}
	if report.Runner != "pytest" || !supportedVersion("pytest", report.Version) {
		return out, errors.New("unsupported pytest native result version")
	}
	out.Stdout, _ = json.Marshal(report)
	out.Stderr = nil
	return out, runErr
}
