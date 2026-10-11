package core

import "testing"

func TestDirect(t *testing.T) {
	if Value() != 7 {
		t.Fatal("direct")
	}
}
