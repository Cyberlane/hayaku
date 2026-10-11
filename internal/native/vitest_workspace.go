package native

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"

	vitestadapter "github.com/Cyberlane/hayaku/internal/adapter/vitest"
	"github.com/Cyberlane/hayaku/internal/model"
)

const vitestOriginalImage = "sha256:0ea54978ec8888bd907c0b0d552b4ac12dd5951ddc37fead577d17d7252e2f91"
const VitestProfileFile = "hayaku.vitest.config.mjs"

// VitestProfile is an opt-in original Vitest execution context. It disables
// ambient projects/transforms and discovers every matching entry in the root.
// It contains no user code, downloaded tool or source-selection declaration.
const VitestProfile = `export default { cacheDir:'/scratch/vite-cache', server:{watch:null}, test:{environment:'node',pool:'threads',minWorkers:1,maxWorkers:1,isolate:true,fileParallelism:false,globals:false,watch:false,cache:false,testTimeout:0,hookTimeout:0,retry:0,allowOnly:false,passWithNoTests:false,sequence:{shuffle:false},include:['**/*.test.ts','**/*.test.js']} };
`
const VitestPackage = "{\"private\":true,\"type\":\"module\"}\n"

func init() { workspaceProviders["vitest"] = vitestWorkspace }

// VitestOriginalCommand describes the complete original scope for enrollment.
// Docker and every SDK input must already be installed. This function performs
// no launch or installation; execution still requires private producer admission.
func VitestOriginalCommand(docker, root, modules, node, addon, image string) model.Command {
	return model.Command{Dir: ".", Executable: docker, Args: []string{
		"run", "--rm", "--pull=never", "--network=none", "--read-only",
		"--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=256", "--memory=1g", "--cpus=1",
		"--user=65534:65534", "--tmpfs", "/scratch:rw,noexec,nosuid,nodev,size=64m,mode=0700,uid=65534,gid=65534",
		"--mount", "type=bind,src=" + node + ",dst=/node,readonly",
		"--mount", "type=bind,src=" + addon + ",dst=/rolldown.node,readonly",
		"--mount", "type=bind,src=" + root + ",dst=/resources,readonly",
		"--mount", "type=bind,src=" + modules + ",dst=/resources/node_modules,readonly",
		"--workdir=/resources", "--entrypoint=/usr/bin/env", image,
		"-i", "CI=true", "TMPDIR=/scratch", "NAPI_RS_NATIVE_LIBRARY_PATH=/rolldown.node", "/node",
		"/resources/node_modules/vitest/vitest.mjs", "run", "--config", VitestProfileFile, "--configLoader", "native", "--reporter=json",
	}}
}

type vitestScopeFile struct {
	name, digest string
	info         os.FileInfo
}

type vitestScope struct {
	root           string
	sources, tests []string
	files          []vitestScopeFile
	directories    map[string]os.FileInfo
	otherDigest    string
}

