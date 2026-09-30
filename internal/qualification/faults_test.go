package qualification

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

type faultManifest struct {
	Schema     int     `json:"schema"`
	Kind       string  `json:"kind"`
	Repository string  `json:"repository"`
	Faults     []fault `json:"faults"`
}

type fault struct {
	ID                   string   `json:"id"`
	Operation            string   `json:"operation"`
	Path                 string   `json:"path"`
	Before               string   `json:"before"`
	After                string   `json:"after"`
	ExpectedFailures     []string `json:"expected_failures"`
	ExpectedBuildFailure bool     `json:"expected_build_failure"`
}

func faultFixture(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	err := fs.WalkDir(os.DirFS(source), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		dest := filepath.Join(dir, filepath.FromSlash(path))
		if entry.IsDir() {
			return os.Mkdir(dest, 0700)
		}
		if !entry.Type().IsRegular() {
			return fs.ErrInvalid
		}
		data, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(path)))
		if err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func observeFault(t *testing.T, dir string) (process.Output, []string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	output, err := process.Run(ctx, model.Command{Executable: "go", Args: []string{"test", "-json", "-count=1", "./..."}}, dir, map[string]string{"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOWORK": "off", "GOFLAGS": ""})
	if err != nil && output.ExitCode != 1 {
		t.Fatalf("native fixture invocation incomplete: %v", err)
	}
	failures := []string{}
	passed := 0
	decoder := json.NewDecoder(bytes.NewReader(output.Stdout))
	for {
		var event struct{ Package, Test, Action string }
		if err := decoder.Decode(&event); err != nil {
			if err != io.EOF {
				t.Fatalf("invalid native JSON: %v", err)
			}
			break
		}
		if event.Test != "" && event.Action == "fail" {
			failures = append(failures, event.Package+"/"+event.Test)
		} else if event.Test != "" && event.Action == "pass" {
			passed++
		}
	}
	sort.Strings(failures)
	return output, failures, passed
}

func TestSeededFaultManifestNativeOracles(t *testing.T) {
	data, err := os.ReadFile("../../testdata/faults/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest faultManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != 1 || manifest.Kind != "seeded-regression-challenges" || manifest.Repository != "go" || len(manifest.Faults) == 0 {
		t.Fatal("unexpected fault manifest contract")
	}
	source, err := filepath.Abs("../../testdata/faults/" + manifest.Repository)
	if err != nil {
		t.Fatal(err)
	}
	baseline := faultFixture(t, source)
	output, failures, passed := observeFault(t, baseline)
	if output.ExitCode != 0 || len(failures) != 0 || passed != 4 {
		t.Fatalf("baseline is not clean: exit %d failures %v passed %d", output.ExitCode, failures, passed)
	}
	seen := map[string]bool{}
	for _, mutation := range manifest.Faults {
		if seen[mutation.ID] || mutation.ID == "" || !filepath.IsLocal(mutation.Path) {
			t.Fatal("invalid fault identity or path")
		}
		seen[mutation.ID] = true
		t.Run(mutation.ID, func(t *testing.T) {
			dir := faultFixture(t, source)
			path := filepath.Join(dir, mutation.Path)
			switch mutation.Operation {
			case "replace":
				data, err := os.ReadFile(path)
				if err != nil || mutation.Before == "" || strings.Count(string(data), mutation.Before) != 1 {
					t.Fatal("fault replacement must match exactly one source token")
				}
				if err := os.WriteFile(path, []byte(strings.Replace(string(data), mutation.Before, mutation.After, 1)), 0600); err != nil {
					t.Fatal(err)
				}
			case "add":
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("fault addition already exists")
				}
				if err := os.WriteFile(path, []byte(mutation.After), 0600); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			default:
				t.Fatal("unsupported fault operation")
			}
			output, failures, _ := observeFault(t, dir)
			if output.ExitCode != 1 {
				t.Fatalf("seeded fault did not fail: exit %d", output.ExitCode)
			}
			expected := append([]string{}, mutation.ExpectedFailures...)
			sort.Strings(expected)
			if !reflect.DeepEqual(failures, expected) {
				t.Fatalf("native failures %v, expected independent oracle %v", failures, expected)
			}
			if mutation.ExpectedBuildFailure && len(failures) != 0 {
				t.Fatal("build failure unexpectedly reached assertions")
			}
		})
	}
}
