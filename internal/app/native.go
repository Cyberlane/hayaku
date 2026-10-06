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
	"github.com/Cyberlane/hayaku/internal/passcache"
	"github.com/Cyberlane/hayaku/internal/report"
)

func runNative(ctx context.Context, args []string, out, errout io.Writer) (status int) {
	flags := flag.NewFlagSet("native", flag.ContinueOnError)
	flags.SetOutput(errout)
	root := flags.String("root", ".", "source root")
	name := flags.String("config", "native.json", "strict native group configuration relative to root")
	cache := flags.String("cache-dir", "", "private receipt directory outside source tree")
	cacheFlags := registerCacheAuthorityFlags(flags)
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
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	authority, err := cacheFlags.loadInvocation(ctx, flags, abs, *cache, *audit)
	if err != nil {
		return failure(errout, err)
	}
	options := native.ExecutionOptions{CacheDir: *cache, Audit: *audit, Authority: authority}
	var auditHandle *passcache.Audit
	if *audit {
		cleanup, err := native.BeginAuditWithOptions(options)
		if err != nil {
			return failure(errout, err)
		}
		options.AuditHandle = cleanup
		auditHandle = cleanup
		defer func() {
			if err := cleanup.Finish(status != 0); err != nil {
				status = failure(errout, errors.New("native audit cleanup incomplete"))
			}
		}()
	}
	var c native.Config
	if err = readToolJSON(abs, *name, &c); err != nil {
		return failure(errout, err)
	}
	value, runErr := native.GoWithOptions(ctx, abs, c, options)
	var current native.Config
	if readToolJSON(abs, *name, &current) != nil || !reflect.DeepEqual(c, current) || !stableCacheAuthority(ctx, authority) {
		value.Complete = false
		value.Passed = false
		_ = native.RevokeWithOptions(options, value)
		runErr = errors.New("native configuration changed during execution")
	}
	if auditHandle != nil {
		if err := auditHandle.Check(runErr != nil || !value.Complete || !value.Passed); err != nil {
			value.Complete, value.Passed = false, false
			for i := range value.Groups {
				value.Groups[i].Complete, value.Groups[i].Qualified, value.Groups[i].Passed = false, false, false
			}
			runErr = errors.Join(runErr, err)
		}
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
