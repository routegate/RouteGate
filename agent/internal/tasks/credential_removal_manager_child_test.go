package tasks_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/ikaevus/routegate/agent/internal/client"
	"github.com/ikaevus/routegate/agent/internal/tasks"
)

func TestCredentialRemovalRealManagerChild(t *testing.T) {
	if os.Getenv("ROUTEGATE_TEST_REMOVAL_MANAGER_CHILD") != "1" {
		t.Skip("started only by the isolated PostgreSQL Manager test")
	}
	if os.Getenv("ROUTEGATE_TEST_REMOVAL_SYSTEMD") != "1" || os.Geteuid() != 0 {
		t.Fatal("requires isolated systemd runner")
	}
	var input tasks.ManagerRemovalRealityFixture
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		t.Fatal("could not read the isolated Manager fixture")
	}
	agent := client.New(input.ManagerURL)
	tasks.RunCredentialRemovalManagerRealityFixture(t, input, os.Getenv("ROUTEGATE_TEST_REMOVAL_SING_BOX"),
		func(ctx context.Context, executor tasks.CredentialRemovalExecutor) (*tasks.CredentialRemovalReport, error) {
			return agent.ProcessHeldRemoval(ctx, input.Token, executor)
		})
}
