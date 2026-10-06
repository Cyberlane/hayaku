package capsule

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/passcache"
)

func auditFailureAuthorities(t *testing.T) (*passcache.Authority, *passcache.Authority) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p := passcache.Policy{Schema: 1, Namespace: "independent-audit-failure", Epoch: 1, PublicKeys: []string{hex.EncodeToString(public)}}
	issuer, err := passcache.NewAuthority(p, private.Seed())
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := passcache.NewAuthority(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	return issuer, verifier
}

func TestAuditFailureRevokesActuallyExecutedCapsuleDomain(t *testing.T) {
	for _, role := range []string{"local", "issuer", "verifier"} {
		for _, fault := range []string{"invalid-request", "invalid-limits", "canceled", "unsupported-module"} {
			t.Run(role+"/"+fault, func(t *testing.T) {
				issuer, verifier := auditFailureAuthorities(t)
				producer := issuer
				if role == "local" {
					producer = nil
				}
				cache := filepath.Join(t.TempDir(), "passes")
				request := validRequest(wasmCommand("", "", nil, nil, false, nil, nil))
				options := Options{CacheDir: cache, Authority: producer}
				cold, err := Run(context.Background(), request, options)
				if err != nil || !cold.Complete || !cold.Qualified || !cold.Passed || cold.Mode != "executed" || cold.CacheStatus != "stored" {
					t.Fatal("actual passing capsule did not mint receipt", err)
				}
				if role == "verifier" {
					options.Authority = verifier
				}
				warm, err := Run(context.Background(), request, options)
				if err != nil || warm.Mode != "reused" || warm.Key != cold.Key {
					t.Fatal("authenticated healthy receipt did not reuse", err)
				}
				foreignKey := strings.Repeat("f", 64)
				foreign, err := passcache.OpenWithAuthority(cache, "independent-foreign-provider", producer)
				if err != nil {
					t.Fatal(err)
				}
				defer foreign.Close()
				if err := foreign.Store(context.Background(), foreignKey, true); err != nil {
					t.Fatal(err)
				}
				foreignPath := filepath.Join(cache, foreignKey+".json")
				foreignBefore, err := os.ReadFile(foreignPath)
				if err != nil {
					t.Fatal(err)
				}
				ctx := context.Background()
				switch fault {
				case "invalid-request":
					request.Name = ""
				case "invalid-limits":
					options.Limits = DefaultLimits()
					options.Limits.MemoryPages = 65537
				case "canceled":
					canceled, cancel := context.WithCancel(ctx)
					cancel()
					ctx = canceled
				case "unsupported-module":
					request.Module = []byte("not-wasm")
				}
				options.NoReuse = true
				failed, err := Run(ctx, request, options)
				if err == nil || failed.Complete || failed.Qualified || failed.Passed || failed.Mode == "reused" {
					t.Fatal("failed audit reported reusable success")
				}
				if fault == "canceled" && !errors.Is(err, context.Canceled) {
					t.Fatal("audit lost cancellation", err)
				}
				if _, err := os.Stat(filepath.Join(cache, cold.Key+".json")); !os.IsNotExist(err) {
					t.Fatal("failed audit retained earlier authenticated own-domain receipt", err)
				}
				foreignAfter, err := os.ReadFile(foreignPath)
				if err != nil || string(foreignAfter) != string(foreignBefore) {
					t.Fatal("failed audit mutated foreign-domain receipt", err)
				}
				if _, status := foreign.Load(foreignKey); status != "hit" {
					t.Fatal("foreign-domain receipt no longer authenticates", status)
				}
				if role != "local" {
					if _, err := os.Stat(filepath.Join(cache, "authority.key")); !os.IsNotExist(err) {
						t.Fatal("portable audit generated local authority")
					}
				}
			})
		}
	}
}

func TestAuditFailureAbsentCapsuleCacheCreatesNoAuthority(t *testing.T) {
	for _, fault := range []string{"invalid", "canceled"} {
		t.Run(fault, func(t *testing.T) {
			cache := filepath.Join(t.TempDir(), "absent")
			ctx := context.Background()
			request := validRequest(wasmCommand("", "", nil, nil, false, nil, nil))
			if fault == "invalid" {
				request.Name = ""
			} else {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			result, err := Run(ctx, request, Options{CacheDir: cache, NoReuse: true})
			if err == nil || result.Passed || result.Complete {
				t.Fatal("invalid absent-cache audit passed")
			}
			if _, err := os.Stat(cache); !os.IsNotExist(err) {
				t.Fatal("failed audit created previously absent cache or authority", err)
			}
		})
	}
}
