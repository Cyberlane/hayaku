package client

import "fixture.test/core"

func Shared() int { return core.Value() * 2 }
