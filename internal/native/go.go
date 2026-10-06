package native

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/passcache"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner"
	goresults "github.com/Cyberlane/hayaku/internal/runner/golang"
)

//go:embed worker/main.go
var workerSource embed.FS

type Config struct {
	Schema       int      `json:"schema"`
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
	return process.Environment(map[string]string{"GOENV": "off", "GOFLAGS": "", "GOWORK": "off", "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOVCS": "*:off", "CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": arch, "GODEBUG": "", "GOEXPERIMENT": "", "GOAMD64": "v1", "GOARM64": "v8.0", "GOFIPS140": "off"})
}
func runGo(ctx context.Context, goTool, dir string, env []string, args ...string) (process.Output, error) {
	return process.RunEnvironment(ctx, model.Command{Executable: goTool, Args: args}, dir, env)
}
func prepareWorker(ctx context.Context, goTool, dir, arch string) (string, error) {
	data, err := workerSource.ReadFile("worker/main.go")
	if err != nil {
		return "", err
	}
	source := filepath.Join(dir, "worker.go")
	if err = os.WriteFile(source, data, 0600); err != nil {
		return "", err
	}
	binary := filepath.Join(dir, "worker")
	_, err = runGo(ctx, goTool, dir, buildEnvironment(arch), "build", "-trimpath", "-buildvcs=false", "-o", binary, source)
	return binary, err
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
	if c.Schema != 1 || len(c.Packages) == 0 || len(c.Packages) > 1000 || (c.Architecture != "arm64" && c.Architecture != "amd64") {
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
				docker, backendErr = ResolveDocker()
				if backendErr == nil {
					worker, backendErr = prepareWorker(ctx, goTool, temporary, c.Architecture)
				}
				if backendErr == nil {
					backend = Backend{Docker: docker, Image: c.Image, Worker: worker, Architecture: c.Architecture}
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
				_, buildErr := runGo(ctx, goTool, source, buildEnvironment(c.Architecture), "test", "-c", "-trimpath", "-buildvcs=false", "-o", binary, p)
				if buildErr != nil {
					group.Reason = "native test build failed"
					group.Complete = false
					returnedErr = buildErr
				} else {
					proof := identity(struct {
						Version string
						Files   map[string][]byte
					}{proofVersion, files})
					artifact := Artifact{Name: p, Program: binary, Proof: proof, Cases: cases, Args: []string{"-test.v", "-test.count=1", "-test.parallel=1", "-test.timeout=60s"}, Env: []string{"GOMAXPROCS=1", "GODEBUG=randautoseed=0", "TZ=UTC"}}
					group, err = Execute(ctx, backend, artifact, cacheDir, audit, decodeGo)
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
