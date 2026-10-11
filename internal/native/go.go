package native

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/passcache"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner"
	goresults "github.com/Cyberlane/hayaku/internal/runner/golang"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

//go:embed worker/main.go
var workerSource embed.FS

type Config struct {
	Schema          int      `json:"schema"`
	Platform        string   `json:"platform,omitempty"`
	Image           string   `json:"image"`
	Architecture    string   `json:"architecture"`
	Packages        []string `json:"packages"`
	workspace       string
	original        func(context.Context, string) (Result, error)
	deferCleanup    bool
	originalContext string
	verifyOriginal  func(context.Context) error
}
type Report struct {
	Schema   int      `json:"schema"`
	Contract string   `json:"contract"`
	Complete bool     `json:"complete"`
	Passed   bool     `json:"passed"`
	Groups   []Result `json:"groups"`
	finalize func(context.Context) error
}

func buildEnvironment(arch string) []string {
	return buildEnvironmentForPlatform("linux", arch)
}
func buildEnvironmentForPlatform(platform, arch string) []string {
	return process.Environment(map[string]string{"GOENV": "off", "GOFLAGS": "", "GOWORK": "off", "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOVCS": "*:off", "CGO_ENABLED": "0", "GOOS": platform, "GOARCH": arch, "GODEBUG": "", "GOEXPERIMENT": "", "GOAMD64": "v1", "GOARM64": "v8.0", "GOFIPS140": "off"})
}
func runGo(ctx context.Context, goTool, dir string, env []string, args ...string) (process.Output, error) {
	return process.RunEnvironment(ctx, model.Command{Executable: goTool, Args: args}, dir, env)
}
func prepareWorker(ctx context.Context, goTool, dir, arch string) (string, error) {
	return prepareWorkerEnvironment(ctx, goTool, dir, arch, buildEnvironment(arch))
}
func prepareWorkerEnvironment(ctx context.Context, goTool, dir, arch string, env []string) (string, error) {
	data, err := workerSource.ReadFile("worker/main.go")
	if err != nil {
		return "", err
	}
	source := filepath.Join(dir, "worker.go")
	if err = os.WriteFile(source, data, 0600); err != nil {
		return "", err
	}
	binary := filepath.Join(dir, "worker")
	_, err = runGo(ctx, goTool, dir, env, "build", "-trimpath", "-buildvcs=false", "-o", binary, source)
	return binary, err
}

// goBuildContext binds the complete installed compiler tree, including tools,
// standard-library sources and runtime, rather than trusting a version label.
// The shared bounded snapshot reader rejects unsupported toolchain trees.
// Values stay private; only digests enter receipts.
type goBuildContext struct {
	tool, root, tree, executable, identity, metadata string
	env                                              []string
}

func captureGoBuild(ctx context.Context, tool, arch string) (goBuildContext, error) {
	return captureGoBuildForPlatform(ctx, tool, "linux", arch)
}
func captureGoBuildForPlatform(ctx context.Context, tool, platform, arch string) (goBuildContext, error) {
	build := goBuildContext{tool: tool, env: slices.Clone(buildEnvironmentForPlatform(platform, arch))}
	if (platform != "linux" && platform != "darwin") || (arch != "amd64" && arch != "arm64") {
		return build, errors.New("unsupported native Go build platform")
	}
	output, err := runGo(ctx, tool, "", build.env, "env", "-json", "GOROOT", "GOVERSION", "GOTOOLDIR")
	if err != nil {
		return build, err
	}
	var metadata map[string]string
	if json.Unmarshal(output.Stdout, &metadata) != nil || metadata["GOROOT"] == "" || metadata["GOVERSION"] == "" || metadata["GOTOOLDIR"] == "" {
		return build, errors.New("native Go build context unavailable")
	}
	build.metadata = identity(metadata)
	build.root, err = filepath.EvalSymlinks(metadata["GOROOT"])
	if err != nil {
		return build, err
	}
	// No ambient GOROOT, compiler replacement or external tool directory is
	// silently trusted. The complete effective build environment is bound.
	tools, err := filepath.EvalSymlinks(metadata["GOTOOLDIR"])
	if err != nil {
		return build, err
	}
	rel, err := filepath.Rel(build.root, tools)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return build, errors.New("native Go tools escape compiler tree")
	}
	build.executable, err = fileDigestContext(ctx, tool)
	if err != nil {
		return build, err
	}
	build.tree, err = snapshot.Digest(ctx, build.root)
	if err != nil {
		return build, err
	}
	build.identity = identity(struct {
		Version    string
		Metadata   map[string]string
		Tool, Tree string
		Env        []string
	}{proofVersion, metadata, build.executable, build.tree, build.env})
	return build, build.verify(ctx)
}
func (build goBuildContext) verify(ctx context.Context) error {
	executable, err := fileDigestContext(ctx, build.tool)
	if err != nil || executable != build.executable {
		return errors.New("native Go compiler changed")
	}
	if build.metadata != "" {
		output, err := runGo(ctx, build.tool, "", build.env, "env", "-json", "GOROOT", "GOVERSION", "GOTOOLDIR")
		var metadata map[string]string
		if err != nil || json.Unmarshal(output.Stdout, &metadata) != nil || identity(metadata) != build.metadata {
			return errors.New("native Go compiler context changed")
		}
		root, err := filepath.EvalSymlinks(metadata["GOROOT"])
		if err != nil || root != build.root {
			return errors.New("native Go compiler root changed")
		}
		tools, err := filepath.EvalSymlinks(metadata["GOTOOLDIR"])
		if err != nil {
			return errors.New("native Go tool directory changed")
		}
		rel, err := filepath.Rel(root, tools)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return errors.New("native Go tools escape compiler tree")
		}
	}
	tree, err := snapshot.Digest(ctx, build.root)
	if err != nil || tree != build.tree {
		return errors.New("native Go toolchain changed")
	}
	return ctx.Err()
}
func goSourceProof(files map[string][]byte) string {
	return identity(struct {
		Version string
		Files   map[string][]byte
	}{proofVersion, files})
}
func goManifestProof(root string) (string, error) {
	values := map[string]string{}
	for _, name := range []string{"go.mod", "go.sum"} {
		path := filepath.Join(root, name)
		_, err := os.Lstat(path)
		if os.IsNotExist(err) {
			values[name] = "absent"
			continue
		}
		if err != nil {
			return "", err
		}
		data, err := readBounded(path, 16<<20)
		if err != nil {
			return "", err
		}
		values[name] = digest(data)
	}
	return identity(values), nil
}

