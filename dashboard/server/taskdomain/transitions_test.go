package taskdomain

import (
	"encoding/json"
	"sort"
	"testing"
)

func addFixtureProject(s *State, id, identity string, ancestors ...string) {
	s.Projects[id] = &Project{ID: id, Name: id, Identity: identity, Ancestors: ancestors}
}

func fixtureSpec(projectID string) Spec {
	return Spec{
		ProjectID:       projectID,
		Objective:       "Complete the task",
		Criteria:        []Criterion{{ID: "checks", Procedure: "run checks", Expected: "passes", Required: true}},
		AllowedScope:    "workspace files",
		OutOfScope:      "external services",
		ApprovalProfile: "workspace-write",
	}
}

func createReadyFixture(t *testing.T, s *State, projectID string) *Task {
	t.Helper()
	task, err := s.Create(fixtureSpec(projectID))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Ready(task); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	return task
}

func startRunningFixture(t *testing.T, s *State, task *Task) *Run {
	t.Helper()
	run, err := s.Begin(task, 4, "runtime-v1", "model-v1")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := s.Advance(task.ID, run.ID, run.Generation, "starting"); err != nil {
		t.Fatalf("Advance starting: %v", err)
	}
	if err := s.Advance(task.ID, run.ID, run.Generation, "running"); err != nil {
		t.Fatalf("Advance running: %v", err)
	}
	return run
}

func validFixtureSubmission(task *Task, run *Run) Submission {
	return Submission{
		TaskID:      task.ID,
		RunID:       run.ID,
		SpecVersion: task.Spec.Version,
		Summary:     "All required checks passed.",
		Evidence: []Evidence{{
			CriterionID: "checks",
			Revision:    "abc123",
			Procedure:   "go test ./...",
			Result:      "passed",
			Passed:      true,
		}},
	}
}

func requireFixtureCode(t *testing.T, err error, want string) {
	t.Helper()
	if got := Code(err); got != want {
		t.Fatalf("got error %v (code %s), want %s", err, got, want)
	}
}

func TestSuccessRequiresEvidenceSubmissionAndHumanReview(t *testing.T) {
	s := NewState()
	addFixtureProject(s, "project", "workspace-a")
	task := createReadyFixture(t, s, "project")
	run := startRunningFixture(t, s, task)

	if err := s.Advance(task.ID, run.ID, run.Generation, "succeeded"); err != nil {
		t.Fatalf("Advance succeeded: %v", err)
	}
	if task.State != "DOING" || task.SubmissionID != "" {
		t.Fatalf("successful runtime completed the task without review: state=%q submission=%q", task.State, task.SubmissionID)
	}
	if err := s.Quiesce(task.ID, run.ID, run.Generation); err != nil {
		t.Fatalf("Quiesce: %v", err)
	}

	missingEvidence := validFixtureSubmission(task, run)
	missingEvidence.Evidence = nil
	requireFixtureCode(t, s.Submit(task, missingEvidence), "EVIDENCE_REQUIRED")
	if task.State != "DOING" || task.SubmissionID != "" || len(s.Submissions) != 0 {
		t.Fatalf("evidence-free submission changed task: state=%q submission=%q count=%d", task.State, task.SubmissionID, len(s.Submissions))
	}

	if err := s.Submit(task, validFixtureSubmission(task, run)); err != nil {
		t.Fatalf("Submit valid evidence: %v", err)
	}
	if task.State != "REVIEW" || task.SubmissionID == "" {
		t.Fatalf("valid submission did not enter review: state=%q submission=%q", task.State, task.SubmissionID)
	}
	if err := s.Review(task, task.SubmissionID, "accept", ""); err != nil {
		t.Fatalf("human accept: %v", err)
	}
	if task.State != "DONE" {
		t.Fatalf("human acceptance left task in %q, want DONE", task.State)
	}
}

