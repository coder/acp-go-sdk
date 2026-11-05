package acp

// Replays of the counterexamples that check.sh reports, against the real
// code. replay.sh copies this file into the package, runs its TestReplay_
// tests and removes the copy. Write each replay as a TestReplay_ test that
// asserts the promise, so it fails while the finding is in the code. Use the
// helpers in agent_session_cancel_test.go (newCancelPeer, waitCancelTest,
// cancelTestPromptLine, sessionCancelLine): a failure that starts with
// "SETUP:" means that the path could not be replayed, not that the promise
// broke. When a fix lands, move the replay into agent_session_cancel_test.go
// as a regression test.
//
// No finding is open at the sync commit, so this file holds no replay.
