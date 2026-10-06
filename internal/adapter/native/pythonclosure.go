package native

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Cyberlane/hayaku/internal/adapter/pytest"
	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/influence"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

//go:embed pythonclosure.py
var pythonClosureBridge string

type PythonSource struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type PythonFixture struct {
	Owner        string   `json:"owner"`
	ID           string   `json:"id"`
	Dependencies []string `json:"dependencies"`
	Scope        string   `json:"scope"`
	Autouse      bool     `json:"autouse"`
}

// PythonClosure describes configured collection and a bounded syntax envelope.
// Neither collected cases nor AST/import/fixture evidence proves runtime influence
// or grants isolation/skip authority. Dynamic execution cases remain explicit gaps.
type PythonClosure struct {
	Version       string                   `json:"version"`
	Digest        string                   `json:"digest"`
	SourceDigest  string                   `json:"source_digest"`
	ContextDigest string                   `json:"context_digest"`
	ToolDigest    string                   `json:"tool_digest"`
	PythonVersion string                   `json:"python_version"`
	RunnerVersion string                   `json:"runner_version"`
	Inventory     model.ExecutionInventory `json:"inventory"`
	Evidence      model.Evidence           `json:"evidence"`
	Sources       []PythonSource           `json:"sources"`
	Fixtures      []PythonFixture          `json:"fixtures"`
	workspace     model.Workspace
	execution     model.Context
	environment   []string
	runtimeDigest string
	expected      string
	sourceRoot    string
	sourceDir     string
}

type pythonMetadata struct {
	Schema        int    `json:"schema"`
	PythonVersion string `json:"python_version"`
	RunnerVersion string `json:"runner_version"`
	RuntimeDigest string `json:"runtime_digest"`
	Items         []struct {
		Identity string `json:"identity"`
		Path     string `json:"path"`
	} `json:"items"`
	Sources  []PythonSource  `json:"sources"`
	Edges    []model.Edge    `json:"edges"`
	Effects  []model.Effect  `json:"effects"`
	Fixtures []PythonFixture `json:"fixtures"`
	Gaps     []model.Gap     `json:"gaps"`
}

