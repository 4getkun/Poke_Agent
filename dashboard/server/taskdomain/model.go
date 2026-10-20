// Package taskdomain contains provider-independent task and review rules.
package taskdomain

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Error struct {
	Code          string `json:"code"`
	MessageKey    string `json:"message_key"`
	SafeDetails   string `json:"safe_details"`
	Retryable     bool   `json:"retryable"`
	CorrelationID string `json:"correlation_id"`
}

func (e *Error) Error() string { return e.Code }
func Fail(code string) error {
	return &Error{Code: code, MessageKey: "error." + code, CorrelationID: ID()}
}
func ID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

type Criterion struct {
	ID        string `json:"criterion_id"`
	Procedure string `json:"procedure"`
	Expected  string `json:"expected"`
	Required  bool   `json:"required"`
}

func cloneSpec(spec Spec) Spec {
	spec.Criteria = append([]Criterion(nil), spec.Criteria...)
	return spec
}

type Spec struct {
	ProjectID       string      `json:"project_id"`
	RoleID          string      `json:"role_id,omitempty"`
	Objective       string      `json:"objective"`
	Criteria        []Criterion `json:"acceptance_criteria"`
	AllowedScope    string      `json:"allowed_scope"`
	OutOfScope      string      `json:"out_of_scope"`
	ApprovalProfile string      `json:"approval_profile"`
	Version         int64       `json:"version"`
}

func (s Spec) Validate() error {
	if strings.TrimSpace(s.ProjectID) == "" || strings.TrimSpace(s.Objective) == "" || strings.TrimSpace(s.AllowedScope) == "" || len(s.Criteria) == 0 {
		return Fail("INVALID_SPEC")
	}
	if s.ApprovalProfile != "read-only" && s.ApprovalProfile != "workspace-write" {
		return Fail("INVALID_POLICY")
	}
	seen := map[string]bool{}
	required := false
	for _, c := range s.Criteria {
		if strings.TrimSpace(c.ID) == "" || seen[c.ID] || strings.TrimSpace(c.Procedure) == "" || strings.TrimSpace(c.Expected) == "" {
			return Fail("INVALID_CRITERION")
		}
		seen[c.ID] = true
		required = required || c.Required
	}
	if !required {
		return Fail("INVALID_CRITERION")
	}
	return nil
}

type Project struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	DisplayPath  string   `json:"display_path"`
	ResolvedPath string   `json:"resolved_path"`
	Identity     string   `json:"identity"`
	Ancestors    []string `json:"ancestors"`
	Archived     bool     `json:"archived"`
}
type Role struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
}
type Task struct {
	ID           string `json:"id"`
	Spec         Spec   `json:"spec"`
	State        string `json:"state"`
	Version      int64  `json:"version"`
	CurrentRunID string `json:"current_run_id,omitempty"`
	SubmissionID string `json:"submission_id,omitempty"`
	Block        string `json:"block,omitempty"`
}
type Run struct {
	ID               string `json:"id"`
	TaskID           string `json:"task_id"`
	SpecVersion      int64  `json:"spec_version"`
	Generation       int64  `json:"generation"`
	State            string `json:"state"`
	ProviderThreadID string `json:"provider_thread_id,omitempty"`
	ProviderTurnID   string `json:"provider_turn_id,omitempty"`
	StartedAt        string `json:"started_at"`
	EndedAt          string `json:"ended_at,omitempty"`
	Error            string `json:"error,omitempty"`
	Snapshot         Spec   `json:"spec_snapshot"`
	RolePrompt       string `json:"role_prompt"`
	RuntimeVersion   string `json:"runtime_version"`
	Model            string `json:"model"`
	EffectivePolicy  string `json:"effective_policy"`
	WorkspaceQuiet   bool   `json:"workspace_quiet"`
}

func (r Run) Terminal() bool {
	return r.State == "succeeded" || r.State == "failed" || r.State == "cancelled" || r.State == "interrupted"
}

