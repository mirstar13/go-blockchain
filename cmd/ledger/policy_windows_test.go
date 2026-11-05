package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestWindowsRequiresPassphrase(t *testing.T) {
	for _, args := range [][]string{
		{"wallet", "create", "--out", "unused.key"},
		{"wallet", "address", "--key", "unused.key"},
		{"wallet", "encrypt", "--key", "unused.key", "--out", "unused-copy.key"},
		{"transfer", "--key", "unused.key"},
		{"cancel", "--key", "unused.key"},
	} {
		var out bytes.Buffer
		err := run(context.Background(), args, &out)
		if err == nil || !strings.Contains(err.Error(), "--passphrase-file") {
			t.Fatalf("%v: %v", args, err)
		}
	}
}
