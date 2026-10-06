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
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/passcache"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

const boundaryVersion = "hayaku-linux-boundary-v4"
const contractVersion = "native-pure-groups-v1"

// Docker uses only already provisioned, immutable images. The daemon and its
// kernel are trusted platform components, not influence evidence from a trace.
type Backend struct{ Docker, Image, Worker, Architecture, Identity, Platform, Sandbox string }

func (b Backend) platform() string {
	if b.Platform == "" {
		return "linux"
	}
	return b.Platform
}

type Artifact struct {
	Name, Program, Proof string
	Cases                []string
	Args                 []string
	Env                  []string
	Envelope             *Envelope `json:"-"`
	// Effects is descriptive input data. Only a complete reviewed provider can
	// establish its meaning; an empty summary never supplies a missing proof.
	Effects   []model.Effect
	authority *artifactAuthority
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
	file, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("native input unavailable")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || before.Mode() != opened.Mode() || before.Size() != opened.Size() || !before.ModTime().Equal(opened.ModTime()) {
		return nil, errors.New("native input changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, before.Size()+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("native input exceeds limit")
	}
	after, err := os.Lstat(name)
	if err != nil || !after.Mode().IsRegular() || after.Mode() != before.Mode() || !os.SameFile(opened, after) || after.Size() != int64(len(data)) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("native input changed")
	}
	return data, nil
}
func fileDigest(name string) (string, error) { return fileDigestContext(context.Background(), name) }
func fileDigestContext(ctx context.Context, name string) (string, error) {
	return snapshot.FileDigest(ctx, name, 256<<20)
}
func command(ctx context.Context, executable string, args ...string) (process.Output, error) {
	return process.Run(ctx, model.Command{Executable: executable, Args: args}, "", nil)
}
func (b Backend) inspect(ctx context.Context) (string, error) {
	if b.platform() == "darwin" {
		return b.inspectDarwin(ctx)
	}
	if b.platform() != "linux" || b.Sandbox != "" {
		return "", errors.New("unsupported native backend")
	}
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
	docker, err := fileDigestContext(ctx, dockerPath)
	if err != nil {
		return "", err
	}
	worker, err := fileDigestContext(ctx, b.Worker)
	if err != nil {
		return "", err
	}
	controller, err := passcache.ExecutableDigest(ctx)
	if err != nil {
		return "", err
	}
	return identity([]string{contractVersion, observablePolicyDigest(), capabilityPolicyDigest(), boundaryVersion, b.Image, b.Architecture, b.Docker, dockerPath, docker, worker, controller, string(version.Stdout), string(info.Stdout)}), nil
}
func (b *Backend) Bind(ctx context.Context) error {
	id, err := b.inspect(ctx)
	if err != nil {
		return err
	}
	b.Identity = id
	if b.platform() == "darwin" {
		return b.bindDarwin(ctx)
	}
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
	if b.platform() == "darwin" {
		return b.executeDarwin(ctx, a)
	}
	return b.executeCaptured(ctx, a, nil)
}

