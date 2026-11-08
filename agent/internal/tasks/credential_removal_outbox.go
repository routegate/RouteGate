package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// PendingHeldRemovalReport reads durable delivery state under the same
// nonblocking lock as the executor. It never reads credentials or starts a
// service. A saved report can be delivered after task authority has expired.
func (e CredentialRemovalExecutor) PendingHeldRemovalReport(ctx context.Context) (*CredentialRemovalReport, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	lock, err := lockRuntimeMutationExisting(e.receiptDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	entries, err := os.ReadDir(e.receiptDir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		operation := strings.TrimSuffix(entry.Name(), ".json")
		if !canonicalTaskIDPattern.MatchString(operation) || entry.Name() != operation+".json" {
			continue
		}
		receipt, err := e.readHeldRemovalReceipt(operation)
		if err != nil {
			return nil, err
		}
		if !receipt.Held || receipt.Acknowledged {
			continue
		}
		if receipt.Report.ServerID != e.serverID || receipt.Report.AgentID != e.agentID {
			return nil, ErrCredentialRemoval
		}
		if receipt.Report.State == "applying" && validRemovalGeneration(receipt.Report.Before) {
			// Acquiring runtime.lock proves the previous admitted worker has
			// exited. Persist the uncertain outcome before attempting delivery.
			receipt.Report.State = "recovery_required"
			receipt.Report.Code = "interrupted_or_failed_mutation"
			if err := e.saveHeldRemovalReceipt(operation, receipt); err != nil {
				return nil, err
			}
		}
		if !validHeldRemovalDelivery(receipt.Report) {
			return nil, ErrCredentialRemoval
		}
		return &receipt.Report, nil
	}
	return nil, nil
}

// Acknowledgement changes only local delivery state after Manager has stored
// this exact report. It grants no execution or recovery authority.
func (e CredentialRemovalExecutor) AcknowledgeHeldRemovalReport(ctx context.Context, report CredentialRemovalReport) error {
	if ctx.Err() != nil || !validHeldRemovalDelivery(report) ||
		report.ServerID != e.serverID || report.AgentID != e.agentID {
		return ErrCredentialRemoval
	}
	lock, err := lockRuntimeMutationExisting(e.receiptDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	receipt, err := e.readHeldRemovalReceipt(report.OperationID)
	if err != nil || !receipt.Held || receipt.Report != report {
		return ErrCredentialRemoval
	}
	if receipt.Acknowledged {
		return nil
	}
	receipt.Acknowledged = true
	return e.saveHeldRemovalReceipt(report.OperationID, receipt)
}

func (e CredentialRemovalExecutor) readHeldRemovalReceipt(operation string) (removalReceipt, error) {
	var receipt removalReceipt
	if !canonicalTaskIDPattern.MatchString(operation) {
		return receipt, ErrCredentialRemoval
	}
	data, err := readRemovalFile(filepath.Join(e.receiptDir, operation+".json"), 16384, true)
	if err != nil {
		return receipt, err
	}
	if _, _, _, err := canonicalRemovalJSON(data); err != nil {
		return receipt, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil || !validRemovalHash(receipt.Fingerprint) ||
		receipt.Report.OperationID != operation {
		return removalReceipt{}, ErrCredentialRemoval
	}
	return receipt, nil
}

func (e CredentialRemovalExecutor) saveHeldRemovalReceipt(operation string, receipt removalReceipt) error {
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return writeRemovalFile(filepath.Join(e.receiptDir, operation+".json"), data)
}

func validHeldRemovalDelivery(report CredentialRemovalReport) bool {
	for _, id := range []string{report.OperationID, report.JobID, report.AccountID, report.ServerID, report.AgentID, report.BaselineVersionID, report.ConfigVersionID} {
		if !canonicalTaskIDPattern.MatchString(id) {
			return false
		}
	}
	if report.SchemaVersion != 1 || !validRemovalHash(report.ConfigHash) || !validRemovalHash(report.BaselineRuntimeHash) {
		return false
	}
	if validSavedRemovalFailure(report) {
		return true
	}
	return report.State == "succeeded" && report.Code == "" &&
		report.SessionEffect == "shared_process_restart_observed" &&
		validRemovalHash(report.ActiveRuntimeHash) && validRemovalGeneration(report.Before) &&
		validRemovalGeneration(report.After) && report.Before.InvocationID != report.After.InvocationID &&
		report.ListenerPort > 0 && report.ListenerPort <= 65535
}
