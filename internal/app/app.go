// Package app coordinates immutable discovery, plans and verified local execution.
package app

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/Cyberlane/hayaku/internal/adapter"
	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/evidence"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/planner"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/report"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

var Version = "0.2.0"

// Run returns a nonzero status for invalid policy, incomplete execution or misses.
func Run(ctx context.Context, args []string, out, errout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errout, "usage: hayaku <init|doctor|plan|explain|run|shadow|version> [options]")
		return 2
	}
	if args[0] == "version" {
		if len(args) != 1 {
			fmt.Fprintln(errout, "version takes no arguments")
			return 2
		}
		fmt.Fprintln(out, Version)
		return 0
	}
	if args[0] == "help" || args[0] == "--help" {
		fmt.Fprintln(out, "hayaku init|doctor|plan|explain|run|shadow|version\nplan --base <revision> --candidate <exact tested commit> --config hayaku.json\nPlans retain full suites. proposal_commands are experimental shadow inputs.")
		return 0
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(errout)
	root := fs.String("root", ".", "Git repository root")
	cfgpath := fs.String("config", "hayaku.json", "configuration path relative to root")
	base := fs.String("base", "", "locally available base commit (no fetch)")
	candidate := fs.String("candidate", "HEAD", "exact candidate commit")
	format := fs.String("format", "json", "json or human (plan only)")
	output := fs.String("output", "", "output file (must not exist)")
	planpath := fs.String("plan", "", "saved plan to explain or revalidate/run")
	cache := fs.Bool("cache", false, "reuse private advisory discovery metadata for planning")
	timeout := fs.Duration("timeout", 10*time.Minute, "maximum command duration")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(errout, "unexpected positional arguments")
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintln(errout, "timeout must be positive")
		return 2
	}
	rootabs, err := filepath.Abs(*root)
	if err != nil {
		return failure(errout, err)
	}
	rootabs, err = filepath.EvalSymlinks(rootabs)
	if err != nil {
		return failure(errout, err)
	}
	if !config.Relative(*cfgpath) {
		return failure(errout, errors.New("config must be a normalized repository-relative path"))
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if args[0] == "init" {
		return initialize(rootabs, *cfgpath, out, errout)
	}
	c, err := readConfig(rootabs, *cfgpath)
	if err != nil {
		return failure(errout, err)
	}
	if args[0] == "doctor" {
		return doctor(rootabs, c, out, errout)
	}
	var supplied *model.Plan
	if *planpath != "" {
		f, err := os.Open(*planpath)
		if err != nil {
			return failure(errout, err)
		}
		p, err := report.Decode(f)
		f.Close()
		if err != nil {
			return failure(errout, err)
		}
		supplied = &p
	}
	if args[0] == "explain" {
		if supplied == nil {
			return failure(errout, errors.New("explain requires --plan"))
		}
		return writeResult(out, errout, *output, func(w io.Writer) error { return report.Human(w, *supplied) })
	}
	if args[0] != "plan" && args[0] != "run" && args[0] != "shadow" {
		return failure(errout, fmt.Errorf("unknown command %q", args[0]))
	}
	if supplied != nil {
		if *base != "" && *base != supplied.Base {
			return failure(errout, errors.New("base differs from supplied plan"))
		}
		*base = supplied.Base
		*candidate = supplied.Candidate
	}
	if *base == "" {
		return failure(errout, errors.New("--base is required; no inferred branch or network fetch"))
	}
	if args[0] != "plan" && *cache {
		return failure(errout, errors.New("execution always rediscovers evidence; --cache is plan-only"))
	}
	p, err := Build(ctx, rootabs, *base, *candidate, c, *cache)
	if err != nil {
		return failure(errout, err)
	}
	if supplied != nil && !report.Equal(p, *supplied) {
		return failure(errout, fmt.Errorf("saved plan differs from fresh source, policy, context or evidence (%v); regenerate it", report.Differences(p, *supplied)))
	}
	if args[0] == "plan" {
		if *format != "json" && *format != "human" {
			return failure(errout, errors.New("format must be json or human"))
		}
		return writeResult(out, errout, *output, func(w io.Writer) error {
			if *format == "human" {
				return report.Human(w, p)
			}
			return report.JSON(w, p)
		})
	}
	if err := validateExecution(ctx, rootabs, c, p); err != nil {
		return failure(errout, err)
	}
	if args[0] == "shadow" {
		r, err := Shadow(ctx, rootabs, c, p)
		status := writeResult(out, errout, *output, func(w io.Writer) error { return report.JSON(w, r) })
		if err != nil {
			return failure(errout, err)
		}
		return status
	}
	r, err := Execute(ctx, rootabs, c, p)
	status := writeResult(out, errout, *output, func(w io.Writer) error { return report.JSON(w, r) })
	if err != nil {
		return failure(errout, err)
	}
	return status
}

