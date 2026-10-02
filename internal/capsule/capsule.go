// Package capsule executes a separate, deterministic WASI suite contract.
// It does not qualify or replace an equivalent native test command. Guests
// receive only copied inputs, explicit arguments/environment, logical clocks,
// and seeded entropy; successful results can be reused within that contract.
package capsule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	BackendVersion   = "hayaku-wasi-preview1-v1-wazero1.12.0-interpreter"
	MaxModuleBytes   = 64 << 20
	MaxInputBytes    = 64 << 20
	MaxInputFiles    = 4096
	MaxArgumentBytes = 1 << 20
	MaxOutputBytes   = 16 << 20
)

// File is an immutable guest-visible regular file or explicit directory. Its
// directory entries, memory-FS modes (0444/0555), timestamps (Unix epoch), and
// absent siblings are fixed. WASI preview1 does not expose POSIX permissions.
type File struct {
	Path      string `json:"path"`
	Data      []byte `json:"-"`
	Directory bool   `json:"directory,omitempty"`
}

type Variable struct {
	Key   string `json:"key"`
	Value string `json:"-"`
}

// Request describes the actual WASI artifact, not a native-suite assertion.
// ProducerIdentity binds a caller-verified compilation/source context. It is
// an additional invalidation input, never evidence of native equivalence.
type Request struct {
	Name             string
	Module           []byte
	Files            []File
	Args             []string
	Env              []Variable
	ProducerIdentity string
	Seed             string
}

// Limits bind deterministic memory/output limits and an operational timeout.
// Host deadlines and cancellations are not guest semantics: they never
// produce a reusable result, and a hit is not a prediction of host-load timing.
type Limits struct {
	Timeout     time.Duration `json:"timeout_ns"`
	MemoryPages uint32        `json:"memory_pages"`
	OutputBytes int64         `json:"output_bytes"`
}

type Options struct {
	CacheDir string
	Limits   Limits
	// NoReuse executes an audit even if a valid receipt exists. The previous
	// receipt is invalidated before the run, so an interrupted/failed audit
	// cannot leave the old passing result authorized.
	NoReuse bool
}

func DefaultLimits() Limits {
	return Limits{Timeout: 30 * time.Second, MemoryPages: 4096, OutputBytes: 1 << 20}
}

// Result deliberately contains digests and fixed diagnostics, not file
// contents, raw environment values, guest output, or host paths.
type Result struct {
	Schema        int      `json:"schema"`
	Name          string   `json:"name"`
	Mode          string   `json:"mode"`
	Key           string   `json:"key"`
	ModuleDigest  string   `json:"module_digest"`
	InputDigest   string   `json:"input_digest"`
	BackendDigest string   `json:"backend_digest"`
	Complete      bool     `json:"complete"`
	Qualified     bool     `json:"qualified"`
	Passed        bool     `json:"passed"`
	ExitCode      uint32   `json:"exit_code"`
	Reason        string   `json:"reason"`
	Gaps          []string `json:"gaps"`
	InputFiles    int      `json:"input_files"`
	StdoutDigest  string   `json:"stdout_digest"`
	StderrDigest  string   `json:"stderr_digest"`
	StdoutBytes   int64    `json:"stdout_bytes"`
	StderrBytes   int64    `json:"stderr_bytes"`
	CacheStatus   string   `json:"cache_status"`
}

type identityFile struct {
	Path      string `json:"path"`
	Digest    string `json:"digest"`
	Size      int    `json:"size"`
	Directory bool   `json:"directory,omitempty"`
}

type identityVariable struct {
	Key    string `json:"key"`
	Digest string `json:"digest"`
}

type requestIdentity struct {
	Contract    string             `json:"contract"`
	Name        string             `json:"name"`
	Module      string             `json:"module"`
	Files       []identityFile     `json:"files"`
	Args        []string           `json:"args"`
	Environment []identityVariable `json:"environment"`
	Producer    string             `json:"producer"`
	Seed        string             `json:"seed"`
	Limits      Limits             `json:"limits"`
	Backend     string             `json:"backend"`
}

