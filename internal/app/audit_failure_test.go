package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/capsule"
	"github.com/Cyberlane/hayaku/internal/passcache"
	"github.com/Cyberlane/hayaku/internal/suite"
)

func TestAuditFailureConfiguredCLIRevokesBeforeConfigurationValidation(t *testing.T) {
	for _, command := range []string{"capsule", "suite"} {
		for _, role := range []string{"local", "portable-verifier"} {
			for _, fault := range []string{"invalid-json", "missing-config", "missing-module", "canceled"} {
				t.Run(command+"/"+role+"/"+fault, func(t *testing.T) {
					root := cliCapsuleFixture(t)
					cache := filepath.Join(t.TempDir(), "passes")
					config := command + ".json"
					args := []string{command, "--root", root, "--config", config, "--cache-dir", cache}
					var issuer, verifier *passcache.Authority
					if role == "portable-verifier" {
						policy, seed, _ := cliAuthorityFixture(t)
						var err error
						issuer, err = passcache.LoadAuthority(context.Background(), policy, seed, nil)
						if err != nil {
							t.Fatal(err)
						}
						verifier, err = passcache.LoadAuthority(context.Background(), policy, "", nil)
						if err != nil {
							t.Fatal(err)
						}
						args = append(args, "--cache-policy", policy, "--cache-signing-key", seed)
					}
					invoke := func(ctx context.Context, argv []string) (int, []byte, string) {
						var out, diagnostic bytes.Buffer
						status := Run(ctx, argv, &out, &diagnostic)
						return status, out.Bytes(), diagnostic.String()
					}
					status, output, diagnostic := invoke(context.Background(), args)
					if status != 0 {
						t.Fatal("actual CLI healthy issuer run failed", diagnostic)
					}
					var ownKeys []string
					if command == "capsule" {
						var r capsule.Result
						if json.Unmarshal(output, &r) != nil || !r.Qualified || !r.Passed || r.Mode != "executed" || r.CacheStatus != "stored" {
							t.Fatal("actual capsule issuer did not mint complete pass")
						}
						ownKeys = []string{r.Key}
					} else {
						var r suite.Result
						if json.Unmarshal(output, &r) != nil || !r.Complete || !r.Passed || r.Executed != 1 {
							t.Fatal("actual suite issuer did not execute complete group")
						}
						ownKeys = []string{r.Groups[0].Key}
					}
					if role == "portable-verifier" {
						args = args[:len(args)-2]
					}
					status, output, diagnostic = invoke(context.Background(), args)
					if status != 0 || !bytes.Contains(output, []byte(`"mode": "reused"`)) {
						t.Fatal("actual CLI authenticated warm control failed", diagnostic)
					}
					foreignKey := strings.Repeat("c", 64)
					foreign, err := passcache.OpenWithAuthority(cache, "independent-cli-foreign", issuer)
					if err != nil {
						t.Fatal(err)
					}
					defer foreign.Close()
					if err := foreign.Store(context.Background(), foreignKey, true); err != nil {
						t.Fatal(err)
					}
					foreignPath := filepath.Join(cache, foreignKey+".json")
					before, err := os.ReadFile(foreignPath)
					if err != nil {
						t.Fatal(err)
					}
					ctx := context.Background()
					switch fault {
					case "invalid-json":
						if err := os.WriteFile(filepath.Join(root, config), []byte(`{"schema":`), 0600); err != nil {
							t.Fatal(err)
						}
					case "missing-config":
						if err := os.Remove(filepath.Join(root, config)); err != nil {
							t.Fatal(err)
						}
					case "missing-module":
						if err := os.Remove(filepath.Join(root, "suite.wasm")); err != nil {
							t.Fatal(err)
						}
					case "canceled":
						canceled, cancel := context.WithCancel(ctx)
						cancel()
						ctx = canceled
					}
					args = append(args, "--no-reuse")
					status, _, diagnostic = invoke(ctx, args)
					if status == 0 {
						t.Fatal("failed configured audit returned CLI success")
					}
					if fault == "canceled" && !strings.Contains(diagnostic, context.Canceled.Error()) {
						t.Fatal("CLI audit lost original cancellation diagnostic", diagnostic)
					}
					for _, key := range ownKeys {
						if _, err := os.Stat(filepath.Join(cache, key+".json")); !os.IsNotExist(err) {
							t.Fatal("pre-validation CLI audit retained authenticated own-domain pass", err)
						}
					}
					after, err := os.ReadFile(foreignPath)
					if err != nil || string(after) != string(before) {
						t.Fatal("CLI cleanup changed foreign-domain bytes", err)
					}
					if _, state := foreign.Load(foreignKey); state != "hit" {
						t.Fatal("foreign-domain authentication lost", state)
					}
					if verifier != nil {
						if err := verifier.Verify(context.Background()); err != nil {
							t.Fatal("fixture authority itself changed", err)
						}
						if _, err := os.Stat(filepath.Join(cache, "authority.key")); !os.IsNotExist(err) {
							t.Fatal("verifier audit generated local secret")
						}
					}
				})
			}
		}
	}
}

