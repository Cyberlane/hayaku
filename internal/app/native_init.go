package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/native"
)

type nativePackageFlags []string

type nativeEnrollmentFile struct {
	info   os.FileInfo
	digest [32]byte
}

func sameEnrollmentFile(a, b os.FileInfo) bool {
	return a != nil && b != nil && a.Mode().IsRegular() && b.Mode().IsRegular() && os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func rollbackNativeEnrollment(owner *os.Root, created map[string]nativeEnrollmentFile) error {
	var result error
	for name, retained := range created {
		current, err := owner.Lstat(name)
		if err == nil && sameEnrollmentFile(retained.info, current) {
			file, openErr := owner.Open(name)
			if openErr == nil {
				opened, statErr := file.Stat()
				var data []byte
				if statErr == nil && sameEnrollmentFile(retained.info, opened) {
					data, statErr = io.ReadAll(io.LimitReader(file, retained.info.Size()+1))
				} else if statErr == nil {
					statErr = errors.New("enrollment opened file changed")
				}
				after, afterErr := file.Stat()
				closeErr := file.Close()
				current, err = owner.Lstat(name)
				if errors.Join(statErr, afterErr, closeErr, err) == nil && sameEnrollmentFile(retained.info, after) && sameEnrollmentFile(retained.info, current) && sha256.Sum256(data) == retained.digest {
					result = errors.Join(result, owner.Remove(name))
					continue
				}
			}
		}
		result = errors.Join(result, errors.New("enrollment file ownership or content changed; preserved foreign changes"))
	}
	return result
}

func (p *nativePackageFlags) String() string { return strings.Join(*p, ",") }
func (p *nativePackageFlags) Set(value string) error {
	if value != "." && (!strings.HasPrefix(value, "./") || !fs.ValidPath(strings.TrimPrefix(value, "./")) || strings.Contains(value, "...") || strings.ContainsAny(value, "\\:")) {
		return errors.New("packages must be explicit relative directories")
	}
	*p = append(*p, value)
	return nil
}

// Enrollment only writes new local configuration. Installation, runtime
// qualification and receipt authority are checked by the execution producer.
func runNativeInit(ctx context.Context, args []string, out, errout io.Writer) int {
	flags := flag.NewFlagSet("native-init", flag.ContinueOnError)
	flags.SetOutput(errout)
	rootFlag := flags.String("root", ".", "existing source directory")
	name := flags.String("config", "native.json", "new configuration file in source root")
	language := flags.String("language", "go", "go or vitest")
	id := flags.String("workspace", "tests", "workspace identity")
	platform := flags.String("platform", runtime.GOOS, "native Go platform; Vitest uses Linux")
	arch := flags.String("architecture", runtime.GOARCH, "native architecture")
	image := flags.String("image", "", "already installed immutable Linux image ID")
	node := flags.String("node", "", "already installed host source-certifier Node")
	linuxNode := flags.String("runtime-node", "", "already installed Linux ARM64 Node")
	modules := flags.String("modules", "", "already installed fixed Vitest dependency tree")
	sdk := flags.String("typescript-sdk", "", "already installed TypeScript package directory")
	loader := flags.String("loader", "", "already provisioned Linux ELF loader")
	libraryDir := flags.String("library-dir", "", "already provisioned Linux shared library directory")
	addon := flags.String("addon", "", "already provisioned Linux Rolldown addon")
	var packages nativePackageFlags
	flags.Var(&packages, "package", "explicit Go test package; repeat for each package")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 || !config.Relative(*name) || filepath.Base(*name) != *name || *name == "package.json" || *name == native.VitestProfileFile || (*language != "go" && *language != "vitest") {
		return failure(errout, errors.New("invalid native enrollment arguments"))
	}
	if err := ctx.Err(); err != nil {
		return failure(errout, err)
	}
	root, err := filepath.Abs(*rootFlag)
	if err != nil {
		return failure(errout, err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return failure(errout, err)
	}
	owner, err := os.OpenRoot(root)
	if err != nil {
		return failure(errout, err)
	}
	defer owner.Close()
	w := native.Workspace{Workspace: model.Workspace{ID: *id, Root: ".", Adapter: *language}}
	c := native.RunConfig{Config: native.Config{Schema: 1, Platform: *platform, Architecture: *arch, Image: *image}}
	if *language == "go" && ((c.Platform != "linux" && c.Platform != "darwin") || (c.Architecture != "arm64" && c.Architecture != "amd64") || (c.Platform == "darwin" && c.Image != "")) {
		return failure(errout, errors.New("unsupported native Go platform or architecture"))
	}
	files := map[string][]byte{}
	if *language == "go" {
		if len(packages) == 0 {
			return failure(errout, errors.New("at least one explicit Go --package is required"))
		}
		tool, err := exec.LookPath("go")
		if err != nil {
			return failure(errout, errors.New("installed Go executable required"))
		}
		tool, err = filepath.EvalSymlinks(tool)
		if err != nil {
			return failure(errout, err)
		}
		w.Patterns = append([]string(nil), packages...)
		w.Command = model.Command{Dir: ".", Executable: tool, Args: append([]string{"test", "-count=1"}, packages...)}
	} else {
		if len(packages) != 0 {
			return failure(errout, errors.New("Go packages cannot configure a Vitest workspace"))
		}
		for _, path := range []*string{node, linuxNode, modules, sdk, loader, libraryDir, addon} {
			if *path == "" || !filepath.IsAbs(*path) || filepath.Clean(*path) != *path || strings.ContainsAny(*path, ",\x00\r\n") {
				return failure(errout, errors.New("Vitest enrollment requires clean absolute installed input paths"))
			}
			if _, err := os.Stat(*path); err != nil {
				return failure(errout, errors.New("Vitest enrollment input unavailable; provision it separately"))
			}
		}
		if *image == "" {
			return failure(errout, errors.New("Vitest enrollment requires an already installed immutable image"))
		}
		docker, err := native.ResolveDocker()
		if err != nil {
			return failure(errout, err)
		}
		c.Platform, c.Architecture = "linux", "arm64"
		w.NodeRuntime = &model.NodeRuntime{Node: *node, Modules: *modules}
		w.Vitest = &native.VitestRuntimeInputs{Node: *linuxNode, TypeScriptSDK: *sdk, Loader: *loader, Addon: *addon}
		for _, role := range []string{"libdl.so.2", "libstdc++.so.6", "libm.so.6", "libgcc_s.so.1", "libpthread.so.0", "libc.so.6"} {
			path := filepath.Join(*libraryDir, role)
			if _, err := os.Stat(path); err != nil {
				return failure(errout, errors.New("Vitest shared library role unavailable"))
			}
			w.Vitest.Libraries = append(w.Vitest.Libraries, native.RuntimeLibrary{Name: role, Path: path})
		}
		w.Command = native.VitestOriginalCommand(docker, root, *modules, *linuxNode, *addon, *image)
		files[native.VitestProfileFile] = []byte(native.VitestProfile)
		if info, err := owner.Lstat("package.json"); err == nil {
			if !info.Mode().IsRegular() || info.Size() != int64(len(native.VitestPackage)) {
				return failure(errout, errors.New("Vitest profile needs its documented minimal package.json; existing file was preserved"))
			}
			file, err := owner.Open("package.json")
			if err != nil {
				return failure(errout, err)
			}
			opened, err := file.Stat()
			var data []byte
			if err == nil && os.SameFile(info, opened) {
				data, err = io.ReadAll(io.LimitReader(file, int64(len(native.VitestPackage)+1)))
			}
			err = errors.Join(err, file.Close())
			if err != nil || string(data) != native.VitestPackage {
				return failure(errout, errors.New("Vitest package context unavailable or changed; existing file was preserved"))
			}
		} else if os.IsNotExist(err) {
			files["package.json"] = []byte(native.VitestPackage)
		} else {
			return failure(errout, err)
		}
	}
	c.Workspaces = []native.Workspace{w}
	if err := config.Validate(model.Config{Schema: 1, Context: model.Context{ID: "native", OS: runtime.GOOS, Arch: runtime.GOARCH}, Workspaces: []model.Workspace{w.Workspace}}); err != nil {
		return failure(errout, err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return failure(errout, err)
	}
	files[*name] = append(data, '\n')
	// Preflight every name before creating any file; O_EXCL also closes races.
	for path := range files {
		if _, err := owner.Lstat(path); !os.IsNotExist(err) {
			return failure(errout, errors.New("native enrollment target exists or is unavailable; no files overwritten"))
		}
	}
	created := map[string]nativeEnrollmentFile{}
	var names []string
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, path := range names {
		data := files[path]
		file, err := owner.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return failure(errout, errors.Join(err, rollbackNativeEnrollment(owner, created)))
		}
		info, err := file.Stat()
		if err == nil {
			created[path] = nativeEnrollmentFile{info, sha256.Sum256(nil)}
			var written int
			written, err = file.Write(data)
			after, statErr := file.Stat()
			if statErr == nil && os.SameFile(info, after) && info.Mode() == after.Mode() {
				created[path] = nativeEnrollmentFile{after, sha256.Sum256(data[:written])}
			} else {
				statErr = errors.Join(statErr, errors.New("created enrollment file changed"))
			}
			err = errors.Join(err, statErr)
		}
		err = errors.Join(err, file.Close())
		if err != nil {
			return failure(errout, errors.Join(err, rollbackNativeEnrollment(owner, created)))
		}
	}
	fmt.Fprintln(out, "Created native configuration. Run hayaku native with this config to verify qualification.")
	return 0
}
