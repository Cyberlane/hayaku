package startup

var enabled bool

func init() { enabled = true }

func Enabled() bool { return enabled }