func (b Backend) executeCaptured(ctx context.Context, a Artifact, primary *capturedExecutable) (result process.Output, runErr error) {
	if b.platform() != "linux" || b.Sandbox != "" {
		return result, errors.New("captured executable requires a Linux backend")
	}
	// Only private copied artifacts are mounted; never the repository, cache,
	// compiler, host sockets, credentials or environment.
	env := a.Env
	scratchBytes := int64(0)
	if a.Envelope != nil {
		if err := a.Envelope.VerifySources(ctx); err != nil {
			return process.Output{}, err
		}
		if len(a.Env) != 0 && !slices.Equal(a.Env, a.Envelope.env) {
			return process.Output{}, errors.New("native artifact conflicts with envelope environment")
		}
		env = a.Envelope.env
		scratchBytes = a.Envelope.scratchBytes
	}
	if err := validateNativeArguments(a.Args, env); err != nil {
		return process.Output{}, err
	}
	dir, err := os.MkdirTemp("", "hayaku-native-envelope-")
	if err != nil {
		return process.Output{}, err
	}
	defer func() {
		if a.Envelope != nil {
			_ = removeEnvelopeCopy(filepath.Join(dir, "resources"))
		}
		if err := os.RemoveAll(dir); err != nil {
			result.Completed = false
			runErr = errors.New("native copied input cleanup failed")
		}
	}()
	if a.Envelope != nil {
		if err = a.Envelope.materialize(ctx, filepath.Join(dir, "resources")); err != nil {
			return process.Output{}, err
		}
		defer func() {
			if err := a.Envelope.VerifySources(ctx); err != nil {
				result.Completed = false
				runErr = err
			}
			if err := a.Envelope.verifyMaterialization(ctx, filepath.Join(dir, "resources")); err != nil {
				result.Completed = false
				runErr = err
			}
		}()
	}
	if err = os.Chmod(dir, 0755); err != nil {
		return process.Output{}, err
	}
	var data []byte
	var primaryCopy os.FileInfo
	if primary != nil {
		if a.Envelope == nil {
			return process.Output{}, errors.New("captured primary requires an input envelope")
		}
		if err := primary.materialize(ctx, filepath.Join(dir, "program"), b.Architecture); err != nil {
			return process.Output{}, err
		}
		primaryCopy, err = os.Lstat(filepath.Join(dir, "program"))
		if err != nil {
			return process.Output{}, err
		}
		defer func() {
			if err := primary.verifySources(ctx); err != nil {
				result.Completed, runErr = false, err
			}
			copyAfter, err := os.Lstat(filepath.Join(dir, "program"))
			if err != nil || !sameProgramInfo(primaryCopy, copyAfter) {
				result.Completed, runErr = false, errors.New("native static executable copy changed")
			} else if err := primary.verifyCopy(filepath.Join(dir, "program")); err != nil {
				result.Completed, runErr = false, err
			}
		}()
		data = primary.data
	} else {
		data, err = readBounded(a.Program, 256<<20)
		if err != nil {
			return process.Output{}, errors.New("native program unavailable")
		}
		if err = os.WriteFile(filepath.Join(dir, "program"), data, 0555); err != nil {
			return process.Output{}, err
		}
	}
	var child *childContract
	if a.Envelope != nil && a.Envelope.child != nil {
		c := a.Envelope.child
		if arch, err := staticChildArchitecture(data); err != nil || arch != b.Architecture {
			return process.Output{}, errors.New("native managed-child main must be static")
		}
		if err := c.materialize(ctx, filepath.Join(dir, "helper"), b.Architecture); err != nil {
			return process.Output{}, err
		}
		defer func() {
			if err := c.verifyCopy(filepath.Join(dir, "helper")); err != nil {
				result.Completed = false
				runErr = err
			}
		}()
		child = c.contract()
	}
	contract, _ := json.Marshal(struct {
		Args         []string       `json:"args"`
		Env          []string       `json:"env"`
		Envelope     bool           `json:"envelope"`
		ScratchBytes int64          `json:"scratch_bytes"`
		Child        *childContract `json:"child,omitempty"`
	}{a.Args, env, a.Envelope != nil, scratchBytes, child})
	if len(contract) > 131072 {
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
	if primary != nil {
		copyBefore, err := os.Lstat(filepath.Join(dir, "program"))
		if err != nil || !sameProgramInfo(primaryCopy, copyBefore) || primary.verifyCopy(filepath.Join(dir, "program")) != nil {
			return process.Output{}, errors.New("native static executable copy changed")
		}
	}
	create := []string{"create", "--label=hayaku.native-boundary=v1", "--pull=never", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges:true", "--user=65534:65534", "--pids-limit=64", "--memory=256m", "--memory-swap=256m", "--workdir=/input", "--entrypoint=/boundary", "--mount", "type=bind,src=" + dir + ",dst=/input,readonly", "--mount", "type=bind,src=" + worker + ",dst=/boundary,readonly"}
	if scratchBytes != 0 {
		create = append(create, "--tmpfs", "/scratch:rw,noexec,nosuid,nodev,mode=0700,uid=65534,gid=65534,size="+strconv.FormatInt(scratchBytes, 10))
	}
	create = append(create, b.Image, "/input/program")
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
		if _, err := command(cleanup, b.Docker, "rm", "--force", "--volumes", id); err != nil {
			result.Completed = false
			runErr = errors.New("native container cleanup failed")
		}
	}()
	marker := boundaryVersion + "\n"
	// Protocol framing has its own fixed bound; it cannot consume the guest's
	// advertised stderr payload allowance.
	result, runErr = process.RunEnvironmentLimits(ctx, model.Command{Executable: b.Docker, Args: []string{"start", "--attach", id}}, "", process.Environment(nil), process.MaxOutput, process.MaxOutput+len(marker))
	attested := strings.HasPrefix(string(result.Stderr), marker)
	if attested {
		result.Stderr = result.Stderr[len(marker):]
	}
	if len(result.Stderr) > process.MaxOutput {
		result.Stderr = result.Stderr[:process.MaxOutput]
		result.Completed = false
		runErr = errors.Join(runErr, errors.New("native stderr payload exceeded limit"))
	}
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
		return result, errors.Join(runErr, ctx.Err(), errors.New("native execution incomplete"))
	}
	exit := state.ExitCode
	result.ExitCode = exit
	// The fixed worker marker is removed before language result decoding. A
	// missing marker means enforcement never reached exec; no receipt is minted.
	if !attested {
		result.Completed = false
		return result, errors.New("native boundary did not attest enforcement")
	}
	if exit != 0 && runErr == nil {
		runErr = errors.New("native test process failed")
	}
	return result, runErr
}

