package tasks

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCredentialRemovalHeldDeadline(t *testing.T) {
	for _, name := range []string{"missing", "expired", "overlong", "expired during validation"} {
		t.Run(name, func(t *testing.T) {
			executor, task, adapter, baseline := removalExecutorFixture(t)
			now := time.Now()
			var deadline time.Time
			switch name {
			case "expired":
				deadline = now.Add(-time.Second)
			case "overlong":
				deadline = now.Add(3 * time.Minute)
			case "expired during validation":
				deadline = now.Add(time.Second)
				adapter.validationHook = func() error {
					time.Sleep(time.Until(deadline) + time.Millisecond)
					return nil
				}
			}
			if !deadline.IsZero() {
				task.DeliveryExpiresAt = &deadline
			}
			report, err := executor.ExecuteHeld(context.Background(), task)
			if err == nil || adapter.restarts != 0 {
				t.Fatal("invalid deadline allowed a restart")
			}
			if name == "expired during validation" && (adapter.validations != 1 || report.Code != "delivery_expired") {
				t.Fatal("deadline was not rechecked after validation")
			}
			active, err := os.ReadFile(executor.activePath)
			if err != nil || !bytes.Equal(active, baseline) {
				t.Fatal("invalid deadline changed the active config")
			}
			if _, err := os.Stat(filepath.Join(executor.receiptDir, "inflight.json")); !os.IsNotExist(err) {
				t.Fatal("pre-mutation expiry left an active fence")
			}
		})
	}
}

func TestCredentialRemovalPromotionCanStopAfterStaging(t *testing.T) {
	executor, _, _, baseline := removalExecutorFixture(t)
	stop := errors.New("expired before rename")
	err := publishRemovalFileCheckpoint(executor.activePath, []byte("replacement"), true, func(step string, _ *os.File) error {
		if step == "before-rename" {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatal("promotion ignored its final checkpoint", err)
	}
	active, err := os.ReadFile(executor.activePath)
	if err != nil || !bytes.Equal(active, baseline) {
		t.Fatal("the final admission check ran after promotion")
	}
}