// issueGoArtifact is the sole production issuer for this reviewed pure-Go
// contract. It checks all branches and exact independent case inventory, binds
// copied/original package and module inputs, and owns the decoder. The unrelated
// workspace tree is checked by Go's aggregate lifecycle, not every group key.
func issueGoArtifact(ctx context.Context, backend Backend, a Artifact, build goBuildContext, root, source, groupDir string, dependencies ...*goDependencyProof) (Artifact, error) {
	selector, workspace := a.Name, ""
	if !slices.Equal(a.Args, []string{"-test.v", "-test.count=1", "-test.parallel=1", "-test.timeout=60s"}) || !slices.Equal(a.Env, []string{"GOMAXPROCS=1", "GODEBUG=randautoseed=0", "TZ=UTC"}) {
		return Artifact{}, errors.New("native Go launch differs from reviewed contract")
	}
	files, err := packageSources(groupDir)
	if err != nil {
		return Artifact{}, err
	}
	cases, err := qualifyGo(files)
	packageProof := goSourceProof(files)
	expectedProof := packageProof
	var dependent *GoClosure
	var originalContext string
	var verifyOriginal func(context.Context) error
	if len(dependencies) > 1 {
		return Artifact{}, errors.New("ambiguous Go dependency proof")
	}
	if len(dependencies) == 1 && dependencies[0] != nil {
		request := dependencies[0]
		selector, workspace = request.name, request.workspace
		originalContext, verifyOriginal = request.originalContext, request.verifyOriginal
		name := selector
		if workspace != "" {
			name = workspace + "::" + selector
		}
		if runtime.GOARCH != "arm64" || (runtime.GOOS != "darwin" && runtime.GOOS != "linux") || backend.Architecture != "arm64" || !slices.Equal(build.env, buildEnvironmentForPlatform(backend.platform(), "arm64")) {
			return Artifact{}, errors.New("Go dependency producer context mismatch")
		}
		if !validPackage(selector) || strings.ContainsAny(workspace, ":\x00\r\n") || request.closure == nil || request.closure.root != root || request.closure.source != source || name != a.Name || !slices.Equal(request.env, build.env) {
			return Artifact{}, errors.New("Go dependency producer context mismatch")
		}
		if workspace != "" && (originalContext == "" || verifyOriginal == nil || verifyOriginal(ctx) != nil) {
			return Artifact{}, errors.New("Go original invocation context unavailable")
		}
		rechecked, checkErr := certifyGoDependency(ctx, request.closure, selector, build.env)
		if checkErr != nil || rechecked.digest != request.digest {
			return Artifact{}, errors.New("Go dependency producer proof mismatch")
		}
		cases, expectedProof, err, dependent = rechecked.cases, rechecked.digest, nil, request.closure
	}
	if err != nil || !slices.Equal(cases, a.Cases) || expectedProof != a.Proof || a.Envelope != nil || len(a.Effects) != 0 {
		return Artifact{}, errors.New("native Go producer evidence mismatch")
	}
	manifest, err := goManifestProof(source)
	if err != nil {
		return Artifact{}, err
	}
	if groupDir != filepath.Join(source, filepath.FromSlash(strings.TrimPrefix(selector, "./"))) {
		return Artifact{}, errors.New("Go package selector differs from producer directory")
	}
	originalGroup := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(selector, "./")))
	verify := func(ctx context.Context) error {
		if verifyOriginal != nil {
			if err := verifyOriginal(ctx); err != nil {
				return err
			}
		}
		if dependent != nil {
			if err := dependent.Revalidate(ctx); err != nil {
				return err
			}
		}
		if err := build.verify(ctx); err != nil {
			return err
		}
		for _, dir := range []string{groupDir, originalGroup} {
			current, err := packageSources(dir)
			if err != nil || goSourceProof(current) != packageProof {
				return errors.New("native Go package changed")
			}
		}
		for _, dir := range []string{root, source} {
			current, err := goManifestProof(dir)
			if err != nil || current != manifest {
				return errors.New("native Go module context changed")
			}
		}
		return ctx.Err()
	}
	if err := verify(ctx); err != nil {
		return Artifact{}, err
	}
	a.Cases, a.Args, a.Env = slices.Clone(a.Cases), slices.Clone(a.Args), slices.Clone(a.Env)
	a.Effects = slices.Clone(a.Effects)
	program, err := fileDigestContext(ctx, a.Program)
	if err != nil {
		return Artifact{}, err
	}
	producer := identity([]string{proofVersion, build.identity, manifest})
	if originalContext != "" {
		producer = identity([]string{producer, originalContext})
	}
	seal := &artifactAuthority{producer: producer, verify: verify, decode: decodeGo}
	if workspace != "" {
		inventory := model.ExecutionInventory{Schema: model.ExecutionSchema, Units: []model.Unit{{ID: a.Name, Workspace: workspace}}}
		for _, name := range a.Cases {
			inventory.Cases = append(inventory.Cases, model.Case{Unit: a.Name, ID: name})
		}
		inventory = cloneInventory(inventory)
		seal.inventory = &inventory
		a.authority = seal
	}
	binding, err := artifactBinding(backend, a, program, producer)
	if err != nil {
		return Artifact{}, err
	}
	seal.binding, seal.location = binding, authorityLocation(backend, a)
	a.authority = seal
	return a, nil
}