func TestStaleSpecSubmissionAndGenerationAreRejectedAndReviseRetainsEvidence(t *testing.T) {
	s := NewState()
	addFixtureProject(s, "project", "workspace-a")
	task := createReadyFixture(t, s, "project")
	run := startRunningFixture(t, s, task)

	requireFixtureCode(t, s.Advance(task.ID, run.ID, run.Generation+1, "succeeded"), "STALE_RUNTIME")
	if run.State != "running" {
		t.Fatalf("stale generation changed run state to %q", run.State)
	}
	if err := s.Advance(task.ID, run.ID, run.Generation, "succeeded"); err != nil {
		t.Fatalf("Advance succeeded: %v", err)
	}
	if err := s.Quiesce(task.ID, run.ID, run.Generation); err != nil {
		t.Fatalf("Quiesce: %v", err)
	}

	staleSpecSubmission := validFixtureSubmission(task, run)
	staleSpecSubmission.SpecVersion++
	requireFixtureCode(t, s.Submit(task, staleSpecSubmission), "STALE_VERSION")
	if task.State != "DOING" || len(s.Submissions) != 0 {
		t.Fatalf("stale spec submission changed task: state=%q submissions=%d", task.State, len(s.Submissions))
	}

	if err := s.Submit(task, validFixtureSubmission(task, run)); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	oldSubmissionID := task.SubmissionID
	oldSubmission := s.Submissions[oldSubmissionID]
	if oldSubmission == nil || len(oldSubmission.Evidence) != 1 {
		t.Fatal("valid submission evidence was not recorded")
	}

	revised := fixtureSpec("project")
	revised.Objective = "Complete the revised task"
	if err := s.Revise(task, revised); err != nil {
		t.Fatalf("Revise: %v", err)
	}
	if task.Spec.Version != 2 || task.State != "READY" || task.SubmissionID != "" {
		t.Fatalf("revision state mismatch: spec=%d state=%q submission=%q", task.Spec.Version, task.State, task.SubmissionID)
	}
	if got := s.Submissions[oldSubmissionID]; got == nil || got.SpecVersion != 1 || len(got.Evidence) != 1 || got.Evidence[0] != oldSubmission.Evidence[0] {
		t.Fatal("revision removed or changed evidence from the prior submission")
	}
	if s.SpecHistory[task.ID+":1"] == nil || s.SpecHistory[task.ID+":2"] == nil {
		t.Fatal("revision did not retain both spec versions")
	}
	requireFixtureCode(t, s.Review(task, oldSubmissionID, "accept", ""), "STALE_SUBMISSION")
}

func TestApprovalCannotBeAcceptedTwice(t *testing.T) {
	s := NewState()
	addFixtureProject(s, "project", "workspace-a")
	task := createReadyFixture(t, s, "project")
	run := startRunningFixture(t, s, task)
	if err := s.AddApproval(task.ID, run.ID, run.Generation, Approval{Kind: "command", Request: json.RawMessage(`{"command":"go test ./..."}`)}); err != nil {
		t.Fatalf("AddApproval: %v", err)
	}

	var ids []string
	for id := range s.Approvals {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) != 1 {
		t.Fatalf("approval count=%d, want 1", len(ids))
	}
	if _, err := s.RespondApproval(ids[0], run.ID, run.Generation, "accept"); err != nil {
		t.Fatalf("first approval response: %v", err)
	}
	requireFixtureCode(t, func() error { _, err := s.RespondApproval(ids[0], run.ID, run.Generation, "decline"); return err }(), "APPROVAL_EXPIRED")
	if got := s.Approvals[ids[0]].State; got != "accept" {
		t.Fatalf("replayed approval changed state to %q", got)
	}
}

func TestBeginRejectsSameAndAncestorDescendantWorkspaceWriters(t *testing.T) {
	tests := []struct {
		name            string
		firstID         string
		firstIdentity   string
		firstAncestors  []string
		secondID        string
		secondIdentity  string
		secondAncestors []string
	}{
		{name: "same workspace identity", firstID: "first", firstIdentity: "root", secondID: "alias", secondIdentity: "root"},
		{name: "parent then child", firstID: "parent", firstIdentity: "root", secondID: "child", secondIdentity: "root/child", secondAncestors: []string{"root"}},
		{name: "child then parent", firstID: "child", firstIdentity: "root/child", firstAncestors: []string{"root"}, secondID: "parent", secondIdentity: "root"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewState()
			addFixtureProject(s, tc.firstID, tc.firstIdentity, tc.firstAncestors...)
			addFixtureProject(s, tc.secondID, tc.secondIdentity, tc.secondAncestors...)
			first := createReadyFixture(t, s, tc.firstID)
			second := createReadyFixture(t, s, tc.secondID)
			if _, err := s.Begin(first, 4, "runtime-v1", "model-v1"); err != nil {
				t.Fatalf("first Begin: %v", err)
			}
			_, err := s.Begin(second, 4, "runtime-v1", "model-v1")
			requireFixtureCode(t, err, "WORKSPACE_BUSY")
			if second.State != "READY" {
				t.Fatalf("conflicting begin changed second task state to %q", second.State)
			}
		})
	}
}

