package native

import "github.com/Cyberlane/hayaku/internal/passcache"

// BeginAuditWithOptions pins existing copies for a configured Go audit. Public
// artifacts still require private admission before Execute mutates any cache.
func BeginAuditWithOptions(options ExecutionOptions) (*passcache.Audit, error) {
	if options.AuditHandle != nil {
		return options.AuditHandle, options.AuditHandle.Validate(options.CacheDir, "hayaku-native-pure-pass-v1", options.Authority)
	}
	return passcache.BeginAudit(options.CacheDir, "hayaku-native-pure-pass-v1", options.Authority)
}
