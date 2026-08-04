package main

import "testing"

func TestStripRegister(t *testing.T) {
	if got := string(stripRegister([]byte("REGISTERpayload"))); got != "payload" {
		t.Fatalf("stripRegister() = %q, want payload", got)
	}
	if got := string(stripRegister([]byte("payload"))); got != "payload" {
		t.Fatalf("stripRegister() modified payload: %q", got)
	}
}
