package tasks

import "context"

// recoveryJournalManagerExpectationV1 models fields that a FUTURE Agent
// would receive from a separately authenticated Manager pre-dispatch
// checkpoint. This type alone does not authenticate the source of those
// fields, and no current Agent Runner or network caller uses this helper.
type recoveryJournalManagerExpectationV1 struct {
	AttemptEpoch              int64
	TaskID                    string
	AgentID                   string
	ServerID                  string
	AgentCredentialGeneration int64
	MarkerSHA256              string
}

// recoveryJournalOfflinePreflightV1 is a historical, non-authorizing
// observation. This object deliberately has no Enrolled, Ready, CanDispatch,
// CanResume, CanUnlock, Approved or ValidatedHost fields.
type recoveryJournalOfflinePreflightV1 struct {
	ObservedHeadSHA256 string
	EventsSeen         int
	LastPhase          recoveryJournalPhaseV1
	Unattested         bool
}

// inspectRecoveryJournalAgainstManagerExpectationV1 is a PRIVATE, strictly
// read-only test/design primitive. It takes the journal's dedicated flock,
// parses and checks every file, matches the *whole attempt* to an expected
// Manager tuple under that flock, and releases it before returning.
//
// A matching local chain is STILL untrusted: privileged Agent/root can
// create a self-consistent journal and callers can forge the expectation.
// This result NEVER authorizes enrollment, local fence removal or mutation.
func inspectRecoveryJournalAgainstManagerExpectationV1(
	ctx context.Context,
	dir string,
	expected recoveryJournalManagerExpectationV1,
) (recoveryJournalOfflinePreflightV1, error) {
	deny := func() (recoveryJournalOfflinePreflightV1, error) {
		return recoveryJournalOfflinePreflightV1{}, errRecoveryJournalUntrustedV1
	}
	if ctx == nil || ctx.Err() != nil ||
		expected.AttemptEpoch <= 0 ||
		expected.AgentCredentialGeneration <= 0 ||
		!canonicalTaskIDPattern.MatchString(expected.TaskID) ||
		!canonicalTaskIDPattern.MatchString(expected.AgentID) ||
		!canonicalTaskIDPattern.MatchString(expected.ServerID) ||
		!validRemovalHash(expected.MarkerSHA256) {
		return deny()
	}

	fd, err := openRecoveryJournalDiskV1(dir)
	if err != nil {
		return deny()
	}
	defer fd.Close()

	events, err := readRecoveryJournalDiskEntriesV1(fd)
	if err != nil || len(events) == 0 || ctx.Err() != nil {
		return deny()
	}
	observed, err := reviewRecoveryJournalChainV1(events)
	if err != nil || !observed.Unattested {
		return deny()
	}
	for _, event := range events {
		if event.AttemptEpoch != expected.AttemptEpoch ||
			event.TaskID != expected.TaskID ||
			event.AgentID != expected.AgentID ||
			event.ServerID != expected.ServerID ||
			event.AgentCredentialGeneration != expected.AgentCredentialGeneration ||
			event.MarkerSHA256 != expected.MarkerSHA256 {
			return deny()
		}
	}
	if ctx.Err() != nil {
		return deny()
	}
	historical := recoveryJournalOfflinePreflightV1{
		ObservedHeadSHA256: observed.HeadSHA256,
		EventsSeen:         observed.EventsSeen,
		LastPhase:          observed.LastPhase,
		Unattested:         true,
	}
	// Closing the journal directory flock makes the result historical;
	// propagate even rare close failures rather than returning success.
	if err := fd.Close(); err != nil {
		return deny()
	}
	return historical, nil
}