func captureVitestScope(ctx context.Context, root string) (vitestScope, error) {
	scope := vitestScope{root: root, directories: map[string]os.FileInfo{}}
	var other []struct{ Name, Digest string }
	var bytes int64
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "node_modules") {
			return filepath.SkipDir
		}
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		if info.IsDir() {
			if len(scope.directories) >= 1024 || info.Mode().Perm()&0005 != 0005 {
				return errors.New("Vitest original source directories unavailable")
			}
			scope.directories[rel] = info
			return nil
		}
		if !info.Mode().IsRegular() || len(scope.files) >= 1024 || info.Size() > 8<<20 {
			return errors.New("Vitest source scope exceeds supported file contract")
		}
		if rel != VitestProfileFile && (strings.HasPrefix(entry.Name(), ".env") || strings.Contains(entry.Name(), ".config.")) {
			return errors.New("Vitest ambient environment or runner configuration unsupported")
		}
		bytes += info.Size()
		if bytes > 16<<20 {
			return errors.New("Vitest source scope exceeds byte bound")
		}
		data, err := readBounded(name, 8<<20)
		if err != nil {
			return err
		}
		file := vitestScopeFile{rel, digest(data), info}
		scope.files = append(scope.files, file)
		if strings.HasSuffix(rel, ".ts") || strings.HasSuffix(rel, ".js") {
			if info.Mode().Perm()&0004 == 0 {
				return errors.New("Vitest original source unreadable")
			}
			scope.sources = append(scope.sources, rel)
			if strings.HasSuffix(rel, ".test.ts") || strings.HasSuffix(rel, ".test.js") {
				scope.tests = append(scope.tests, rel)
			}
		} else {
			if rel == VitestProfileFile {
				if string(data) != VitestProfile || info.Mode().Perm()&0004 == 0 {
					return errors.New("Vitest original profile differs from accepted context")
				}
			} else if entry.Name() == "package.json" {
				if rel != "package.json" || string(data) != VitestPackage || info.Mode().Perm()&0004 == 0 {
					return errors.New("Vitest package context differs from accepted context")
				}
			} else if strings.HasSuffix(rel, ".mjs") || strings.HasSuffix(rel, ".cjs") || strings.HasPrefix(entry.Name(), "tsconfig") || strings.HasPrefix(entry.Name(), "jsconfig") {
				return errors.New("Vitest ambient module or compiler configuration unsupported")
			}
			other = append(other, struct{ Name, Digest string }{rel, file.digest})
		}
		return nil
	})
	if err != nil {
		return scope, err
	}
	if len(scope.sources) == 0 || len(scope.sources) > 128 || len(scope.tests) == 0 {
		return scope, errors.New("Vitest complete source/test inventory unavailable")
	}
	for _, required := range []string{VitestProfileFile, "package.json"} {
		if !slices.ContainsFunc(scope.files, func(file vitestScopeFile) bool { return file.name == required }) {
			return scope, errors.New("Vitest original profile or package missing")
		}
	}
	scope.otherDigest = identity(other)
	return scope, nil
}

func (scope vitestScope) verify(ctx context.Context) error {
	current, err := captureVitestScope(ctx, scope.root)
	if err != nil {
		return err
	}
	if !slices.Equal(scope.sources, current.sources) || !slices.Equal(scope.tests, current.tests) || len(scope.files) != len(current.files) || len(scope.directories) != len(current.directories) || scope.otherDigest != current.otherDigest {
		return errors.New("Vitest complete scope changed")
	}
	for i, file := range scope.files {
		after := current.files[i]
		if file.name != after.name || file.digest != after.digest || !sameProgramInfo(file.info, after.info) {
			return errors.New("Vitest source file changed")
		}
	}
	for name, info := range scope.directories {
		if after := current.directories[name]; after == nil || !os.SameFile(info, after) || info.Mode() != after.Mode() {
			return errors.New("Vitest source directory changed")
		}
	}
	return ctx.Err()
}

// The private issuer requires this live configured scope together with the
// accepted controller and runtime owners. This proof alone grants no authority.
type vitestWorkspaceProof struct {
	scope   vitestScope
	profile string
	seal    string
}

func admitVitestWorkspace(ctx context.Context, root string, w Workspace, c RunConfig, docker string) (*vitestWorkspaceProof, error) {
	if err := verifyVitestController(runtime.GOOS, runtime.GOARCH); err != nil {
		return nil, err
	}
	return captureVitestWorkspaceProof(ctx, root, w, c, docker)
}

func verifyVitestController(goos, arch string) error {
	if arch != "arm64" || (goos != "darwin" && goos != "linux") {
		return errors.New("Vitest controller has no accepted runtime profile")
	}
	return nil
}

// Descriptive scope capture stays portable. Both actual admission and the
// private issuer independently check the real controller before execution.
func captureVitestWorkspaceProof(ctx context.Context, root string, w Workspace, c RunConfig, docker string) (*vitestWorkspaceProof, error) {
	if c.Platform != "linux" || c.Architecture != "arm64" || c.Image != vitestOriginalImage || len(c.Environment) != 0 || w.NodeRuntime == nil || w.Vitest == nil || len(w.Prerequisites) != 0 || len(w.Patterns) != 0 || len(w.BuildFlags) != 0 || len(w.Inputs) != 0 || w.SwiftRuntime != nil {
		return nil, errors.New("Vitest configured invocation has no accepted complete runtime profile")
	}
	workspaceRoot := filepath.Join(root, filepath.FromSlash(w.Root))
	for _, path := range []string{workspaceRoot, w.NodeRuntime.Modules, w.Vitest.Node, w.Vitest.Addon} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, ",\x00\r\n") {
			return nil, errors.New("Vitest original mount path unsupported")
		}
	}
	if !reflect.DeepEqual(w.Command, VitestOriginalCommand(docker, workspaceRoot, w.NodeRuntime.Modules, w.Vitest.Node, w.Vitest.Addon, c.Image)) {
		return nil, errors.New("Vitest original command differs from qualified profile")
	}
	scope, err := captureVitestScope(ctx, workspaceRoot)
	if err != nil {
		return nil, err
	}
	proof := &vitestWorkspaceProof{scope: scope, profile: identity([]string{vitestadapter.RuntimeProfile, VitestProfile, w.ID, w.Root})}
	proof.seal = proof.currentBinding()
	return proof, nil
}

