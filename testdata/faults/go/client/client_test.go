package client

import "testing"

func TestConsumer(t *testing.T) {
	if Value() != 42 {
		t.Fatal("producer regression reaches downstream consumer")
	}
}
