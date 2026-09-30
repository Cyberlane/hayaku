// Package pytest collects whole-file units with the installed native pytest.
// Dynamic imports, plugins, fixtures and runtime inputs remain unqualified.
package pytest

import (
	"bytes"
	"context"
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

	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

const maxCollectionBytes = 8 << 20

// The JSON file avoids interpreting arbitrary plugin console output as node
// IDs. Collection still executes trusted project Python and plugin code.
const collectionBridge = `import json, pathlib, sys
import pytest
class Collector:
    def __init__(self):
        self.items = []
    def pytest_collection_finish(self, session):
        self.items = [{"nodeid": item.nodeid, "path": str(item.path)} for item in session.items]
collector = Collector()
args = json.loads(sys.argv[2])
code = pytest.main(args + ["-p", "no:cacheprovider", "--collect-only", "-q"], plugins=[collector])
payload = {"schema": 1, "pytest_version": pytest.__version__, "python_version": sys.version.split()[0], "exit_code": int(code), "items": collector.items}
pathlib.Path(sys.argv[1]).write_text(json.dumps(payload), encoding="utf-8")
sys.exit(int(code))
`

type collection struct {
	Schema        int    `json:"schema"`
	PytestVersion string `json:"pytest_version"`
	PythonVersion string `json:"python_version"`
	ExitCode      int    `json:"exit_code"`
	Items         []struct {
		NodeID string `json:"nodeid"`
		Path   string `json:"path"`
	} `json:"items"`
}

func Discover(ctx context.Context, root string, w model.Workspace, execution model.Context) (model.Evidence, error) {
	e := model.Evidence{Adapter: "pytest", Version: "pytest-native-v1", Workspace: w.ID, Inputs: map[string][]string{}, Nodes: []string{}, Edges: []model.Edge{}, Units: []model.Unit{}, Gaps: []model.Gap{
		{Code: "runtime-unqualified", Workspace: w.ID, Detail: "Python imports, plugins, fixtures, services, file reads and subprocesses have no enforced complete influence model"},
		{Code: "python-dependency-unqualified", Workspace: w.ID, Detail: "Every regular workspace input influences every collected file; no Python import narrowing is claimed"},
		{Code: "collection-side-effects", Workspace: w.ID, Detail: "Native pytest collection imports trusted project and plugin code; collection is not a sandbox or a network isolation boundary"},
		{Code: "collection-context-modified", Workspace: w.ID, Detail: "Bytecode writes and pytest cacheprovider are disabled for immutable collection only; original execution flags are retained"},
	}}
	_, _, err := commandParts(w.Command)
	if err != nil {
		return e, err
	}
	if execution.OS != "" && execution.OS != runtime.GOOS || execution.Arch != "" && execution.Arch != runtime.GOARCH {
		return e, errors.New("pytest collection requires the native host context")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return e, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return e, err
	}
	dir := filepath.Join(root, filepath.FromSlash(w.Root), filepath.FromSlash(w.Command.Dir))
	if !within(root, dir) {
		return e, errors.New("pytest working directory escapes snapshot")
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return e, err
	}
	if !within(root, dir) {
		return e, errors.New("pytest working directory escapes snapshot")
	}
	temporary, err := os.MkdirTemp("", "hayaku-pytest-collection-")
	if err != nil {
		return e, err
	}
	defer os.RemoveAll(temporary)
	outputPath := filepath.Join(temporary, "collection.json")
	args, err := json.Marshal(w.Command.Args[2:])
	if err != nil {
		return e, err
	}
	env := map[string]string{}
	for key, value := range execution.Env {
		env[key] = value
	}
	// These prevent interpreter bytecode and installer/version-check activity.
	// They do not prevent network calls made by project code or plugins.
	env["PYTHONDONTWRITEBYTECODE"] = "1"
	env["PIP_NO_INDEX"] = "1"
	env["PIP_DISABLE_PIP_VERSION_CHECK"] = "1"
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_, err = process.Run(ctx, model.Command{Executable: w.Command.Executable, Args: []string{"-c", collectionBridge, outputPath, string(args)}}, dir, env)
	if err != nil {
		if ctx.Err() != nil {
			return e, ctx.Err()
		}
		return e, errors.New("native pytest collection failed; diagnostic output withheld")
	}
	metadata, err := os.Lstat(outputPath)
	if err != nil || !metadata.Mode().IsRegular() {
		return e, errors.New("pytest metadata is not a regular file")
	}
	file, err := os.Open(outputPath)
	if err != nil {
		return e, errors.New("pytest collection did not produce metadata")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxCollectionBytes+1))
	if err != nil {
		return e, err
	}
	if len(data) > maxCollectionBytes {
		return e, errors.New("pytest collection metadata exceeds limit")
	}
	collected, err := decodeCollection(data)
	if err != nil {
		return e, err
	}
	e.Version += ";pytest=" + collected.PytestVersion + ";python=" + collected.PythonVersion
	files := map[string]bool{}
	workspaceRoot := filepath.Join(root, filepath.FromSlash(w.Root))
	for _, item := range collected.Items {
		if item.NodeID == "" || strings.ContainsRune(item.NodeID, 0) || !filepath.IsAbs(item.Path) {
			return e, errors.New("invalid pytest item identity")
		}
		resolved, err := filepath.EvalSymlinks(item.Path)
		if err != nil {
			return e, errors.New("pytest item file unavailable")
		}
		if !within(workspaceRoot, resolved) {
			return e, errors.New("pytest item file is outside its workspace")
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.Mode().IsRegular() {
			return e, errors.New("pytest item is not a regular file")
		}
		rel, err := filepath.Rel(dir, resolved)
		if err != nil {
			return e, err
		}
		rel = filepath.ToSlash(rel)
		if !config.Relative(rel) || rel == "." || strings.HasPrefix(rel, "-") || strings.Contains(rel, "::") {
			return e, errors.New("pytest item cannot be represented by a whole-file selector")
		}
		files[rel] = true
	}
	selectors := []string{}
	for file := range files {
		selectors = append(selectors, file)
	}
	sort.Strings(selectors)
	for _, selector := range selectors {
		id := w.ID + ":pytest:" + selector
		e.Nodes = append(e.Nodes, id)
		e.Units = append(e.Units, model.Unit{ID: id, Workspace: w.ID, Selector: selector, Kind: "pytest-file"})
	}
	err = filepath.WalkDir(workspaceRoot, func(input string, entry fs.DirEntry, walkErr error) error {
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
			return errors.New("unsupported pytest snapshot input type")
		}
		if len(e.Inputs) >= 50000 {
			return errors.New("pytest workspace input limit exceeded")
		}
		if (len(e.Inputs)+1)*len(e.Nodes) > 1000000 {
			return errors.New("pytest ownership graph exceeds limit")
		}
		rel, err := filepath.Rel(root, input)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !config.Relative(rel) {
			return errors.New("unsafe pytest input path")
		}
		e.Inputs[rel] = append([]string(nil), e.Nodes...)
		return nil
	})
	return e, err
}

