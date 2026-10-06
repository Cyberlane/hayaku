package capsule

import "github.com/Cyberlane/hayaku/internal/passcache"

// BeginAudit pins existing local copies before configured request validation.
func BeginAudit(options Options) (*passcache.Audit, error) {
	if options.AuditHandle != nil {
		return options.AuditHandle, options.AuditHandle.Validate(options.CacheDir, "hayaku-wasi-capsule-pass-v1", options.Authority)
	}
	return passcache.BeginAudit(options.CacheDir, "hayaku-wasi-capsule-pass-v1", options.Authority)
}
