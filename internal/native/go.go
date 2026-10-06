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
	"slices"
	"sort"
	"strings"
	"time"

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
	Schema       int      `json:"schema"`
	Platform     string   `json:"platform,omitempty"`
	Image        string   `json:"image"`
	Architecture string   `json:"architecture"`
	Packages     []string `json:"packages"`
}
type Report struct {
	Schema   int      `json:"schema"`
	Contract string   `json:"contract"`
	Complete bool     `json:"complete"`
	Passed   bool     `json:"passed"`
	Groups   []Result `json:"groups"`
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
func issueGoArtifact(ctx context.Context, backend Backend, a Artifact, build goBuildContext, root, source, groupDir string) (Artifact, error) {
	if !slices.Equal(a.Args, []string{"-test.v", "-test.count=1", "-test.parallel=1", "-test.timeout=60s"}) || !slices.Equal(a.Env, []string{"GOMAXPROCS=1", "GODEBUG=randautoseed=0", "TZ=UTC"}) {
		return Artifact{}, errors.New("native Go launch differs from reviewed contract")
	}
	files, err := packageSources(groupDir)
	if err != nil {
		return Artifact{}, err
	}
	cases, err := qualifyGo(files)
	if err != nil || !slices.Equal(cases, a.Cases) || goSourceProof(files) != a.Proof || a.Envelope != nil || len(a.Effects) != 0 {
		return Artifact{}, errors.New("native Go producer evidence mismatch")
	}
	manifest, err := goManifestProof(source)
	if err != nil {
		return Artifact{}, err
	}
	proof := a.Proof
	originalGroup := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(a.Name, "./")))
	verify := func(ctx context.Context) error {
		if err := build.verify(ctx); err != nil {
			return err
		}
		for _, dir := range []string{groupDir, originalGroup} {
			current, err := packageSources(dir)
			if err != nil || goSourceProof(current) != proof {
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
	binding, err := artifactBinding(backend, a, program, producer)
	if err != nil {
		return Artifact{}, err
	}
	a.authority = &artifactAuthority{binding: binding, location: authorityLocation(backend, a), producer: producer, verify: verify, decode: decodeGo}
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
	report := Report{Schema: 1, Contract: contractVersion}
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
		cache, e := passcache.Open(cacheDir, "hayaku-native-pure-pass-v1")
		if e != nil {
			return report, errors.New("native audit authority unavailable")
		}
		// Revocation is cleanup authorized by --no-reuse, including an already
		// cancelled invocation. It has its own short bounded context.
		revoke, stop := context.WithTimeout(context.Background(), 10*time.Second)
		e = cache.RevokeAll(revoke)
		stop()
		cache.Close()
		if e != nil {
			return report, errors.New("native audit could not revoke previous passes")
		}
	}
	temporary, err := os.MkdirTemp("", "hayaku-native-build-")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(temporary)
	source := filepath.Join(temporary, "source")
	if err = os.Mkdir(source, 0700); err != nil {
		return report, err
	}
	sourceDigest, err := capture(ctx, root, source)
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
	var returnedErr error
	var build goBuildContext
	for index, p := range c.Packages {
		group := Result{Name: p, Context: "original-host", Cache: "disabled"}
		files, readErr := packageSources(filepath.Join(source, filepath.FromSlash(strings.TrimPrefix(p, "./"))))
		cases, proofErr := qualifyGo(files)
		if readErr != nil {
			proofErr = readErr
		}
		if proofErr != nil {
			group, err = fullGo(ctx, goTool, root, p, proofErr.Error())
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
				group, err = fullGo(ctx, goTool, root, p, "native boundary unavailable ("+backendErr.Error()+"); executing complete original group")
				if err != nil {
					returnedErr = err
				}
			} else {
				binary := filepath.Join(temporary, fmt.Sprintf("group-%d", index))
				_, buildErr := runGo(ctx, goTool, source, build.env, "test", "-c", "-trimpath", "-buildvcs=false", "-o", binary, p)
				if buildErr != nil {
					group.Reason = "native test build failed"
					group.Complete = false
					returnedErr = buildErr
				} else {
					artifact := Artifact{Name: p, Program: binary, Proof: goSourceProof(files), Cases: cases, Args: []string{"-test.v", "-test.count=1", "-test.parallel=1", "-test.timeout=60s"}, Env: []string{"GOMAXPROCS=1", "GODEBUG=randautoseed=0", "TZ=UTC"}}
					artifact, err = issueGoArtifact(ctx, backend, artifact, build, root, source, filepath.Join(source, filepath.FromSlash(strings.TrimPrefix(p, "./"))))
					if err == nil {
						group, err = Execute(ctx, backend, artifact, cacheDir, audit, decodeGo)
					}
					if !group.Complete && ctx.Err() == nil {
						group, err = fullGo(ctx, goTool, root, p, "native execution evidence incomplete; executing complete original group")
					}
					if err != nil {
						returnedErr = err
					}
				}
			}
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
	if !report.Complete {
		_ = Revoke(cacheDir, report)
	}
	return report, returnedErr
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
	cache, err := passcache.Open(cacheDir, "hayaku-native-pure-pass-v1")
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
