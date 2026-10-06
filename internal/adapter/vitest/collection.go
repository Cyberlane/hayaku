package vitest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/noderuntime"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

// Collection is independent case evidence, never native qualification. Loading
// tests executes trusted user setup/imports; runtime effects remain explicit gaps.
type Collection struct {
	SourceDigest  string                   `json:"source_digest"`
	ContextDigest string                   `json:"context_digest"`
	Evidence      model.Evidence           `json:"evidence"`
	Inventory     model.ExecutionInventory `json:"inventory"`
}

type collectionFile struct {
	File    string `json:"file"`
	Project string `json:"project"`
}
type collectionCase struct {
	File    string `json:"file"`
	Project string `json:"project"`
	Test    string `json:"test"`
}
type collectedInventory struct {
	Schema      int              `json:"schema"`
	Version     string           `json:"version"`
	ViteVersion string           `json:"viteVersion"`
	Files       []collectionFile `json:"files"`
	Cases       []collectionCase `json:"cases"`
}

// Collect resolves the configured graph, then collects actual cases separately
// from test execution. Callers must use disposable trusted snapshots; this is
// not a sandbox, and unchanged before/after bytes do not establish isolation.
func Collect(ctx context.Context, root string, w model.Workspace, c model.Context) (Collection, error) {
	var result Collection
	if w.NodeRuntime == nil {
		return result, errors.New("independent Vitest collection requires bound runtime")
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return result, errors.New("Vitest collection root unavailable")
	}
	before, contextBefore, err := collectionIdentity(ctx, root, w, c)
	if err != nil {
		return result, err
	}
	result.Evidence, err = Discover(ctx, root, w, c)
	if err != nil {
		return Collection{}, err
	}
	out, err := bridge(ctx, root, w, c, "collect", nil)
	if err != nil {
		return Collection{}, errors.New("independent Vitest collection failed")
	}
	result.Inventory, err = decodeCollection(out, w.ID, result.Evidence)
	if err != nil {
		return Collection{}, err
	}
	after, contextAfter, err := collectionIdentity(ctx, root, w, c)
	if err != nil || before != after || contextBefore != contextAfter {
		return Collection{}, errors.New("Vitest source changed during collection")
	}
	result.SourceDigest, result.ContextDigest = before, contextBefore
	result.Evidence.Gaps = append(result.Evidence.Gaps, model.Gap{Code: "collection-effects-unqualified", Workspace: w.ID, Detail: "Native collection executes imports and setup; complete case evidence does not establish an enforced runtime influence boundary"})
	return result, nil
}

func collectionIdentity(ctx context.Context, root string, w model.Workspace, c model.Context) (string, string, error) {
	runtime := *w.NodeRuntime
	local := filepath.Join(root, w.Root, w.Command.Dir, "node_modules")
	if info, err := os.Stat(filepath.Join(local, "vitest", "package.json")); err == nil && info.Mode().IsRegular() {
		runtime.Modules = local
	}
	var excluded []string
	if relative, err := filepath.Rel(root, runtime.Modules); err == nil && relative != "." && filepath.IsLocal(relative) {
		excluded = []string{filepath.ToSlash(relative)}
	}
	tools, err := noderuntime.Identity(ctx, runtime)
	if err != nil {
		return "", "", err
	}
	source, err := snapshot.DigestWithModules(ctx, root, excluded)
	if err != nil {
		return "", "", err
	}
	contextDigest := config.Digest(struct {
		Root, Bridge string
		Workspace    model.Workspace
		Context      model.Context
		Tools        noderuntime.Fingerprint
		Environment  []string
	}{root, nativeBridge, w, c, tools, process.Environment(c.Env)})
	return source, contextDigest, nil
}

func decodeCollection(out process.Output, workspace string, evidence model.Evidence) (model.ExecutionInventory, error) {
	inventory := model.ExecutionInventory{Schema: model.ExecutionSchema, Units: evidence.Units}
	if !out.Completed || out.ExitCode != 0 || len(out.Stdout) > process.MaxOutput {
		return inventory, errors.New("Vitest collection process incomplete or failed")
	}
	var data collectedInventory
	if config.DecodeValue(bytes.NewReader(out.Stdout), &data, process.MaxOutput) != nil || data.Schema != 1 || !supportedCollectionVersions(data.Version, data.ViteVersion) || evidence.Version != "vitest-vite-graph-v1:"+data.Version+":vite:"+data.ViteVersion || len(data.Files) != len(evidence.Units) || len(data.Cases) > 100000 {
		return inventory, errors.New("invalid configured Vitest case inventory")
	}
	want := map[string]bool{}
	for _, unit := range evidence.Units {
		if unit.Workspace != workspace || unit.Kind != "vitest-file" || want[unit.ID] {
			return inventory, errors.New("invalid configured Vitest file ownership")
		}
		want[unit.ID] = true
	}
	fileID := func(file, project string) (string, error) {
		if !validRelative(file) || strings.ContainsAny(file+project, ":\x00\r\n") {
			return "", errors.New("invalid Vitest collection path/project")
		}
		id := workspace + ":" + project + ":" + file
		if !want[id] {
			return "", errors.New("collected Vitest file absent from independent graph")
		}
		return id, nil
	}
	seen := map[string]bool{}
	for _, item := range data.Files {
		id, err := fileID(item.File, item.Project)
		if err != nil || seen[id] {
			return inventory, errors.New("duplicate or unexpected collected Vitest file")
		}
		seen[id] = true
	}
	counts := map[[2]string]int{}
	for _, item := range data.Cases {
		id, err := fileID(item.File, item.Project)
		if err != nil {
			return inventory, err
		}
		var identity []json.RawMessage
		var name string
		var occurrence int
		if json.Unmarshal([]byte(item.Test), &identity) != nil || len(identity) != 2 || json.Unmarshal(identity[0], &name) != nil || name == "" || json.Unmarshal(identity[1], &occurrence) != nil || occurrence != counts[[2]string{id, name}] {
			return inventory, errors.New("invalid Vitest case identity or occurrence")
		}
		counts[[2]string{id, name}]++
		inventory.Cases = append(inventory.Cases, model.Case{Unit: id, ID: item.Test})
	}
	if err := runner.ValidateInventory(inventory); err != nil {
		return inventory, err
	}
	sort.Slice(inventory.Cases, func(i, j int) bool {
		if inventory.Cases[i].Unit != inventory.Cases[j].Unit {
			return inventory.Cases[i].Unit < inventory.Cases[j].Unit
		}
		return inventory.Cases[i].ID < inventory.Cases[j].ID
	})
	return inventory, nil
}

// Collection compatibility is independently accepted from discovery/execution.
func supportedCollectionVersions(vitest, vite string) bool {
	return vitest == "4.1.11" && vite == "8.2.2"
}