func TestBeginAllowsTwoIndependentWorkspacesAndEnforcesLimit(t *testing.T) {
	s := NewState()
	for _, id := range []string{"one", "two", "three"} {
		addFixtureProject(s, id, "workspace-"+id)
	}
	first := createReadyFixture(t, s, "one")
	second := createReadyFixture(t, s, "two")
	third := createReadyFixture(t, s, "three")
	if _, err := s.Begin(first, 2, "runtime-v1", "model-v1"); err != nil {
		t.Fatalf("first Begin: %v", err)
	}
	if _, err := s.Begin(second, 2, "runtime-v1", "model-v1"); err != nil {
		t.Fatalf("second independent Begin: %v", err)
	}
	_, err := s.Begin(third, 2, "runtime-v1", "model-v1")
	requireFixtureCode(t, err, "RUN_LIMIT")
	if third.State != "READY" {
		t.Fatalf("run-limit rejection changed third task state to %q", third.State)
	}
	_, err = s.Begin(third, 5, "runtime-v1", "model-v1")
	requireFixtureCode(t, err, "INVALID_LIMIT")
}

func TestTerminalRaceKeepsFirstResult(t *testing.T) {
	s := NewState()
	addFixtureProject(s, "project", "workspace-a")
	task := createReadyFixture(t, s, "project")
	run := startRunningFixture(t, s, task)
	if err := s.Advance(task.ID, run.ID, run.Generation, "succeeded"); err != nil {
		t.Fatalf("first terminal result: %v", err)
	}
	endedAt := run.EndedAt
	if endedAt == "" {
		t.Fatal("terminal result did not record EndedAt")
	}
	requireFixtureCode(t, s.Advance(task.ID, run.ID, run.Generation, "failed"), "STALE_RUNTIME")
	if run.State != "succeeded" || run.EndedAt != endedAt {
		t.Fatalf("later terminal event overwrote first result: state=%q ended=%q want=%q", run.State, run.EndedAt, endedAt)
	}
}

func TestRecoverExpiresApprovalAndUnknownEffectBlocksRecovery(t *testing.T) {
	s := NewState()
	addFixtureProject(s, "project", "workspace-a")
	task := createReadyFixture(t, s, "project")
	run := startRunningFixture(t, s, task)
	if err := s.AddApproval(task.ID, run.ID, run.Generation, Approval{Kind: "file_change", Request: json.RawMessage(`{"path":"file.txt"}`)}); err != nil {
		t.Fatalf("AddApproval: %v", err)
	}
	s.Effects["effect-1"] = &Effect{ID: "effect-1", RunID: run.ID, Outcome: "pending"}
	var approvalID string
	for id, approval := range s.Approvals {
		if approval.RunID == run.ID {
			approvalID = id
		}
	}

	s.Recover()
	if run.State != "interrupted" || run.Error != "HOST_RESTART" || run.WorkspaceQuiet {
		t.Fatalf("recovered run state mismatch: state=%q error=%q quiet=%t", run.State, run.Error, run.WorkspaceQuiet)
	}
	if approvalID == "" || s.Approvals[approvalID].State != "expired" {
		t.Fatalf("pending approval was not expired: id=%q", approvalID)
	}
	requireFixtureCode(t, func() error {
		_, err := s.RespondApproval(approvalID, run.ID, run.Generation, "accept")
		return err
	}(), "APPROVAL_EXPIRED")
	if s.Effects["effect-1"].Outcome != "unknown" || task.Block != "RECOVERY_REQUIRED" {
		t.Fatalf("unknown effect recovery state mismatch: outcome=%q block=%q", s.Effects["effect-1"].Outcome, task.Block)
	}
	requireFixtureCode(t, s.ConfirmRecovery(task, "verified ownership", true), "RECOVERY_REQUIRED")
	if task.Block != "RECOVERY_REQUIRED" || task.State == "READY" {
		t.Fatalf("unknown effect allowed task recovery: block=%q state=%q", task.Block, task.State)
	}
	_, err := s.Begin(task, 4, "runtime-v1", "model-v1")
	requireFixtureCode(t, err, "INVALID_TRANSITION")
}
