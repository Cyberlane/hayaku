package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	goprovider "github.com/Cyberlane/hayaku/internal/adapter/golang"
	swiftprovider "github.com/Cyberlane/hayaku/internal/adapter/swift"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/noderuntime"
	"github.com/Cyberlane/hayaku/internal/process"
)

// ToolIdentity binds advisory cache data to actual installed executable bytes,
// not just a mutable PATH name or mtime. Execution always discovers afresh.
func ToolIdentity(executable string) (string, error) { return toolIdentityAt(executable, ".") }

func toolIdentityAt(executable, dir string) (string, error) {
	var path string
	var err error
	if strings.ContainsAny(executable, "/\\") {
		path = executable
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
	} else {
		path, err = exec.LookPath(executable)
	}
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 256<<20 || (runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0) {
		return "", errors.New("unsupported executable type or size")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, 256<<20+1))
	if err != nil {
		return "", err
	}
	if n != info.Size() {
		return "", errors.New("executable changed while hashing")
	}
	return path + ":" + hex.EncodeToString(h.Sum(nil)), nil
}

func toolsIdentity(ctx context.Context, c model.Config, root string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	values := map[string]string{}
	for _, w := range c.Workspaces {
		if w.Adapter == "swift" || w.Adapter == "xcode" {
			id, err := swiftprovider.RuntimeIdentity(ctx, w.SwiftRuntime)
			if err != nil {
				return "", err
			}
			values[w.ID+"\x00swift-runtime"] = id
			for _, args := range [][]string{{"--find", "swift"}, {"--find", "swiftc"}, {"--find", "xcodebuild"}, {"--find", "xcresulttool"}, {"--show-sdk-path"}, {"--show-sdk-version"}} {
				out, err := process.Run(ctx, model.Command{Dir: ".", Executable: "/usr/bin/xcrun", Args: args}, root, c.Context.Env)
				if err != nil {
					return "", errors.New("cannot establish installed Swift compiler/SDK context")
				}
				value := strings.TrimSpace(string(out.Stdout))
				if args[0] == "--find" {
					value, err = toolIdentityAt(value, root)
					if err != nil {
						return "", err
					}
				}
				values[w.ID+"\x00"+strings.Join(args, " ")] = value
			}
		}
		if w.NodeRuntime != nil {
			if err := validateVitestRunner(root, w); err != nil {
				return "", err
			}
			id, err := noderuntime.Identity(ctx, *w.NodeRuntime)
			if err != nil {
				return "", err
			}
			values[w.ID+"\x00node-runtime"] = id.Digest
		}
		commands := append([]model.Command{w.Command}, w.Prerequisites...)
		for _, command := range commands {
			key := filepath.Join(root, w.Root, command.Dir) + "\x00" + command.Executable
			if _, ok := values[key]; ok {
				continue
			}
			id, err := toolIdentityAt(command.Executable, filepath.Join(root, w.Root, command.Dir))
			if err != nil {
				return "", err
			}
			values[key] = id
		}
	}
	return config.Digest(values), nil
}

// EffectiveContextDigest includes persisted native Go settings, privately. Only
// snapshot-root locations are normalized so base/candidate copies can compare.
func EffectiveContextDigest(ctx context.Context, root string, c model.Config) (string, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	native := map[string][]string{}
	for _, w := range c.Workspaces {
		if w.Adapter != "go" {
			continue
		}
		env, err := goprovider.ExecutionEnvironment(ctx, root, w, c.Context)
		if err != nil {
			return "", err
		}
		for i, pair := range env {
			key, value, _ := strings.Cut(pair, "=")
			if key == "GOWORK" && strings.HasPrefix(value, root+string(filepath.Separator)) {
				env[i] = key + "=<repository>" + strings.TrimPrefix(value, root)
			}
		}
		native[w.ID] = env
	}
	return config.Digest(struct {
		Ambient string
		Native  map[string][]string
	}{ContextDigest(c.Context), native}), nil
}
