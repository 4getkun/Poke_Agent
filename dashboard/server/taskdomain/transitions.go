package taskdomain

import (
	"fmt"
	"strings"
)

func (s *State) Task(id string, version int64) (*Task, error) {
	t := s.Tasks[id]
	if t == nil {
		return nil, Fail("NOT_FOUND")
	}
	if t.Version != version {
		return nil, Fail("STALE_VERSION")
	}
	return t, nil
}
func (s *State) validateReferences(spec Spec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	p := s.Projects[spec.ProjectID]
	if p == nil || p.Archived {
		return Fail("INVALID_PROJECT")
	}
	if spec.RoleID != "" && s.Roles[spec.RoleID] == nil {
		return Fail("INVALID_ROLE")
	}
	return nil
}
func (s *State) Create(spec Spec) (*Task, error) {
	if err := s.validateReferences(spec); err != nil {
		return nil, err
	}
	spec.Version = 1
	t := &Task{ID: ID(), Spec: cloneSpec(spec), State: "BACKLOG", Version: 1}
	s.Tasks[t.ID] = t
	s.recordSpec(t)
	return t, nil
}
func (s *State) recordSpec(t *Task) {
	copy := cloneSpec(t.Spec)
	s.SpecHistory[fmt.Sprintf("%s:%d", t.ID, t.Spec.Version)] = &copy
}
func (s *State) Ready(t *Task) error {
	if t.State != "BACKLOG" {
		return Fail("INVALID_TRANSITION")
	}
	if err := s.validateReferences(t.Spec); err != nil {
		return err
	}
	t.State = "READY"
	t.Version++
	return nil
}
func overlap(a, b *Project) bool {
	if a.Identity == b.Identity {
		return true
	}
	for _, id := range a.Ancestors {
		if id == b.Identity {
			return true
		}
	}
	for _, id := range b.Ancestors {
		if id == a.Identity {
			return true
		}
	}
	return false
}
func (s *State) unresolved(taskID string) bool {
	for _, e := range s.Effects {
		r := s.Runs[e.RunID]
		if r != nil && r.TaskID == taskID && (e.Outcome == "pending" || e.Outcome == "unknown") {
			return true
		}
	}
	return false
}

// Begin is called inside the same transaction that reserves workspace ownership.
func (s *State) Begin(t *Task, limit int, runtimeVersion, model string) (*Run, error) {
	if t.State != "READY" {
		return nil, Fail("INVALID_TRANSITION")
	}
	if t.Block != "" || s.unresolved(t.ID) {
		return nil, Fail("RECOVERY_REQUIRED")
	}
	if limit < 1 || limit > 4 {
		return nil, Fail("INVALID_LIMIT")
	}
	if err := s.validateReferences(t.Spec); err != nil {
		return nil, err
	}
	p := s.Projects[t.Spec.ProjectID]
	if p.Identity == "" {
		return nil, Fail("WORKSPACE_IDENTITY_UNKNOWN")
	}
	active := 0
	var generation int64 = 1
	for _, r := range s.Runs {
		if r.Generation >= generation {
			generation = r.Generation + 1
		}
		if !r.WorkspaceQuiet {
			q := s.Projects[r.Snapshot.ProjectID]
			if q == nil || overlap(p, q) {
				return nil, Fail("WORKSPACE_BUSY")
			}
			if !r.Terminal() {
				active++
			}
		}
	}
	if active >= limit {
		return nil, Fail("RUN_LIMIT")
	}
	role := ""
	if r := s.Roles[t.Spec.RoleID]; r != nil {
		role = r.Prompt
	}
	r := &Run{ID: ID(), TaskID: t.ID, SpecVersion: t.Spec.Version, Generation: generation, State: "queued", StartedAt: Now(), Snapshot: cloneSpec(t.Spec), RolePrompt: role, RuntimeVersion: runtimeVersion, Model: model, EffectivePolicy: t.Spec.ApprovalProfile}
	if previous := s.Runs[t.CurrentRunID]; previous != nil && previous.SpecVersion == t.Spec.Version {
		r.ProviderThreadID = previous.ProviderThreadID
	}
	s.Runs[r.ID] = r
	t.CurrentRunID = r.ID
	t.SubmissionID = ""
	t.State = "DOING"
	t.Version++
	return r, nil
}

