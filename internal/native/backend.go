// Package native implements an explicit restricted native group contract. The
// backend and receipt engine are framework independent; proof.go and go.go are
// the small Go-specific qualification/build/result boundary.
package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/passcache"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner"
)

const boundaryVersion = "hayaku-linux-boundary-v1"
const contractVersion = "native-pure-groups-v1"

// Docker uses only already provisioned, immutable images. The daemon and its
// kernel are trusted platform components, not influence evidence from a trace.
type Backend struct{ Docker, Image, Worker, Architecture, Identity string }
type Artifact struct {
	Name, Program, Proof string
	Cases                []string
	Args                 []string
	Env                  []string
}
type Result struct {
	Name      string         `json:"name"`
	Context   string         `json:"context"`
	Key       string         `json:"key,omitempty"`
	Qualified bool           `json:"qualified"`
	Complete  bool           `json:"complete"`
	Passed    bool           `json:"passed"`
	Executed  bool           `json:"executed"`
	Reused    bool           `json:"reused"`
	Cache     string         `json:"cache"`
	Reason    string         `json:"reason,omitempty"`
	Outcomes  model.Outcomes `json:"outcomes"`
}
type receipt struct {
	Schema   int            `json:"schema"`
	Key      string         `json:"key"`
	Cases    []string       `json:"cases"`
	Outcomes model.Outcomes `json:"outcomes"`
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func identity(value any) string { data, _ := json.Marshal(value); return digest(data) }
func readBounded(name string, limit int64) ([]byte, error) {
	before, err := os.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Size() > limit {
		return nil, errors.New("native input must be a bounded regular file")
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, errors.New("native input unavailable")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("native input changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("native input exceeds limit")
	}
	after, err := os.Lstat(name)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(opened, after) || after.Size() != int64(len(data)) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("native input changed")
	}
	return data, nil
}
func fileDigest(name string) (string, error) {
	data, err := readBounded(name, 256<<20)
	if err != nil {
		return "", err
	}
	return digest(data), nil
}
func command(ctx context.Context, executable string, args ...string) (process.Output, error) {
	return process.Run(ctx, model.Command{Executable: executable, Args: args}, "", nil)
}
func (b Backend) inspect(ctx context.Context) (string, error) {
	if !strings.HasPrefix(b.Image, "sha256:") || len(b.Image) != 71 {
		return "", errors.New("native image must be an installed immutable sha256 ID")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(b.Image, "sha256:")); err != nil {
		return "", errors.New("invalid image digest")
	}
	version, err := command(ctx, b.Docker, "version", "--format", "{{json .Server}}")
	if err != nil {
		return "", errors.New("Docker Linux daemon unavailable")
	}
	image, err := command(ctx, b.Docker, "image", "inspect", b.Image, "--format", "{{.Id}} {{.Os}} {{.Architecture}}")
	if err != nil || strings.TrimSpace(string(image.Stdout)) != b.Image+" linux "+b.Architecture {
		return "", errors.New("native image platform or identity mismatch")
	}
	info, err := command(ctx, b.Docker, "info", "--format", "{{.ID}} {{.OSType}} {{.Architecture}} {{.KernelVersion}} {{.SecurityOptions}}")
	if err != nil {
		return "", errors.New("native daemon identity unavailable")
	}
	if !strings.Contains(string(info.Stdout), " linux ") {
		return "", errors.New("native boundary requires Linux daemon")
	}
	dockerPath, err := filepath.EvalSymlinks(b.Docker)
	if err != nil {
		return "", errors.New("Docker CLI unavailable")
	}
	docker, err := fileDigest(dockerPath)
	if err != nil {
		return "", err
	}
	worker, err := fileDigest(b.Worker)
	if err != nil {
		return "", err
	}
	controller, err := passcache.ExecutableDigest(ctx)
	if err != nil {
		return "", err
	}
	return identity([]string{contractVersion, boundaryVersion, b.Image, b.Architecture, b.Docker, dockerPath, docker, worker, controller, string(version.Stdout), string(info.Stdout)}), nil
}
func (b *Backend) Bind(ctx context.Context) error {
	id, err := b.inspect(ctx)
	if err != nil {
		return err
	}
	b.Identity = id
	// A fresh boundary probe is required even for an all-cache-hit invocation.
	// Kernel/version strings alone are not proof that Landlock can be installed.
	output, err := b.execute(ctx, Artifact{Program: b.Worker, Args: []string{"--version"}, Env: []string{"GOMAXPROCS=1"}})
	if err != nil || !output.Completed || output.ExitCode != 0 || string(output.Stdout) != boundaryVersion+"\n" || len(output.Stderr) != 0 {
		b.Identity = ""
		return errors.New("native enforcement probe failed")
	}
	if err = b.unchanged(ctx); err != nil {
		b.Identity = ""
		return err
	}
	return nil
}
func (b Backend) unchanged(ctx context.Context) error {
	id, err := b.inspect(ctx)
	if err != nil || id != b.Identity {
		return errors.New("native backend changed")
	}
	return nil
}
func (b Backend) execute(ctx context.Context, a Artifact) (process.Output, error) {
	// Only private copied artifacts are mounted; never the repository, cache,
	// compiler, host sockets, credentials or environment.
	dir, err := os.MkdirTemp("", "hayaku-native-envelope-")
	if err != nil {
		return process.Output{}, err
	}
	defer os.RemoveAll(dir)
	data, err := readBounded(a.Program, 256<<20)
	if err != nil || len(data) > 256<<20 {
		return process.Output{}, errors.New("native program unavailable")
	}
	if err = os.Chmod(dir, 0755); err != nil {
		return process.Output{}, err
	}
	if err = os.WriteFile(filepath.Join(dir, "program"), data, 0555); err != nil {
		return process.Output{}, err
	}
	contract, _ := json.Marshal(struct {
		Args []string `json:"args"`
		Env  []string `json:"env"`
	}{a.Args, a.Env})
	if len(contract) > 65536 || len(a.Args) > 128 || len(a.Env) > 128 {
		return process.Output{}, errors.New("native arguments or environment exceed limits")
	}
	if err = os.WriteFile(filepath.Join(dir, "contract.json"), contract, 0444); err != nil {
		return process.Output{}, err
	}
	worker := filepath.Join(dir, "boundary")
	data, err = readBounded(b.Worker, 256<<20)
	if err != nil {
		return process.Output{}, err
	}
	if err = os.WriteFile(worker, data, 0555); err != nil {
		return process.Output{}, err
	}
	// Colon/comma are Docker mount grammar, never accepted as path data here.
	if strings.ContainsAny(dir, ",:") {
		return process.Output{}, errors.New("unsupported Docker mount path")
	}
	create := []string{"create", "--label=hayaku.native-boundary=v1", "--pull=never", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges:true", "--user=65534:65534", "--pids-limit=64", "--memory=256m", "--memory-swap=256m", "--workdir=/input", "--entrypoint=/boundary", "--mount", "type=bind,src=" + dir + ",dst=/input,readonly", "--mount", "type=bind,src=" + worker + ",dst=/boundary,readonly", b.Image, "/input/program"}
	made, err := command(ctx, b.Docker, create...)
	if err != nil {
		return made, errors.New("native container creation failed")
	}
	id := strings.TrimSpace(string(made.Stdout))
	if len(id) != 64 {
		return process.Output{}, errors.New("invalid container identity")
	}
	if _, err = hex.DecodeString(id); err != nil {
		return process.Output{}, errors.New("invalid container identity")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = command(cleanup, b.Docker, "rm", "--force", "--volumes", id)
	}()
	result, runErr := command(ctx, b.Docker, "start", "--attach", id)
	// Docker attach completion is not itself the guest's exit status.
	status, statusErr := command(ctx, b.Docker, "inspect", id, "--format", "{{json .State}}")
	var state struct {
		Status                                       string
		ExitCode                                     int
		OOMKilled, Running, Paused, Restarting, Dead bool
		Error                                        string
	}
	if statusErr != nil || json.Unmarshal(status.Stdout, &state) != nil || state.Status != "exited" || state.OOMKilled || state.Running || state.Paused || state.Restarting || state.Dead || state.Error != "" || !result.Completed {
		result.Completed = false
		return result, errors.New("native execution incomplete")
	}
	exit := state.ExitCode
	result.ExitCode = exit
	// The fixed worker marker is removed before language result decoding. A
	// missing marker means enforcement never reached exec; no receipt is minted.
	marker := boundaryVersion + "\n"
	if !strings.HasPrefix(string(result.Stderr), marker) {
		result.Completed = false
		return result, errors.New("native boundary did not attest enforcement")
	}
	result.Stderr = result.Stderr[len(marker):]
	if exit != 0 && runErr == nil {
		runErr = errors.New("native test process failed")
	}
	return result, runErr
}

// Execute owns cache authority and lifecycle. Decode is a language boundary;
// qualification is supplied only by reviewed in-process provider code, never
// by a JSON declaration, previous coverage or user result report.
func Execute(ctx context.Context, b Backend, a Artifact, cacheDir string, audit bool, decode func(process.Output, Artifact) (model.Outcomes, error)) (Result, error) {
	r := Result{Name: a.Name, Context: "linux-" + b.Architecture, Qualified: a.Proof != "", Cache: "disabled"}
	if !r.Qualified || len(a.Cases) == 0 || b.Identity == "" {
		return r, errors.New("native artifact is not qualified")
	}
	program, err := fileDigest(a.Program)
	if err != nil {
		return r, err
	}
	r.Key = identity(struct {
		Contract, Backend, Program, Proof, Name string
		Cases, Args, Env                        []string
	}{contractVersion, b.Identity, program, a.Proof, a.Name, a.Cases, a.Args, a.Env})
	cache, cacheErr := passcache.Open(cacheDir, "hayaku-native-pure-pass-v1")
	if cacheErr != nil {
		r.Cache = "unavailable"
	}
	defer cache.Close()
	if err = b.unchanged(ctx); err != nil {
		return r, err
	}
	data, status := cache.Load(r.Key)
	if cacheErr == nil {
		r.Cache = status
	}
	var prior receipt
	valid := status == "hit" && json.Unmarshal(data, &prior) == nil && prior.Schema == 1 && prior.Key == r.Key && identity(prior.Cases) == identity(a.Cases) && passing(prior.Outcomes, a)
	if audit {
		if err = cache.Invalidate(r.Key); err != nil {
			return r, err
		}
		r.Cache = "audit"
	} else if valid {
		if err = ctx.Err(); err != nil {
			return r, err
		}
		if err = b.unchanged(ctx); err != nil {
			return r, err
		}
		after, e := fileDigest(a.Program)
		if e != nil || after != program {
			return r, errors.New("native program changed")
		}
		r.Outcomes = prior.Outcomes
		r.Complete = true
		r.Passed = true
		r.Reused = true
		return r, nil
	}
	if status == "hit" && !valid {
		r.Cache = "invalid"
	}
	r.Executed = true
	output, runErr := b.execute(ctx, a)
	r.Outcomes, err = decode(output, a)
	r.Complete = err == nil && output.Completed
	r.Passed = r.Complete && output.ExitCode == 0 && !runner.Failed(r.Outcomes)
	after, e := fileDigest(a.Program)
	if e != nil || after != program {
		r.Complete = false
		r.Passed = false
		err = errors.New("native program changed")
	}
	if e = b.unchanged(ctx); e != nil {
		r.Complete = false
		r.Passed = false
		err = e
	}
	if e = ctx.Err(); e != nil {
		r.Complete = false
		r.Passed = false
		err = e
	}
	if audit && valid && r.Passed && identity(prior.Outcomes) != identity(r.Outcomes) {
		r.Qualified = false
		r.Passed = false
		err = errors.New("native audit outcome mismatch")
	}
	if r.Passed && passing(r.Outcomes, a) {
		if e = cache.Store(ctx, r.Key, receipt{Schema: 1, Key: r.Key, Cases: a.Cases, Outcomes: r.Outcomes}); e != nil {
			r.Cache = "store-failed"
			if ctx.Err() != nil {
				r.Complete = false
				r.Passed = false
				return r, ctx.Err()
			}
		}
	}
	if !r.Complete {
		r.Qualified = false
		_ = cache.Invalidate(r.Key)
	}
	if err != nil {
		return r, err
	}
	return r, runErr
}
func passing(o model.Outcomes, a Artifact) bool {
	if runner.Validate(o, []model.Unit{{ID: a.Name, Workspace: "native"}}, true, 0) != nil || runner.Failed(o) || len(o.Units) != 1 || o.Units[0].Action != "pass" || len(o.Tests) != len(a.Cases) {
		return false
	}
	want := map[string]bool{}
	for _, name := range a.Cases {
		if name == "" || want[name] {
			return false
		}
		want[name] = true
	}
	seen := map[string]bool{}
	for _, test := range o.Tests {
		if test.Unit != a.Name || !want[test.Test] || test.Action != "pass" || seen[test.Test] {
			return false
		}
		seen[test.Test] = true
	}
	for _, name := range a.Cases {
		if !seen[name] {
			return false
		}
	}
	return true
}

// ResolveDocker preserves argv[0] for multicall CLIs. inspect separately binds
// the resolved executable bytes and the invocation path.
func ResolveDocker() (string, error) {
	name, err := exec.LookPath("docker")
	if err != nil {
		return "", errors.New("installed Docker CLI required")
	}
	return filepath.Abs(name)
}
