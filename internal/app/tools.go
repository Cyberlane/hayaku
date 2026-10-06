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
	"reflect"
	"time"

	"github.com/Cyberlane/hayaku/internal/adapter/nextest"
	"github.com/Cyberlane/hayaku/internal/capsule"
	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/metrics"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/pilot"
	"github.com/Cyberlane/hayaku/internal/report"
	nativerunner "github.com/Cyberlane/hayaku/internal/runner/native"
	"github.com/Cyberlane/hayaku/internal/snapshot"
	"github.com/Cyberlane/hayaku/internal/suite"
)

type capsuleConfig struct {
	Schema           int               `json:"schema"`
	Name             string            `json:"name"`
	Module           string            `json:"module"`
	Inputs           []string          `json:"inputs"`
	InputEnvelope    *inputsConfig     `json:"input_envelope,omitempty"`
	Args             []string          `json:"args"`
	Env              map[string]string `json:"env"`
	ProducerIdentity string            `json:"producer_identity"`
	Seed             string            `json:"seed"`
	Limits           capsule.Limits    `json:"limits"`
}
type inputsConfig struct {
	Schema int                 `json:"schema"`
	Roots  []inputRoot         `json:"roots"`
	Inputs []inputbundle.Input `json:"inputs"`
}
type inputRoot struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Mount string `json:"mount"`
}
type resultInventory struct {
	Schema int                 `json:"schema"`
	Cases  []nativerunner.Case `json:"cases"`
}
type nativeInventory struct {
	Schema       int                 `json:"schema"`
	Adapter      string              `json:"adapter"`
	Workspace    string              `json:"workspace"`
	Candidate    string              `json:"candidate"`
	SourceDigest string              `json:"source_digest"`
	ConfigDigest string              `json:"config_digest"`
	Qualified    bool                `json:"qualified"`
	Inventory    nextest.Inventory   `json:"inventory"`
	Units        []model.Unit        `json:"units"`
	Cases        []nativerunner.Case `json:"cases"`
}

