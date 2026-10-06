package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/producer"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

type generatedView struct {
	session *producer.Session
	private string
	root    string
	records []model.ProducerRecord
	entries []inputbundle.Entry
}

func (v *generatedView) Close() error {
	if v == nil {
		return nil
	}
	return errors.Join(v.session.Close(), os.RemoveAll(v.private))
}

func (v *generatedView) verify(ctx context.Context) error {
	if v == nil {
		return nil
	}
	records, err := v.session.Records(ctx)
	if err != nil {
		return err
	}
	if config.Digest(records) != config.Digest(v.records) {
		return errors.New("captured producer identity changed")
	}
	return nil
}

func prepareGenerated(ctx context.Context, root, revision string, c model.Config) (_ *generatedView, err error) {
	if len(c.Producers) == 0 {
		return nil, nil
	}
	session, err := producer.New(ctx, producer.Request{Source: root, Revision: revision, Stages: c.Producers, Environment: process.Environment(c.Context.Env)})
	if err != nil {
		return nil, err
	}
	v := &generatedView{session: session}
	defer func() {
		if err != nil {
			err = errors.Join(err, v.Close())
		}
	}()
	if err = session.Run(ctx); err != nil {
		return nil, err
	}
	v.records, err = session.Records(ctx)
	if err != nil {
		return nil, err
	}
	v.entries, err = session.Entries(ctx)
	if err != nil {
		return nil, err
	}
	v.private, err = os.MkdirTemp("", "hayaku-generated-consumer-")
	if err != nil {
		return nil, errors.New("cannot reserve generated consumer tree")
	}
	resolved, err := filepath.EvalSymlinks(v.private)
	if err != nil {
		return nil, errors.New("cannot resolve generated consumer tree")
	}
	v.private = resolved
	v.root = filepath.Join(v.private, "source")
	if err = session.Materialize(ctx, v.root); err != nil {
		return nil, err
	}
	return v, nil
}

// producerToolsIdentity rejects candidate-checkout executables. Revision-owned
// generator scripts must be passed explicitly through {source} arguments.
func producerToolsIdentity(ctx context.Context, root string, c model.Config) (map[string]string, error) {
	identities := map[string]string{}
	for _, stage := range c.Producers {
		resolved, err := filepath.EvalSymlinks(stage.Command.Executable)
		if err != nil {
			return nil, errors.New("cannot resolve configured producer executable")
		}
		for _, name := range []string{stage.Command.Executable, resolved} {
			rel, e := filepath.Rel(root, name)
			if e == nil && config.Relative(filepath.ToSlash(rel)) {
				return nil, errors.New("producer executable must be external; pass revision-owned generator source through {source}")
			}
		}
		info, err := os.Lstat(resolved)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return nil, errors.New("invalid producer executable")
		}
		digest, err := snapshot.FileDigest(ctx, resolved, inputbundle.MaxBytes)
		if err != nil {
			return nil, errors.New("cannot bind configured producer executable bytes")
		}
		identities["producer-"+stage.ID] = config.Digest(struct {
			Alias, Resolved, Digest string
			Mode                    uint32
		}{stage.Command.Executable, resolved, digest, uint32(info.Mode())})
	}
	return identities, nil
}

func generatedChanges(old, current []inputbundle.Entry) []model.Change {
	before, after := map[string]inputbundle.Entry{}, map[string]inputbundle.Entry{}
	for _, entry := range old {
		before[entry.Path] = entry
	}
	for _, entry := range current {
		after[entry.Path] = entry
	}
	var changes []model.Change
	for name, entry := range before {
		if replacement, found := after[name]; !found {
			changes = append(changes, model.Change{Path: name, Status: "D"})
		} else if replacement != entry {
			changes = append(changes, model.Change{Path: name, Status: "M"})
		}
	}
	for name := range after {
		if _, found := before[name]; !found {
			changes = append(changes, model.Change{Path: name, Status: "A"})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes
}

// Generated provenance joins the existing common influence graph. Host-side
// producer execution is unclosed, so all consumers retain full broadening,
// including a comparison with no changed source or generated entries.
func addProducerEvidence(items []model.Evidence, stages []model.ProducerStage) error {
	if len(stages) == 0 {
		return nil
	}
	if len(items) == 0 {
		return errors.New("producer evidence requires a workspace inventory")
	}
	nodes := map[string]bool{}
	for _, item := range items {
		for _, node := range item.Nodes {
			nodes[node] = true
		}
	}
	ids := map[string]string{}
	for _, stage := range stages {
		id := items[0].Workspace + ":producer:" + stage.ID
		if nodes[id] {
			return errors.New("producer node collides with adapter inventory")
		}
		ids[stage.ID] = id
		nodes[id] = true
		items[0].Nodes = append(items[0].Nodes, id)
	}
	for _, stage := range stages {
		for _, dependency := range stage.DependsOn {
			items[0].Edges = append(items[0].Edges, model.Edge{From: ids[dependency], To: ids[stage.ID], Reason: "generated-stage-dependency"})
		}
		for i := range items {
			for input, owners := range items[i].Inputs {
				if input == stage.Output || strings.HasPrefix(input, stage.Output+"/") {
					for _, owner := range owners {
						items[0].Edges = append(items[0].Edges, model.Edge{From: ids[stage.ID], To: owner, Reason: "generated-input"})
					}
				}
			}
		}
	}
	items[0].Gaps = append(items[0].Gaps, model.Gap{Code: "producer-unqualified", Detail: "Generated inputs have observed provenance; host producer influence and materialized-link equivalence remain unclosed, retaining every original consumer command"})
	for i := range items {
		for _, unit := range items[i].Units {
			items[i].Effects = append(items[i].Effects, model.Effect{Owner: unit.ID, Kind: "unknown"})
		}
	}
	return nil
}
