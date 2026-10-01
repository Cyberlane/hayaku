package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"

	vitestprovider "github.com/Cyberlane/hayaku/internal/adapter/vitest"
	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/report"
)

// BroadenFromObservations adds runtime influence to the existing static envelope.
// An observation is never evidence that an unobserved dependency is absent.
func BroadenFromObservations(ctx context.Context, p *model.Plan, c model.Config, filename string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.Mode != "full-fallback" || config.Digest(c) != p.ConfigDigest {
		return errors.New("observations cannot authorize affected execution")
	}
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, report.MaxPlan+1))
	if err != nil || len(data) > report.MaxPlan {
		return errors.New("runtime observation report exceeds limit")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var observed Execution
	if err := d.Decode(&observed); err != nil {
		return errors.New("invalid runtime observation report")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing runtime observation data")
	}
	if observed.Schema != model.Schema || observed.Mode != "runtime-observation" || observed.Candidate != p.Base || observed.ContextDigest != p.ContextDigest || observed.ConfigDigest != p.ConfigDigest || observed.ToolsDigest != p.ToolsDigest {
		return errors.New("runtime observation identities differ from base plan")
	}
	selected := map[string]model.Unit{}
	proposed := map[string]bool{}
	for _, u := range p.Selected {
		selected[u.ID] = u
	}
	for _, u := range p.Proposed {
		proposed[u.ID] = true
	}
	seen := map[string]bool{}
	configured := map[string]bool{}
	for _, w := range c.Workspaces {
		configured[w.ID] = w.Adapter == "vitest" && w.NodeRuntime != nil
	}
	add := func(workspace, owner, code string) {
		for id, u := range selected {
			if u.Workspace != workspace {
				continue
			}
			_, rest, _ := strings.Cut(id, ":")
			_, file, _ := strings.Cut(rest, ":")
			if owner != "*" && file != owner {
				continue
			}
			if !proposed[id] {
				proposed[id] = true
				p.Reasons = append(p.Reasons, model.Reason{Unit: id, Code: code})
			}
		}
	}
	comparisonBudget := 1000000
	for _, command := range observed.Commands {
		if !configured[command.Workspace] {
			return errors.New("unexpected runtime observation workspace")
		}
		if seen[command.Workspace] {
			return errors.New("duplicate runtime observation workspace")
		}
		seen[command.Workspace] = true
		trace := command.RuntimeTrace
		if trace == nil || !command.Complete || !observed.Passed || !trace.Complete {
			add(command.Workspace, "*", "runtime-observation-incomplete")
			continue
		}
		if err := vitestprovider.ValidateTrace(*trace); err != nil {
			return err
		}
		owners := map[string]bool{}
		for _, owner := range trace.Owners {
			owners[owner] = true
		}
		for _, unit := range p.Selected {
			if unit.Workspace != command.Workspace {
				continue
			}
			_, rest, _ := strings.Cut(unit.ID, ":")
			_, file, _ := strings.Cut(rest, ":")
			if !owners[file] {
				add(command.Workspace, "*", "runtime-observation-incomplete-worker-coverage")
			}
		}
		// Any unsupported API, unknown attribution or failed capture broadens scope.
		if len(trace.Gaps) > 0 {
			add(command.Workspace, "*", "runtime-observation-gap")
		}
	observationLoop:
		for _, observation := range trace.Observations {
			for _, change := range p.Changes {
				if comparisonBudget%1024 == 0 {
					if err := ctx.Err(); err != nil {
						return err
					}
				}
				comparisonBudget--
				if comparisonBudget < 0 {
					add(command.Workspace, "*", "runtime-observation-analysis-limit")
					break observationLoop
				}
				for _, changed := range []string{change.Path, change.OldPath} {
					if changed == "" {
						continue
					}
					affected := changed == observation.Path
					if observation.Operation == "directory" || observation.Operation == "metadata" || observation.Operation == "exists" {
						affected = affected || observation.Path == "." || strings.HasPrefix(changed, observation.Path+"/")
					}
					if affected {
						add(command.Workspace, observation.Owner, "runtime-observed-influence")
					}
				}
			}
		}
	}
	for _, w := range c.Workspaces {
		if w.Adapter == "vitest" && w.NodeRuntime != nil && !seen[w.ID] {
			add(w.ID, "*", "runtime-observation-missing-workspace")
		}
	}
	p.Proposed = nil
	for _, u := range p.Selected {
		if proposed[u.ID] {
			p.Proposed = append(p.Proposed, u)
		}
	}
	renderProposalCommands(p, c)
	sort.Slice(p.Proposed, func(i, j int) bool { return p.Proposed[i].ID < p.Proposed[j].ID })
	sort.Slice(p.Reasons, func(i, j int) bool {
		a, b := p.Reasons[i], p.Reasons[j]
		return a.Unit+"\x00"+a.Code+"\x00"+a.Input < b.Unit+"\x00"+b.Code+"\x00"+b.Input
	})
	p.Gaps = append(p.Gaps, model.Gap{Code: "runtime-observation-not-enforced", Detail: "Observed APIs only broaden proposals; workers, native bypasses and unobserved effects remain unqualified"})
	// This is an explicit invariant even when the observation input is forged.
	if p.Mode != "full-fallback" || config.Digest(c) != p.ConfigDigest {
		return errors.New("observations cannot authorize affected execution")
	}
	return nil
}
