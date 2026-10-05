package app

import (
	"context"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"reflect"
	"time"

	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/native"
	"github.com/Cyberlane/hayaku/internal/report"
)

func runNative(ctx context.Context, args []string, out, errout io.Writer) int {
	flags := flag.NewFlagSet("native", flag.ContinueOnError)
	flags.SetOutput(errout)
	root := flags.String("root", ".", "source root")
	name := flags.String("config", "native.json", "strict native group configuration relative to root")
	cache := flags.String("cache-dir", "", "private receipt directory outside source tree")
	audit := flags.Bool("no-reuse", false, "invalidate old passes and execute all groups")
	output := flags.String("output", "", "new report file")
	timeout := flags.Duration("timeout", 10*time.Minute, "maximum duration")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 || !config.Relative(*name) || *timeout <= 0 || *timeout > 24*time.Hour {
		return failure(errout, errors.New("invalid native arguments"))
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		return failure(errout, err)
	}
	var c native.Config
	if err = readToolJSON(abs, *name, &c); err != nil {
		return failure(errout, err)
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	value, runErr := native.Go(ctx, abs, c, *cache, *audit)
	var current native.Config
	if readToolJSON(abs, *name, &current) != nil || !reflect.DeepEqual(c, current) {
		value.Complete = false
		value.Passed = false
		_ = native.Revoke(*cache, value)
		runErr = errors.New("native configuration changed during execution")
	}
	if status := writeResult(out, errout, *output, func(w io.Writer) error { return report.JSON(w, value) }); status != 0 {
		return status
	}
	if runErr != nil {
		return failure(errout, runErr)
	}
	if !value.Complete || !value.Passed {
		return 1
	}
	return 0
}