func failure(w io.Writer, err error) int { fmt.Fprintln(w, "hayaku:", err); return 1 }

func readConfig(root, path string) (model.Config, error) {
	real, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return model.Config{}, err
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || !config.Relative(filepath.ToSlash(rel)) {
		return model.Config{}, errors.New("config resolves outside repository")
	}
	f, err := os.Open(real)
	if err != nil {
		return model.Config{}, err
	}
	defer f.Close()
	return config.Decode(f)
}

func ContextDigest(c model.Context) string {
	// Hash values privately: reports contain neither inherited nor configured env.
	env := process.Environment(c.Env)
	sort.Strings(env)
	return config.Digest(struct {
		Context                     model.Context
		HostOS, HostArch, Toolchain string
		Env                         []string
	}{c, runtime.GOOS, runtime.GOARCH, runtime.Version(), env})
}

func Build(ctx context.Context, root, base, candidate string, c model.Config, useCache bool) (model.Plan, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return model.Plan{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return model.Plan{}, err
	}
	if err := config.Validate(c); err != nil {
		return model.Plan{}, err
	}
	if c.Context.OS != runtime.GOOS || c.Context.Arch != runtime.GOARCH {
		return model.Plan{}, errors.New("context OS/arch differs from installed host; build separate matrix plans on their actual runner")
	}
	cacheDir := ""
	if useCache {
		metadata, err := snapshot.MetadataDirectory(ctx, root)
		if err != nil {
			return model.Plan{}, err
		}
		cacheDir = filepath.Join(metadata, "hayaku-cache")
	}
	pair, err := snapshot.Capture(ctx, root, base, candidate)
	if err != nil {
		return model.Plan{}, err
	}
	defer pair.Close()
	var old, current []model.Evidence
	contextDigest := ""
	selfPath, err := os.Executable()
	if err != nil {
		return model.Plan{}, err
	}
	selfIdentity, err := ToolIdentity(selfPath)
	if err != nil {
		return model.Plan{}, err
	}
	toolDigest, err := toolsIdentity(ctx, c, root)
	if err != nil {
		return model.Plan{}, err
	}
	for _, tree := range []struct {
		id, dir string
		dest    *[]model.Evidence
	}{{pair.Base, pair.BaseDir, &old}, {pair.Candidate, pair.CandidateDir, &current}} {
		if err := prepareNodeRuntimes(ctx, tree.dir, c); err != nil {
			return model.Plan{}, err
		}
		nativeContext, err := EffectiveContextDigest(ctx, tree.dir, c)
		if err != nil {
			return model.Plan{}, err
		}
		if contextDigest == "" {
			contextDigest = nativeContext
		} else if contextDigest != nativeContext {
			return model.Plan{}, errors.New("native contexts differ across snapshots")
		}
		originalTree, err := executionTreeDigest(ctx, tree.dir, c)
		if err != nil {
			return model.Plan{}, err
		}
		for _, w := range c.Workspaces {
			key := config.Digest(struct {
				Source, Context, Version, Tools, Implementation string
				Workspace                                       model.Workspace
			}{tree.id, contextDigest, Version, toolDigest, selfIdentity, w})
			var e model.Evidence
			hit := false
			if useCache {
				e, hit = evidence.Load(cacheDir, key)
			}
			if !hit {
				e, err = adapter.Discover(ctx, tree.dir, w, c.Context)
				if err != nil {
					return model.Plan{}, fmt.Errorf("workspace %s discovery: %w", w.ID, err)
				}
				adapter.Normalize(&e)
				if useCache {
					if err := evidence.Store(cacheDir, key, e); err != nil {
						return model.Plan{}, fmt.Errorf("private cache: %w", err)
					}
				}
			}
			afterTree, err := executionTreeDigest(ctx, tree.dir, c)
			if err != nil {
				return model.Plan{}, err
			}
			if originalTree != afterTree {
				return model.Plan{}, fmt.Errorf("workspace %s discovery mutated immutable source", w.ID)
			}
			*tree.dest = append(*tree.dest, e)
		}
	}
	finalTools, err := toolsIdentity(ctx, c, root)
	if err != nil {
		return model.Plan{}, err
	}
	if finalTools != toolDigest {
		return model.Plan{}, errors.New("installed tools or runtime dependencies changed during planning")
	}
	p, err := planner.Build(old, current, pair.Changes, c)
	if err != nil {
		return p, err
	}
	p.Schema = model.Schema
	p.Version = Version
	p.Base = pair.Base
	p.Candidate = pair.Candidate
	p.ConfigDigest = config.Digest(c)
	p.ContextDigest = contextDigest
	p.ToolsDigest = toolDigest
	p.ProposalCommands = []model.Command{}
	for _, w := range c.Workspaces {
		units := []model.Unit{}
		for _, u := range p.Proposed {
			if u.Workspace == w.ID {
				units = append(units, u)
			}
		}
		if len(units) == 0 {
			continue
		}
		cmd, err := adapter.Proposal(w, units)
		if err != nil {
			p.Gaps = append(p.Gaps, model.Gap{Code: "proposal-command-unavailable", Workspace: w.ID, Detail: "Original runner arguments cannot be safely narrowed; keep full command"})
			continue
		}
		for _, pre := range w.Prerequisites {
			pre.Dir = filepath.ToSlash(filepath.Join(w.Root, pre.Dir))
			p.ProposalCommands = append(p.ProposalCommands, pre)
		}
		p.ProposalCommands = append(p.ProposalCommands, cmd)
	}
	sort.Slice(p.Gaps, func(i, j int) bool {
		a, b := p.Gaps[i], p.Gaps[j]
		return a.Workspace+"\x00"+a.Code+"\x00"+a.Detail < b.Workspace+"\x00"+b.Code+"\x00"+b.Detail
	})
	return p, nil
}

