// Package process runs bounded native commands without a shell.
package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
)

const MaxOutput = 32 << 20

type Output struct {
	Completed bool
	Stdout    []byte
	Stderr    []byte
	ExitCode  int
	Duration  time.Duration
}

type limited struct {
	buffer   bytes.Buffer
	max      int
	cancel   context.CancelFunc
	overflow bool
}

func (b *limited) Write(p []byte) (int, error) {
	if len(p) > b.max-b.buffer.Len() {
		b.overflow = true
		b.cancel()
		return 0, errors.New("process output limit exceeded")
	}
	return b.buffer.Write(p)
}

func Environment(overrides map[string]string) []string {
	values := map[string]string{}
	for _, pair := range os.Environ() {
		key, value, ok := strings.Cut(pair, "=")
		if ok {
			values[key] = value
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func Run(ctx context.Context, cmd model.Command, dir string, env map[string]string) (Output, error) {
	return RunEnvironment(ctx, cmd, dir, Environment(env))
}

// RunEnvironment uses an explicitly established native environment without ambient merging.
func RunEnvironment(ctx context.Context, cmd model.Command, dir string, env []string) (Output, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := &limited{max: MaxOutput, cancel: cancel}
	stderr := &limited{max: MaxOutput, cancel: cancel}
	proc := exec.CommandContext(ctx, cmd.Executable, cmd.Args...)
	proc.Dir = dir
	proc.Env = env
	proc.Stdout = stdout
	proc.Stderr = stderr
	proc.WaitDelay = 2 * time.Second
	start := time.Now()
	err := proc.Run()
	out := Output{Stdout: stdout.buffer.Bytes(), Stderr: stderr.buffer.Bytes(), ExitCode: -1, Duration: time.Since(start)}
	if proc.ProcessState != nil {
		out.ExitCode = proc.ProcessState.ExitCode()
		out.Completed = proc.ProcessState.Exited() && out.ExitCode >= 0
	}
	if stdout.overflow || stderr.overflow {
		out.Completed = false
		return out, errors.New("process output exceeded limit")
	}
	if ctx.Err() != nil {
		out.Completed = false
		return out, ctx.Err()
	}
	if err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			out.Completed = false
		}
		return out, fmt.Errorf("command %s failed (exit %d)", cmd.Executable, out.ExitCode)
	}
	return out, nil
}
