//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package process

import "os/exec"

// Preserve direct-process cancellation on platforms without this process-group
// contract. Complete descendant cleanup is not qualified there (including
// Windows pending the separate job-object/runtime acceptance work).
func configureCancellation(command *exec.Cmd) {}