func writeResult(out, errout io.Writer, path string, fn func(io.Writer) error) int {
	if path == "" {
		if err := fn(out); err != nil {
			return failure(errout, err)
		}
		return 0
	}
	// Never overwrite a previous review artifact. Encode before opening target.
	var b bytes.Buffer
	if err := fn(&b); err != nil {
		return failure(errout, err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return failure(errout, err)
	}
	if _, err = f.Write(b.Bytes()); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return failure(errout, err)
	}
	return 0
}

func initialize(root, path string, out, errout io.Writer) int {
	type detected struct {
		file, id, tool string
		args           []string
	}
	candidates := []detected{{"go.mod", "go", "go", []string{"test", "./..."}}, {"Cargo.toml", "cargo", "cargo", []string{"test", "--workspace"}}, {"pyproject.toml", "pytest", "python3", []string{"-m", "pytest"}}, {"pom.xml", "maven", "mvn", []string{"test"}}, {"build.gradle", "gradle", "gradle", []string{"test"}}, {"build.sbt", "sbt", "sbt", []string{"test"}}}
	c := model.Config{Schema: model.Schema, Context: config.HostContext(), Workspaces: []model.Workspace{}}
	for _, d := range candidates {
		if _, err := os.Stat(filepath.Join(root, d.file)); err == nil {
			c.Workspaces = append(c.Workspaces, model.Workspace{ID: d.id, Root: ".", Adapter: d.id, Command: model.Command{Dir: ".", Executable: d.tool, Args: d.args}})
		}
	}
	if w, ok := detectVitest(root); ok {
		c.Workspaces = append(c.Workspaces, w)
	}
	if len(c.Workspaces) == 0 {
		return failure(errout, errors.New("no supported root manifest detected; create explicit hayaku.json with original suite command (adapter command)"))
	}
	result := writeResult(out, errout, filepath.Join(root, path), func(w io.Writer) error { return report.JSON(w, c) })
	if result == 0 {
		fmt.Fprintln(out, "Created", path, "— review original commands, context and workspace roots before use.")
	}
	return result
}

func doctor(root string, c model.Config, out, errout io.Writer) int {
	type finding struct {
		Workspace, Adapter, Tool string
		Available, RootExists    bool
		Boundary                 string
	}
	var findings []finding
	for _, w := range c.Workspaces {
		_, err := toolIdentityAt(w.Command.Executable, filepath.Join(root, w.Root, w.Command.Dir))
		info, rootErr := os.Stat(filepath.Join(root, w.Root, w.Command.Dir))
		findings = append(findings, finding{w.ID, w.Adapter, w.Command.Executable, err == nil, rootErr == nil && info.IsDir(), "Unqualified runtime influence: full suite required"})
	}
	value := struct {
		Schema       int                  `json:"schema"`
		Context      model.Context        `json:"context"`
		Capabilities []adapter.Capability `json:"capabilities"`
		Workspaces   []finding            `json:"workspaces"`
	}{model.Schema, model.Context{ID: c.Context.ID, OS: c.Context.OS, Arch: c.Context.Arch}, adapter.Capabilities(), findings}
	if err := report.JSON(out, value); err != nil {
		return failure(errout, err)
	}
	for _, f := range findings {
		if !f.Available || !f.RootExists {
			return 1
		}
	}
	return 0
}