func decodeCollection(data []byte) (collection, error) {
	var result collection
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, errors.New("invalid pytest collection metadata")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return result, errors.New("trailing pytest collection metadata")
	}
	majorText, _, _ := strings.Cut(result.PytestVersion, ".")
	major, err := strconv.Atoi(majorText)
	if err != nil || major < 7 || major > 9 || result.PythonVersion == "" || result.Schema != 1 || result.ExitCode != 0 || len(result.Items) == 0 || len(result.Items) > 100000 {
		return result, errors.New("unsupported, incomplete or empty pytest collection metadata")
	}
	return result, nil
}

// Proposal rounds parameterized/class cases to whole files and retains every
// supported flag. It is a shadow command, never production omission authority.
func Proposal(w model.Workspace, units []model.Unit) (model.Command, error) {
	flags, _, err := commandParts(w.Command)
	if err != nil {
		return model.Command{}, err
	}
	selectors := map[string]bool{}
	for _, unit := range units {
		if unit.Workspace != w.ID || unit.Kind != "pytest-file" || !config.Relative(unit.Selector) || unit.Selector == "." || strings.HasPrefix(unit.Selector, "-") || strings.Contains(unit.Selector, "::") {
			return model.Command{}, errors.New("invalid pytest proposal unit")
		}
		selectors[unit.Selector] = true
	}
	if len(selectors) == 0 {
		return model.Command{}, errors.New("empty pytest proposal")
	}
	files := []string{}
	for file := range selectors {
		files = append(files, file)
	}
	sort.Strings(files)
	args := append([]string{"-m", "pytest"}, flags...)
	args = append(args, files...)
	return model.Command{Dir: w.Command.Dir, Executable: w.Command.Executable, Args: args}, nil
}

func commandParts(command model.Command) (flags, selectors []string, err error) {
	name := filepath.Base(command.Executable)
	if name != "python" && name != "python3" && !strings.HasPrefix(name, "python3.") {
		return nil, nil, errors.New("pytest native discovery requires a Python interpreter command")
	}
	if len(command.Args) < 2 || command.Args[0] != "-m" || command.Args[1] != "pytest" {
		return nil, nil, errors.New("pytest native discovery requires python -m pytest")
	}
	boolean := map[string]bool{"-q": true, "-v": true, "-x": true, "-s": true, "--quiet": true, "--verbose": true, "--exitfirst": true, "--disable-warnings": true, "--strict-config": true, "--strict-markers": true, "--doctest-modules": true}
	valued := map[string]bool{"-k": true, "-m": true, "-c": true, "-o": true, "-p": true, "--maxfail": true, "--capture": true, "--tb": true, "--import-mode": true, "--confcutdir": true, "--rootdir": true, "--override-ini": true, "--ignore": true, "--ignore-glob": true, "--deselect": true, "--doctest-glob": true}
	for i := 2; i < len(command.Args); i++ {
		arg := command.Args[i]
		if !strings.HasPrefix(arg, "-") {
			if arg == "" || strings.ContainsRune(arg, 0) {
				return nil, nil, errors.New("invalid pytest selector")
			}
			selectors = append(selectors, arg)
			continue
		}
		key, _, hasValue := strings.Cut(arg, "=")
		if boolean[key] {
			if hasValue {
				return nil, nil, errors.New("boolean pytest flag has a value")
			}
			flags = append(flags, arg)
			continue
		}
		if !valued[key] {
			return nil, nil, fmt.Errorf("unsupported pytest flag %q", key)
		}
		flags = append(flags, arg)
		if !hasValue {
			i++
			if i >= len(command.Args) {
				return nil, nil, errors.New("missing pytest flag value")
			}
			flags = append(flags, command.Args[i])
		}
	}
	return flags, selectors, nil
}

func within(root, input string) bool {
	rel, err := filepath.Rel(root, input)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
