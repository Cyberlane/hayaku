package capsule

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

const maxHostCalls = 1000000

// Run qualifies only complete functional outcomes of this WASI contract.
// Operational deadline, cancellation, platform exhaustion, traps and denied
// capabilities cannot authorize reuse. A cache hit never certifies the outcome
// or wall-clock timing of another native command or another host workload.
func Run(ctx context.Context, request Request, options Options) (Result, error) {
	request, limits, err := normalize(ctx, request, options.Limits)
	if err != nil {
		return Result{}, err
	}
	executable, err := currentExecutableDigest(ctx)
	if err != nil {
		return Result{}, err
	}
	backend := digest([]byte(BackendVersion + "\x00" + executable + "\x00" + runtime.Version() + "\x00" + runtime.GOOS + "/" + runtime.GOARCH))
	key, inputs := identities(request, limits, backend)
	result := Result{Schema: 1, Name: request.Name, Mode: "executed", Key: key, ModuleDigest: digest(request.Module), InputDigest: inputs, BackendDigest: backend, InputFiles: len(request.Files), Gaps: []string{}, Reason: "execution-incomplete"}
	cache, cacheErr := openPassCache(options.CacheDir)
	if cache != nil {
		defer cache.root.Close()
	}
	previous, status := cache.load(result, limits.OutputBytes)
	if cacheErr != nil {
		status = "unavailable"
	}
	result.CacheStatus = status
	if status == "hit" && !options.NoReuse {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		current, err := currentExecutableDigest(ctx)
		if err != nil || current != executable {
			return result, fmt.Errorf("capsule backend identity changed")
		}
		result.Mode, result.Complete, result.Qualified, result.Passed = "reused", true, true, true
		result.Reason = "authenticated-complete-pass"
		result.StdoutDigest, result.StderrDigest = previous.StdoutDigest, previous.StderrDigest
		result.StdoutBytes, result.StderrBytes = previous.StdoutBytes, previous.StderrBytes
		return result, nil
	}
	if options.NoReuse {
		if err := cache.invalidate(key); err != nil {
			return result, fmt.Errorf("capsule audit cannot invalidate the prior pass")
		}
		result.CacheStatus = "audit"
	} else if status == "invalid" {
		// A malformed object is never used as pass evidence. Failure to remove
		// it cannot turn a later fresh run into a cache hit.
		_ = cache.invalidate(key)
	}
	result, err = execute(ctx, request, limits, result)
	if err != nil {
		return result, err
	}
	if !result.Complete || !result.Qualified || !result.Passed {
		return result, nil
	}
	current, identityErr := currentExecutableDigest(ctx)
	if identityErr != nil || current != executable {
		result.Complete, result.Qualified, result.Passed = false, false, false
		result.Reason, result.Gaps = "backend-changed", []string{"backend-changed"}
		return result, fmt.Errorf("capsule backend identity changed")
	}
	if options.NoReuse && status == "hit" && (previous.StdoutDigest != result.StdoutDigest || previous.StderrDigest != result.StderrDigest || previous.StdoutBytes != result.StdoutBytes || previous.StderrBytes != result.StderrBytes) {
		result.Qualified, result.Passed = false, false
		result.Reason, result.Gaps = "audit-outcome-mismatch", []string{"audit-outcome-mismatch"}
		return result, fmt.Errorf("capsule audit did not reproduce the authenticated outcome")
	}
	if cache != nil {
		if err := cache.store(ctx, result); err != nil {
			result.CacheStatus = "store-failed"
			if ctx.Err() != nil {
				result.Complete, result.Qualified, result.Passed = false, false, false
				result.Reason = "cancelled"
				return result, ctx.Err()
			}
		} else {
			result.CacheStatus = "stored"
		}
	}
	return result, nil
}