func pythonDigest(value any) string {
	data, ok := value.([]byte)
	if !ok {
		data, _ = json.Marshal(value)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Bound and validate every fixed-model string before JSON can replace raw bytes.
func pythonContextEncoding(value reflect.Value, nodes, size *int) bool {
	*nodes--
	if *nodes < 0 {
		return false
	}
	switch value.Kind() {
	case reflect.String:
		*size -= value.Len()
		return *size >= 0 && utf8.ValidString(value.String()) && !strings.ContainsRune(value.String(), 0)
	case reflect.Pointer, reflect.Interface:
		return value.IsNil() || pythonContextEncoding(value.Elem(), nodes, size)
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			if !pythonContextEncoding(value.Field(index), nodes, size) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		if value.Len() > *nodes {
			return false
		}
		for index := 0; index < value.Len(); index++ {
			if !pythonContextEncoding(value.Index(index), nodes, size) {
				return false
			}
		}
	case reflect.Map:
		if value.Len() > *nodes/2 {
			return false
		}
		entries := value.MapRange()
		for entries.Next() {
			if !pythonContextEncoding(entries.Key(), nodes, size) || !pythonContextEncoding(entries.Value(), nodes, size) {
				return false
			}
		}
	}
	return true
}

func pythonHelper(ctx context.Context, root, dir string, w model.Workspace, env []string, mode string, files []string) (pythonMetadata, error) {
	var metadata pythonMetadata
	temporary, err := os.MkdirTemp("", "hayaku-python-closure-")
	if err != nil {
		return metadata, errors.New("Python closure helper unavailable")
	}
	defer os.RemoveAll(temporary)
	script, output := filepath.Join(temporary, "closure.py"), filepath.Join(temporary, "metadata.json")
	if err := os.WriteFile(script, []byte(pythonClosureBridge), 0600); err != nil {
		return metadata, errors.New("Python closure helper unavailable")
	}
	request, err := json.Marshal(map[string]any{"root": root, "cwd": dir, "runner": w.Adapter, "mode": mode, "files": files, "args": w.Command.Args[2:], "output": output})
	if err != nil || len(request) > 2<<20 {
		return metadata, errors.New("Python closure request exceeds bounds")
	}
	out, err := process.RunEnvironmentLimits(ctx, model.Command{Executable: w.Command.Executable, Args: []string{script, string(request)}}, dir, env, 16<<20, 1<<20)
	if err != nil || !out.Completed || out.ExitCode != 0 {
		if ctx.Err() != nil {
			return metadata, ctx.Err()
		}
		return metadata, errors.New("Python closure collection/analysis failed; diagnostics withheld")
	}
	data, err := readMetadata(output)
	if err != nil || config.DecodeValue(strings.NewReader(string(data)), &metadata, 16<<20) != nil || metadata.Schema != 1 || !supportedVersion("unittest", metadata.PythonVersion) {
		return metadata, errors.New("invalid Python closure metadata")
	}
	return metadata, nil
}

// CollectPythonClosure reuses the existing installed runner command protocols.
// Collection runs trusted project startup/hooks once, never test bodies here.
// Before/after guards observe stable drift; they cannot attest transient effects.
func CollectPythonClosure(ctx context.Context, root string, w model.Workspace, execution model.Context) (PythonClosure, error) {
	var result PythonClosure
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if w.Adapter != "unittest" && w.Adapter != "pytest" {
		return result, errors.New("Python closure requires unittest or pytest")
	}
	var parsedCommand parsed
	var err error
	if w.Adapter == "unittest" {
		parsedCommand, err = parse(w)
	} else {
		_, err = pytest.Proposal(w, []model.Unit{{Workspace: w.ID, Kind: "pytest-file", Selector: "__hayaku_validation__.py"}})
	}
	if err != nil {
		return result, errors.New("unsupported configured Python command")
	}
	root, dir, err := locations(root, w, execution)
	if err != nil {
		return result, errors.New("invalid Python snapshot context")
	}
	tool, err := exec.LookPath(w.Command.Executable)
	if err != nil {
		return result, errors.New("configured Python interpreter unavailable")
	}
	tool, err = filepath.Abs(tool)
	if err != nil {
		return result, errors.New("invalid Python interpreter path")
	}
	// Clone the fixed configuration, preserving private full original context.
	declared := struct {
		Workspace model.Workspace
		Context   model.Context
	}{w, execution}
	nodes, size := 100000, 1<<20
	if !pythonContextEncoding(reflect.ValueOf(declared), &nodes, &size) {
		return result, errors.New("invalid or oversized Python context encoding")
	}
	configuration, _ := json.Marshal(declared)
	var captured struct {
		Workspace model.Workspace
		Context   model.Context
	}
	if len(configuration) > 1<<20 || json.Unmarshal(configuration, &captured) != nil {
		return result, errors.New("Python closure context exceeds bounds")
	}
	w, execution = captured.Workspace, captured.Context
	for key := range execution.Env {
		if key == "" || strings.Contains(key, "=") {
			return result, errors.New("invalid Python environment key")
		}
	}
	for _, value := range process.Environment(execution.Env) {
		if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return result, errors.New("invalid Python closure context encoding")
		}
	}
	w.Command.Executable = tool
	frozen := model.Context{ID: execution.ID, OS: execution.OS, Arch: execution.Arch, Env: map[string]string{}}
	for _, pair := range process.Environment(execution.Env) {
		key, value, _ := strings.Cut(pair, "=")
		frozen.Env[key] = value
	}
	frozen.Env["PYTHONDONTWRITEBYTECODE"] = "1"
	frozen.Env["PIP_NO_INDEX"] = "1"
	frozen.Env["PIP_DISABLE_PIP_VERSION_CHECK"] = "1"
	env := process.Environment(frozen.Env)
	source, err := snapshot.Digest(ctx, root)
	if err != nil {
		return result, errors.New("Python snapshot unavailable or unbounded")
	}
	guard := func() error {
		current, err := snapshot.Digest(ctx, root)
		if err != nil || current != source || !reflect.DeepEqual(process.Environment(frozen.Env), env) {
			return errors.New("Python source or effective context drifted")
		}
		return nil
	}
	before, err := pythonHelper(ctx, root, dir, w, env, "identity", nil)
	if err != nil || before.RuntimeDigest == "" || !supportedVersion(w.Adapter, before.RunnerVersion) {
		return result, errors.New("installed Python runner identity unavailable or unsupported")
	}
	if err := guard(); err != nil {
		return result, err
	}
	var collected inventory
	if w.Adapter == "unittest" {
		collected, _, err = invokeEnvironment(ctx, root, dir, w, frozen, parsedCommand, "discover", nil, env)
	} else {
		var metadata pythonMetadata
		metadata, err = pythonHelper(ctx, root, dir, w, env, "collect", nil)
		collected = inventory{Schema: 1, Runner: w.Adapter, Version: metadata.RunnerVersion, Complete: true}
		byPath := map[string]int{}
		for _, entry := range metadata.Items {
			if !config.Relative(entry.Path) || entry.Path == "." {
				return result, errors.New("invalid collected Python source identity")
			}
			path := filepath.Join(root, filepath.FromSlash(entry.Path))
			selector, _ := filepath.Rel(dir, path)
			position, exists := byPath[path]
			if !exists {
				position = len(collected.Items)
				byPath[path] = position
				collected.Items = append(collected.Items, item{Selector: filepath.ToSlash(selector), Path: path})
			}
			collected.Items[position].Tests = append(collected.Items[position].Tests, entry.Identity)
		}
		if metadata.PythonVersion != before.PythonVersion {
			return result, errors.New("Python collection interpreter version changed")
		}
	}
	if err != nil || collected.Schema != 1 || !collected.Complete || collected.Runner != w.Adapter || collected.Version != before.RunnerVersion {
		return result, errors.New("Python collection failed or changed runner identity")
	}
	if err := guard(); err != nil {
		return result, err
	}
	result = PythonClosure{Version: "python-source-closure-v1", SourceDigest: source, PythonVersion: before.PythonVersion, RunnerVersion: before.RunnerVersion,
		ContextDigest: pythonDigest(append(append(configuration, 0), []byte(strings.Join(append([]string{root, dir}, env...), "\x00"))...)),
		ToolDigest:    pythonDigest([]byte(before.RuntimeDigest + pythonClosureBridge + unittestBridge)),
		Inventory:     model.ExecutionInventory{Schema: model.ExecutionSchema},
		Evidence:      model.Evidence{Adapter: w.Adapter, Workspace: w.ID, Version: "python-source-closure-v1", Inputs: map[string][]string{}}}
	if len(collected.Items) == 0 || len(collected.Items) > 50000 {
		return PythonClosure{}, errors.New("Python collection has no bounded independent inventory")
	}
	workspace := filepath.Join(root, filepath.FromSlash(w.Root))
	selectors := map[string]bool{}
	for _, entry := range collected.Items {
		resolved, err := filepath.EvalSymlinks(entry.Path)
		if !validSelector(w.Adapter, entry.Selector) || selectors[entry.Selector] || err != nil || !within(workspace, resolved) {
			return PythonClosure{}, errors.New("invalid or ambiguous Python collection owner")
		}
		info, err := os.Lstat(resolved)
		if err != nil || !info.Mode().IsRegular() || filepath.Ext(resolved) != ".py" {
			return PythonClosure{}, errors.New("Python collected owner is not a source file")
		}
		if len(entry.Tests) == 0 || len(result.Inventory.Cases)+len(entry.Tests) > 100000 {
			return PythonClosure{}, errors.New("Python collected case membership exceeds bounds")
		}
		if w.Adapter == "pytest" && filepath.Join(dir, filepath.FromSlash(entry.Selector)) != resolved {
			return PythonClosure{}, errors.New("Python selector and source ownership disagree")
		}
		selectors[entry.Selector] = true
		unit := model.Unit{ID: w.ID + ":" + w.Adapter + ":" + entry.Selector, Workspace: w.ID, Selector: entry.Selector, Kind: w.Adapter + "-file"}
		result.Inventory.Units = append(result.Inventory.Units, unit)
		for _, test := range entry.Tests {
			if !utf8.ValidString(test) {
				return PythonClosure{}, errors.New("invalid Python case encoding")
			}
			result.Inventory.Cases = append(result.Inventory.Cases, model.Case{Unit: unit.ID, ID: test})
		}
	}
	if err := runner.ValidateInventory(result.Inventory); err != nil {
		return PythonClosure{}, err
	}
	result.Evidence.Units = append([]model.Unit(nil), result.Inventory.Units...)
	for _, unit := range result.Inventory.Units {
		result.Evidence.Nodes = append(result.Evidence.Nodes, unit.ID)
	}
	var files []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || ctx.Err() != nil {
			return errors.New("Python source scan interrupted")
		}
		if entry.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if !entry.Type().IsRegular() || !config.Relative(rel) || len(result.Evidence.Inputs) >= 50000 || (len(result.Evidence.Inputs)+1)*len(result.Inventory.Units) > 1000000 {
			return errors.New("unsupported or oversized Python source envelope")
		}
		result.Evidence.Inputs[rel] = append([]string(nil), result.Evidence.Nodes...)
		if strings.HasSuffix(rel, ".py") {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return PythonClosure{}, err
	}
	syntax, err := pythonHelper(ctx, root, dir, w, env, "analyze", files)
	if err != nil || syntax.PythonVersion != before.PythonVersion || len(syntax.Sources) != len(files) {
		return PythonClosure{}, errors.New("Python syntax envelope failed or changed identity")
	}
	for index, input := range syntax.Sources {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(files[index])))
		if err != nil || input.Path != files[index] || pythonDigest(data) != input.Digest {
			return PythonClosure{}, errors.New("Python syntax source membership differs")
		}
	}
	knownSources := make(map[string]bool, len(files))
	for _, file := range files {
		knownSources[file] = true
	}
	for _, edge := range syntax.Edges {
		if !knownSources[edge.From] || !knownSources[edge.To] || edge.Reason != "static-python-import" {
			return PythonClosure{}, errors.New("invalid Python import edge ownership")
		}
	}
	for _, effect := range syntax.Effects {
		if !knownSources[effect.Owner] || effect.Kind == "" || (effect.Path != "" && !config.Relative(effect.Path)) {
			return PythonClosure{}, errors.New("invalid Python effect ownership or path")
		}
	}
	for _, fixture := range syntax.Fixtures {
		if !knownSources[fixture.Owner] || fixture.ID == "" || fixture.Scope == "" {
			return PythonClosure{}, errors.New("invalid Python fixture ownership")
		}
	}
	result.Sources, result.Fixtures = syntax.Sources, syntax.Fixtures
	result.Evidence.Edges, result.Evidence.Effects = syntax.Edges, syntax.Effects
	sourceID := func(path string) string { return w.ID + ":python-source:" + path }
	for _, file := range files {
		result.Evidence.Nodes = append(result.Evidence.Nodes, sourceID(file))
	}
	for index := range result.Evidence.Edges {
		result.Evidence.Edges[index].From = sourceID(result.Evidence.Edges[index].From)
		result.Evidence.Edges[index].To = sourceID(result.Evidence.Edges[index].To)
	}
	for index := range result.Evidence.Effects {
		result.Evidence.Effects[index].Owner = sourceID(result.Evidence.Effects[index].Owner)
		if influence.Validate(result.Evidence.Effects[index]) != nil {
			kind := result.Evidence.Effects[index].Kind
			result.Evidence.Effects[index].Kind, result.Evidence.Effects[index].Path = "unknown", ""
			result.Evidence.Gaps = append(result.Evidence.Gaps, model.Gap{Code: "unsupported-python-effect-" + kind, Workspace: w.ID, Detail: "Effect category cannot assert a bounded common influence contract"})
		}
	}
	for index, entry := range collected.Items {
		relative, _ := filepath.Rel(root, entry.Path)
		result.Evidence.Edges = append(result.Evidence.Edges, model.Edge{From: sourceID(filepath.ToSlash(relative)), To: result.Inventory.Units[index].ID, Reason: "collected-python-owner"})
	}
	for _, gap := range syntax.Gaps {
		gap.Workspace = w.ID
		if gap.Detail == "" {
			gap.Detail = "Unresolved Python startup/import context outside captured source"
		}
		result.Evidence.Gaps = append(result.Evidence.Gaps, gap)
	}
	for _, code := range []string{"runtime-unqualified", "installed-package-plugin-stdlib-native-unqualified", "collection-side-effects", "collection-context-modified", "static-import-resolution-unqualified", "source-drift-observational-only"} {
		result.Evidence.Gaps = append(result.Evidence.Gaps, model.Gap{Code: code, Workspace: w.ID, Detail: "Descriptive Python collection/source evidence cannot establish a complete enforced influence boundary"})
	}
	if len(w.Prerequisites) != 0 {
		result.Evidence.Gaps = append(result.Evidence.Gaps, model.Gap{Code: "prerequisite-provenance-unqualified", Workspace: w.ID, Detail: "Configured prerequisite production/reset semantics are not established by native collection"})
	}
	after, err := pythonHelper(ctx, root, dir, w, env, "identity", nil)
	if err != nil || after.RuntimeDigest != before.RuntimeDigest || after.PythonVersion != before.PythonVersion || after.RunnerVersion != before.RunnerVersion {
		return PythonClosure{}, errors.New("installed Python runtime or runner identity drifted")
	}
	if err := guard(); err != nil {
		return PythonClosure{}, err
	}
	sort.Slice(result.Evidence.Gaps, func(i, j int) bool {
		return result.Evidence.Gaps[i].Code+result.Evidence.Gaps[i].Detail < result.Evidence.Gaps[j].Code+result.Evidence.Gaps[j].Detail
	})
	result.workspace, result.execution, result.environment, result.runtimeDigest = w, frozen, append([]string(nil), env...), before.RuntimeDigest
	result.sourceRoot, result.sourceDir = root, dir
	result.Digest = pythonDigest(result)
	result.expected = result.Digest
	return result, nil
}

