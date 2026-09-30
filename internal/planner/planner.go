// Package planner separates experimental graph proposals from required runs.
package planner

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Cyberlane/hayaku/internal/graph"
	"github.com/Cyberlane/hayaku/internal/model"
)

type inventory struct {
	nodes      map[string]string
	units      map[string]model.Unit
	inputs     map[string][]string
	workspaces map[string]bool
	edges      []model.Edge
	gaps       []model.Gap
}

// Build never authorizes omitted tests. Proposed is an experimental, possibly
// incomplete graph envelope; Selected and Commands retain every original run.
func Build(base, candidate []model.Evidence, changes []model.Change, config model.Config) (model.Plan, error) {
	plan := model.Plan{Schema: model.Schema, Version: "dev", Mode: "full-fallback", Proposed: []model.Unit{}, Selected: []model.Unit{}, Reasons: []model.Reason{}, Gaps: []model.Gap{}, Commands: []model.Command{}, ProposalCommands: []model.Command{}}
	workspaces, err := validateConfig(config)
	if err != nil {
		return plan, err
	}
	configuredAdapters := make(map[string]string)
	for _, workspace := range config.Workspaces {
		configuredAdapters[workspace.ID] = workspace.Adapter
	}
	for _, items := range [][]model.Evidence{base, candidate} {
		for _, item := range items {
			if adapter, exists := configuredAdapters[item.Workspace]; exists && item.Adapter != adapter {
				return plan, fmt.Errorf("workspace evidence adapter does not match configuration")
			}
		}
	}
	old, err := inspect(base, workspaces)
	if err != nil {
		return plan, fmt.Errorf("base evidence: %w", err)
	}
	current, err := inspect(candidate, workspaces)
	if err != nil {
		return plan, fmt.Errorf("candidate evidence: %w", err)
	}
	if len(current.units) == 0 {
		return plan, fmt.Errorf("candidate discovery contains no runnable units")
	}
	for id, before := range old.units {
		if after, ok := current.units[id]; ok && before.Workspace != after.Workspace {
			return plan, fmt.Errorf("unit %q changes workspace identity", id)
		}
	}
	nodes := make(map[string]string)
	for id, workspace := range old.nodes {
		nodes[id] = workspace
	}
	for id, workspace := range current.nodes {
		if owner, found := nodes[id]; found && owner != workspace {
			return plan, fmt.Errorf("node %q changes workspace identity", id)
		}
		nodes[id] = workspace
	}
	var nodeIDs []string
	for id := range nodes {
		nodeIDs = append(nodeIDs, id)
	}
	sort.Strings(nodeIDs)
	edges := append(append([]model.Edge(nil), old.edges...), current.edges...)
	influence, err := graph.New(nodeIDs, edges)
	if err != nil {
		return plan, err
	}
	inputs := make(map[string][]string)
	for input, owners := range old.inputs {
		inputs[input] = append(inputs[input], owners...)
	}
	for input, owners := range current.inputs {
		inputs[input] = append(inputs[input], owners...)
	}
	oldVersions := make(map[string]string)
	for _, item := range base {
		oldVersions[item.Workspace] = item.Version
	}
	for _, item := range candidate {
		if version, exists := oldVersions[item.Workspace]; exists && version != item.Version {
			plan.Gaps = append(plan.Gaps, model.Gap{Code: "adapter-version-change", Workspace: item.Workspace, Detail: "snapshot evidence uses different adapter versions"})
		}
	}
	for _, workspace := range config.Workspaces {
		for _, input := range workspace.Inputs {
			for id, owner := range nodes {
				if owner == workspace.ID {
					inputs[input] = append(inputs[input], id)
				}
			}
		}
		if !old.workspaces[workspace.ID] || !current.workspaces[workspace.ID] {
			plan.Gaps = append(plan.Gaps, model.Gap{Code: "missing-evidence", Workspace: workspace.ID, Detail: "both snapshot inventories are required for graph proposals"})
		}
		for _, command := range cloneCommands(workspace.Prerequisites) {
			command.Dir = filepath.Join(workspace.Root, command.Dir)
			plan.Commands = append(plan.Commands, command)
		}
		command := cloneCommand(workspace.Command)
		command.Dir = filepath.Join(workspace.Root, command.Dir)
		plan.Commands = append(plan.Commands, command)
	}
	for _, contract := range config.Contracts {
		for _, consumer := range contract.Consumers {
			if workspaces[consumer] {
				for id, owner := range nodes {
					if owner == consumer {
						inputs[contract.Input] = append(inputs[contract.Input], id)
					}
				}
			} else if influence.Has(consumer) {
				inputs[contract.Input] = append(inputs[contract.Input], consumer)
			} else {
				return plan, fmt.Errorf("contract references unknown consumer %q", consumer)
			}
		}
	}
	plan.Gaps = append(plan.Gaps, old.gaps...)
	plan.Gaps = append(plan.Gaps, current.gaps...)
	plan.Gaps = append(plan.Gaps, model.Gap{Code: "runtime-unqualified", Detail: "native dependency metadata does not establish complete runtime influence; original commands remain required"})
	plan.Changes = append([]model.Change{}, changes...)
	sort.Slice(plan.Changes, func(i, j int) bool {
		a, b := plan.Changes[i], plan.Changes[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.OldPath != b.OldPath {
			return a.OldPath < b.OldPath
		}
		return a.Status < b.Status
	})
	proposed := make(map[string]model.Reason)
	for id := range current.units {
		if _, exists := old.units[id]; !exists {
			proposed[id] = model.Reason{Unit: id, Code: "new-unit"}
		}
	}
	seeds := make(map[string]bool)
	seedInputs := make(map[string]string)
	for _, change := range plan.Changes {
		if !validInput(change.Path) || change.Status == "" || strings.ContainsRune(change.Status, 0) {
			return plan, fmt.Errorf("invalid change inventory")
		}
		if change.OldPath != "" && !validInput(change.OldPath) {
			return plan, fmt.Errorf("invalid old change path")
		}
		paths := []string{change.Path}
		if change.OldPath != "" {
			paths = append(paths, change.OldPath)
		}
		for _, input := range paths {
			owners, known := inputs[input]
			if !known || len(owners) == 0 {
				plan.Gaps = append(plan.Gaps, model.Gap{Code: "unknown-input", Detail: input})
				continue
			}
			for _, owner := range owners {
				seeds[owner] = true
				if prior, exists := seedInputs[owner]; !exists || input < prior {
					seedInputs[owner] = input
				}
			}
		}
	}
	var orderedSeeds []string
	for seed := range seeds {
		orderedSeeds = append(orderedSeeds, seed)
	}
	reached, err := influence.Closure(orderedSeeds)
	if err != nil {
		return plan, err
	}
	pathBudget := 1000000
	for _, id := range sortedUnitIDs(current.units) {
		if _, affected := reached[id]; affected {
			via := graph.Path(reached, id)
			pathBudget -= len(via)
			if pathBudget < 0 {
				return plan, fmt.Errorf("influence explanations exceed limit")
			}
			proposed[id] = model.Reason{Unit: id, Code: "graph-influence", Input: seedInputs[via[0]], Via: via}
		}
	}
	plan.Gaps = canonicalGaps(plan.Gaps)
	for _, gap := range plan.Gaps {
		if gap.Code == "runtime-unqualified" {
			continue
		}
		var scoped map[string]string
		if gap.Workspace != "" {
			var scopeSeeds []string
			for node, owner := range nodes {
				if owner == gap.Workspace {
					scopeSeeds = append(scopeSeeds, node)
				}
			}
			scoped, err = influence.Closure(scopeSeeds)
			if err != nil {
				return plan, err
			}
		}
		for id, unit := range current.units {
			_, dependent := scoped[id]
			if gap.Workspace == "" || gap.Workspace == unit.Workspace || dependent {
				if _, exists := proposed[id]; !exists {
					proposed[id] = model.Reason{Unit: id, Code: "gap:" + gap.Code}
				}
			}
		}
	}
	for _, id := range sortedUnitIDs(current.units) {
		unit := current.units[id]
		plan.Selected = append(plan.Selected, unit)
		plan.Reasons = append(plan.Reasons, model.Reason{Unit: id, Code: "full-fallback"})
		if reason, included := proposed[id]; included {
			plan.Proposed = append(plan.Proposed, unit)
			plan.Reasons = append(plan.Reasons, reason)
		} else {
			plan.Reasons = append(plan.Reasons, model.Reason{Unit: id, Code: "proposal-omitted-no-graph-influence"})
		}
	}
	plan.Evidence = canonicalEvidence(candidate)
	return plan, nil
}

func validateConfig(config model.Config) (map[string]bool, error) {
	if config.Schema != model.Schema || len(config.Workspaces) == 0 {
		return nil, fmt.Errorf("unsupported config schema or empty workspace inventory")
	}
	workspaces := make(map[string]bool)
	for _, workspace := range config.Workspaces {
		if workspace.ID == "" || workspaces[workspace.ID] {
			return nil, fmt.Errorf("empty or duplicate workspace identity")
		}
		workspaces[workspace.ID] = true
		for _, command := range append(append([]model.Command(nil), workspace.Prerequisites...), workspace.Command) {
			if command.Executable == "" || strings.ContainsRune(command.Executable, 0) || strings.ContainsRune(command.Dir, 0) {
				return nil, fmt.Errorf("invalid configured command")
			}
			for _, arg := range command.Args {
				if strings.ContainsRune(arg, 0) {
					return nil, fmt.Errorf("invalid command argument")
				}
			}
		}
		for _, input := range workspace.Inputs {
			if !validInput(input) {
				return nil, fmt.Errorf("invalid workspace input path")
			}
		}
	}
	for _, contract := range config.Contracts {
		if !validInput(contract.Input) || len(contract.Consumers) == 0 || contract.Reason == "" {
			return nil, fmt.Errorf("incomplete cross-language contract")
		}
	}
	return workspaces, nil
}

func inspect(evidence []model.Evidence, workspaces map[string]bool) (inventory, error) {
	result := inventory{nodes: map[string]string{}, units: map[string]model.Unit{}, inputs: map[string][]string{}, workspaces: map[string]bool{}}
	for _, item := range evidence {
		if !workspaces[item.Workspace] || result.workspaces[item.Workspace] || item.Adapter == "" || item.Version == "" {
			return result, fmt.Errorf("unknown, duplicate or unversioned workspace evidence")
		}
		result.workspaces[item.Workspace] = true
		for _, node := range item.Nodes {
			if node == "" {
				return result, fmt.Errorf("empty evidence node")
			}
			if _, found := result.nodes[node]; found {
				return result, fmt.Errorf("duplicate evidence node %q", node)
			}
			result.nodes[node] = item.Workspace
		}
		for _, unit := range item.Units {
			if unit.ID == "" || unit.Workspace != item.Workspace || unit.Selector == "" || unit.Kind == "" {
				return result, fmt.Errorf("incomplete or conflicting unit identity")
			}
			if _, found := result.units[unit.ID]; found {
				return result, fmt.Errorf("duplicate unit %q", unit.ID)
			}
			result.units[unit.ID] = unit
		}
		result.edges = append(result.edges, item.Edges...)
		result.gaps = append(result.gaps, item.Gaps...)
	}
	for _, item := range evidence {
		for input, owners := range item.Inputs {
			if !validInput(input) || len(owners) == 0 {
				return result, fmt.Errorf("invalid or ownerless evidence input")
			}
			for _, owner := range owners {
				if _, found := result.nodes[owner]; !found {
					return result, fmt.Errorf("input %q references unknown owner %q", input, owner)
				}
			}
			result.inputs[input] = append(result.inputs[input], owners...)
		}
	}
	for id, unit := range result.units {
		if result.nodes[id] != unit.Workspace {
			return result, fmt.Errorf("unit %q is absent from its workspace node inventory", id)
		}
	}
	for _, gap := range result.gaps {
		if gap.Code == "" || gap.Detail == "" || gap.Workspace != "" && !workspaces[gap.Workspace] {
			return result, fmt.Errorf("invalid uncertainty gap")
		}
	}
	var nodes []string
	for node := range result.nodes {
		nodes = append(nodes, node)
	}
	_, err := graph.New(nodes, result.edges)
	return result, err
}

func validInput(input string) bool {
	return input != "" && input != "." && !strings.ContainsRune(input, 0) && !path.IsAbs(input) && path.Clean(input) == input && input != ".." && !strings.HasPrefix(input, "../")
}
func sortedUnitIDs(units map[string]model.Unit) []string {
	var ids []string
	for id := range units {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
func cloneCommand(command model.Command) model.Command {
	command.Args = append([]string(nil), command.Args...)
	return command
}
func cloneCommands(commands []model.Command) []model.Command {
	result := make([]model.Command, len(commands))
	for i, command := range commands {
		result[i] = cloneCommand(command)
	}
	return result
}

func canonicalGaps(gaps []model.Gap) []model.Gap {
	sort.Slice(gaps, func(i, j int) bool {
		a, b := gaps[i], gaps[j]
		if a.Workspace != b.Workspace {
			return a.Workspace < b.Workspace
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Detail < b.Detail
	})
	result := []model.Gap{}
	for _, gap := range gaps {
		if len(result) == 0 || result[len(result)-1] != gap {
			result = append(result, gap)
		}
	}
	return result
}

func canonicalEvidence(evidence []model.Evidence) []model.Evidence {
	result := make([]model.Evidence, len(evidence))
	for i, item := range evidence {
		item.Nodes = append([]string{}, item.Nodes...)
		sort.Strings(item.Nodes)
		item.Units = append([]model.Unit{}, item.Units...)
		sort.Slice(item.Units, func(i, j int) bool { return item.Units[i].ID < item.Units[j].ID })
		item.Edges = append([]model.Edge{}, item.Edges...)
		sort.Slice(item.Edges, func(i, j int) bool {
			a, b := item.Edges[i], item.Edges[j]
			if a.From != b.From {
				return a.From < b.From
			}
			if a.To != b.To {
				return a.To < b.To
			}
			return a.Reason < b.Reason
		})
		item.Gaps = canonicalGaps(append([]model.Gap{}, item.Gaps...))
		inputs := make(map[string][]string, len(item.Inputs))
		for input, owners := range item.Inputs {
			copied := append([]string{}, owners...)
			sort.Strings(copied)
			inputs[input] = copied
		}
		item.Inputs = inputs
		result[i] = item
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Workspace < result[j].Workspace })
	return result
}
