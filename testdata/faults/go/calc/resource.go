package calc

import (
	_ "embed"
	"strings"
)

//go:embed config.txt
var config string

func Resource() string { return strings.TrimSpace(config) }