func (proof *vitestWorkspaceProof) currentBinding() string {
	type file struct {
		Name, Digest string
		Info         any
	}
	files := make([]file, len(proof.scope.files))
	for i, entry := range proof.scope.files {
		files[i] = file{entry.name, entry.digest, physicalDynamicInfo(entry.info)}
	}
	type directory struct {
		Name string
		Info any
	}
	dirs := []directory{}
	for _, name := range slices.Sorted(maps.Keys(proof.scope.directories)) {
		dirs = append(dirs, directory{name, physicalDynamicInfo(proof.scope.directories[name])})
	}
	return identity([]any{proof.profile, proof.scope.root, proof.scope.sources, proof.scope.tests, proof.scope.otherDigest, files, dirs})
}

func (proof *vitestWorkspaceProof) verify(ctx context.Context) error {
	if proof == nil || !nodeSourceSHA(proof.profile) || proof.seal == "" || proof.seal != proof.currentBinding() {
		return errors.New("Vitest complete configured scope unavailable")
	}
	return proof.scope.verify(ctx)
}

// This last ecosystem boundary shares selection, signed receipts and audits
// with Go. No configured command or caller-supplied source proof is an issuer.
func vitestWorkspace(ctx context.Context, root string, w Workspace, c RunConfig, options ExecutionOptions) (report Report, returnedErr error) {
	report = Report{Schema: 1, Contract: contractVersion, Groups: []Result{}}
	fallback := func(reason string) (Report, error) {
		group, err := fullWorkspace(ctx, root, w, c.Environment, reason)
		return Report{Schema: 1, Contract: contractVersion, Groups: []Result{group}, Complete: group.Complete, Passed: group.Passed}, err
	}
	if options.Audit {
		return fallback("forced audit: executing complete original Vitest scope")
	}
	docker, err := ResolveDocker()
	if err != nil {
		return fallback("installed native boundary unavailable; executing complete original Vitest scope")
	}
	proof, err := admitVitestWorkspace(ctx, root, w, c, docker)
	if err != nil {
		return fallback(err.Error() + "; executing complete original Vitest scope")
	}
	temporary, err := os.MkdirTemp("", "hayaku-vitest-provider-")
	if err != nil {
		return report, err
	}
	info, err := os.Lstat(temporary)
	if err != nil {
		return report, errors.Join(err, os.Remove(temporary))
	}
	cleanup, err := retainDynamicDirectory(temporary, info, []string{"worker.go", "worker"})
	if err != nil {
		return report, errors.Join(err, os.Remove(temporary))
	}
	var source *NodeSourceCertificate
	var runtimeInputs *Runtime
	var prepared *PreparedVitestRuntime
	var build goBuildContext
	var attempted Report
	invalidated := false
	finalize := func(post context.Context) error {
		var err error
		if prepared != nil {
			err = errors.Join(err, prepared.Verify(post))
		}
		if source != nil {
			err = errors.Join(err, source.Verify(post))
		}
		if runtimeInputs != nil {
			err = errors.Join(err, runtimeInputs.Verify(post))
		}
		if build.identity != "" {
			err = errors.Join(err, build.verify(post))
		}
		err = errors.Join(err, proof.verify(post))
		if prepared != nil {
			err = errors.Join(err, prepared.Close())
			if prepared.prepared != nil {
				err = errors.Join(err, prepared.prepared.Abandon())
			}
		}
		if source != nil {
			err = errors.Join(err, source.Close())
			if source.cleanup != nil {
				err = errors.Join(err, source.cleanup.abandon())
			}
		}
		if runtimeInputs != nil {
			err = errors.Join(err, runtimeInputs.Close())
			if runtimeInputs.cleanup != nil {
				err = errors.Join(err, runtimeInputs.cleanup.abandon())
			}
		}
		err = errors.Join(err, cleanup.close(), cleanup.abandon())
		if err != nil || invalidated {
			err = errors.Join(err, RevokeWithOptions(options, attempted))
		}
		return err
	}
	// Retain owners through every workspace, including complete original fallback.
	defer func() { report.finalize = finalize }()
	full := func(reason string) (Report, error) {
		invalidated = true
		revokeErr := RevokeWithOptions(options, attempted)
		value, err := fallback(reason)
		return value, errors.Join(revokeErr, err)
	}
	tool, err := exec.LookPath("go")
	if err == nil {
		tool, err = filepath.EvalSymlinks(tool)
	}
	if err == nil {
		build, err = captureGoBuildForPlatform(ctx, tool, "linux", "arm64")
	}
	var worker string
	if err == nil {
		worker, err = prepareWorkerEnvironment(ctx, tool, temporary, "arm64", build.env)
	}
	backend := Backend{Docker: docker, Image: c.Image, Worker: worker, Architecture: "arm64", Platform: "linux"}
	if err == nil {
		err = backend.Bind(ctx)
	}
	if err != nil {
		return full("native worker unavailable; executing complete original Vitest scope")
	}
	nodeSHA := vitestNodeSHA256
	if value, hashErr := fileDigestContext(ctx, w.NodeRuntime.Node); hashErr != nil {
		return full("source compiler unavailable; executing complete original Vitest scope")
	} else {
		nodeSHA = value
	}
	if nodeSHA != vitestNodeSHA256 && nodeSHA != vitestCertifierDarwinNodeSHA256 {
		return full("source compiler executable unqualified; executing complete original Vitest scope")
	}
	source, err = certifyNodeSources(ctx, NodeCertifierRequest{Root: proof.scope.root, Node: w.NodeRuntime.Node, TypeScriptSDK: w.Vitest.TypeScriptSDK, NodeSHA256: nodeSHA, CompilerSHA256: vitestCompilerSHA256, Sources: proof.scope.sources, Tests: proof.scope.tests}, vitestSDKDigest)
	if err != nil {
		return full("source grammar or compiler unqualified; executing complete original Vitest scope")
	}
	runtimeInputs, err = CaptureRuntime(ctx, RuntimeRequest{Loader: w.Vitest.Loader, Libraries: slices.Clone(w.Vitest.Libraries), Addons: []RuntimeAddon{{Name: "rolldown", Path: w.Vitest.Addon}}})
	if err != nil {
		return full("runtime closure unavailable; executing complete original Vitest scope")
	}
	prepared, err = PrepareVitestRuntime(ctx, backend, NodeRuntimeRequest{Files: vitestadapter.RuntimeRequest{Root: proof.scope.root, Modules: w.NodeRuntime.Modules, Workspace: w.ID, Sources: proof.scope.sources, Tests: proof.scope.tests}, Node: w.Vitest.Node, Runtime: runtimeInputs, Source: source})
	if err != nil {
		return full("runtime profile unqualified; executing complete original Vitest scope")
	}
	var inventory model.ExecutionInventory
	inventory, err = prepared.Collect(ctx)
	if err != nil {
		return full("native collection incomplete; executing complete original Vitest scope")
	}
	artifacts, err := issueVitestArtifacts(ctx, prepared, proof, inventory)
	if err != nil {
		return full("native source authority incomplete; executing complete original Vitest scope")
	}
	report.Complete, report.Passed = true, true
	for _, artifact := range artifacts {
		group, err := ExecuteWithOptions(ctx, backend, artifact, options, nil)
		attempted.Groups = append(attempted.Groups, group)
		report.Groups = append(report.Groups, group)
		if !group.Complete && ctx.Err() == nil {
			return full("native execution evidence incomplete; executing complete original Vitest scope")
		}
		report.Complete = report.Complete && group.Complete
		report.Passed = report.Passed && group.Passed
		returnedErr = errors.Join(returnedErr, err)
		if ctx.Err() != nil {
			break
		}
	}
	return report, returnedErr
}
