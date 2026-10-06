// Package cmake collects observed build and runner inventories. File API data
// describes a configured build; it does not prove runtime independence.
package cmake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Cyberlane/hayaku/internal/adapter/buildclosure"
	"github.com/Cyberlane/hayaku/internal/model"
	native "github.com/Cyberlane/hayaku/internal/runner/native"
)

const replyDir = ".cmake/api/v1/reply/"

var objectVersions = map[string]int{"codemodel": 2, "cmakeFiles": 1, "cache": 2, "toolchains": 1}

type reference struct {
	Kind     string                     `json:"kind"`
	Version  struct{ Major, Minor int } `json:"version"`
	JSONFile string                     `json:"jsonFile"`
}

type apiIndex struct {
	CMake struct {
		Version struct {
			Major, Minor, Patch int
			String              string
			IsDirty             bool
		} `json:"version"`
		Generator struct {
			Name        string
			MultiConfig bool
		} `json:"generator"`
	} `json:"cmake"`
	Objects []reference                     `json:"objects"`
	Reply   map[string]map[string]reference `json:"reply"`
}

type paths struct{ Source, Build string }
type apiModel struct {
	reference
	Paths          paths `json:"paths"`
	Configurations []struct {
		Name        string                                     `json:"name"`
		Targets     []struct{ Name, ID, JSONFile string }      `json:"targets"`
		Directories []struct{ Source, Build, JSONFile string } `json:"directories"`
	} `json:"configurations"`
}

type apiTarget struct {
	Name, ID, Type string
	Paths          paths
	Artifacts      []struct{ Path string }
	Dependencies   []struct{ ID string }
	Sources        []struct {
		Path        string
		IsGenerated bool
	}
	CompileGroups []struct {
		Language string
		Includes []struct {
			Path     string
			IsSystem bool
		}
		Defines                 []struct{ Define string }
		CompileCommandFragments []struct{ Fragment string }
	}
	Link struct {
		CommandFragments []struct{ Fragment, Role string }
	}
}

// Target reports logical source/build ownership and opaque settings identities.
// Physical SDK paths, source contents and definition values stay private.
type Target struct {
	ID, Name, Kind                              string
	Sources, Generated, Artifacts, Dependencies []string
	SettingsDigest                              string
}

type FileAPI struct {
	Version, Generator, Configuration, Digest string
	Targets                                   []Target
	HeaderInputs                              []string
	Gaps                                      []model.Gap
	compilers                                 map[string]string
}

// Ninja retains dependencies after removing compiler .d files. Its bounded
// native deps report is an observation, not proof of complete header influence.
func ninjaHeaders(data []byte, source, build string) ([]string, error) {
	if len(data) > native.MaxResultBytes {
		return nil, errors.New("Ninja dependencies exceed bounds")
	}
	var result []string
	seen := map[string]bool{}
	remaining := -1
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "    ") {
			if remaining <= 0 {
				return nil, errors.New("unexpected Ninja header record")
			}
			remaining--
			value := strings.TrimPrefix(line, "    ")
			if !identity(value) {
				return nil, errors.New("invalid Ninja header path")
			}
			if !filepath.IsAbs(value) {
				value = filepath.Join(build, filepath.FromSlash(value))
			}
			name, ok := logical(source, build, value)
			if !ok {
				name = "external:" + digest(value)
			}
			if !seen[name] {
				if len(result) >= 50000 {
					return nil, errors.New("Ninja dependency count exceeds bounds")
				}
				seen[name] = true
				result = append(result, name)
			}
			continue
		}
		if remaining > 0 {
			return nil, errors.New("truncated Ninja header group")
		}
		owner, tail, ok := strings.Cut(line, ": #deps ")
		if !ok || !identity(owner) || !strings.HasSuffix(tail, " (VALID)") {
			return nil, errors.New("unsupported or stale Ninja dependency group")
		}
		count, _, ok := strings.Cut(tail, ", deps mtime ")
		if !ok {
			return nil, errors.New("unsupported Ninja dependency header")
		}
		n, err := strconv.Atoi(count)
		if err != nil || n < 0 || n > 50000 {
			return nil, errors.New("invalid Ninja dependency group size")
		}
		remaining = n
	}
	if remaining > 0 {
		return nil, errors.New("truncated final Ninja header group")
	}
	sort.Strings(result)
	return result, nil
}