// Execute owns cache authority and lifecycle. Decode is a language boundary;
// qualification is supplied only by reviewed in-process provider code, never
// by a JSON declaration, previous coverage or user result report.
func Execute(ctx context.Context, b Backend, a Artifact, cacheDir string, audit bool, decode func(process.Output, Artifact) (model.Outcomes, error)) (Result, error) {
	return ExecuteWithOptions(ctx, b, a, ExecutionOptions{CacheDir: cacheDir, Audit: audit}, decode)
}

type ExecutionOptions struct {
	CacheDir  string
	Audit     bool
	Authority *passcache.Authority
}

func ExecuteWithOptions(ctx context.Context, b Backend, a Artifact, options ExecutionOptions, decode func(process.Output, Artifact) (model.Outcomes, error)) (Result, error) {
	cacheDir, audit := options.CacheDir, options.Audit
	r := Result{Name: a.Name, Context: b.platform() + "-" + b.Architecture, Qualified: a.authority != nil, Cache: "disabled"}
	// The operational envelope is not a reviewed language/runtime proof. Reject
	// before cache lookup or audit mutation, including caller-supplied Proof.
	if a.Envelope != nil {
		r.Qualified = false
		return r, errors.New("native input envelope has no qualified provider")
	}
	if !r.Qualified || b.Identity == "" || runner.ValidateInventory(a.inventory()) != nil {
		r.Qualified = false
		return r, errors.New("native artifact is not qualified")
	}
	if err := validateCapabilityEffects(a.Effects); err != nil {
		r.Qualified = false
		return r, err
	}
	key, program, err := verifyAuthority(ctx, b, a)
	if err != nil {
		r.Qualified = false
		return r, err
	}
	if err = b.unchanged(ctx); err != nil {
		r.Qualified = false
		return r, err
	}
	r.Key = key
	// A caller cannot substitute a decoder for the reviewed producer boundary.
	decode = a.authority.decode
	if options.Authority != nil {
		if err := options.Authority.Verify(ctx); err != nil {
			return r, err
		}
	}
	cache, cacheErr := passcache.OpenWithAuthority(cacheDir, "hayaku-native-pure-pass-v1", options.Authority)
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
		after, e := fileDigestContext(ctx, a.Program)
		if e != nil || after != program {
			return r, errors.New("native program changed")
		}
		if _, _, e := verifyAuthority(ctx, b, a); e != nil {
			r.Qualified = false
			return r, e
		}
		if options.Authority != nil {
			if err := options.Authority.Verify(ctx); err != nil {
				return r, err
			}
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
	if err == nil {
		err = runner.ValidateExact(r.Outcomes, a.inventory(), output.Completed, output.ExitCode)
	}
	r.Complete = err == nil && output.Completed
	r.Passed = r.Complete && output.ExitCode == 0 && !runner.Failed(r.Outcomes)
	after, e := fileDigestContext(ctx, a.Program)
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
	if _, _, e := verifyAuthority(ctx, b, a); e != nil {
		r.Complete = false
		r.Passed = false
		err = e
	}
	if options.Authority != nil {
		if e := options.Authority.Verify(ctx); e != nil {
			r.Complete, r.Passed = false, false
			_ = cache.Invalidate(r.Key)
			err = e
		}
	}
	if audit && valid && r.Passed && identity(prior.Outcomes) != identity(r.Outcomes) {
		r.Qualified = false
		r.Passed = false
		err = errors.New("native audit outcome mismatch")
	}
	if r.Passed && passing(r.Outcomes, a) {
		if cache != nil && !cache.CanStore() {
			r.Cache = "verification-only"
		} else if e = cache.Store(ctx, r.Key, receipt{Schema: 1, Key: r.Key, Cases: a.Cases, Outcomes: r.Outcomes}); e != nil {
			r.Cache = "store-failed"
			if ctx.Err() != nil {
				r.Complete = false
				r.Passed = false
				return r, ctx.Err()
			}
		}
	}
	if options.Authority != nil {
		if e := options.Authority.Verify(ctx); e != nil {
			r.Complete, r.Passed = false, false
			err = e
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

func validateNativeArguments(args, env []string) error {
	if len(args) > 128 || len(env) > 128 {
		return errors.New("native arguments or environment exceed limits")
	}
	bytes := 0
	for _, list := range [][]string{args, env} {
		for _, value := range list {
			if len(value) > 65536-bytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
				return errors.New("invalid native argument or environment")
			}
			bytes += len(value)
		}
	}
	seen := map[string]bool{}
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || seen[key] {
			return errors.New("invalid native environment key")
		}
		seen[key] = true
	}
	return nil
}
func passing(o model.Outcomes, a Artifact) bool {
	return runner.AllPassed(o, a.inventory())
}

func (a Artifact) inventory() model.ExecutionInventory {
	inventory := model.ExecutionInventory{Schema: model.ExecutionSchema, Units: []model.Unit{{ID: a.Name, Workspace: "native"}}}
	for _, name := range a.Cases {
		inventory.Cases = append(inventory.Cases, model.Case{Unit: a.Name, ID: name})
	}
	return inventory
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
