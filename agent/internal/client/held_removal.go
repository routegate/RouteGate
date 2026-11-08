package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/ikaevus/routegate/agent/internal/tasks"
)

const heldRemovalPath = "/api/v1/agent/credential-removals/"

// ProcessHeldRemoval deliberately uses neither generic task dispatch nor
// generic completion. Unknown outcomes are left for recovery.
func (c *Client) ProcessHeldRemoval(ctx context.Context, token string, executor tasks.CredentialRemovalExecutor) (*tasks.CredentialRemovalReport, error) {
	pending, err := executor.PendingHeldRemovalReport(ctx)
	if err != nil {
		return nil, errors.New("credential removal result state unavailable")
	}
	if pending != nil {
		return c.deliverHeldRemovalReport(ctx, token, executor, *pending)
	}
	var work NextWorkResponse
	if c.doJSONExpect(ctx, http.MethodPost, heldRemovalPath+"claim", token, nil, &work, http.StatusOK) != nil ||
		work.RecoveryChallenge != nil {
		return nil, errors.New("credential removal claim failed")
	}
	if work.Task == nil {
		return nil, nil
	}
	report, err := executor.ExecuteHeld(ctx, *work.Task)
	if err != nil && (report.State != "recovery_required" || report.JobID != work.Task.ID) {
		return nil, errors.New("credential removal requires reconciliation")
	}
	return c.deliverHeldRemovalReport(ctx, token, executor, report)
}

func (c *Client) deliverHeldRemovalReport(ctx context.Context, token string, executor tasks.CredentialRemovalExecutor, report tasks.CredentialRemovalReport) (*tasks.CredentialRemovalReport, error) {
	path := heldRemovalPath + url.PathEscape(report.JobID) + "/result"
	if c.doJSONExpect(ctx, http.MethodPost, path, token, report, nil, http.StatusNoContent) != nil {
		return nil, errors.New("credential removal result was not acknowledged")
	}
	if executor.AcknowledgeHeldRemovalReport(ctx, report) != nil {
		return nil, errors.New("credential removal acknowledgement was not saved")
	}
	if report.State != "succeeded" {
		return &report, errors.New("credential removal requires reconciliation")
	}
	return &report, nil
}