func queryFiles() map[string][]byte {
	files := map[string][]byte{}
	for kind, major := range objectVersions {
		files[".cmake/api/v1/query/client-hayaku/"+kind+"-v"+string(rune('0'+major))] = []byte{}
	}
	return files
}

// decode permits native extension fields while retaining shared duplicate,
// nesting, size and trailing-data checks before projecting the known schema.
func decode(data []byte, destination any) error {
	var raw map[string]json.RawMessage
	if err := native.DecodeJSON(data, &raw); err != nil {
		return err
	}
	if raw == nil {
		return errors.New("CMake metadata is not an object")
	}
	return json.Unmarshal(data, destination)
}

func identity(value string) bool {
	return value != "" && len(value) <= 4096 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func replyName(name string) (string, error) {
	if !identity(name) || path.Base(name) != name || name == "." || !strings.HasSuffix(name, ".json") || strings.Contains(name, "\\") {
		return "", errors.New("invalid CMake reply reference")
	}
	return replyDir + name, nil
}

func digest(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func readReplies(ctx context.Context, session *buildclosure.Session, names []string) (map[string][]byte, error) {
	data, err := session.ReadFiles(ctx, "build", names, native.MaxResultBytes)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]byte, len(names))
	for i, name := range names {
		result[name] = data[i]
	}
	return result, nil
}

func logical(source, build, value string) (string, bool) {
	if !identity(value) {
		return "", false
	}
	if !filepath.IsAbs(value) {
		clean := filepath.ToSlash(filepath.Clean(value))
		if clean == "." || clean != value || strings.HasPrefix(clean, "../") {
			return "", false
		}
		return "source/" + clean, true
	}
	for _, root := range []struct{ name, path string }{{"source", source}, {"build", build}} {
		rel, err := filepath.Rel(root.path, value)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return root.name + "/" + filepath.ToSlash(rel), true
		}
	}
	return "", false
}