func execute(ctx context.Context, request Request, limits Limits, result Result) (Result, error) {
	executionContext, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	guard := &hostGuard{}
	executionContext = experimental.WithFunctionListenerFactory(executionContext, guard)
	config := wazero.NewRuntimeConfigInterpreter().WithCoreFeatures(api.CoreFeaturesV2).WithMemoryLimitPages(limits.MemoryPages).WithCloseOnContextDone(true)
	r := wazero.NewRuntimeWithConfig(executionContext, config)
	defer r.Close(context.Background())
	bounded, err := boundModuleTables(request.Module)
	if err != nil {
		result.Reason, result.Gaps = "unsupported-module-interface", []string{"unsupported-module-interface"}
		return result, err
	}
	compiled, err := r.CompileModule(executionContext, bounded)
	if err != nil {
		result.Reason = "module-invalid"
		return result, fmt.Errorf("capsule module could not be compiled")
	}
	defer compiled.Close(context.Background())
	if err := validateImports(compiled); err != nil {
		result.Reason, result.Gaps = "unsupported-module-interface", []string{"unsupported-module-interface"}
		return result, err
	}
	if _, err := wasi_snapshot_preview1.Instantiate(executionContext, r); err != nil {
		return result, fmt.Errorf("capsule WASI host could not be initialized")
	}
	clock := &logicalClock{guard: guard}
	seed, _ := hex.DecodeString(request.Seed)
	entropy := &seededReader{seed: seed}
	budget := &outputBudget{remaining: limits.OutputBytes, guard: guard}
	stdout := &digestWriter{hash: sha256.New(), budget: budget}
	stderr := &digestWriter{hash: sha256.New(), budget: budget}
	moduleConfig := wazero.NewModuleConfig().WithName("hayaku-capsule-guest").WithArgs(request.Args...).WithFS(newMemoryFS(request.Files)).WithStdin(strings.NewReader("")).WithStdout(stdout).WithStderr(stderr).
		WithWalltime(clock.walltime, sys.ClockResolution(1000000)).WithNanotime(clock.nanotime, sys.ClockResolution(1000000)).WithNanosleep(clock.sleep).WithOsyield(func() {}).WithRandSource(entropy)
	for _, variable := range request.Env {
		moduleConfig = moduleConfig.WithEnv(variable.Key, variable.Value)
	}
	_, runErr := r.InstantiateModule(executionContext, compiled, moduleConfig)
	result.StdoutDigest, result.StderrDigest = hex.EncodeToString(stdout.hash.Sum(nil)), hex.EncodeToString(stderr.hash.Sum(nil))
	result.StdoutBytes, result.StderrBytes = stdout.count, stderr.count
	if executionContext.Err() != nil {
		result.Reason, result.Gaps = "cancelled-or-timeout", []string{"operational-cancellation"}
		return result, executionContext.Err()
	}
	if guard.violation != "" {
		result.Reason, result.Gaps = guard.violation, []string{guard.violation}
		return result, fmt.Errorf("capsule attempted an unsupported capability")
	}
	var exit *sys.ExitError
	if runErr != nil && !errors.As(runErr, &exit) {
		result.Reason, result.Gaps = "guest-trap", []string{"guest-trap"}
		return result, fmt.Errorf("capsule guest trapped")
	}
	if exit != nil {
		result.ExitCode = exit.ExitCode()
	}
	result.Complete, result.Qualified = true, true
	result.Passed = result.ExitCode == 0
	if result.Passed {
		result.Reason = "complete-pass"
	} else {
		result.Reason = "complete-failure"
	}
	return result, nil
}

// The complete exported WASI preview1 interface is pinned, including functions
// compilers commonly import but never call. Dangerous calls trap before the
// built-in host function; the guest cannot catch an errno and obtain a receipt.
var wasiFunctions = map[string]bool{
	"args_get": true, "args_sizes_get": true, "environ_get": true, "environ_sizes_get": true,
	"clock_res_get": true, "clock_time_get": true, "random_get": true, "sched_yield": true,
	"fd_advise": true, "fd_allocate": true, "fd_close": true, "fd_datasync": true,
	"fd_fdstat_get": true, "fd_fdstat_set_flags": true, "fd_fdstat_set_rights": true,
	"fd_filestat_get": true, "fd_filestat_set_size": true, "fd_filestat_set_times": true,
	"fd_pread": true, "fd_prestat_get": true, "fd_prestat_dir_name": true, "fd_pwrite": true,
	"fd_read": true, "fd_readdir": true, "fd_renumber": true, "fd_seek": true, "fd_sync": true,
	"fd_tell": true, "fd_write": true, "path_create_directory": true, "path_filestat_get": true,
	"path_filestat_set_times": true, "path_link": true, "path_open": true, "path_readlink": true,
	"path_remove_directory": true, "path_rename": true, "path_symlink": true, "path_unlink_file": true,
	"poll_oneoff": true, "proc_exit": true, "proc_raise": true,
	"sock_accept": true, "sock_recv": true, "sock_send": true, "sock_shutdown": true,
}

func validateImports(module wazero.CompiledModule) error {
	if len(module.ImportedMemories()) != 0 {
		return fmt.Errorf("capsule cannot import external memory")
	}
	for _, function := range module.ImportedFunctions() {
		name, field, imported := function.Import()
		if !imported || name != wasi_snapshot_preview1.ModuleName || !wasiFunctions[field] {
			return fmt.Errorf("capsule imports an unsupported host interface")
		}
	}
	start, found := module.ExportedFunctions()["_start"]
	if !found || len(start.ParamTypes()) != 0 || len(start.ResultTypes()) != 0 || module.ExportedMemories()["memory"] == nil {
		return fmt.Errorf("capsule requires a WASI command entry point and private memory")
	}
	return nil
}

type hostGuard struct {
	violation string
	calls     uint64
}

func (h *hostGuard) deny(reason string) {
	if h.violation == "" {
		h.violation = reason
	}
	panic(fmt.Errorf("capsule capability denied"))
}

func (h *hostGuard) NewFunctionListener(definition api.FunctionDefinition) experimental.FunctionListener {
	if definition.ModuleName() != wasi_snapshot_preview1.ModuleName {
		return nil
	}
	return experimental.FunctionListenerFunc(h.before)
}