func (s *State) Active(taskID, runID string, generation int64) (*Task, *Run, error) {
	t := s.Tasks[taskID]
	r := s.Runs[runID]
	if t == nil || r == nil || r.TaskID != taskID || t.CurrentRunID != runID || r.Generation != generation || r.SpecVersion != t.Spec.Version {
		return nil, nil, Fail("STALE_RUNTIME")
	}
	return t, r, nil
}
func (s *State) Advance(taskID, runID string, generation int64, next string) error {
	_, r, err := s.Active(taskID, runID, generation)
	if err != nil {
		return err
	}
	if r.Terminal() {
		return Fail("STALE_RUNTIME")
	}
	allowed := map[string][]string{"queued": {"starting", "cancelled", "interrupted"}, "starting": {"running", "waiting_approval", "cancelling", "succeeded", "failed", "interrupted"}, "running": {"waiting_approval", "cancelling", "succeeded", "failed", "interrupted"}, "waiting_approval": {"running", "cancelling", "succeeded", "failed", "interrupted"}, "cancelling": {"cancelled", "succeeded", "failed", "interrupted"}}
	ok := false
	for _, v := range allowed[r.State] {
		if v == next {
			ok = true
		}
	}
	if !ok {
		return Fail("INVALID_TRANSITION")
	}
	r.State = next
	if r.Terminal() {
		r.EndedAt = Now()
		for _, a := range s.Approvals {
			if a.RunID == r.ID && a.State == "pending" {
				a.State = "expired"
			}
		}
		s.notify(taskID, next)
	}
	return nil
}

// Quiesce requires live supervisor evidence. A UI checkbox cannot call this.
func (s *State) Quiesce(taskID, runID string, generation int64) error {
	t, r, err := s.Active(taskID, runID, generation)
	if err != nil {
		return err
	}
	if !r.Terminal() {
		return Fail("RUNTIME_ACTIVE")
	}
	r.WorkspaceQuiet = true
	if r.State != "succeeded" {
		t.State = "READY"
		if s.unresolved(t.ID) {
			t.Block = "UNKNOWN_EFFECT"
		}
		t.Version++
	}
	return nil
}
func (s *State) AddApproval(taskID, runID string, generation int64, a Approval) error {
	_, r, err := s.Active(taskID, runID, generation)
	if err != nil {
		return err
	}
	if r.State != "running" && r.State != "starting" && r.State != "waiting_approval" {
		return Fail("APPROVAL_EXPIRED")
	}
	if a.Kind != "command" && a.Kind != "file_change" {
		return Fail("UNSUPPORTED_APPROVAL")
	}
	a.ID = ID()
	a.RunID = runID
	a.Generation = generation
	a.State = "pending"
	a.Choices = []string{"accept", "decline"}
	s.Approvals[a.ID] = &a
	r.State = "waiting_approval"
	s.notify(taskID, "approval")
	return nil
}
func (s *State) RespondApproval(id, runID string, generation int64, decision string) (*Approval, error) {
	a := s.Approvals[id]
	r := s.Runs[runID]
	if a == nil || r == nil || a.RunID != runID || a.Generation != generation || a.State != "pending" {
		return nil, Fail("APPROVAL_EXPIRED")
	}
	_, _, err := s.Active(r.TaskID, runID, generation)
	if err != nil {
		return nil, err
	}
	if r.State != "waiting_approval" {
		return nil, Fail("APPROVAL_EXPIRED")
	}
	if decision != "accept" && decision != "decline" {
		return nil, Fail("UNSUPPORTED_APPROVAL")
	}
	a.State = decision
	r.State = "running"
	return a, nil
}
func (s *State) Submit(t *Task, sub Submission) error {
	r := s.Runs[t.CurrentRunID]
	if t.State != "DOING" || r == nil || r.State != "succeeded" || !r.WorkspaceQuiet || t.Block != "" || s.unresolved(t.ID) {
		return Fail("SUBMISSION_NOT_READY")
	}
	if sub.TaskID != t.ID || sub.RunID != r.ID || sub.SpecVersion != t.Spec.Version {
		return Fail("STALE_VERSION")
	}
	if strings.TrimSpace(sub.Summary) == "" {
		return Fail("INVALID_SUBMISSION")
	}
	seen := map[string]bool{}
	for _, e := range sub.Evidence {
		if seen[e.CriterionID] || e.Revision == "" || e.Procedure == "" || e.Result == "" {
			return Fail("INVALID_EVIDENCE")
		}
		seen[e.CriterionID] = true
		valid := false
		for _, c := range t.Spec.Criteria {
			if c.ID == e.CriterionID {
				valid = true
			}
		}
		if !valid {
			return Fail("INVALID_EVIDENCE")
		}
	}
	for _, c := range t.Spec.Criteria {
		if c.Required {
			ok := false
			for _, e := range sub.Evidence {
				if e.CriterionID == c.ID && e.Passed {
					ok = true
				}
			}
			if !ok {
				return Fail("EVIDENCE_REQUIRED")
			}
		}
	}
	sub.ID = ID()
	s.Submissions[sub.ID] = &sub
	t.SubmissionID = sub.ID
	t.State = "REVIEW"
	t.Version++
	s.notify(t.ID, "review")
	return nil
}
func (s *State) Review(t *Task, submissionID, decision, reason string) error {
	sub := s.Submissions[submissionID]
	if t.State != "REVIEW" || sub == nil || t.SubmissionID != submissionID || sub.SpecVersion != t.Spec.Version || sub.RunID != t.CurrentRunID {
		return Fail("STALE_SUBMISSION")
	}
	if t.Block != "" || s.unresolved(t.ID) {
		return Fail("RECOVERY_REQUIRED")
	}
	switch decision {
	case "accept":
		t.State = "DONE"
	case "reject":
		if strings.TrimSpace(reason) == "" {
			return Fail("REASON_REQUIRED")
		}
		t.State = "READY"
	default:
		return Fail("INVALID_DECISION")
	}
	d := &ReviewDecision{ID: ID(), TaskID: t.ID, SubmissionID: submissionID, SpecVersion: t.Spec.Version, Decision: decision, Reason: reason, At: Now()}
	s.Decisions[d.ID] = d
	t.Version++
	return nil
}
func (s *State) Revise(t *Task, spec Spec) error {
	if t.State == "DONE" || t.State == "CANCELLED" {
		return Fail("INVALID_TRANSITION")
	}
	if r := s.Runs[t.CurrentRunID]; r != nil && (!r.Terminal() || !r.WorkspaceQuiet) {
		return Fail("RUNTIME_ACTIVE")
	}
	if err := s.validateReferences(spec); err != nil {
		return err
	}
	spec.Version = t.Spec.Version + 1
	t.Spec = cloneSpec(spec)
	t.SubmissionID = ""
	t.State = "READY"
	t.Version++
	s.recordSpec(t)
	return nil
}
func (s *State) Retry(t *Task) error {
	r := s.Runs[t.CurrentRunID]
	if t.State != "DOING" || r == nil || !r.Terminal() || !r.WorkspaceQuiet {
		return Fail("RUNTIME_ACTIVE")
	}
	if t.Block != "" || s.unresolved(t.ID) {
		return Fail("RECOVERY_REQUIRED")
	}
	t.State = "READY"
	t.Version++
	return nil
}
func (s *State) notify(taskID, kind string) {
	n := &Notification{ID: ID(), TaskID: taskID, Kind: kind, At: Now()}
	s.Notifications[n.ID] = n
}