// The tools have independent schemas. A diagnostic inventory or measurement
// cannot be passed as a capsule receipt or authorize native omission.
func runTool(ctx context.Context, args []string, out, errout io.Writer) int {
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(errout)
	root := fs.String("root", ".", "input root")
	configuration := fs.String("config", "", "strict JSON configuration")
	output := fs.String("output", "", "report file (must not exist)")
	cache := fs.String("cache-dir", "", "private capsule receipt directory")
	cacheFlags := registerCacheAuthorityFlags(fs)
	audit := fs.Bool("no-reuse", false, "execute and invalidate any previous receipt first")
	destination := fs.String("destination", "", "new input envelope directory (must not exist)")
	baseline := fs.String("baseline", "", "baseline measurement JSON")
	measurement := fs.String("measurement", "", "candidate measurement JSON")
	format := fs.String("format", "", "native result format")
	inventory := fs.String("inventory", "", "complete expected case inventory JSON")
	results := fs.String("report", "", "native result report")
	packagePath := fs.String("package", "./internal/graph", "single Go package for the separate WASI pilot")
	workspaceID := fs.String("workspace", "", "configured nextest workspace to inventory")
	timeout := fs.Duration("timeout", 10*time.Minute, "maximum tool duration")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 || *timeout <= 0 || *timeout > 24*time.Hour {
		return failure(errout, errors.New("invalid tool arguments or timeout"))
	}
	allowed := map[string]bool{"root": true, "output": true, "timeout": true}
	switch args[0] {
	case "inventory":
		allowed["config"] = true
		allowed["workspace"] = true
	case "pilot":
		allowed["cache-dir"] = true
		allowed["package"] = true
	case "capsule", "suite":
		for _, k := range []string{"config", "cache-dir", "no-reuse", "cache-policy", "cache-signing-key"} {
			allowed[k] = true
		}
	case "inputs":
		allowed["config"] = true
		allowed["destination"] = true
	case "compare":
		allowed["baseline"] = true
		allowed["measurement"] = true
	case "results":
		for _, k := range []string{"format", "inventory", "report"} {
			allowed[k] = true
		}
	}
	invalid := false
	fs.Visit(func(f *flag.Flag) {
		if !allowed[f.Name] {
			invalid = true
		}
	})
	if invalid {
		return failure(errout, errors.New("option is not valid for this tool"))
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		return failure(errout, err)
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	cachePath := *cache
	if cachePath != "" && !filepath.IsAbs(cachePath) {
		cachePath = filepath.Join(abs, cachePath)
	}
	authority, err := cacheFlags.load(ctx, fs, abs, cachePath)
	if err != nil {
		return failure(errout, err)
	}
	var value any
	var unsuccessful bool
	switch args[0] {
	case "inventory":
		abs, err = filepath.EvalSymlinks(abs)
		if err != nil {
			return failure(errout, err)
		}
		name := *configuration
		if name == "" {
			name = "hayaku.json"
		}
		if !config.Relative(name) {
			return failure(errout, errors.New("inventory config must be repository-relative"))
		}
		c, err := readConfig(abs, name)
		if err != nil {
			return failure(errout, err)
		}
		var workspace *model.Workspace
		for index := range c.Workspaces {
			if c.Workspaces[index].ID == *workspaceID {
				workspace = &c.Workspaces[index]
			}
		}
		if workspace == nil || workspace.Adapter != "nextest" {
			return failure(errout, errors.New("inventory requires a configured nextest workspace"))
		}
		pair, err := snapshot.Capture(ctx, abs, "HEAD", "HEAD")
		if err != nil {
			return failure(errout, err)
		}
		defer pair.Close()
		if err := snapshot.ValidateCandidate(ctx, abs, pair.Candidate); err != nil {
			return failure(errout, err)
		}
		source, err := snapshot.Digest(ctx, pair.CandidateDir)
		if err != nil {
			return failure(errout, err)
		}
		list, err := nextest.Collect(ctx, pair.CandidateDir, *workspace, c.Context)
		if err != nil {
			return failure(errout, err)
		}
		evidence, err := nextest.Discover(ctx, pair.CandidateDir, *workspace, c.Context)
		if err != nil {
			return failure(errout, err)
		}
		cases, err := list.ExpectedJUnitCases(evidence.Units)
		if err != nil {
			return failure(errout, err)
		}
		current, err := snapshot.Digest(ctx, pair.CandidateDir)
		if err != nil || current != source {
			return failure(errout, errors.New("inventory source changed during native collection"))
		}
		if err := snapshot.ValidateCandidate(ctx, abs, pair.Candidate); err != nil {
			return failure(errout, err)
		}
		value = nativeInventory{Schema: 1, Adapter: "nextest", Workspace: workspace.ID, Candidate: pair.Candidate, SourceDigest: source, ConfigDigest: config.Digest(c), Inventory: list, Units: evidence.Units, Cases: cases}
	case "pilot":
		r, err := pilot.Run(ctx, abs, *packagePath, *cache)
		if err != nil {
			return failure(errout, err)
		}
		value = r
		unsuccessful = !r.Comparison.Valid || !r.BaselineResult.Passed || !r.CandidateResult.Passed
	case "capsule":
		var c capsuleConfig
		if err := readToolJSON(abs, *configuration, &c); err != nil {
			return failure(errout, err)
		}
		loaded, err := loadCapsule(ctx, abs, c)
		if err != nil {
			return failure(errout, err)
		}
		options := capsule.Options{CacheDir: cachePath, Limits: c.Limits, NoReuse: *audit, Authority: authority}
		r, runErr := capsule.Run(ctx, loaded.request, options)
		var current capsuleConfig
		stable := readToolJSON(abs, *configuration, &current) == nil && reflect.DeepEqual(c, current) && loaded.verify(ctx, abs) == nil && stableCacheAuthority(ctx, authority)
		if !stable {
			_ = capsule.Revoke(options, r.Key)
			invalidateCapsuleResult(&r)
		}
		value = r
		unsuccessful = runErr != nil || !r.Complete || !r.Qualified || !r.Passed
	case "suite":
		start := time.Now()
		var c suiteConfig
		if err := readToolJSON(abs, *configuration, &c); err != nil {
			return failure(errout, err)
		}
		if c.Schema != 1 || len(c.Groups) == 0 || len(c.Groups) > suite.MaxGroups {
			return failure(errout, errors.New("invalid suite inventory"))
		}
		var loaded []loadedCapsule
		var groups []suite.Group
		totalBytes := 0
		for _, group := range c.Groups {
			item, err := loadCapsule(ctx, abs, group)
			if err != nil {
				return failure(errout, err)
			}
			totalBytes += len(item.request.Module)
			for _, file := range item.request.Files {
				totalBytes += len(file.Data)
			}
			if totalBytes > suite.MaxBatchBytes {
				return failure(errout, errors.New("suite input envelope exceeds bound"))
			}
			loaded = append(loaded, item)
			groups = append(groups, suite.Group{Request: item.request, Limits: group.Limits})
		}
		r, runErr := suite.Run(ctx, groups, suite.Options{CacheDir: cachePath, NoReuse: *audit, Authority: authority})
		var current suiteConfig
		stable := readToolJSON(abs, *configuration, &current) == nil && reflect.DeepEqual(c, current) && stableCacheAuthority(ctx, authority)
		for _, item := range loaded {
			if item.verify(ctx, abs) != nil {
				stable = false
			}
		}
		if !stable {
			r.Complete, r.Passed = false, false
			for i := range r.Groups {
				_ = capsule.Revoke(capsule.Options{CacheDir: cachePath, Authority: authority}, r.Groups[i].Key)
				invalidateCapsuleResult(&r.Groups[i])
			}
		}
		unsuccessful = runErr != nil || !r.Complete || !r.Passed
		r.Duration = time.Since(start)
		value = r

	case "inputs":
		var c inputsConfig
		if err := readToolJSON(abs, *configuration, &c); err != nil {
			return failure(errout, err)
		}
		if c.Schema != 1 {
			return failure(errout, errors.New("unsupported input configuration schema"))
		}
		r := inputRequest(abs, c)
		bundle, err := inputbundle.Capture(ctx, r)
		if err != nil {
			return failure(errout, err)
		}
		if *destination != "" {
			p := *destination
			if !filepath.IsAbs(p) {
				p = filepath.Join(abs, p)
			}
			if err := bundle.Materialize(ctx, p); err != nil {
				return failure(errout, err)
			}
		} else if err := bundle.VerifySources(ctx); err != nil {
			return failure(errout, err)
		}
		value = bundle.Manifest()
	case "compare":
		var before, after metrics.Run
		if err := readToolJSON(abs, *baseline, &before); err != nil {
			return failure(errout, err)
		}
		if err := readToolJSON(abs, *measurement, &after); err != nil {
			return failure(errout, err)
		}
		r, err := metrics.Compare(before, after)
		if err != nil {
			return failure(errout, err)
		}
		value = r
		unsuccessful = !r.Valid
	case "results":
		var expected resultInventory
		if err := readCaseInventory(abs, *inventory, &expected); err != nil {
			return failure(errout, err)
		}
		if expected.Schema != 1 {
			return failure(errout, errors.New("unsupported case inventory schema"))
		}
		if *results == "" {
			return failure(errout, errors.New("native report is required"))
		}
		p := *results
		if !filepath.IsAbs(p) {
			p = filepath.Join(abs, p)
		}
		f, err := os.Open(p)
		if err != nil {
			return failure(errout, errors.New("cannot read native report"))
		}
		defer f.Close()
		var r nativerunner.Result
		switch *format {
		case "junit":
			r, err = nativerunner.ReconcileJUnit(f, expected.Cases)
		case "trx":
			r, err = nativerunner.ReconcileTRX(f, expected.Cases)
		case "swift":
			r, err = nativerunner.ReconcileSwift(f, expected.Cases)
		case "libtest":
			r, err = nativerunner.ReconcileLibtest(f, expected.Cases)
		default:
			return failure(errout, errors.New("unsupported native result format"))
		}
		if err != nil {
			return failure(errout, err)
		}
		value = r
		unsuccessful = r.Failed || len(r.Missing) > 0
	}
	status := writeResult(out, errout, *output, func(w io.Writer) error { return report.JSON(w, value) })
	if unsuccessful && status == 0 {
		fmt.Fprintln(errout, "hayaku: tool result is failed, incomplete or incomparable")
		return 1
	}
	return status
}

func readCaseInventory(root, name string, expected *resultInventory) error {
	if name == "" {
		return errors.New("case inventory is required")
	}
	if !filepath.IsAbs(name) {
		name = filepath.Join(root, name)
	}
	f, err := os.Open(name)
	if err != nil {
		return errors.New("cannot read case inventory")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil || len(data) > 16<<20 {
		return errors.New("case inventory exceeds read bounds")
	}
	if err := config.DecodeValue(bytes.NewReader(data), expected, 16<<20); err == nil {
		return nil
	}
	var native nativeInventory
	if err := config.DecodeValue(bytes.NewReader(data), &native, 16<<20); err != nil || native.Schema != 1 || native.Adapter != "nextest" || native.Workspace == "" || native.Candidate == "" || native.SourceDigest == "" || native.ConfigDigest == "" || native.Qualified {
		return errors.New("invalid strict case inventory")
	}
	cases, err := native.Inventory.ExpectedJUnitCases(native.Units)
	if err != nil || !reflect.DeepEqual(cases, native.Cases) {
		return errors.New("native case inventory differs from configured scopes")
	}
	*expected = resultInventory{Schema: 1, Cases: cases}
	return nil
}

func inputRequest(root string, c inputsConfig) inputbundle.Request {
	r := inputbundle.Request{Inputs: c.Inputs}
	for _, source := range c.Roots {
		p := source.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		r.Roots = append(r.Roots, inputbundle.Root{Name: source.Name, Path: p, Mount: source.Mount})
	}
	return r
}

func readToolJSON(root, name string, dest any) error {
	if name == "" {
		return errors.New("JSON input path is required")
	}
	if !filepath.IsAbs(name) {
		name = filepath.Join(root, name)
	}
	f, err := os.Open(name)
	if err != nil {
		return errors.New("cannot read JSON input")
	}
	defer f.Close()
	if err := config.DecodeValue(f, dest, 16<<20); err != nil {
		return errors.New("invalid strict JSON input")
	}
	return nil
}