// capture binds a copied immutable build tree. Source contents and environment
// values stay private; reports contain only identities and outcomes.
func capture(ctx context.Context, root, dest string) (string, error) {
	before, err := treeIdentity(ctx, root)
	if err != nil {
		return "", err
	}
	err = filepath.WalkDir(root, func(path string, e fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if rel == ".git" && e.IsDir() {
			return filepath.SkipDir
		}
		target := filepath.Join(dest, rel)
		if e.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("native source contains nonregular input")
		}
		data, err := readBounded(path, 16<<20)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
	if err != nil {
		return "", err
	}
	after, err := treeIdentity(ctx, root)
	if err != nil || before != after {
		return "", errors.New("native source changed during capture")
	}
	copied, err := treeIdentity(ctx, dest)
	if err != nil || copied != before {
		return "", errors.New("native capture identity mismatch")
	}
	return before, nil
}
func treeIdentity(ctx context.Context, root string) (string, error) {
	type input struct {
		Name   string
		Mode   fs.FileMode
		Digest string
	}
	inputs := []input{}
	var total int64
	err := filepath.WalkDir(root, func(path string, e fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if rel == ".git" && e.IsDir() {
			return filepath.SkipDir
		}
		if rel == "." {
			return nil
		}
		if len(inputs) > 50000 {
			return errors.New("native source exceeds entry limit")
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("native source contains links or special inputs")
		}
		item := input{Name: filepath.ToSlash(rel), Mode: info.Mode().Perm()}
		if !info.IsDir() {
			if info.Size() > 16<<20 {
				return errors.New("native source exceeds file limit")
			}
			data, err := readBounded(path, 16<<20)
			if err != nil {
				return err
			}
			total += int64(len(data))
			if total > 128<<20 {
				return errors.New("native source exceeds total limit")
			}
			item.Digest = digest(data)
		} else {
			item.Mode = 0
			item.Digest = "directory"
		}
		inputs = append(inputs, item)
		return nil
	})
	return identity(inputs), err
}
func Go(ctx context.Context, root string, c Config, cacheDir string, audit bool) (Report, error) {
	return GoWithOptions(ctx, root, c, ExecutionOptions{CacheDir: cacheDir, Audit: audit})
}

func GoWithOptions(ctx context.Context, root string, c Config, options ExecutionOptions) (report Report, returnedErr error) {
	cacheDir, audit := options.CacheDir, options.Audit
	report = Report{Schema: 1, Contract: contractVersion}
	if c.workspace == "" && (c.original != nil || c.deferCleanup || c.originalContext != "" || c.verifyOriginal != nil) || c.workspace != "" && (strings.ContainsAny(c.workspace, ":\x00\r\n") || c.original == nil || !c.deferCleanup || c.originalContext == "" || c.verifyOriginal == nil) {
		return report, errors.New("invalid private Go workspace lifecycle")
	}
	var cleanup *passcache.Audit
	if audit {
		owned := options.AuditHandle == nil
		var err error
		cleanup, err = BeginAuditWithOptions(options)
		if err != nil {
			return report, err
		}
		options.AuditHandle = cleanup
		if owned {
			defer func() {
				failed := returnedErr != nil || !report.Complete || !report.Passed
				if err := cleanup.Finish(failed); err != nil {
					returnedErr = errors.Join(returnedErr, err)
					report.Complete, report.Passed = false, false
				}
			}()
		}
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if !audit && options.Authority != nil {
		if err := options.Authority.Verify(ctx); err != nil {
			return report, err
		}
	}
	if c.Platform == "" {
		c.Platform = "linux"
	}
	if c.Schema != 1 || len(c.Packages) == 0 || len(c.Packages) > 1000 || (c.Architecture != "arm64" && c.Architecture != "amd64") || (c.Platform != "linux" && c.Platform != "darwin") || (c.Platform == "darwin" && c.Image != "") {
		return report, errors.New("invalid native Go configuration")
	}
	seen := map[string]bool{}
	for _, p := range c.Packages {
		if !validPackage(p) || seen[p] {
			return report, errors.New("native packages must be unique explicit relative directories")
		}
		seen[p] = true
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		return report, errors.New("installed Go compiler required")
	}
	goTool, err = filepath.EvalSymlinks(goTool)
	if err != nil {
		return report, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return report, err
	}
	if cacheDir != "" {
		absoluteCache, e := filepath.Abs(cacheDir)
		if e != nil {
			return report, e
		}
		relative, e := filepath.Rel(root, absoluteCache)
		if e == nil && (relative == "." || (!strings.HasPrefix(relative, ".."+string(filepath.Separator)) && relative != ".." && relative != ".git" && !strings.HasPrefix(relative, ".git"+string(filepath.Separator)))) {
			return report, errors.New("native pass cache must be outside the source tree or inside .git")
		}
	}
	if audit {
		cache, err := passcache.OpenWithAuthority(cacheDir, "hayaku-native-pure-pass-v1", options.Authority)
		if err != nil {
			return report, err
		}
		err = cleanup.Observe(cache)
		err = errors.Join(err, cache.Close())
		if err != nil {
			return report, err
		}
		if err := cleanup.Revoke(); err != nil {
			return report, errors.New("native audit could not revoke previous passes")
		}
	}
	if options.Authority != nil {
		if err := options.Authority.Verify(ctx); err != nil {
			return report, err
		}
	}
	if audit && c.original != nil {
		group, err := c.original(ctx, "Go workspace audit executes complete configured original scope")
		report.Complete, report.Passed, report.Groups = group.Complete, group.Passed, []Result{group}
		return report, err
	}
	temporary, err := os.MkdirTemp("", "hayaku-native-build-")
	if err != nil {
		return report, err
	}
	var helperClosure *GoClosure
	var build goBuildContext
	owner := &goBuildOwner{root: root, source: filepath.Join(temporary, "source"), temporary: temporary, options: options, verifyOriginal: c.verifyOriginal}
	defer func() {
		owner.build, owner.closure = build, helperClosure
		if c.deferCleanup {
			report.finalize = owner.finish
		} else {
			finishGoBuild(temporary, options, &report, &returnedErr, helperClosure)
		}
	}()
	source := filepath.Join(temporary, "source")
	if err = os.Mkdir(source, 0700); err != nil {
		return report, err
	}
	sourceDigest, err := capture(ctx, root, source)
	owner.sourceDigest = sourceDigest
	if err != nil {
		return report, err
	}
	report.Complete = true
	report.Passed = true
	// Backend setup is lazy: an entirely unqualified suite needs neither Docker
	// nor an image, and executes the original complete package commands instead.
	var backend Backend
	backendReady := false
	var backendErr error
	helperAttempted := false
	var helperError error
	fellBack := false
	full := func(p, reason string) (Result, error) {
		if c.original == nil {
			return fullGo(ctx, goTool, root, p, reason)
		}
		fellBack = true
		return owner.original(ctx, c.original, reason)
	}
	for index, p := range c.Packages {
		name := p
		if c.workspace != "" {
			name = c.workspace + "::" + p
		}
		group := Result{Name: name, Context: "original-host", Cache: "disabled"}
		files, readErr := packageSources(filepath.Join(source, filepath.FromSlash(strings.TrimPrefix(p, "./"))))
		cases, proofErr := qualifyGo(files)
		if readErr != nil {
			proofErr = readErr
		}
		var dependency *goDependencyProof
		if readErr == nil && (c.workspace != "" || proofErr != nil && dependencyImportCandidate(files)) {
			if !helperAttempted {
				helperAttempted = true
				helperClosure, helperError = resolveGoDependencyClosure(ctx, root, source, temporary, sourceDigest, goTool, c)
			}
			if helperError == nil {
				dependency, proofErr = certifyGoDependency(ctx, helperClosure, p, buildEnvironmentForPlatform(c.Platform, c.Architecture))
				if proofErr == nil {
					cases = dependency.cases
					dependency.workspace = c.workspace
					dependency.originalContext, dependency.verifyOriginal = c.originalContext, c.verifyOriginal
				}
			} else {
				proofErr = helperError
			}
		}
		if proofErr != nil {
			group, err = full(p, proofErr.Error())
			if err != nil {
				returnedErr = err
			}
		} else {
			if !backendReady && backendErr == nil {
				var docker, worker string
				build, backendErr = captureGoBuildForPlatform(ctx, goTool, c.Platform, c.Architecture)
				if backendErr == nil && c.Platform == "linux" {
					docker, backendErr = ResolveDocker()
				}
				if backendErr == nil {
					if c.Platform == "darwin" {
						worker, backendErr = prepareDarwinWorker(ctx, goTool, temporary, build.env)
					} else {
						worker, backendErr = prepareWorkerEnvironment(ctx, goTool, temporary, c.Architecture, build.env)
					}
				}
				if backendErr == nil {
					backend = Backend{Docker: docker, Image: c.Image, Worker: worker, Architecture: c.Architecture, Platform: c.Platform}
					if c.Platform == "darwin" {
						backend.Sandbox = "/usr/bin/sandbox-exec"
					}
					backendErr = backend.Bind(ctx)
					backendReady = backendErr == nil
				}
			}
			if backendErr != nil {
				group, err = full(p, "native boundary unavailable ("+backendErr.Error()+"); executing complete original group")
				if err != nil {
					returnedErr = err
				}
			} else {
				binary := filepath.Join(temporary, fmt.Sprintf("group-%d", index))
				_, buildErr := runGo(ctx, goTool, source, build.env, "test", "-c", "-trimpath", "-buildvcs=false", "-o", binary, p)
				if buildErr != nil {
					if c.original != nil {
						group, err = full(p, "native test build failed; executing complete original workspace")
						returnedErr = err
					} else {
						group.Reason = "native test build failed"
						group.Complete = false
						returnedErr = buildErr
					}
				} else {
					proof := goSourceProof(files)
					if dependency != nil {
						proof = dependency.digest
					}
					artifact := Artifact{Name: name, Program: binary, Proof: proof, Cases: cases, Args: []string{"-test.v", "-test.count=1", "-test.parallel=1", "-test.timeout=60s"}, Env: []string{"GOMAXPROCS=1", "GODEBUG=randautoseed=0", "TZ=UTC"}}
					artifact, err = issueGoArtifact(ctx, backend, artifact, build, root, source, filepath.Join(source, filepath.FromSlash(strings.TrimPrefix(p, "./"))), dependency)
					if err == nil {
						group, err = ExecuteWithOptions(ctx, backend, artifact, options, decodeGo)
						// Keep attempted authority independently of the public result,
						// which an incomplete execution may replace with full scope.
						owner.report.Groups = append(owner.report.Groups, group)
					}
					if !group.Complete && (ctx.Err() == nil || c.original != nil) {
						group, err = full(p, "native execution evidence incomplete; executing complete original group")
					}
					if err != nil {
						returnedErr = err
					}
				}
			}
		}
		if fellBack {
			report.Groups = []Result{group}
			report.Complete, report.Passed = group.Complete, group.Passed
			returnedErr = err
			break
		}
		report.Groups = append(report.Groups, group)
		report.Complete = report.Complete && group.Complete
		report.Passed = report.Passed && group.Passed
	}
	after, err := treeIdentity(ctx, root)
	copied, copyErr := treeIdentity(ctx, source)
	if err != nil || copyErr != nil || after != sourceDigest || copied != sourceDigest {
		report.Complete = false
		report.Passed = false
		returnedErr = errors.New("native source changed during execution")
	}
	if build.identity != "" {
		if e := build.verify(ctx); e != nil {
			report.Complete = false
			report.Passed = false
			returnedErr = e
		}
	}
	if ctx.Err() != nil {
		report.Complete = false
		report.Passed = false
		returnedErr = ctx.Err()
	}
	// Revoke every receipt on aggregate identity drift, including earlier groups.
	if options.Authority != nil {
		if e := options.Authority.Verify(ctx); e != nil {
			report.Complete, report.Passed = false, false
			returnedErr = e
		}
	}
	if !report.Complete {
		returnedErr = errors.Join(returnedErr, RevokeWithOptions(options, owner.report))
	}
	return report, returnedErr
}

// Failed cleanup invalidates the aggregate and all receipts minted by it.
func finishGoBuild(temporary string, options ExecutionOptions, report *Report, returnedErr *error, closure *GoClosure) {
	if closure != nil {
		closure.closed = true
	}
	if cleanupErr := os.RemoveAll(temporary); cleanupErr != nil {
		report.Complete, report.Passed = false, false
		*returnedErr = errors.Join(*returnedErr, errors.New("native Go build cleanup failed"), cleanupErr, RevokeWithOptions(options, *report))
	}
}

type goBuildOwner struct {
	verifyOriginal                        func(context.Context) error
	root, source, temporary, sourceDigest string
	build                                 goBuildContext
	closure                               *GoClosure
	options                               ExecutionOptions
	report                                Report
	once                                  sync.Once
	err                                   error
}

func (owner *goBuildOwner) original(ctx context.Context, run func(context.Context, string) (Result, error), reason string) (Result, error) {
	revokeErr := RevokeWithOptions(owner.options, owner.report)
	owner.err = errors.Join(owner.err, revokeErr)
	group, err := run(ctx, reason)
	if revokeErr != nil {
		group.Complete, group.Passed = false, false
	}
	return group, errors.Join(revokeErr, err)
}

// The coordinator keeps this owner alive through all later workspaces. Each
// guard and cleanup remains mandatory even when another guard fails.
func (owner *goBuildOwner) finish(ctx context.Context) error {
	owner.once.Do(func() {
		if owner.verifyOriginal != nil {
			owner.err = errors.Join(owner.err, owner.verifyOriginal(ctx))
		}
		if owner.sourceDigest != "" {
			original, err := treeIdentity(ctx, owner.root)
			copied, copyErr := treeIdentity(ctx, owner.source)
			if err != nil || copyErr != nil || original != owner.sourceDigest || copied != owner.sourceDigest {
				owner.err = errors.Join(owner.err, errors.New("native Go retained source changed"), err, copyErr)
			}
		}
		if owner.closure != nil {
			owner.err = errors.Join(owner.err, owner.closure.Revalidate(ctx))
		}
		if owner.build.identity != "" {
			owner.err = errors.Join(owner.err, owner.build.verify(ctx))
		}
		if owner.options.Authority != nil {
			owner.err = errors.Join(owner.err, owner.options.Authority.Verify(ctx))
		}
		owner.err = errors.Join(owner.err, ctx.Err())
		if owner.err != nil {
			owner.report.Complete, owner.report.Passed = false, false
			owner.err = errors.Join(owner.err, RevokeWithOptions(owner.options, owner.report))
		}
		finishGoBuild(owner.temporary, owner.options, &owner.report, &owner.err, owner.closure)
	})
	return owner.err
}

func init() { workspaceProviders["go"] = goWorkspace }

func goWorkspaceConfig(w Workspace, c RunConfig) (Config, bool) {
	// New typed workspace qualification is limited to the reviewed ARM64 cohort.
	if c.Architecture != "arm64" {
		return Config{}, false
	}
	args := w.Command.Args
	if w.Adapter != "go" || w.Command.Dir != "." || len(args) < 3 || args[0] != "test" || args[1] != "-count=1" || !slices.Equal(w.Patterns, args[2:]) || len(w.Prerequisites) != 0 || len(w.BuildFlags) != 0 || len(w.Inputs) != 0 || w.NodeRuntime != nil || w.SwiftRuntime != nil || w.Vitest != nil || len(c.Environment) != 0 {
		return Config{}, false
	}
	seen := map[string]bool{}
	for _, selector := range args[2:] {
		if !validPackage(selector) || seen[selector] {
			return Config{}, false
		}
		seen[selector] = true
	}
	if w.Command.Executable != "go" && !filepath.IsAbs(w.Command.Executable) {
		return Config{}, false
	}
	value := c.Config
	value.Packages, value.workspace, value.deferCleanup = slices.Clone(args[2:]), w.ID, true
	return value, true
}

func goWorkspace(ctx context.Context, root string, w Workspace, c RunConfig, options ExecutionOptions) (Report, error) {
	w.Command.Args, w.Patterns = slices.Clone(w.Command.Args), slices.Clone(w.Patterns)
	original := func(ctx context.Context, reason string) (Result, error) {
		return fullWorkspace(ctx, root, w, c.Environment, reason)
	}
	if options.Audit {
		group, err := original(ctx, "Go workspace audit executes complete configured original scope")
		return Report{Schema: 1, Contract: contractVersion, Complete: group.Complete, Passed: group.Passed, Groups: []Result{group}}, err
	}
	config, admitted := goWorkspaceConfig(w, c)
	if admitted {
		var err error
		config.originalContext, err = goOriginalContext(ctx, root, w, c)
		admitted = err == nil
		if admitted {
			config.verifyOriginal = func(ctx context.Context) error {
				current, err := goOriginalContext(ctx, root, w, c)
				if err != nil || current != config.originalContext {
					return errors.Join(errors.New("Go original invocation context changed"), err)
				}
				return nil
			}
		}
	}
	if !admitted {
		group, err := original(ctx, "Go workspace invocation outside qualified contract; executing complete original scope")
		return Report{Schema: 1, Contract: contractVersion, Complete: group.Complete, Passed: group.Passed, Groups: []Result{group}}, err
	}
	called := false
	config.original = func(ctx context.Context, reason string) (Result, error) {
		if called {
			return Result{}, errors.New("Go original workspace fallback attempted twice")
		}
		called = true
		return original(ctx, reason)
	}
	report, err := GoWithOptions(ctx, filepath.Join(root, filepath.FromSlash(w.Root)), config, options)
	if len(report.Groups) == 0 && !called {
		group, fallbackErr := config.original(ctx, "Go workspace producer unavailable; executing complete original scope")
		report.Complete, report.Passed, report.Groups = group.Complete, group.Passed, []Result{group}
		err = fallbackErr
	}
	return report, err
}

func goOriginalContext(ctx context.Context, root string, w Workspace, c RunConfig) (string, error) {
	if runtime.GOOS != c.Platform || runtime.GOARCH != c.Architecture {
		return "", errors.New("Go original host differs from native target")
	}
	// Bind both command routes, not just the physical compiler reached once.
	// A retained producer must reject a later alias or PATH entry retarget.
	selections := [2]struct{ Spelling, Selected, Physical, Digest string }{}
	for i, spelling := range []string{"go", w.Command.Executable} {
		selected, err := exec.LookPath(spelling)
		if err != nil || !filepath.IsAbs(selected) {
			return "", errors.New("Go original compiler selection unavailable")
		}
		physical, err := filepath.EvalSymlinks(selected)
		if err != nil {
			return "", errors.New("Go original compiler alias unavailable")
		}
		if i == 1 && physical != selections[0].Physical {
			return "", errors.New("Go original compiler differs from selected native compiler")
		}
		sum := selections[0].Digest
		if i == 0 {
			sum, err = fileDigestContext(ctx, physical)
			if err != nil {
				return "", errors.New("Go original compiler content unavailable")
			}
		}
		selections[i].Spelling, selections[i].Selected, selections[i].Physical, selections[i].Digest = spelling, selected, physical, sum
	}
	values := map[string]string{}
	for key, allowed := range map[string][]string{
		"GOFLAGS": {""}, "GOENV": {"", "off"}, "GOTOOLCHAIN": {"", "local"}, "GOWORK": {"", "off"},
		"GOOS": {"", runtime.GOOS}, "GOARCH": {"", runtime.GOARCH}, "GOEXPERIMENT": {""}, "GOAMD64": {"", "v1"}, "GOARM64": {"", "v8.0"}, "GOFIPS140": {"", "off"}, "GODEBUG": {""},
	} {
		value := os.Getenv(key)
		if !slices.Contains(allowed, value) {
			return "", errors.New("Go original ambient build setting outside qualified contract")
		}
		values[key] = value
	}
	// These locations and compiler controls do not grant effects, but their
	// accepted original invocation values must remain live through the batch.
	for _, key := range []string{"PATH", "GOROOT", "GOPATH", "GOCACHE", "GOTMPDIR", "GO111MODULE", "GOPROXY", "GOSUMDB", "GOVCS", "CGO_ENABLED", "CC", "CXX", "CGO_CFLAGS", "CGO_CPPFLAGS", "CGO_CXXFLAGS", "CGO_FFLAGS", "CGO_LDFLAGS"} {
		values[key] = os.Getenv(key)
	}
	if values["GOENV"] == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return "", errors.New("Go original user settings unavailable")
		}
		if _, err := os.Lstat(filepath.Join(configDir, "go", "env")); !os.IsNotExist(err) {
			return "", errors.New("Go original user settings require full scope")
		}
	}
	directory := filepath.Join(root, filepath.FromSlash(w.Root))
	if values["GOWORK"] == "" {
		for cwd := directory; ; cwd = filepath.Dir(cwd) {
			if _, err := os.Lstat(filepath.Join(cwd, "go.work")); !os.IsNotExist(err) {
				return "", errors.New("Go original workspace file requires full scope")
			}
			if filepath.Dir(cwd) == cwd {
				break
			}
		}
	}
	data, err := snapshot.ReadFile(ctx, filepath.Join(directory, "go.mod"), 16<<20)
	if err != nil {
		return "", errors.New("Go original module unavailable")
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "toolchain" {
			return "", errors.New("Go original toolchain selection requires full scope")
		}
	}
	return identity(struct {
		Version, OS, Arch, Module string
		Workspace                 model.Workspace
		Environment               map[string]string
		Tools                     [2]struct{ Spelling, Selected, Physical, Digest string }
	}{"go-original-context-v2", runtime.GOOS, runtime.GOARCH, digest(data), w.Workspace, values, selections}), ctx.Err()
}

