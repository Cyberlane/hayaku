package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestArchivesReproducibleWithCorrectContentAndModes(t *testing.T) {
	entries := []archiveEntry{{Name: "docs/safety.md", Data: []byte("safety contract\n"), Mode: 0644}, {Name: "hayaku", Data: []byte("binary bytes"), Mode: 0755}}
	timestamp := time.Unix(1790760000, 0).UTC()
	for _, zipped := range []bool{false, true} {
		dir := t.TempDir()
		first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
		if err := writeArchive(first, append([]archiveEntry{}, entries...), timestamp, zipped); err != nil {
			t.Fatal(err)
		}
		if err := writeArchive(second, []archiveEntry{entries[1], entries[0]}, timestamp, zipped); err != nil {
			t.Fatal(err)
		}
		a, _ := os.ReadFile(first)
		b, _ := os.ReadFile(second)
		if !bytes.Equal(a, b) {
			t.Fatalf("archive order/timestamps were not deterministic, zip=%v", zipped)
		}
		got := map[string]string{}
		modes := map[string]uint32{}
		if zipped {
			reader, err := zip.OpenReader(first)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			for _, file := range reader.File {
				body, err := file.Open()
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(body)
				_ = body.Close()
				if err != nil {
					t.Fatal(err)
				}
				got[file.Name], modes[file.Name] = string(data), uint32(file.Mode().Perm())
				if !file.Modified.Equal(timestamp) {
					t.Fatalf("zip timestamp = %s, expected %s", file.Modified, timestamp)
				}
			}
		} else {
			compressed, err := gzip.NewReader(bytes.NewReader(a))
			if err != nil {
				t.Fatal(err)
			}
			defer compressed.Close()
			reader := tar.NewReader(compressed)
			for {
				header, err := reader.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(reader)
				if err != nil {
					t.Fatal(err)
				}
				got[header.Name], modes[header.Name] = string(data), uint32(header.Mode)
				if !header.ModTime.Equal(timestamp) || header.Uid != 0 || header.Gid != 0 {
					t.Fatalf("unexpected archive metadata: %#v", header)
				}
			}
		}
		if !reflect.DeepEqual(got, map[string]string{"docs/safety.md": "safety contract\n", "hayaku": "binary bytes"}) || !reflect.DeepEqual(modes, map[string]uint32{"docs/safety.md": 0644, "hayaku": 0755}) {
			t.Fatalf("archive entries=%v modes=%v", got, modes)
		}
		if err := writeArchive(first, entries, timestamp, zipped); err == nil {
			t.Fatal("existing archive overwritten")
		}
	}
}

func TestDocumentationPreservesLinkedFilesAndRequiresLicenseNotices(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"README.md":                                      "Getting started\n",
		"LICENSE":                                        "Distribution license\n",
		"THIRD_PARTY_NOTICES.md":                         "Bundled third-party notices\n",
		"CONTRIBUTING.md":                                "Contribution instructions\n",
		"assets/branding/hayaku-logo.png":                "\x89PNG\r\n\x1a\n\x00\xff",
		"docs/architecture.md":                           "Core architecture\n",
		"docs/ci.md":                                     "CI integration\n",
		"docs/implementation-status.md":                  "Current implementation\n",
		"docs/pilot-methodology.md":                      "Measurement methodology\n",
		"docs/pilot-results.md":                          "Pilot observations\n",
		"docs/release-checks.md":                         "Release check commands\n",
		"docs/releases/v0.1.0.md":                        "First release notes\n",
		"docs/releases/v0.2.0.md":                        "Vitest release notes\n",
		"docs/releases/v0.2.1.md":                        "Vitest scope correction notes\n",
		"docs/releases/v0.3.0.md":                        "Runtime observation release notes\n",
		"docs/apple.md":                                  "Apple XCTest setup\n",
		"docs/releases/v0.5.1.md":                        "Xcode 26.3 compatibility release notes\n",
		"docs/releases/v0.5.0.md":                        "Apple and generic runtime release notes\n",
		"docs/releases/v0.4.0.md":                        "Strict WASI reuse release notes\n",
		"docs/capsules.md":                               "WASI suite contract\n",
		"docs/inputs.md":                                 "Immutable inputs\n",
		"docs/measurements.md":                           "Net measurements\n",
		"docs/runner-support.md":                         "Native runner status\n",
		"docs/v0.4-development.md":                       "Acceptance map\n",
		"docs/runtime-observations.md":                   "Runtime observation setup\n",
		"docs/vitest.md":                                 "Vitest setup\n",
		"docs/research/README.md":                        "Research index\n",
		"docs/research/2026-09-30-design-and-safety.md":  "Design research\n",
		"docs/research/2026-09-30-framework-adapters.md": "Adapter research\n",
		"docs/research/2026-09-30-meta-predictive-test-selection.md": "Predictive selection research\n",
		"docs/research/2026-09-30-mori-architecture.md":              "Mori research\n",
		"docs/research/2026-09-30-stryker-mutation-testing.md":       "Mutation research\n",
		"docs/safety.md":        "Safety contract\n",
		"docs/qualification.md": "Qualification limits\n",
		"docs/distribution.md":  "Distribution instructions\n",
		"docs/verification.md":  "Verification evidence\n",
	}
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := documentation(root)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string, len(entries))
	for _, entry := range entries {
		got[entry.Name] = string(entry.Data)
		if entry.Mode != 0644 {
			t.Fatalf("document %s has mode %o", entry.Name, entry.Mode)
		}
	}
	if !reflect.DeepEqual(got, files) {
		t.Fatalf("bundled documents = %v, expected %v", got, files)
	}
	for _, name := range []string{"LICENSE", "THIRD_PARTY_NOTICES.md"} {
		path := filepath.Join(root, name)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if entries, err := documentation(root); err == nil || entries != nil {
			t.Fatalf("missing %s did not prevent distribution", name)
		}
		if err := os.WriteFile(path, []byte(files[name]), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunRejectsExistingAndInRepositoryOutputs(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{{}, {"--output", root, "unexpected"}, {"--root", root, "--output", filepath.Join(root, "dist")}, {"--root", root, "--output", root}} {
		if err := run(context.Background(), args, io.Discard); err == nil {
			t.Fatalf("accepted invalid invocation %v", args)
		}
	}
	output := t.TempDir()
	writePath := filepath.Join(output, "keep")
	if err := os.WriteFile(writePath, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"--root", root, "--output", output}, io.Discard); err == nil {
		t.Fatal("existing output accepted")
	}
	data, err := os.ReadFile(writePath)
	if err != nil || string(data) != "preserve" {
		t.Fatal("unrelated existing output modified")
	}
}