func TestAuditFailureAbsentCLICacheCreatesNoAuthority(t *testing.T) {
	for _, command := range []string{"capsule", "suite", "native"} {
		t.Run(command, func(t *testing.T) {
			cache := filepath.Join(t.TempDir(), "absent")
			var out, diagnostic bytes.Buffer
			status := Run(context.Background(), []string{command, "--root", t.TempDir(), "--config", "missing.json", "--cache-dir", cache, "--no-reuse"}, &out, &diagnostic)
			if status == 0 {
				t.Fatal("missing config audit passed")
			}
			if _, err := os.Stat(cache); !os.IsNotExist(err) {
				t.Fatal("failed pre-validation CLI audit created absent cache authority", err)
			}
		})
	}
}

type auditFailureCLIOutputRetarget struct {
	cache, retained, replacement string
	observed                     bool
	fault                        error
}

func (w *auditFailureCLIOutputRetarget) Write(data []byte) (int, error) {
	if !bytes.Contains(data, []byte(`"passed": true`)) {
		return 0, errors.New("actual passing result did not reach output boundary")
	}
	w.observed = true
	w.fault = os.Rename(w.cache, w.retained)
	if w.fault == nil {
		w.fault = os.Rename(w.replacement, w.cache)
	}
	return 0, errors.New("independent output transport failure")
}

func TestAuditFailureCLIOutputFailureRevokesPinnedOriginalRoot(t *testing.T) {
	for _, command := range []string{"capsule", "suite"} {
		t.Run(command, func(t *testing.T) {
			root := cliCapsuleFixture(t)
			baseline := filepath.Join(t.TempDir(), "baseline")
			args := []string{command, "--root", root, "--config", command + ".json", "--cache-dir", baseline}
			for _, mode := range []string{"executed", "reused"} {
				var out, diagnostic bytes.Buffer
				if Run(context.Background(), args, &out, &diagnostic) != 0 || !bytes.Contains(out.Bytes(), []byte(`"mode": "`+mode+`"`)) {
					t.Fatal("actual healthy CLI control did not execute and reuse", diagnostic.String())
				}
			}
			owner := t.TempDir()
			cache, retained, replacement := filepath.Join(owner, "passes"), filepath.Join(owner, "retained"), filepath.Join(owner, "replacement")
			foreignKey := strings.Repeat("b", 64)
			foreign, err := passcache.Open(replacement, "cli-result-replacement-foreign")
			if err != nil {
				t.Fatal(err)
			}
			defer foreign.Close()
			if err := foreign.Store(context.Background(), foreignKey, true); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(replacement, foreignKey+".json"))
			if err != nil {
				t.Fatal(err)
			}
			writer := &auditFailureCLIOutputRetarget{cache: cache, retained: retained, replacement: replacement}
			var diagnostic bytes.Buffer
			status := Run(context.Background(), []string{command, "--root", root, "--config", command + ".json", "--cache-dir", cache, "--no-reuse"}, writer, &diagnostic)
			if !writer.observed || writer.fault != nil {
				t.Fatal("actual passing result output boundary not reached", writer.fault)
			}
			if status == 0 || !strings.Contains(diagnostic.String(), "independent output transport failure") {
				t.Fatal("output failure did not fail CLI with original diagnostic", status, diagnostic.String())
			}
			entries, err := os.ReadDir(retained)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".json") {
					t.Fatal("CLI cleanup left original actual pass behind renamed root")
				}
			}
			after, err := os.ReadFile(filepath.Join(cache, foreignKey+".json"))
			if err != nil || string(after) != string(before) {
				t.Fatal("CLI failed audit mutated replacement authority", err)
			}
			if _, state := foreign.Load(foreignKey); state != "hit" {
				t.Fatal("foreign replacement pass no longer authenticates", state)
			}
		})
	}
}