func validPackage(p string) bool {
	if p == "." {
		return true
	}
	return strings.HasPrefix(p, "./") && !strings.ContainsAny(p, "\\\x00\r\n") && !strings.Contains(p, "...") && filepath.Clean(p[2:]) == p[2:] && p[2:] != "." && !strings.HasPrefix(p[2:], "../") && !filepath.IsAbs(p[2:])
}
func packageSources(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".go") {
			data, err := readBounded(filepath.Join(dir, name), 16<<20)
			if err != nil {
				return nil, err
			}
			files[name] = data
		} else if strings.HasSuffix(name, ".s") || strings.HasSuffix(name, ".syso") || strings.HasSuffix(name, ".c") || strings.HasSuffix(name, ".h") {
			return nil, errors.New("native code outside pure contract")
		}
	}
	return files, nil
}
func fallbackEnvironment() []string {
	return process.Environment(map[string]string{"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOVCS": "*:off", "GOENV": "off", "GOFLAGS": "", "GOWORK": "off"})
}
func fullGo(ctx context.Context, goTool, root, name, reason string) (Result, error) {
	group := Result{Name: name, Context: "original-host", Reason: reason, Executed: true, Cache: "disabled"}
	env := fallbackEnvironment()
	listed, err := runGo(ctx, goTool, root, env, "list", "-f", "{{.ImportPath}}", name)
	if err != nil {
		return group, err
	}
	packageName := strings.TrimSpace(string(listed.Stdout))
	if packageName == "" || strings.ContainsAny(packageName, "\r\n") {
		return group, errors.New("invalid complete package inventory")
	}
	output, runErr := runGo(ctx, goTool, root, env, "test", "-json", "-count=1", name)
	result, err := goresults.Reconcile(bytes.NewReader(output.Stdout), []string{packageName})
	o := model.Outcomes{Complete: err == nil, Failed: result.Failed}
	for _, item := range result.Packages {
		o.Units = append(o.Units, model.Outcome{Unit: name, Action: item.Action})
	}
	for _, item := range result.Tests {
		o.Tests = append(o.Tests, model.Outcome{Unit: name, Test: item.Test, Action: item.Action})
	}
	if err == nil {
		err = runner.Validate(o, []model.Unit{{ID: name, Workspace: "native"}}, output.Completed, output.ExitCode)
	}
	if err == nil && len(o.Tests) == 0 {
		err = errors.New("native group produced an empty test inventory")
		o.Complete = false
	}
	group.Outcomes = o
	group.Complete = err == nil && output.Completed
	group.Passed = group.Complete && output.ExitCode == 0 && !runner.Failed(o)
	if err != nil {
		return group, err
	}
	return group, runErr
}