// Recover never resumes or kills by PID. Workspace remains blocked until the
// supervisor verifies ownership is gone and the human resolves unknown effects.
func (s *State) Recover() {
	for _, r := range s.Runs {
		if !r.Terminal() {
			r.State = "interrupted"
			r.EndedAt = Now()
			r.Error = "HOST_RESTART"
			r.WorkspaceQuiet = false
			if t := s.Tasks[r.TaskID]; t != nil {
				t.Block = "RECOVERY_REQUIRED"
				t.Version++
			}
			s.notify(r.TaskID, "interrupted")
		}
	}
	for _, a := range s.Approvals {
		if a.State == "pending" {
			a.State = "expired"
		}
	}
	for _, e := range s.Effects {
		if e.Outcome == "pending" {
			e.Outcome = "unknown"
		}
	}
}
func (s *State) ConfirmRecovery(t *Task, reason string, ownershipVerified bool) error {
	if t.Block != "RECOVERY_REQUIRED" || strings.TrimSpace(reason) == "" {
		return Fail("REASON_REQUIRED")
	}
	if !ownershipVerified || s.unresolved(t.ID) {
		return Fail("RECOVERY_REQUIRED")
	}
	for _, r := range s.Runs {
		if r.TaskID == t.ID && !r.Terminal() {
			return Fail("RUNTIME_ACTIVE")
		}
	}
	for _, r := range s.Runs {
		if r.TaskID == t.ID {
			r.WorkspaceQuiet = true
		}
	}
	record := &RecoveryRecord{ID: ID(), TaskID: t.ID, RunID: t.CurrentRunID, Reason: reason, At: Now()}
	s.Recovery[record.ID] = record
	t.Block = ""
	t.State = "READY"
	t.Version++
	return nil
}

// ResolveEffect records a human's observed result. It never repeats the effect.
func (s *State) ResolveEffect(t *Task, effectID, outcome, evidence string) error {
	if t.Block != "RECOVERY_REQUIRED" {
		return Fail("RECOVERY_REQUIRED")
	}
	effect := s.Effects[effectID]
	if effect == nil || s.Runs[effect.RunID] == nil || s.Runs[effect.RunID].TaskID != t.ID {
		return Fail("NOT_FOUND")
	}
	if effect.Outcome != "unknown" || !s.Runs[effect.RunID].Terminal() || (outcome != "succeeded" && outcome != "failed") {
		return Fail("INVALID_TRANSITION")
	}
	if strings.TrimSpace(evidence) == "" || len(evidence) > 8192 {
		return Fail("RECOVERY_EVIDENCE_REQUIRED")
	}
	effect.Outcome, effect.EvidenceRef = outcome, strings.TrimSpace(evidence)
	t.Version++
	return nil
}
