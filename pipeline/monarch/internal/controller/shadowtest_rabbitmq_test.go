package controller

import "testing"

func TestProdShadowQueueArgs(t *testing.T) {
	t.Parallel()
	args := prodShadowQueueArgs()
	if got := args[amqpArgMaxLength]; got != int32(500) {
		t.Fatalf("x-max-length = %v want 500", got)
	}
	if got := args[amqpArgOverflow]; got != "drop-head" {
		t.Fatalf("x-overflow = %v want drop-head", got)
	}
}
