package db

import (
	"context"
	"strings"
	"testing"
)

func TestRuntimeLoginRejectsInvalidPasswordBeforeDatabase(t *testing.T) {
	for _, password := range []string{"", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("A", 64), strings.Repeat("a", 63) + "'"} {
		if err := ProvisionRuntimeLogin(context.Background(), nil, "runtime", password); err == nil {
			t.Fatal("invalid password accepted")
		}
	}
}