// CollectFileAPI follows only the current index and its referenced objects from
// the session's sealed build generation. Unreferenced JSON is never evidence.
func CollectFileAPI(ctx context.Context, session *buildclosure.Session) (FileAPI, error) {
	manifest, err := session.Manifest(ctx, "build")
	if err != nil {
		return FileAPI{}, err
	}
	var indices []string
	for _, entry := range manifest.Entries {
		name := strings.TrimPrefix(entry.Path, "build/")
		if !strings.HasPrefix(name, replyDir) || strings.Contains(strings.TrimPrefix(name, replyDir), "/") {
			continue
		}
		base := path.Base(name)
		if strings.HasPrefix(base, "error-") && strings.HasSuffix(base, ".json") {
			return FileAPI{}, errors.New("CMake error index present")
		}
		if strings.HasPrefix(base, "index-") && strings.HasSuffix(base, ".json") {
			if entry.Kind != "file" {
				return FileAPI{}, errors.New("CMake index is not regular metadata")
			}
			indices = append(indices, name)
		}
	}
	if len(indices) == 0 || len(indices) > 128 {
		return FileAPI{}, errors.New("missing or excessive CMake indices")
	}
	sort.Strings(indices)
	current, err := session.ReadFile(ctx, "build", indices[len(indices)-1], native.MaxResultBytes)
	if err != nil {
		return FileAPI{}, err
	}
	var index apiIndex
	if err = decode(current, &index); err != nil {
		return FileAPI{}, err
	}
	if index.CMake.Version.Major != 4 || index.CMake.Version.Minor != 4 || index.CMake.Version.Patch != 3 || index.CMake.Version.String != "4.4.3" || index.CMake.Version.IsDirty || index.CMake.Generator.MultiConfig || index.CMake.Generator.Name != "Ninja" || len(index.Objects) > 64 {
		return FileAPI{}, errors.New("unadmitted CMake version or generator")
	}
	client := index.Reply["client-hayaku"]
	if len(client) != 4 {
		return FileAPI{}, errors.New("incomplete CMake client replies")
	}
	objects := map[string]reference{}
	for _, ref := range index.Objects {
		if _, err = replyName(ref.JSONFile); err != nil {
			return FileAPI{}, err
		}
		if _, exists := objects[ref.JSONFile]; exists {
			return FileAPI{}, errors.New("duplicate CMake object reference")
		}
		objects[ref.JSONFile] = ref
	}
	var names []string
	byKind := map[string]string{}
	for kind, major := range objectVersions {
		ref, ok := client[kind+"-v"+string(rune('0'+major))]
		if !ok || ref.Kind != kind || ref.Version.Major != major || ref.Version.Minor < 0 || objects[ref.JSONFile] != ref {
			return FileAPI{}, errors.New("inconsistent CMake client reference")
		}
		name, err := replyName(ref.JSONFile)
		if err != nil {
			return FileAPI{}, err
		}
		names = append(names, name)
		byKind[kind] = name
	}
	sort.Strings(names)
	data, err := readReplies(ctx, session, names)
	if err != nil {
		return FileAPI{}, err
	}
	for kind, name := range byKind {
		var header reference
		if err = decode(data[name], &header); err != nil {
			return FileAPI{}, err
		}
		if header.Kind != kind || header.Version != client[kind+"-v"+string(rune('0'+objectVersions[kind]))].Version {
			return FileAPI{}, errors.New("CMake object kind/version changed")
		}
	}
	var codemodel apiModel
	if err = decode(data[byKind["codemodel"]], &codemodel); err != nil {
		return FileAPI{}, err
	}
	if codemodel.Paths.Source != session.Source() || codemodel.Paths.Build != session.Build() || len(codemodel.Configurations) != 1 {
		return FileAPI{}, errors.New("inconsistent CMake roots or configurations")
	}
	var modules struct {
		Paths  paths
		Inputs []struct {
			Path                             string
			IsGenerated, IsExternal, IsCMake bool
		}
	}
	if err = decode(data[byKind["cmakeFiles"]], &modules); err != nil {
		return FileAPI{}, err
	}
	if modules.Paths != codemodel.Paths || len(modules.Inputs) == 0 || len(modules.Inputs) > 50000 {
		return FileAPI{}, errors.New("invalid CMake module inputs")
	}
	for _, input := range modules.Inputs {
		if !identity(input.Path) {
			return FileAPI{}, errors.New("invalid CMake module input path")
		}
	}
	var cache struct {
		Entries []struct{ Name, Type, Value string }
	}
	if err = decode(data[byKind["cache"]], &cache); err != nil {
		return FileAPI{}, err
	}
	if len(cache.Entries) == 0 || len(cache.Entries) > 50000 {
		return FileAPI{}, errors.New("invalid CMake cache entries")
	}
	cacheNames := map[string]bool{}
	for _, entry := range cache.Entries {
		if !identity(entry.Name) || !identity(entry.Type) || cacheNames[entry.Name] {
			return FileAPI{}, errors.New("invalid or duplicate CMake cache entry")
		}
		cacheNames[entry.Name] = true
	}
	var toolchains struct {
		Toolchains []struct {
			Language string
			Compiler struct{ Path, ID, Version string }
		}
	}
	if err = decode(data[byKind["toolchains"]], &toolchains); err != nil {
		return FileAPI{}, err
	}
	if len(toolchains.Toolchains) == 0 || len(toolchains.Toolchains) > 64 {
		return FileAPI{}, errors.New("invalid CMake toolchain count")
	}
	languages := map[string]bool{}
	for _, toolchain := range toolchains.Toolchains {
		if !identity(toolchain.Language) || languages[toolchain.Language] {
			return FileAPI{}, errors.New("invalid or duplicate CMake toolchain language")
		}
		if !identity(toolchain.Compiler.Path) || !filepath.IsAbs(toolchain.Compiler.Path) {
			return FileAPI{}, errors.New("CMake compiler path missing or nonabsolute")
		}
		if (toolchain.Compiler.ID != "" && !identity(toolchain.Compiler.ID)) || (toolchain.Compiler.Version != "" && !identity(toolchain.Compiler.Version)) {
			return FileAPI{}, errors.New("invalid CMake compiler description")
		}
		languages[toolchain.Language] = true
	}
	config := codemodel.Configurations[0]
	if len(config.Targets) == 0 || len(config.Targets) > 50000 || len(config.Directories) > 50000 {
		return FileAPI{}, errors.New("invalid CMake target/directory count")
	}
	names = nil
	ids := map[string]bool{}
	for _, target := range config.Targets {
		if !identity(target.ID) || !identity(target.Name) || ids[target.ID] {
			return FileAPI{}, errors.New("invalid or duplicate CMake target identity")
		}
		ids[target.ID] = true
		name, err := replyName(target.JSONFile)
		if err != nil {
			return FileAPI{}, err
		}
		names = append(names, name)
	}
	for _, directory := range config.Directories {
		if directory.JSONFile == "" {
			continue
		}
		name, err := replyName(directory.JSONFile)
		if err != nil {
			return FileAPI{}, err
		}
		names = append(names, name)
	}
	sort.Strings(names)
	targetData, err := readReplies(ctx, session, names)
	if err != nil {
		return FileAPI{}, err
	}
	result := FileAPI{Version: "cmake-file-api-v1", Generator: "Ninja", Configuration: config.Name, compilers: map[string]string{}, Gaps: []model.Gap{
		{Code: "cmake-observed-build-only", Detail: "Configured metadata does not close compiler helpers, implicit SDK inputs, linked libraries or native runtime effects."},
	}}
	for _, toolchain := range toolchains.Toolchains {
		result.compilers[toolchain.Language] = toolchain.Compiler.Path
		if toolchain.Compiler.ID == "" || toolchain.Compiler.Version == "" {
			result.Gaps = append(result.Gaps, model.Gap{Code: "cmake-compiler-description-incomplete", Detail: "Optional compiler ID/version metadata is absent; explicit compiler bytes and path admission remain required."})
		}
	}
	for _, directory := range config.Directories {
		if directory.JSONFile == "" {
			continue
		}
		var entry struct{ Paths paths }
		if err = decode(targetData[replyDir+directory.JSONFile], &entry); err != nil {
			return FileAPI{}, err
		}
		if entry.Paths.Source != directory.Source || entry.Paths.Build != directory.Build {
			return FileAPI{}, errors.New("inconsistent CMake directory reference")
		}
	}
	for _, ref := range config.Targets {
		var target apiTarget
		if err = decode(targetData[replyDir+ref.JSONFile], &target); err != nil {
			return FileAPI{}, err
		}
		if target.ID != ref.ID || target.Name != ref.Name || !identity(target.Type) || len(target.Sources) > 50000 || len(target.Dependencies) > 50000 || len(target.Artifacts) > 1024 || len(target.CompileGroups) > 1024 {
			return FileAPI{}, errors.New("invalid CMake target metadata")
		}
		projected := Target{ID: target.ID, Name: target.Name, Kind: target.Type, SettingsDigest: digest(targetData[replyDir+ref.JSONFile])}
		for _, dep := range target.Dependencies {
			if !ids[dep.ID] {
				return FileAPI{}, errors.New("unresolved CMake target dependency")
			}
			projected.Dependencies = append(projected.Dependencies, dep.ID)
		}
		for _, source := range target.Sources {
			name, ok := logical(session.Source(), session.Build(), source.Path)
			if !ok {
				result.Gaps = append(result.Gaps, model.Gap{Code: "cmake-external-source", Detail: "A target source lies outside the private source/build view."})
				continue
			}
			if source.IsGenerated {
				projected.Generated = append(projected.Generated, name)
			} else {
				projected.Sources = append(projected.Sources, name)
			}
		}
		for _, artifact := range target.Artifacts {
			value := artifact.Path
			if !filepath.IsAbs(value) {
				value = filepath.Join(session.Build(), filepath.FromSlash(value))
			}
			name, ok := logical(session.Source(), session.Build(), value)
			if !ok || !strings.HasPrefix(name, "build/") {
				return FileAPI{}, errors.New("CMake artifact escapes build view")
			}
			projected.Artifacts = append(projected.Artifacts, strings.TrimPrefix(name, "build/"))
		}
		result.Targets = append(result.Targets, projected)
	}
	// Bind complete private native objects, including fields not projected above.
	_, binding, err := session.Records(ctx)
	if err != nil {
		return FileAPI{}, err
	}
	result.Digest = digest(struct {
		Session          string
		Index            []byte
		Objects, Targets map[string][]byte
	}{binding, current, data, targetData})
	if err = session.Verify(ctx); err != nil {
		return FileAPI{}, err
	}
	return result, nil
}
