package app

import (
	"context"
	"errors"
	"flag"
	"time"

	"github.com/Cyberlane/hayaku/internal/passcache"
)

type cacheAuthorityFlags struct{ policy, signingKey *string }

func registerCacheAuthorityFlags(flags *flag.FlagSet) cacheAuthorityFlags {
	return cacheAuthorityFlags{
		policy:     flags.String("cache-policy", "", "absolute protected portable verification policy outside source/cache"),
		signingKey: flags.String("cache-signing-key", "", "absolute private issuer seed file; omission selects verification-only"),
	}
}

func (f cacheAuthorityFlags) load(ctx context.Context, flags *flag.FlagSet, root, cache string) (*passcache.Authority, error) {
	selected, signingSelected := false, false
	flags.Visit(func(flag *flag.Flag) {
		if flag.Name == "cache-policy" || flag.Name == "cache-signing-key" {
			selected = true
		}
		if flag.Name == "cache-signing-key" {
			signingSelected = true
		}
	})
	if !selected {
		return nil, nil
	}
	if signingSelected && *f.signingKey == "" {
		return nil, errors.New("explicit portable cache signing key must not be empty")
	}
	if *f.policy == "" || cache == "" {
		return nil, errors.New("portable cache requires an explicit policy and cache directory")
	}
	return passcache.LoadAuthority(ctx, *f.policy, *f.signingKey, []string{root, cache})
}

func stableCacheAuthority(ctx context.Context, authority *passcache.Authority) bool {
	return authority == nil || authority.Verify(ctx) == nil
}

// An explicitly requested audit may need its trusted policy for cleanup even
// when the invocation was already cancelled. Policy admission stays bounded;
// an unavailable policy never selects local authentication as a fallback.
func (f cacheAuthorityFlags) loadInvocation(ctx context.Context, flags *flag.FlagSet, root, cache string, audit bool) (*passcache.Authority, error) {
	if !audit {
		return f.load(ctx, flags, root, cache)
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return f.load(cleanup, flags, root, cache)
}
