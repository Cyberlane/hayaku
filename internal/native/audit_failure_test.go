package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/passcache"
)

func TestAuditFailureConfiguredNativeValidationRevokesOnlyOwnDomain(t *testing.T) {
	for _, fault := range []string{"invalid-schema", "duplicate-packages", "missing-compiler", "canceled"} {
		t.Run(fault, func(t *testing.T) {
			issuer, verifier := nativePortableAuthority(t)
			cache := filepath.Join(t.TempDir(), "passes")
			ownKey, foreignKey := strings.Repeat("8", 64), strings.Repeat("9", 64)
			// These signed values are synthetic cleanup controls, not native
			// qualification evidence. The installed fixture below proves reuse.
			for _, item := range []struct{ domain, key string }{{"hayaku-native-pure-pass-v1", ownKey}, {"independent-native-foreign", foreignKey}} {
				c, err := passcache.OpenWithAuthority(cache, item.domain, issuer)
				if err != nil {
					t.Fatal(err)
				}
				if err := c.Store(context.Background(), item.key, true); err != nil {
					t.Fatal(err)
				}
				if _, state := c.Load(item.key); state != "hit" {
					t.Fatal("synthetic authenticated cleanup control unhealthy", state)
				}
				c.Close()
			}
			before, err := os.ReadFile(filepath.Join(cache, foreignKey+".json"))
			if err != nil {
				t.Fatal(err)
			}
			config := Config{Schema: 1, Architecture: runtime.GOARCH, Packages: []string{"."}}
			ctx := context.Background()
			switch fault {
			case "invalid-schema":
				config.Schema = 2
			case "duplicate-packages":
				config.Packages = []string{".", "."}
			case "missing-compiler":
				t.Setenv("PATH", t.TempDir())
			case "canceled":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			report, err := GoWithOptions(ctx, t.TempDir(), config, ExecutionOptions{CacheDir: cache, Audit: true, Authority: verifier})
			if err == nil || report.Complete || report.Passed || len(report.Groups) != 0 {
				t.Fatal("invalid configured native audit executed or passed")
			}
			if fault == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal("configured native audit lost typed cancellation", err)
			}
			if _, err := os.Stat(filepath.Join(cache, ownKey+".json")); !os.IsNotExist(err) {
				t.Fatal("early configured audit retained authenticated native-domain control", err)
			}
			after, err := os.ReadFile(filepath.Join(cache, foreignKey+".json"))
			if err != nil || string(after) != string(before) {
				t.Fatal("native configured audit changed foreign domain", err)
			}
		})
	}
}

func TestAuditFailureAbsentNativeCacheCreatesNoAuthority(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "absent")
	report, err := GoWithOptions(context.Background(), t.TempDir(), Config{}, ExecutionOptions{CacheDir: cache, Audit: true})
	if err == nil || report.Passed || report.Complete {
		t.Fatal("invalid native config passed")
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatal("invalid native audit created absent authority", err)
	}
}

func TestInstalledDarwinAuditFailureRevokesActualNativePassBeforeInvalidConfig(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires actual installed macOS native enforcement; not a Linux acceptance result")
	}
	issuer, verifier := nativePortableAuthority(t)
	root := t.TempDir()
	write(t, root, "go.mod", "module sample\ngo 1.26\n")
	write(t, root, "sample_test.go", "package sample;import \"testing\";func TestArithmetic(t *testing.T){if 1+1!=2{t.Fatal(\"wrong\")}}")
	cache := filepath.Join(t.TempDir(), "passes")
	config := Config{Schema: 1, Platform: "darwin", Architecture: runtime.GOARCH, Packages: []string{"."}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cold, err := GoWithOptions(ctx, root, config, ExecutionOptions{CacheDir: cache, Authority: issuer})
	if err != nil || !cold.Complete || !cold.Passed || len(cold.Groups) != 1 || !cold.Groups[0].Qualified || cold.Groups[0].Reused {
		t.Fatal("actual native cold issuer execution did not qualify", err)
	}
	warm, err := GoWithOptions(ctx, root, config, ExecutionOptions{CacheDir: cache, Authority: verifier})
	if err != nil || !warm.Passed || len(warm.Groups) != 1 || !warm.Groups[0].Reused || warm.Groups[0].Key != cold.Groups[0].Key {
		t.Fatal("actual native transported-verifier healthy receipt did not reuse", err)
	}
	foreignKey := strings.Repeat("7", 64)
	foreign, err := passcache.OpenWithAuthority(cache, "actual-native-foreign", issuer)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if err := foreign.Store(context.Background(), foreignKey, true); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(cache, foreignKey+".json"))
	if err != nil {
		t.Fatal(err)
	}
	config.Schema = 2
	failed, err := GoWithOptions(ctx, root, config, ExecutionOptions{CacheDir: cache, Audit: true, Authority: verifier})
	if err == nil || failed.Complete || failed.Passed || len(failed.Groups) != 0 {
		t.Fatal("invalid configured native audit passed")
	}
	if _, err := os.Stat(filepath.Join(cache, cold.Groups[0].Key+".json")); !os.IsNotExist(err) {
		t.Fatal("invalid audit retained previously actually qualified native pass", err)
	}
	after, err := os.ReadFile(filepath.Join(cache, foreignKey+".json"))
	if err != nil || string(after) != string(before) {
		t.Fatal("actual native cleanup removed foreign authority", err)
	}
	if _, state := foreign.Load(foreignKey); state != "hit" {
		t.Fatal("foreign authority no longer verifies", state)
	}
}
