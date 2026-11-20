package main

import (
	"context"
	"testing"
)

func TestCheckRuntimeRequiresExplicitRuntimeCredentials(t *testing.T) {
	t.Setenv("ROUTEGATE_DATABASE_URL", "")
	t.Setenv("ROUTEGATE_MIGRATOR_DATABASE_URL", "postgres://owner:secret@127.0.0.1/routegate")
	if err := checkRuntime(context.Background()); err == nil || err.Error() != "ROUTEGATE_DATABASE_URL is required for runtime verification" {
		t.Fatalf("must refuse missing runtime credentials without falling back to owner: %v", err)
	}
}