// RevalidatePythonClosure checks immutable metadata/source/runtime identities,
// including removal of exported inventory/source entries. It does not recollect
// project hooks or upgrade descriptive data into reusable execution authority.
func RevalidatePythonClosure(ctx context.Context, root string, closure PythonClosure) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	want := closure.Digest
	closure.Digest = ""
	if want == "" || closure.expected != want || pythonDigest(closure) != want {
		return errors.New("Python closure metadata changed")
	}
	root, dir, err := locations(root, closure.workspace, closure.execution)
	if err != nil || root != closure.sourceRoot || dir != closure.sourceDir {
		return errors.New("Python closure source context unavailable")
	}
	current, err := snapshot.Digest(ctx, root)
	if err != nil || current != closure.SourceDigest || !reflect.DeepEqual(process.Environment(closure.execution.Env), closure.environment) {
		return errors.New("Python closure source or effective environment changed")
	}
	identity, err := pythonHelper(ctx, root, dir, closure.workspace, closure.environment, "identity", nil)
	if err != nil || identity.RuntimeDigest != closure.runtimeDigest || identity.PythonVersion != closure.PythonVersion || identity.RunnerVersion != closure.RunnerVersion {
		return errors.New("Python closure runtime identity changed")
	}
	current, err = snapshot.Digest(ctx, root)
	if err != nil || current != closure.SourceDigest || !reflect.DeepEqual(process.Environment(closure.execution.Env), closure.environment) {
		return errors.New("Python closure source drifted during revalidation")
	}
	return nil
}
