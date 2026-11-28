package tasks_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/ikaevus/routegate/agent/internal/client"
	"github.com/ikaevus/routegate/agent/internal/tasks"
)

// Started only by the existing disposable PostgreSQL/systemd CI fixture.
func TestCredentialRemovalRealManagerChild(t *testing.T) {
	if os.Getenv("ROUTEGATE_TEST_REMOVAL_MANAGER_CHILD") != "1" {
		t.Skip("requires the isolated Manager parent")
	}
	if os.Geteuid() != 0 || os.Getenv("ROUTEGATE_TEST_REMOVAL_SYSTEMD") != "1" {
		t.Fatal("requires disposable root systemd runner")
	}
	var input struct {
		ManagerURL, Token string
		Fixture           tasks.ManagerPreparedRealityFixture
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		t.Fatal("invalid private Manager input")
	}
	agent := client.New(input.ManagerURL)
	tasks.RunManagerPreparedRealityFixture(t, input.Fixture, os.Getenv("ROUTEGATE_TEST_SING_BOX"),
		func(ctx context.Context, _ tasks.ConfigTask, executor tasks.CredentialRemovalExecutor) (tasks.CredentialRemovalReport, error) {
			report, err := agent.ProcessHeldRemoval(ctx, input.Token, executor)
			if err != nil {
				return tasks.CredentialRemovalReport{}, err
			}
			if report == nil {
				return tasks.CredentialRemovalReport{}, errors.New("Manager did not deliver held removal")
			}
			// Once evidence is durable, Manager must stop delivering the operation.
			again, err := agent.ProcessHeldRemoval(ctx, input.Token, executor)
			if err != nil || again != nil {
				return tasks.CredentialRemovalReport{}, errors.New("Manager redelivered completed evidence")
			}
			return *report, nil
		})
}