var terminal = regexp.MustCompile(`^--- (PASS|FAIL|SKIP): ([^ ]+) \([0-9.]+s\)$`)

func decodeGo(output process.Output, a Artifact) (model.Outcomes, error) {
	o := model.Outcomes{Complete: output.Completed}
	starts := map[string]bool{}
	ends := map[string]bool{}
	expected := map[string]bool{}
	for _, name := range a.Cases {
		expected[name] = true
	}
	unit := ""
	for _, line := range strings.Split(string(output.Stdout), "\n") {
		if strings.HasPrefix(line, "=== RUN   ") {
			name := strings.TrimPrefix(line, "=== RUN   ")
			if !expected[name] || starts[name] {
				return o, errors.New("unexpected or duplicate native start")
			}
			starts[name] = true
		}
		if match := terminal.FindStringSubmatch(line); match != nil {
			if !expected[match[2]] || !starts[match[2]] || ends[match[2]] {
				return o, errors.New("unexpected native terminal")
			}
			ends[match[2]] = true
			action := strings.ToLower(match[1])
			o.Tests = append(o.Tests, model.Outcome{Unit: a.Name, Test: match[2], Action: action})
			o.Failed = o.Failed || action == "fail"
		}
		if line == "PASS" || line == "FAIL" {
			if unit != "" {
				return o, errors.New("duplicate native group terminal")
			}
			unit = strings.ToLower(line)
		}
	}
	if len(ends) != len(expected) || unit == "" || len(output.Stderr) != 0 {
		return o, errors.New("native case inventory or diagnostics incomplete")
	}
	sort.Slice(o.Tests, func(i, j int) bool { return o.Tests[i].Test < o.Tests[j].Test })
	o.Units = []model.Outcome{{Unit: a.Name, Action: unit}}
	o.Failed = o.Failed || unit == "fail"
	err := runner.ValidateExact(o, a.inventory(), output.Completed, output.ExitCode)
	return o, err
}

// Revoke invalidates this invocation's native receipts when a caller detects
// live policy drift after execution. It never authorizes execution or reuse.
func Revoke(cacheDir string, report Report) error {
	return RevokeWithOptions(ExecutionOptions{CacheDir: cacheDir}, report)
}
func RevokeWithOptions(options ExecutionOptions, report Report) error {
	cache, err := passcache.OpenWithAuthority(options.CacheDir, "hayaku-native-pure-pass-v1", options.Authority)
	if err != nil {
		return err
	}
	defer cache.Close()
	for _, r := range report.Groups {
		if r.Key != "" {
			if err = cache.Invalidate(r.Key); err != nil {
				return err
			}
		}
	}
	return nil
}
