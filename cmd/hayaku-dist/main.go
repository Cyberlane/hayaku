// hayaku-dist builds local archives. It neither signs nor publishes releases.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Cyberlane/hayaku/internal/adapter"
	"github.com/Cyberlane/hayaku/internal/app"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

type target struct{ os, arch string }

var targets = []target{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}, {"windows", "amd64"}, {"windows", "arm64"}}

type archiveRecord struct {
	File     string `json:"file"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Verified string `json:"verified"`
}

type manifest struct {
	Schema         int                  `json:"schema"`
	PlanSchema     int                  `json:"plan_schema"`
	Version        string               `json:"version"`
	Source         string               `json:"source"`
	SourceEpoch    int64                `json:"source_date_epoch"`
	GoTool         string               `json:"go_tool"`
	AdapterVersion map[string]string    `json:"adapter_implementation_versions"`
	Capabilities   []adapter.Capability `json:"capabilities"`
	Archives       []archiveRecord      `json:"archives"`
	Qualification  string               `json:"qualification"`
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("hayaku-dist", flag.ContinueOnError)
	rootFlag := flags.String("root", ".", "clean committed repository root")
	destFlag := flags.String("output", "", "nonexisting output directory outside the repository")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *destFlag == "" {
		return errors.New("usage: hayaku-dist --root PATH --output NONEXISTING-DIRECTORY")
	}
	root, err := filepath.Abs(*rootFlag)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	destination, err := filepath.Abs(*destFlag)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return errors.New("output parent must already exist")
	}
	destination = filepath.Join(parent, filepath.Base(destination))
	if relative, err := filepath.Rel(root, destination); err != nil || relative == "." || filepath.IsLocal(relative) {
		return errors.New("distribution output must be outside the source repository")
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return errors.New("distribution output already exists or cannot be inspected")
	}
	head, err := native(ctx, root, "git", "--no-replace-objects", "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	id := strings.TrimSpace(string(head.Stdout))
	if err := snapshot.ValidateCandidate(ctx, root, id); err != nil {
		return err
	}
	commitTime, err := native(ctx, root, "git", "--no-replace-objects", "show", "--no-patch", "--format=%ct", id)
	if err != nil {
		return err
	}
	epoch, err := strconv.ParseInt(strings.TrimSpace(string(commitTime.Stdout)), 10, 64)
	if err != nil || epoch < 0 {
		return errors.New("invalid source commit timestamp")
	}
	goTool, err := native(ctx, root, "go", "version")
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".hayaku-dist-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	m := manifest{Schema: 1, PlanSchema: model.Schema, Version: app.Version, Source: id, SourceEpoch: epoch, GoTool: strings.TrimSpace(string(goTool.Stdout)), AdapterVersion: adapter.ImplementationVersions(), Capabilities: adapter.Capabilities(), Qualification: "Cross-compilation and archive validation only; native downloaded-artifact acceptance is not established", Archives: []archiveRecord{}}
	entries, err := documentation(root)
	if err != nil {
		return err
	}
	for _, platform := range targets {
		name := "hayaku"
		if platform.os == "windows" {
			name += ".exe"
		}
		binary := filepath.Join(stage, name)
		cmd := model.Command{Executable: "go", Args: []string{"build", "-mod=readonly", "-trimpath", "-buildvcs=true", "-ldflags=-buildid=", "-o", binary, "./cmd/hayaku"}}
		env := map[string]string{"GOOS": platform.os, "GOARCH": platform.arch, "CGO_ENABLED": "0", "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOWORK": "off", "GOFLAGS": "", "SOURCE_DATE_EPOCH": strconv.FormatInt(epoch, 10)}
		if _, err := process.Run(ctx, cmd, root, env); err != nil {
			return fmt.Errorf("cross-build %s/%s: %w", platform.os, platform.arch, err)
		}
		if err := verifyBuild(binary, id, platform); err != nil {
			return err
		}
		data, err := os.ReadFile(binary)
		if err != nil {
			return err
		}
		if err := os.Remove(binary); err != nil {
			return err
		}
		files := append([]archiveEntry{}, entries...)
		files = append(files, archiveEntry{Name: name, Data: data, Mode: 0755})
		filename := "hayaku-" + platform.os + "-" + platform.arch + ".tar.gz"
		if platform.os == "windows" {
			filename = "hayaku-" + platform.os + "-" + platform.arch + ".zip"
		}
		archivePath := filepath.Join(stage, filename)
		if err := writeArchive(archivePath, files, time.Unix(epoch, 0).UTC(), platform.os == "windows"); err != nil {
			return err
		}
		file, err := os.Open(archivePath)
		if err != nil {
			return err
		}
		h := sha256.New()
		size, hashErr := io.Copy(h, file)
		closeErr := file.Close()
		if err := errors.Join(hashErr, closeErr); err != nil {
			return err
		}
		m.Archives = append(m.Archives, archiveRecord{File: filename, SHA256: hex.EncodeToString(h.Sum(nil)), Size: size, OS: platform.os, Arch: platform.arch, Verified: "embedded Go build information source/platform validated"})
	}
	if err := snapshot.ValidateCandidate(ctx, root, id); err != nil {
		return fmt.Errorf("source changed during build: %w", err)
	}
	encoded, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "manifest.json"), append(encoded, '\n'), 0600); err != nil {
		return err
	}
	var checksums strings.Builder
	for _, record := range m.Archives {
		fmt.Fprintf(&checksums, "%s  %s\n", record.SHA256, record.File)
	}
	if err := os.WriteFile(filepath.Join(stage, "SHA256SUMS"), []byte(checksums.String()), 0600); err != nil {
		return err
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return errors.New("distribution destination appeared during build")
	}
	if err := os.Rename(stage, destination); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Built %d local archives from %s in %s\n", len(m.Archives), id, destination)
	return err
}

func native(ctx context.Context, root, executable string, args ...string) (process.Output, error) {
	return process.Run(ctx, model.Command{Executable: executable, Args: args}, root, map[string]string{"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOWORK": "off", "GOFLAGS": ""})
}

func verifyBuild(path, source string, platform target) error {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return err
	}
	settings := map[string]string{}
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	if settings["GOOS"] != platform.os || settings["GOARCH"] != platform.arch || settings["vcs.revision"] != source || settings["vcs.modified"] != "false" || settings["CGO_ENABLED"] != "0" {
		return errors.New("binary build provenance does not match source or platform")
	}
	return nil
}

type archiveEntry struct {
	Name string
	Data []byte
	Mode int64
}

func documentation(root string) ([]archiveEntry, error) {
	entries := []archiveEntry{}
	for _, name := range []string{
		"README.md", "LICENSE", "THIRD_PARTY_NOTICES.md", "CONTRIBUTING.md",
		"assets/branding/hayaku-logo.png",
		"docs/architecture.md", "docs/ci.md", "docs/distribution.md",
		"docs/implementation-status.md", "docs/pilot-methodology.md", "docs/pilot-results.md",
		"docs/qualification.md", "docs/release-checks.md", "docs/releases/v0.1.0.md", "docs/releases/v0.2.0.md", "docs/releases/v0.2.1.md", "docs/releases/v0.3.0.md", "docs/releases/v0.4.0.md", "docs/runtime-observations.md", "docs/vitest.md",
		"docs/capsules.md", "docs/inputs.md", "docs/measurements.md", "docs/runner-support.md", "docs/v0.4-development.md",
		"docs/research/README.md", "docs/research/2026-09-30-design-and-safety.md",
		"docs/research/2026-09-30-framework-adapters.md", "docs/research/2026-09-30-meta-predictive-test-selection.md",
		"docs/research/2026-09-30-mori-architecture.md", "docs/research/2026-09-30-stryker-mutation-testing.md",
		"docs/safety.md", "docs/verification.md",
	} {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		entries = append(entries, archiveEntry{Name: name, Data: data, Mode: 0644})
	}
	return entries, nil
}

func writeArchive(path string, entries []archiveEntry, timestamp time.Time, zipped bool) (err error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	if zipped {
		writer := zip.NewWriter(file)
		for _, entry := range entries {
			header := &zip.FileHeader{Name: entry.Name, Method: zip.Deflate, Modified: timestamp}
			header.SetMode(os.FileMode(entry.Mode))
			body, err := writer.CreateHeader(header)
			if err != nil {
				_ = writer.Close()
				return err
			}
			if _, err := body.Write(entry.Data); err != nil {
				_ = writer.Close()
				return err
			}
		}
		return writer.Close()
	}
	compressed := gzip.NewWriter(file)
	compressed.Header.ModTime = timestamp
	writer := tar.NewWriter(compressed)
	for _, entry := range entries {
		if err := writer.WriteHeader(&tar.Header{Name: entry.Name, Mode: entry.Mode, Size: int64(len(entry.Data)), ModTime: timestamp, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
			_ = writer.Close()
			_ = compressed.Close()
			return err
		}
		if _, err := writer.Write(entry.Data); err != nil {
			_ = writer.Close()
			_ = compressed.Close()
			return err
		}
	}
	return errors.Join(writer.Close(), compressed.Close())
}