type Evidence struct {
	CriterionID string `json:"criterion_id"`
	Revision    string `json:"revision"`
	Procedure   string `json:"procedure"`
	Result      string `json:"result"`
	Passed      bool   `json:"passed"`
}
type Submission struct {
	ID          string     `json:"submission_id"`
	TaskID      string     `json:"task_id"`
	RunID       string     `json:"run_id"`
	SpecVersion int64      `json:"spec_version"`
	Summary     string     `json:"summary"`
	Evidence    []Evidence `json:"criterion_evidence"`
	Artifacts   []Artifact `json:"artifact_refs"`
	Limitations []string   `json:"known_limitations"`
}
type ReviewDecision struct {
	ID           string `json:"id"`
	TaskID       string `json:"task_id"`
	SubmissionID string `json:"submission_id"`
	SpecVersion  int64  `json:"spec_version"`
	Decision     string `json:"decision"`
	Reason       string `json:"reason"`
	At           string `json:"occurred_at"`
}
type Approval struct {
	ID         string          `json:"id"`
	RunID      string          `json:"run_id"`
	Generation int64           `json:"generation"`
	Kind       string          `json:"kind"`
	Request    json.RawMessage `json:"request"`
	ProviderID json.RawMessage `json:"provider_id"`
	State      string          `json:"state"`
	Choices    []string        `json:"choices"`
}
type Effect struct {
	ID          string `json:"effect_id"`
	RunID       string `json:"run_id"`
	Description string `json:"description"`
	Outcome     string `json:"outcome"`
	EvidenceRef string `json:"evidence_ref"`
}
type Artifact struct {
	Path        string `json:"path_or_uri"`
	Revision    string `json:"revision_or_hash"`
	Description string `json:"description"`
}
type Fact struct {
	Text   string `json:"text"`
	Source string `json:"source_ref"`
}
type Decision struct {
	Text   string `json:"text"`
	Reason string `json:"reason"`
	Source string `json:"source_ref"`
}
type ContextPacket struct {
	SchemaVersion int         `json:"schema_version"`
	TaskID        string      `json:"task_id"`
	SpecVersion   int64       `json:"spec_version"`
	SourceRunID   string      `json:"source_run_id"`
	Objective     string      `json:"objective"`
	AllowedScope  string      `json:"allowed_scope"`
	Criteria      []Criterion `json:"acceptance_criteria"`
	Facts         []Fact      `json:"verified_facts"`
	Decisions     []Decision  `json:"decisions"`
	Questions     []string    `json:"open_questions"`
	Next          []string    `json:"next_actions"`
	Artifacts     []Artifact  `json:"artifact_refs"`
	Effects       []Effect    `json:"effects"`
}

func (p ContextPacket) Validate() error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if len(b) > 16*1024 {
		return Fail("CONTEXT_TOO_LARGE")
	}
	if p.SchemaVersion != 1 || p.TaskID == "" || p.SpecVersion < 1 {
		return Fail("INVALID_CONTEXT")
	}
	return nil
}

type Notification struct {
	ID     string `json:"id"`
	TaskID string `json:"task_id"`
	Kind   string `json:"kind"`
	Read   bool   `json:"read"`
	At     string `json:"created_at"`
}
type Message struct {
	ID     string `json:"id"`
	TaskID string `json:"task_id"`
	RunID  string `json:"run_id"`
	Role   string `json:"role"`
	Body   string `json:"body"`
	At     string `json:"created_at"`
}
type Event struct {
	SchemaVersion  int    `json:"schema_version"`
	Seq            int64  `json:"event_seq"`
	ID             string `json:"event_id"`
	Type           string `json:"type"`
	At             string `json:"occurred_at"`
	ConversationID string `json:"conversation_id,omitempty"`
	TaskID         string `json:"task_id,omitempty"`
	RunID          string `json:"run_id,omitempty"`
	Generation     int64  `json:"runtime_generation,omitempty"`
	Payload        any    `json:"payload"`
}
type RecoveryRecord struct {
	ID     string `json:"id"`
	TaskID string `json:"task_id"`
	RunID  string `json:"run_id"`
	Reason string `json:"reason"`
	At     string `json:"occurred_at"`
}

type State struct {
	Projects      map[string]*Project        `json:"projects"`
	Roles         map[string]*Role           `json:"roles"`
	Tasks         map[string]*Task           `json:"tasks"`
	Runs          map[string]*Run            `json:"runs"`
	Approvals     map[string]*Approval       `json:"approvals"`
	Submissions   map[string]*Submission     `json:"submissions"`
	Decisions     map[string]*ReviewDecision `json:"review_decisions"`
	Effects       map[string]*Effect         `json:"effects"`
	Contexts      map[string]*ContextPacket  `json:"contexts"`
	Notifications map[string]*Notification   `json:"notifications"`
	Recovery      map[string]*RecoveryRecord `json:"recovery"`
	SpecHistory   map[string]*Spec           `json:"spec_history"`
}

func NewState() *State {
	return &State{Projects: map[string]*Project{}, Roles: map[string]*Role{}, Tasks: map[string]*Task{}, Runs: map[string]*Run{}, Approvals: map[string]*Approval{}, Submissions: map[string]*Submission{}, Decisions: map[string]*ReviewDecision{}, Effects: map[string]*Effect{}, Contexts: map[string]*ContextPacket{}, Notifications: map[string]*Notification{}, Recovery: map[string]*RecoveryRecord{}, SpecHistory: map[string]*Spec{}}
}
func Code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "INTERNAL_ERROR"
}
