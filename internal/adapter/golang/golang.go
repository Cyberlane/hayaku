// Package golang extracts native Go package evidence. It never certifies
// arbitrary runtime inputs and therefore always reports an assurance gap.
package golang

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
)

const maxOutput = 64 << 20

type listedPackage struct {
	Dir, ImportPath, Name, ForTest                                                                                                             string
	DepOnly, Incomplete                                                                                                                        bool
	GoFiles, CgoFiles, CFiles, CXXFiles, MFiles, HFiles, FFiles, SFiles, SwigFiles, SwigCXXFiles, SysoFiles, IgnoredGoFiles, IgnoredOtherFiles []string
	TestGoFiles, XTestGoFiles, EmbedFiles, TestEmbedFiles, XTestEmbedFiles                                                                     []string
	Imports, TestImports, XTestImports                                                                                                         []string
	Module                                                                                                                                     *struct {
		Dir, GoMod string
		Replace    *struct{ Dir, GoMod string }
	}
	Error      *json.RawMessage
	DepsErrors []json.RawMessage
}

// Discover invokes only the installed Go tool, with network/toolchain downloads
// disabled. Native graph evidence remains a shadow proposal, never omission proof.
func Discover(ctx context.Context, snapshotRoot string, w model.Workspace, execution model.Context) (model.Evidence, error) {
	e := model.Evidence{Adapter: "go", Version: "go-native-v1", Workspace: w.ID, Inputs: map[string][]string{}, Gaps: []model.Gap{{Code: "runtime-unqualified", Workspace: w.ID, Detail: "Go metadata does not establish runtime file, service, subprocess or shared-state influence"}}}
	flags, patterns, err := commandParts(w)
	if err != nil {
		return e, err
	}
	root, err := filepath.Abs(snapshotRoot)
	if err != nil {
		return e, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return e, err
	}
	dir := filepath.Join(root, filepath.FromSlash(w.Root), filepath.FromSlash(w.Command.Dir))
	if !within(root, dir) {
		return e, errors.New("Go workspace escapes snapshot")
	}
	env, err := ExecutionEnvironment(ctx, root, w, execution)
	if err != nil {
		return e, err
	}
	version, err := invoke(ctx, dir, env, w.Command.Executable, []string{"version"}, 4096)
	if err != nil {
		return e, fmt.Errorf("installed Go tool unavailable: %w", err)
	}
	e.Version += ":" + strings.TrimSpace(string(version))
	// Honor native vendoring when no explicit module mode was configured.
	_, originalBuild, _, _ := parseCommand(w.Command)
	explicitMode := false
	for _, flag := range originalBuild {
		if flag == "-mod" || strings.HasPrefix(flag, "-mod=") {
			explicitMode = true
		}
	}
	if !explicitMode {
		work := "off"
		for _, pair := range env {
			if k, v, ok := strings.Cut(pair, "="); ok && k == "GOWORK" {
				work = v
			}
		}
		if work != "off" && work != "" {
			if _, err := os.Stat(filepath.Join(filepath.Dir(work), "vendor", "modules.txt")); err == nil {
				flags[len(flags)-1] = "-mod=vendor"
			}
		} else {
			for ancestor := dir; within(root, ancestor); ancestor = filepath.Dir(ancestor) {
				if _, err := os.Stat(filepath.Join(ancestor, "vendor", "modules.txt")); err == nil {
					flags[len(flags)-1] = "-mod=vendor"
					break
				}
				if _, err := os.Stat(filepath.Join(ancestor, "go.mod")); err == nil || ancestor == root {
					break
				}
			}
		}
	}
	args := append([]string{"list", "-deps", "-test", "-json"}, flags...)
	args = append(args, patterns...)
	output, err := invoke(ctx, dir, env, w.Command.Executable, args, maxOutput)
	if err != nil {
		return e, fmt.Errorf("Go package discovery failed: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(output))
	nodes, units, edges := map[string]bool{}, map[string]model.Unit{}, map[model.Edge]bool{}
	manifests := map[string]bool{}
	id := func(p string) string { return w.ID + ":" + canonical(p) }
	addInput := func(path, owner string) {
		if path == "" {
			return
		}
		if !within(root, path) {
			e.Gaps = append(e.Gaps, model.Gap{Code: "external-source", Workspace: w.ID, Detail: "A source or module definition is outside the immutable snapshot"})
			return
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, old := range e.Inputs[rel] {
			if old == owner {
				return
			}
		}
		e.Inputs[rel] = append(e.Inputs[rel], owner)
	}
	count := 0
	for {
		var p listedPackage
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return e, errors.New("invalid Go discovery JSON")
		}
		count++
		if count > 100000 {
			return e, errors.New("Go discovery package limit exceeded")
		}
		if p.ImportPath == "" || p.Incomplete || p.Error != nil || len(p.DepsErrors) > 0 {
			return e, errors.New("incomplete Go package metadata")
		}
		owner := id(p.ImportPath)
		nodes[owner] = true
		for _, set := range [][]string{p.Imports, p.TestImports, p.XTestImports} {
			for _, dep := range set {
				from := id(dep)
				nodes[from] = true
				if from != owner {
					edges[model.Edge{From: from, To: owner, Reason: "Go import"}] = true
				}
			}
		}
		if !within(root, p.Dir) {
			continue
		}
		if p.ForTest != "" {
			original := id(p.ForTest)
			nodes[original] = true
			if original != owner {
				edges[model.Edge{From: owner, To: original, Reason: "Go external test package"}] = true
			}
		}
		if p.ForTest == "" && !isTestBinary(p) && !p.DepOnly {
			units[owner] = model.Unit{ID: owner, Workspace: w.ID, Selector: p.ImportPath, Kind: "go-package"}
		}
		if isTestBinary(p) {
			continue
		}
		for _, files := range [][]string{p.GoFiles, p.CgoFiles, p.CFiles, p.CXXFiles, p.MFiles, p.HFiles, p.FFiles, p.SFiles, p.SwigFiles, p.SwigCXXFiles, p.SysoFiles, p.IgnoredGoFiles, p.IgnoredOtherFiles, p.TestGoFiles, p.XTestGoFiles, p.EmbedFiles, p.TestEmbedFiles, p.XTestEmbedFiles} {
			for _, f := range files {
				addInput(filepath.Join(p.Dir, f), owner)
			}
		}
		if p.Module != nil {
			manifests[p.Module.GoMod] = true
			if p.Module.Replace != nil {
				manifests[p.Module.Replace.GoMod] = true
			}
		}
	}
	if count == 0 || len(units) == 0 {
		return e, errors.New("Go discovery returned no runnable packages")
	}
	// Changes to workspace/module graph definitions affect every discovered unit.
	for ancestor := dir; within(root, ancestor); ancestor = filepath.Dir(ancestor) {
		for _, name := range []string{"go.mod", "go.sum", "go.work", "go.work.sum"} {
			path := filepath.Join(ancestor, name)
			if _, err := os.Stat(path); err == nil {
				manifests[path] = true
			}
		}
		if ancestor == root {
			break
		}
	}
	for manifest := range manifests {
		if manifest == "" {
			continue
		}
		for owner := range units {
			addInput(manifest, owner)
			if filepath.Base(manifest) == "go.mod" {
				if _, err := os.Stat(filepath.Join(filepath.Dir(manifest), "go.sum")); err == nil {
					addInput(filepath.Join(filepath.Dir(manifest), "go.sum"), owner)
				}
			}
		}
	}
	for n := range nodes {
		e.Nodes = append(e.Nodes, n)
	}
	sort.Strings(e.Nodes)
	for _, u := range units {
		e.Units = append(e.Units, u)
	}
	sort.Slice(e.Units, func(i, j int) bool { return e.Units[i].ID < e.Units[j].ID })
	for edge := range edges {
		e.Edges = append(e.Edges, edge)
	}
	sort.Slice(e.Edges, func(i, j int) bool {
		a, b := e.Edges[i], e.Edges[j]
		if a.From != b.From {
			return a.From < b.From
		}
		return a.To < b.To
	})
	for path := range e.Inputs {
		sort.Strings(e.Inputs[path])
	}
	return e, nil
}

func isTestBinary(p listedPackage) bool {
	if p.Name != "main" || !strings.HasSuffix(p.ImportPath, ".test") {
		return false
	}
	for _, file := range p.GoFiles {
		if filepath.Base(file) == "_testmain.go" || filepath.IsAbs(file) {
			return true
		}
	}
	return false
}

func canonical(p string) string {
	if i := strings.Index(p, " ["); i >= 0 {
		return p[:i]
	}
	return p
}
func within(root, path string) bool {
	if path == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Proposal preserves all supported command flags and replaces only package
// patterns. It is advisory; callers must retain prerequisites and full commands.
func Proposal(w model.Workspace, units []model.Unit) (model.Command, error) {
	_, _, err := commandParts(w)
	if err != nil {
		return model.Command{}, err
	}
	flags, _, _, err := parseCommand(w.Command)
	if err != nil {
		return model.Command{}, err
	}
	selectors := map[string]bool{}
	for _, u := range units {
		if u.Workspace != w.ID || u.Kind != "go-package" || u.Selector == "" || strings.HasPrefix(u.Selector, "-") {
			return model.Command{}, errors.New("invalid Go proposal unit")
		}
		selectors[u.Selector] = true
	}
	if len(selectors) == 0 {
		return model.Command{}, errors.New("empty Go proposal has no executable command")
	}
	pkgs := []string{}
	for p := range selectors {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	args := append([]string{"test"}, flags...)
	args = append(args, pkgs...)
	return model.Command{Dir: w.Command.Dir, Executable: w.Command.Executable, Args: args}, nil
}

// parseCommand accepts an explicit conservative flag vocabulary. Unsupported
// wrappers, custom test-binary flags and position-changing flags require full run.
func parseCommand(c model.Command) (flags, build, patterns []string, err error) {
	if (filepath.Base(c.Executable) != "go" && filepath.Base(c.Executable) != "go.exe") || len(c.Args) == 0 || c.Args[0] != "test" {
		return nil, nil, nil, errors.New("Go proposals require a plain go test command")
	}
	boolFlags := map[string]bool{"race": true, "cover": true, "v": true, "short": true, "failfast": true, "fullpath": true, "shuffle": false, "trimpath": true}
	valueFlags := map[string]bool{"tags": true, "mod": true, "p": true, "parallel": true, "count": true, "timeout": true, "shuffle": true, "cpu": true, "vet": true, "covermode": true, "coverpkg": true}
	buildFlag := map[string]bool{"race": true, "tags": true, "mod": true, "p": true, "trimpath": true, "cover": true, "covermode": true, "coverpkg": true}
	for i := 1; i < len(c.Args); i++ {
		arg := c.Args[i]
		if !strings.HasPrefix(arg, "-") {
			patterns = append(patterns, arg)
			continue
		}
		name, value, has := strings.Cut(strings.TrimPrefix(arg, "-"), "=")
		if name == "json" {
			if has {
				return nil, nil, nil, errors.New("JSON formatting must be enabled without a value")
			}
			flags = append(flags, arg)
			continue
		}
		if !boolFlags[name] && !valueFlags[name] {
			return nil, nil, nil, fmt.Errorf("unsupported Go test flag %q", name)
		}
		item := []string{arg}
		if valueFlags[name] && !has {
			i++
			if i >= len(c.Args) {
				return nil, nil, nil, errors.New("missing Go flag value")
			}
			item = append(item, c.Args[i])
			value = c.Args[i]
		}
		if name == "mod" && value != "readonly" && value != "vendor" {
			return nil, nil, nil, errors.New("discovery cannot modify module metadata")
		}
		flags = append(flags, item...)
		if buildFlag[name] {
			build = append(build, item...)
		}
	}
	if len(patterns) == 0 {
		patterns = []string{"."}
	}
	for _, p := range patterns {
		if p == "" || strings.HasSuffix(p, ".go") {
			return nil, nil, nil, errors.New("Go discovery requires package patterns")
		}
	}
	return flags, build, patterns, nil
}
func commandParts(w model.Workspace) ([]string, []string, error) {
	_, build, patterns, err := parseCommand(w.Command)
	if err != nil {
		return nil, nil, err
	}
	if len(w.Patterns) > 0 {
		if strings.Join(patterns, "\x00") != strings.Join(w.Patterns, "\x00") {
			return nil, nil, errors.New("Go patterns differ from configured command")
		}
	}
	if len(w.BuildFlags) > 0 && strings.Join(build, "\x00") != strings.Join(w.BuildFlags, "\x00") {
		return nil, nil, errors.New("Go build flags differ from configured command")
	}
	// readonly preserves existing graph files; vendor uses its native frozen graph.
	hasMod := false
	for _, f := range build {
		if f == "-mod" || strings.HasPrefix(f, "-mod=") {
			hasMod = true
		}
	}
	if !hasMod {
		build = append(build, "-mod=readonly")
	}
	return build, patterns, nil
}

// ExecutionEnvironment captures the installed tool's effective native context
// before disabling persisted configuration. Discovery and execution must use
// this same environment. Values are process inputs and must never be reported.
func ExecutionEnvironment(ctx context.Context, snapshotRoot string, w model.Workspace, c model.Context) ([]string, error) {
	root, err := filepath.Abs(snapshotRoot)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, filepath.FromSlash(w.Root), filepath.FromSlash(w.Command.Dir))
	if !within(root, dir) {
		return nil, errors.New("Go execution cwd escapes snapshot")
	}
	values := map[string]string{}
	for _, pair := range os.Environ() {
		k, v, ok := strings.Cut(pair, "=")
		if ok {
			values[k] = v
		}
	}
	for k, v := range c.Env {
		values[k] = v
	}
	if path, ok := c.Env["PATH"]; ok && path != os.Getenv("PATH") {
		return nil, errors.New("different PATH overrides are unsupported; use an absolute Go runner path")
	}
	if c.OS != "" && values["GOOS"] != "" && values["GOOS"] != c.OS {
		return nil, errors.New("effective GOOS conflicts with declared execution context")
	}
	if c.Arch != "" && values["GOARCH"] != "" && values["GOARCH"] != c.Arch {
		return nil, errors.New("effective GOARCH conflicts with declared execution context")
	}
	if c.OS != "" {
		values["GOOS"] = c.OS
	}
	if c.Arch != "" {
		values["GOARCH"] = c.Arch
	}
	// Never allow native go env to fetch a toolchain or dependency. Other Go
	// variables remain intact so persisted and inherited scope are observed.
	offline(values)
	if values["GOFLAGS"] != "" {
		return nil, errors.New("effective GOFLAGS is unsupported: move Go flags into the configured command argv")
	}
	output, err := invoke(ctx, dir, environment(values), w.Command.Executable, []string{"env", "-json"}, 1<<20)
	if err != nil {
		return nil, errors.New("effective Go environment could not be established (native diagnostics withheld)")
	}
	effective := map[string]string{}
	if json.Unmarshal(output, &effective) != nil || effective["GOVERSION"] == "" {
		return nil, errors.New("invalid effective Go environment")
	}
	if effective["GOFLAGS"] != "" {
		return nil, errors.New("effective GOFLAGS is unsupported: move persisted or inherited Go flags into the configured command argv")
	}
	work := effective["GOWORK"]
	if work != "" && work != "off" {
		path, err := filepath.EvalSymlinks(work)
		if err != nil || !within(root, path) {
			return nil, errors.New("effective GOWORK is outside the snapshot: use an in-snapshot workspace or explicitly set GOWORK=off")
		}
		effective["GOWORK"] = path
	} else {
		effective["GOWORK"] = "off"
	}
	module := effective["GOMOD"]
	if module != "" && module != os.DevNull {
		path, err := filepath.EvalSymlinks(module)
		if err != nil || !within(root, path) {
			return nil, errors.New("effective Go module is outside the snapshot")
		}
	} else if effective["GOWORK"] == "off" {
		return nil, errors.New("Go adapter requires a snapshot-contained module or workspace")
	}
	// These values describe the tool; they cannot configure a future process.
	readOnly := map[string]bool{"GOGCCFLAGS": true, "GOVERSION": true, "GOMOD": true, "GOHOSTOS": true, "GOHOSTARCH": true, "GOTOOLDIR": true, "GOTELEMETRYDIR": true, "GOENV": true}
	for k, v := range effective {
		if !readOnly[k] {
			values[k] = v
		}
	}
	values["GOENV"] = "off"
	values["GOFLAGS"] = ""
	offline(values)
	return environment(values), nil
}
func offline(values map[string]string) {
	for k, v := range map[string]string{"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GONOPROXY": "", "GOVCS": "*:off", "GOTELEMETRY": "off"} {
		values[k] = v
	}
}
func environment(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, k+"="+values[k])
	}
	return env
}

type cappedBuffer struct {
	data     bytes.Buffer
	limit    int
	exceeded bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.data.Len()+len(p) > b.limit {
		b.exceeded = true
		return 0, errors.New("process output limit exceeded")
	}
	return b.data.Write(p)
}
func invoke(ctx context.Context, dir string, env []string, executable string, args []string, limit int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.WaitDelay = time.Second
	output := &cappedBuffer{limit: limit}
	stderr := &cappedBuffer{limit: 1 << 20}
	cmd.Stdout = output
	cmd.Stderr = stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if output.exceeded || stderr.exceeded {
		return nil, errors.New("process output limit exceeded")
	}
	if err != nil {
		return nil, errors.New("native Go process failed (diagnostic output withheld)")
	}
	return output.data.Bytes(), nil
}

// Execution requests machine-readable native results while preserving the
// configured test scope and all supported flags. No flag is used to skip tests.
func Execution(w model.Workspace) (model.Command, error) {
	if _, _, err := commandParts(w); err != nil {
		return model.Command{}, err
	}
	command := w.Command
	command.Args = append([]string(nil), w.Command.Args...)
	for _, arg := range command.Args {
		if arg == "-json" {
			return command, nil
		}
		if strings.HasPrefix(arg, "-json=") {
			return model.Command{}, errors.New("JSON formatting must be enabled without a value")
		}
	}
	command.Args = append([]string{"test", "-json"}, command.Args[1:]...)
	return command, nil
}
