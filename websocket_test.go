package websocket

import (
	"testing"
)

func TestSecKey(t *testing.T) {
	exp := "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	key64 := "dGhlIHNhbXBsZSBub25jZQ=="

	sum := secKeyHash(key64)

	if exp != sum {
		t.Errorf("expected [%v], got [%v]", exp, sum)
	}
}
