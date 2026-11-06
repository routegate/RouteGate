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
	var work NextWorkResponse
	if c.doJSONExpect(ctx, http.MethodPost, heldRemovalPath+"claim", token, nil, &work, http.StatusOK) != nil ||
		work.RecoveryChallenge != nil {
		return nil, errors.New("credential removal claim failed")
	}
	if work.Task == nil {
		return nil, nil
	}
	report, err := executor.ExecuteHeld(ctx, *work.Task)
	if err != nil {
		return nil, errors.New("credential removal requires reconciliation")
	}
	path := heldRemovalPath + url.PathEscape(report.JobID) + "/result"
	if c.doJSONExpect(ctx, http.MethodPost, path, token, report, nil, http.StatusNoContent) != nil {
		return nil, errors.New("credential removal result was not acknowledged")
	}
	return &report, nil
}