func (h *hostGuard) before(_ context.Context, module api.Module, definition api.FunctionDefinition, params []uint64, _ experimental.StackIterator) {
	h.calls++
	if h.calls > maxHostCalls {
		h.deny("host-call-bound-exceeded")
	}
	name := definition.Name()
	switch name {
	case "fd_allocate", "fd_datasync", "fd_fdstat_set_rights", "fd_filestat_set_size", "fd_filestat_set_times", "fd_pwrite", "fd_renumber", "fd_sync", "path_create_directory", "path_filestat_set_times", "path_link", "path_readlink", "path_remove_directory", "path_rename", "path_symlink", "path_unlink_file":
		h.deny("filesystem-mutation-or-unsupported-operation")
	case "fd_fdstat_set_flags":
		// Go initializes stdio (and sometimes regular files) as nonblocking.
		// These are in-memory descriptors, never host file handles. Permit
		// NONBLOCK=4 or clearing that flag; append/sync requests are denied.
		if len(params) != 2 || params[1]&^uint64(4) != 0 {
			h.deny("filesystem-mutation-or-unsupported-operation")
		}
	case "sock_accept", "sock_recv", "sock_send", "sock_shutdown", "proc_raise":
		h.deny("network-or-process-capability")
	case "fd_write":
		if len(params) == 0 || (uint32(params[0]) != 1 && uint32(params[0]) != 2) {
			h.deny("filesystem-write-capability")
		}
	case "path_open":
		// WASI OFLAG_CREAT=1, EXCL=4, TRUNC=8; FD_WRITE right=64.
		// Go advertises other unused mutation rights for readonly opens;
		// actual mutation entry points are independently denied above.
		if len(params) != 9 || params[4]&13 != 0 || params[5]&64 != 0 || params[7]&1 != 0 {
			h.deny("filesystem-write-capability")
		}
	case "poll_oneoff":
		h.validatePoll(module, params)
	case "clock_res_get", "clock_time_get":
		if len(params) == 0 || params[0] > 1 {
			h.deny("unsupported-clock-operation")
		}
	}
}

func (h *hostGuard) validatePoll(module api.Module, params []uint64) {
	if len(params) != 4 || params[2] == 0 || params[2] > 4096 {
		h.deny("unsupported-poll-operation")
	}
	data, ok := module.Memory().Read(uint32(params[0]), uint32(params[2])*48)
	if !ok {
		return // The builtin deterministically returns EFAULT without access.
	}
	for offset := 0; offset < len(data); offset += 48 {
		// Only relative virtual realtime/monotonic-clock subscriptions are
		// qualified; no host descriptors, absolute/CPU clocks or poll wait.
		if data[offset+8] != 0 || binary.LittleEndian.Uint32(data[offset+16:]) > 1 || binary.LittleEndian.Uint16(data[offset+40:]) != 0 || binary.LittleEndian.Uint64(data[offset+24:]) > 1e18 {
			h.deny("unsupported-poll-operation")
		}
	}
}

type logicalClock struct {
	nanoseconds int64
	guard       *hostGuard
}

func (l *logicalClock) advance(delta int64) int64 {
	if delta < 0 || delta > 1e18-l.nanoseconds {
		l.guard.deny("logical-clock-bound-exceeded")
	}
	l.nanoseconds += delta
	return l.nanoseconds
}

func (l *logicalClock) walltime() (int64, int32) {
	ns := l.advance(int64(time.Millisecond))
	return ns / int64(time.Second), int32(ns % int64(time.Second))
}

func (l *logicalClock) nanotime() int64 {
	return l.advance(int64(time.Millisecond))
}

func (l *logicalClock) sleep(ns int64) { l.advance(ns) }

// This SHA256 counter stream is deterministic guest entropy, not a source for
// passwords, secrets or cryptographic key generation. Each run starts afresh.
type seededReader struct {
	seed    []byte
	counter uint64
	block   []byte
}

func (s *seededReader) Read(output []byte) (int, error) {
	count := len(output)
	for len(output) > 0 {
		if len(s.block) == 0 {
			var counter [8]byte
			binary.LittleEndian.PutUint64(counter[:], s.counter)
			hash := sha256.New()
			hash.Write(s.seed)
			hash.Write(counter[:])
			s.block = hash.Sum(nil)
			s.counter++
		}
		n := copy(output, s.block)
		s.block, output = s.block[n:], output[n:]
	}
	return count, nil
}

type outputBudget struct {
	remaining int64
	guard     *hostGuard
}

type digestWriter struct {
	hash   hash.Hash
	budget *outputBudget
	count  int64
}

func (d *digestWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > d.budget.remaining {
		d.budget.guard.violation = "output-bound-exceeded"
		return 0, io.ErrShortWrite
	}
	d.budget.remaining -= int64(len(data))
	d.count += int64(len(data))
	return d.hash.Write(data)
}