func normalize(ctx context.Context, request Request, limits Limits) (Request, Limits, error) {
	if err := ctx.Err(); err != nil {
		return Request{}, Limits{}, err
	}
	if request.Name == "" || len(request.Name) > 128 || !utf8.ValidString(request.Name) || strings.IndexFunc(request.Name, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
		return Request{}, Limits{}, fmt.Errorf("capsule suite name is invalid")
	}
	if len(request.Module) < 8 || len(request.Module) > MaxModuleBytes {
		return Request{}, Limits{}, fmt.Errorf("capsule module exceeds size bounds")
	}
	if !validDigest(request.ProducerIdentity) {
		return Request{}, Limits{}, fmt.Errorf("capsule compilation identity must be a SHA256 digest")
	}
	if request.Seed == "" {
		request.Seed = strings.Repeat("0", 64)
	}
	if !validDigest(request.Seed) {
		return Request{}, Limits{}, fmt.Errorf("capsule seed must be a SHA256 digest")
	}
	if limits.Timeout == 0 {
		limits.Timeout = 30 * time.Second
	}
	if limits.MemoryPages == 0 {
		limits.MemoryPages = 4096
	}
	if limits.OutputBytes == 0 {
		limits.OutputBytes = 1 << 20
	}
	if limits.Timeout <= 0 || limits.Timeout > 10*time.Minute || limits.MemoryPages > 8192 || limits.OutputBytes < 1 || limits.OutputBytes > MaxOutputBytes {
		return Request{}, Limits{}, fmt.Errorf("capsule resource limits are invalid")
	}
	if len(request.Args) == 0 || len(request.Args) > 1024 || request.Args[0] == "" || len(request.Env) > 256 || len(request.Files) > MaxInputFiles {
		return Request{}, Limits{}, fmt.Errorf("capsule argument or input bounds exceeded")
	}
	argumentBytes := 0
	request.Args = append([]string(nil), request.Args...)
	for _, arg := range request.Args {
		argumentBytes += len(arg)
		if !utf8.ValidString(arg) || strings.ContainsRune(arg, 0) {
			return Request{}, Limits{}, fmt.Errorf("capsule argument contains NUL")
		}
	}
	request.Env = append([]Variable(nil), request.Env...)
	sort.Slice(request.Env, func(i, j int) bool { return request.Env[i].Key < request.Env[j].Key })
	for i, variable := range request.Env {
		argumentBytes += len(variable.Key) + len(variable.Value)
		if variable.Key == "" || !utf8.ValidString(variable.Key) || !utf8.ValidString(variable.Value) || strings.ContainsAny(variable.Key, "=\x00") || strings.ContainsRune(variable.Value, 0) || (i > 0 && request.Env[i-1].Key == variable.Key) {
			return Request{}, Limits{}, fmt.Errorf("capsule environment is invalid")
		}
	}
	if argumentBytes > MaxArgumentBytes {
		return Request{}, Limits{}, fmt.Errorf("capsule arguments/environment exceed size bound")
	}
	request.Module = append([]byte(nil), request.Module...)
	request.Files = append([]File(nil), request.Files...)
	sort.Slice(request.Files, func(i, j int) bool { return request.Files[i].Path < request.Files[j].Path })
	total := 0
	names := make(map[string]bool, len(request.Files))
	for i := range request.Files {
		file := &request.Files[i]
		_, exists := names[file.Path]
		if !validInputPath(file.Path) || exists || (file.Directory && len(file.Data) != 0) {
			return Request{}, Limits{}, fmt.Errorf("capsule file path is invalid or duplicated")
		}
		for parent := path.Dir(file.Path); parent != "."; parent = path.Dir(parent) {
			if names[parent] {
				return Request{}, Limits{}, fmt.Errorf("capsule file conflicts with a directory")
			}
		}
		names[file.Path] = !file.Directory
		total += len(file.Data)
		if total > MaxInputBytes {
			return Request{}, Limits{}, fmt.Errorf("capsule file inputs exceed size bound")
		}
		file.Data = append([]byte(nil), file.Data...)
	}
	return request, limits, ctx.Err()
}

func validDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validInputPath(name string) bool {
	return name != "." && len(name) <= 4096 && utf8.ValidString(name) && fs.ValidPath(name) && !strings.ContainsAny(name, "\\:\x00")
}

func digest(data []byte) string {
	value := sha256.Sum256(data)
	return hex.EncodeToString(value[:])
}

func identities(request Request, limits Limits, backend string) (key, inputs string) {
	files := make([]identityFile, len(request.Files))
	for i, file := range request.Files {
		files[i] = identityFile{Path: file.Path, Digest: digest(file.Data), Size: len(file.Data), Directory: file.Directory}
	}
	environment := make([]identityVariable, len(request.Env))
	for i, variable := range request.Env {
		environment[i] = identityVariable{Key: variable.Key, Digest: digest([]byte(variable.Value))}
	}
	fileBytes, _ := json.Marshal(files)
	inputDigest := digest(fileBytes)
	identity := requestIdentity{Contract: BackendVersion, Name: request.Name, Module: digest(request.Module), Files: files, Args: request.Args, Environment: environment, Producer: request.ProducerIdentity, Seed: request.Seed, Limits: limits, Backend: backend}
	data, _ := json.Marshal(identity)
	return digest(data), inputDigest
}
