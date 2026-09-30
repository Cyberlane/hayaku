package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "internal", "qualification"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/Cyberlane/hayaku\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, mutationPath), []byte("package qualification\nfunc isFailure(outcome string) bool {\n"+beforeMutation+"\n}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestMutationIsExactBenignAndSourceDriftStops(t *testing.T) {
	dir := fixture(t)
	if err := mutate(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, mutationPath))
	if err != nil || !strings.Contains(string(data), afterMutation) || strings.Contains(string(data), beforeMutation) {
		t.Fatalf("mutation bytes = %q, %v", data, err)
	}
	if err := mutate(dir); err == nil {
		t.Fatal("repeated or drifted mutation source accepted")
	}
	wrong := fixture(t)
	if err := os.WriteFile(filepath.Join(wrong, "go.mod"), []byte("module different\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := mutate(wrong); err == nil {
		t.Fatal("another project accepted by Hayaku-only pilot")
	}
}

func TestLocalCloneDoesNotMutateSourceAndIgnoresGitRedirection(t *testing.T) {
	root := fixture(t)
	ctx := context.Background()
	for _, args := range [][]string{{"init", "--initial-branch=main"}, {"add", "--all"}, {"commit", "-m", "fixture"}} {
		if _, err := localGit(ctx, root, args...); err != nil {
			t.Fatal(err)
		}
	}
	query, err := localGit(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(query.Stdout))
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "nonexistent"))
	destination := filepath.Join(t.TempDir(), "copy")
	if err := clone(ctx, root, destination, id); err != nil {
		t.Fatal(err)
	}
	if err := mutate(destination); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(root, mutationPath))
	if err != nil || !strings.Contains(string(original), beforeMutation) {
		t.Fatal("original source changed")
	}
	query, err = localGit(ctx, root, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(query.Stdout)) != id {
		t.Fatal("original branch changed")
	}
}

func TestPilotRejectsUnsupportedModesAndPreservesOutputs(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{{}, {"--output", "/tmp/out", "--iterations", "0"}, {"--output", "/tmp/out", "--cache-mode", "cold"}, {"--output", "/tmp/out", "--timeout", "0s"}, {"--root", root, "--output", filepath.Join(root, "result")}} {
		if err := run(context.Background(), args, io.Discard); err == nil {
			t.Fatalf("invalid pilot invocation accepted: %v", args)
		}
	}
	file := filepath.Join(t.TempDir(), "result.json")
	if err := writeJSON(file, map[string]int{"preserve": 1}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(file)
	if err := writeJSON(file, map[string]int{"replace": 1}); err == nil {
		t.Fatal("previous artifact overwritten")
	}
	after, _ := os.ReadFile(file)
	if string(before) != string(after) {
		t.Fatal("unrelated artifact modified")
	}
}

func TestMedianIncludesNegativeSavings(t *testing.T) {
	for _, test := range []struct {
		values []time.Duration
		want   time.Duration
	}{{nil, 0}, {[]time.Duration{3, 1, 2}, 2}, {[]time.Duration{-8, -2}, -5}, {[]time.Duration{2, 4}, 3}} {
		if got := median(test.values); got != test.want {
			t.Fatalf("median = %s, want %s", got, test.want)
		}
	}
}
