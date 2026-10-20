//go:build windows

package localapp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"pokegents/dashboard/server/history"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"pokegents/dashboard/server/processhost"
	"pokegents/dashboard/server/runtimeadapter"
	"pokegents/dashboard/server/sqlstore"
	d "pokegents/dashboard/server/taskdomain"
)

type Command struct {
	RequestID       string          `json:"request_id"`
	Action          string          `json:"action"`
	TaskID          string          `json:"task_id"`
	ExpectedVersion int64           `json:"expected_version"`
	Data            json.RawMessage `json:"data"`
}
type Capabilities struct {
	AppVersion         string `json:"app_version"`
	SchemaVersion      int    `json:"schema_version"`
	RuntimeVersion     string `json:"runtime_version"`
	RuntimePath        string `json:"runtime_path"`
	ChatEnabled        bool   `json:"chat_enabled"`
	DisabledReason     string `json:"disabled_reason"`
	LocalPreviewAssets bool   `json:"local_preview_assets,omitempty"`
	DesktopEnabled     bool   `json:"desktop_enabled"`
	TerminalAvailable  bool   `json:"terminal_available"`
}
type runtimeRef struct {
	client       *runtimeadapter.Client
	thread, turn string
	mu           sync.Mutex
	done         chan struct{}
	cancel       context.CancelFunc
	interrupt    sync.Once
}
type Service struct {
	Store        *sqlstore.Store
	Capabilities Capabilities
	History      *history.Manager
	mu           sync.Mutex
	live         map[string]*runtimeRef
	closing      bool
}

func NewService(store *sqlstore.Store, capabilities Capabilities) *Service {
	return &Service{Store: store, Capabilities: capabilities, live: map[string]*runtimeRef{}}
}
func decode(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return d.Fail("INVALID_REQUEST")
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return d.Fail("INVALID_REQUEST")
	}
	return nil
}

func readPreferences(ctx context.Context, tx *sql.Tx) (map[string]any, error) {
	prefs := map[string]any{"locale": "ja-JP", "board_order": []string{}}
	var saved []byte
	err := tx.QueryRowContext(ctx, `SELECT payload FROM settings WHERE id='preferences'`).Scan(&saved)
	if errors.Is(err, sql.ErrNoRows) {
		return prefs, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(saved, &prefs); err != nil {
		return nil, d.Fail("INVALID_REQUEST")
	}
	if _, ok := prefs["locale"]; !ok {
		prefs["locale"] = "ja-JP"
	}
	if _, ok := prefs["board_order"]; !ok {
		prefs["board_order"] = []string{}
	}
	return prefs, nil
}

func writePreferences(ctx context.Context, tx *sql.Tx, prefs map[string]any) error {
	b, err := json.Marshal(prefs)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO settings(id,payload) VALUES('preferences',?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload`, string(b))
	return err
}

func validAgentSprite(sprite string) bool {
	switch sprite {
	case "slowking", "hawlucha", "bulbasaur", "pikachu", "squirtle", "charmander":
		return true
	default:
		return false
	}
}

type agentProfileInput struct {
	TaskID string `json:"task_id,omitempty"`
	Name   string `json:"name"`
	Sprite string `json:"sprite"`
}

func normalizeAgentProfile(name, sprite string) (agentProfileInput, error) {
	profile := agentProfileInput{Name: strings.TrimSpace(name), Sprite: sprite}
	if profile.Name == "" || utf8.RuneCountInString(profile.Name) > 80 || !validAgentSprite(profile.Sprite) {
		return agentProfileInput{}, d.Fail("INVALID_REQUEST")
	}
	return profile, nil
}

func saveAgentProfile(ctx context.Context, tx *sql.Tx, prefs map[string]any, taskID string, profile agentProfileInput) error {
	profiles, ok := prefs["agent_profiles"].(map[string]any)
	if !ok {
		if _, exists := prefs["agent_profiles"]; exists {
			return d.Fail("INVALID_REQUEST")
		}
		profiles = map[string]any{}
	}
	profiles[taskID] = map[string]string{"name": profile.Name, "sprite": profile.Sprite}
	prefs["agent_profiles"] = profiles
	return writePreferences(ctx, tx, prefs)
}

func (s *Service) Recover(ctx context.Context) error {
	_, err := s.Store.Update(ctx, "", nil, func(state *d.State, _ *sql.Tx) (any, []d.Event, error) {
		state.Recover()
		return true, []d.Event{{Type: "host.recovered"}}, nil
	})
	return err
}

func (s *Service) Execute(ctx context.Context, c Command) (json.RawMessage, error) {
	if len(c.RequestID) < 8 || len(c.RequestID) > 128 {
		return nil, d.Fail("INVALID_REQUEST_ID")
	}
	if len(c.Data) > runtimeadapter.MaxInput {
		return nil, d.Fail("INPUT_LIMIT")
	}
	raw, _ := json.Marshal(c)
	var launch *d.Run
	var project *d.Project
	var input string
	var stop string
	var approval *d.Approval
	var approvalDecision string
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil, d.Fail("APP_CLOSING")
	}
	s.mu.Unlock()
	result, err := s.Store.Update(ctx, c.RequestID, raw, func(state *d.State, tx *sql.Tx) (any, []d.Event, error) {
		var result any
		if c.Action == "settings.locale" {
			var data struct {
				Locale string `json:"locale"`
			}
			if err := decode(c.Data, &data); err != nil {
				return nil, nil, err
			}
			if data.Locale != "ja-JP" && data.Locale != "en-US" {
				return nil, nil, d.Fail("INVALID_REQUEST")
			}
			prefs, err := readPreferences(ctx, tx)
			if err != nil {
				return nil, nil, err
			}
			prefs["locale"] = data.Locale
			if err := writePreferences(ctx, tx, prefs); err != nil {
				return nil, nil, err
			}
			result = data
		} else if c.Action == "settings.desktop" {
			var data struct {
				Notifications *bool `json:"notifications"`
				Background    *bool `json:"background"`
			}
			if err := decode(c.Data, &data); err != nil || data.Notifications == nil || data.Background == nil {
				return nil, nil, d.Fail("INVALID_REQUEST")
			}
			prefs, err := readPreferences(ctx, tx)
			if err != nil {
				return nil, nil, err
			}
			prefs["desktop"] = DesktopPreferences{Notifications: *data.Notifications, Background: *data.Background}
			if err := writePreferences(ctx, tx, prefs); err != nil {
				return nil, nil, err
			}
			result = prefs["desktop"]
		} else if c.Action == "settings.agent" {
			var data agentProfileInput
			if err := decode(c.Data, &data); err != nil {
				return nil, nil, err
			}
			profile, err := normalizeAgentProfile(data.Name, data.Sprite)
			if data.TaskID == "" || state.Tasks[data.TaskID] == nil || err != nil {
				return nil, nil, d.Fail("INVALID_REQUEST")
			}
			data.Name = profile.Name
			prefs, err := readPreferences(ctx, tx)
			if err != nil {
				return nil, nil, err
			}
			if err := saveAgentProfile(ctx, tx, prefs, data.TaskID, profile); err != nil {
				return nil, nil, err
			}
			result = data
		} else if c.Action == "settings.board" {
			var data struct {
				Order []string `json:"order"`
			}
			if err := decode(c.Data, &data); err != nil {
				return nil, nil, err
			}
			if len(data.Order) > 10000 {
				return nil, nil, d.Fail("INVALID_REQUEST")
			}
			seen := make(map[string]bool, len(data.Order))
			for _, taskID := range data.Order {
				if taskID == "" || seen[taskID] || state.Tasks[taskID] == nil {
					return nil, nil, d.Fail("INVALID_REQUEST")
				}
				seen[taskID] = true
			}
			prefs, err := readPreferences(ctx, tx)
			if err != nil {
				return nil, nil, err
			}
			prefs["board_order"] = data.Order
			if err := writePreferences(ctx, tx, prefs); err != nil {
				return nil, nil, err
			}
			result = data
		} else if c.Action == "project.create" {
			var data struct{ Name, Path string }
			if err := decode(c.Data, &data); err != nil {
				return nil, nil, err
			}
			p, err := processhost.Workspace(data.Path)
			if err != nil {
				return nil, nil, err
			}
			for _, old := range state.Projects {
				if old.Identity == p.Identity {
					return nil, nil, d.Fail("PROJECT_EXISTS")
				}
			}
			if strings.TrimSpace(data.Name) != "" {
				p.Name = data.Name
			}
			state.Projects[p.ID] = p
			result = p
		} else if c.Action == "role.save" {
			var data struct{ ID, Name, Prompt string }
			if err := decode(c.Data, &data); err != nil {
				return nil, nil, err
			}
			if strings.TrimSpace(data.Name) == "" {
				return nil, nil, d.Fail("INVALID_ROLE")
			}
			if data.ID == "" {
				data.ID = d.ID()
			} else if state.Roles[data.ID] == nil {
				return nil, nil, d.Fail("NOT_FOUND")
			}
			r := &d.Role{ID: data.ID, Name: data.Name, Prompt: data.Prompt}
			state.Roles[r.ID] = r
			result = r
		} else if c.Action == "task.create" {
			var data struct {
				Spec  d.Spec             `json:"spec"`
				Agent *agentProfileInput `json:"agent"`
			}
			if err := decode(c.Data, &data); err != nil {
				return nil, nil, err
			}
			t, err := state.Create(data.Spec)
			if err != nil {
				return nil, nil, err
			}
			if data.Agent != nil {
				profile, err := normalizeAgentProfile(data.Agent.Name, data.Agent.Sprite)
				if err != nil {
					return nil, nil, err
				}
				prefs, err := readPreferences(ctx, tx)
				if err != nil {
					return nil, nil, err
				}
				if err := saveAgentProfile(ctx, tx, prefs, t.ID, profile); err != nil {
					return nil, nil, err
				}
			}
			result = t
		} else if c.Action == "notification.read" {
			var data struct{ ID string }
			if err := decode(c.Data, &data); err != nil {
				return nil, nil, err
			}
			n := state.Notifications[data.ID]
			if n == nil {
				return nil, nil, d.Fail("NOT_FOUND")
			}
			n.Read = true
			result = n
		} else if c.Action == "approval.respond" {
			var data struct {
				ApprovalID string `json:"approval_id"`
				RunID      string `json:"run_id"`
				Generation int64  `json:"generation"`
				Decision   string `json:"decision"`
			}
			if err := decode(c.Data, &data); err != nil {
				return nil, nil, err
			}
			a, err := state.RespondApproval(data.ApprovalID, data.RunID, data.Generation, data.Decision)
			if err != nil {
				return nil, nil, err
			}
			approval = a
			approvalDecision = data.Decision
			result = a
		} else {
			t, err := state.Task(c.TaskID, c.ExpectedVersion)
			if err != nil {
				return nil, nil, err
			}
			switch c.Action {
			case "task.ready":
				err = state.Ready(t)
			case "task.revise":
				var data struct {
					Spec d.Spec `json:"spec"`
				}
				err = decode(c.Data, &data)
				if err == nil {
					err = state.Revise(t, data.Spec)
				}
			case "task.start":
				if !s.Capabilities.ChatEnabled {
					return nil, nil, d.Fail("RUNTIME_GATE_CLOSED")
				}
				var data struct{ Input string }
				if err = decode(c.Data, &data); err != nil {
					break
				}
				if strings.TrimSpace(data.Input) == "" {
					err = d.Fail("INPUT_REQUIRED")
					break
				}
				original := state.Projects[t.Spec.ProjectID]
				if original == nil {
					err = d.Fail("INVALID_PROJECT")
					break
				}
				p, e := processhost.Workspace(original.DisplayPath)
				if e != nil {
					err = e
					break
				}
				if p.Identity != original.Identity {
					err = d.Fail("WORKSPACE_CHANGED")
					break
				}
				original.ResolvedPath = p.ResolvedPath
				original.Ancestors = p.Ancestors
				r, e := state.Begin(t, 2, s.Capabilities.RuntimeVersion, "")
				if e != nil {
					err = e
					break
				}
				owner, ownerErr := processhost.NewOwnership()
				if ownerErr != nil {
					err = d.Fail("OWNERSHIP_UNVERIFIED")
					break
				}
				privateOwner, ownerErr := json.Marshal(owner)
				if ownerErr != nil {
					err = ownerErr
					break
				}
				_, err = tx.ExecContext(ctx, `INSERT INTO runtime_instances(id,run_id,generation,payload) VALUES(?,?,?,?)`, r.ID, r.ID, r.Generation, string(privateOwner))
				if err != nil {
					break
				}
				launch = r
				project = original
				input = data.Input
				err = sqlstore.AddMessage(ctx, tx, d.Message{ID: d.ID(), TaskID: t.ID, RunID: r.ID, Role: "user", Body: input, At: d.Now()})
				result = r
			case "task.cancel":
				r := state.Runs[t.CurrentRunID]
				if r == nil {
					err = d.Fail("NOT_FOUND")
					break
				}
				if r.Terminal() {
					err = d.Fail("INVALID_TRANSITION")
					break
				}
				next := "cancelling"
				if r.State == "queued" {
					next = "cancelled"
				}
				err = state.Advance(t.ID, r.ID, r.Generation, next)
				if err == nil {
					stop = r.ID
					if next == "cancelled" {
						err = state.Quiesce(t.ID, r.ID, r.Generation)
					}
				}
			case "task.submit":
				var data struct {
					Submission d.Submission `json:"submission"`
				}
				err = decode(c.Data, &data)
				if err == nil {
					err = state.Submit(t, data.Submission)
				}
			case "task.review":
				var data struct {
					SubmissionID     string `json:"submission_id"`
					Decision, Reason string
				}
				err = decode(c.Data, &data)
				if err == nil {
					err = state.Review(t, data.SubmissionID, data.Decision, data.Reason)
				}
			case "task.retry":
				err = state.Retry(t)
			case "context.save":
				var data struct{ Packet d.ContextPacket }
				err = decode(c.Data, &data)
				if err == nil {
					err = data.Packet.Validate()
				}
				if err == nil {
					if data.Packet.TaskID != t.ID || data.Packet.SpecVersion != t.Spec.Version {
						err = d.Fail("STALE_VERSION")
					} else {
						if data.Packet.SourceRunID != "" {
							source := state.Runs[data.Packet.SourceRunID]
							if source == nil || source.TaskID != t.ID || source.SpecVersion != t.Spec.Version {
								err = d.Fail("STALE_VERSION")
								break
							}
						}
						data.Packet.Objective = t.Spec.Objective
						data.Packet.AllowedScope = t.Spec.AllowedScope
						data.Packet.Criteria = append([]d.Criterion(nil), t.Spec.Criteria...)
						data.Packet.Effects = []d.Effect{}
						for _, effect := range state.Effects {
							source := state.Runs[effect.RunID]
							if source != nil && source.TaskID == t.ID {
								data.Packet.Effects = append(data.Packet.Effects, *effect)
							}
						}
						if err = data.Packet.Validate(); err != nil {
							break
						}
						state.Contexts[t.ID] = &data.Packet
						t.Version++
					}
				}
			case "recovery.confirm":
				var data struct{ Reason string }
				if err = decode(c.Data, &data); err == nil {
					err = s.verifyRecoveryTask(ctx, tx, state, t)
					if err == nil {
						err = state.ConfirmRecovery(t, data.Reason, true)
					}
				}
			case "recovery.resolve_effect":
				var data struct {
					EffectID string `json:"effect_id"`
					Outcome  string `json:"outcome"`
					Evidence string `json:"evidence"`
				}
				if err = decode(c.Data, &data); err == nil {
					err = s.verifyRecoveryTask(ctx, tx, state, t)
					if err == nil {
						err = state.ResolveEffect(t, data.EffectID, data.Outcome, data.Evidence)
					}
				}
			default:
				err = d.Fail("UNKNOWN_ACTION")
			}
			if err != nil {
				return nil, nil, err
			}
			if result == nil {
				result = t
			}
		}
		return result, []d.Event{{Type: c.Action, TaskID: c.TaskID}}, nil
	})
	if err != nil {
		return nil, err
	}
	if launch != nil {
		s.launch(launch, project, input)
	}
	if stop != "" {
		go s.stop(stop)
	}
	if approval != nil {
		s.mu.Lock()
		ref := s.live[approval.RunID]
		s.mu.Unlock()
		if ref != nil {
			ref.mu.Lock()
			client := ref.client
			ref.mu.Unlock()
			if client != nil {
				if e := client.Approval(approval.ProviderID, approvalDecision); e != nil {
					go s.stop(approval.RunID)
				}
			}
		}
	}
	return result, nil
}

func (s *Service) verifyRecoveryTask(ctx context.Context, tx *sql.Tx, state *d.State, task *d.Task) error {
	if state.Runs[task.CurrentRunID] == nil {
		return d.Fail("RECOVERY_REQUIRED")
	}
	for _, run := range state.Runs {
		if run.TaskID == task.ID && (!run.WorkspaceQuiet || run.ID == task.CurrentRunID) {
			if err := s.verifyRecoveryOwner(ctx, tx, run); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) verifyRecoveryOwner(ctx context.Context, tx *sql.Tx, run *d.Run) error {
	if run == nil || !run.Terminal() {
		return d.Fail("RECOVERY_REQUIRED")
	}
	s.mu.Lock()
	active := s.live[run.ID] != nil
	s.mu.Unlock()
	if active {
		return d.Fail("RECOVERY_REQUIRED")
	}
	var owner processhost.Ownership
	var privateOwner []byte
	if tx.QueryRowContext(ctx, `SELECT payload FROM runtime_instances WHERE id=? AND run_id=? AND generation=?`, run.ID, run.ID, run.Generation).Scan(&privateOwner) != nil || json.Unmarshal(privateOwner, &owner) != nil || processhost.VerifyReleased(owner) != nil {
		return d.Fail("RECOVERY_REQUIRED")
	}
	return nil
}

func (s *Service) update(run *d.Run, eventType string, fn func(*d.State, *d.Task, *d.Run, *sql.Tx) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	_, err := s.Store.Update(ctx, "", nil, func(state *d.State, tx *sql.Tx) (any, []d.Event, error) {
		t, r, err := state.Active(run.TaskID, run.ID, run.Generation)
		if err != nil {
			return nil, nil, err
		}
		if err = fn(state, t, r, tx); err != nil {
			return nil, nil, err
		}
		return true, []d.Event{{Type: eventType, TaskID: t.ID, RunID: r.ID, Generation: r.Generation}}, nil
	})
	return err
}
func (s *Service) launch(run *d.Run, project *d.Project, input string) {
	ctx, cancel := context.WithCancel(context.Background())
	ref := &runtimeRef{done: make(chan struct{}), cancel: cancel}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		cancel()
		s.finish(run, "interrupted", "APP_CLOSING", true)
		return
	}
	s.live[run.ID] = ref
	s.mu.Unlock()
	go func() {
		defer close(ref.done)
		defer cancel()
		defer func() { s.mu.Lock(); delete(s.live, run.ID); s.mu.Unlock() }()
		if err := s.update(run, "run.starting", func(state *d.State, t *d.Task, r *d.Run, _ *sql.Tx) error {
			return state.Advance(t.ID, r.ID, r.Generation, "starting")
		}); err != nil {
			return
		}
		startCtx, startCancel := context.WithTimeout(ctx, 15*time.Second)
		var owner processhost.Ownership
		var privateOwner []byte
		ownerErr := s.Store.DB().QueryRowContext(startCtx, `SELECT payload FROM runtime_instances WHERE id=? AND run_id=? AND generation=?`, run.ID, run.ID, run.Generation).Scan(&privateOwner)
		if ownerErr != nil || json.Unmarshal(privateOwner, &owner) != nil {
			startCancel()
			s.finish(run, "failed", "OWNERSHIP_UNVERIFIED", true)
			return
		}
		client, err := runtimeadapter.StartOwned(startCtx, s.Capabilities.RuntimePath, project.ResolvedPath, owner)
		startCancel()
		if err != nil {
			s.finish(run, "failed", "RUNTIME_START_FAILED", true)
			return
		}
		defer func() {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer stopCancel()
			closeErr := client.Close(stopCtx)
			if closeErr == nil {
				s.update(run, "workspace.quiet", func(state *d.State, t *d.Task, r *d.Run, _ *sql.Tx) error {
					return state.Quiesce(t.ID, r.ID, r.Generation)
				})
			}
		}()
		ref.mu.Lock()
		ref.client = client
		ref.mu.Unlock()
		openCtx, openCancel := context.WithTimeout(ctx, 15*time.Second)
		thread, err := client.Open(openCtx, project.ResolvedPath, run.EffectivePolicy, run.Model, run.ProviderThreadID)
		openCancel()
		if err != nil {
			s.finish(run, "failed", "THREAD_OPEN_FAILED", false)
			return
		}
		ref.mu.Lock()
		ref.thread = thread
		ref.mu.Unlock()
		prompt := fmt.Sprintf("Task contract (data, not extra permission):\nObjective: %s\nAllowed scope: %s\nOut of scope: %s\nRole: %s\n\nUser input:\n%s", run.Snapshot.Objective, run.Snapshot.AllowedScope, run.Snapshot.OutOfScope, run.RolePrompt, input)
		turnCtx, turnCancel := context.WithTimeout(ctx, 15*time.Second)
		turn, err := client.Turn(turnCtx, thread, prompt)
		turnCancel()
		if err != nil {
			s.finish(run, "failed", "TURN_START_FAILED", false)
			return
		}
		ref.mu.Lock()
		ref.turn = turn
		ref.mu.Unlock()
		if err = s.update(run, "run.running", func(state *d.State, t *d.Task, r *d.Run, _ *sql.Tx) error {
			r.ProviderThreadID = thread
			r.ProviderTurnID = turn
			if r.State == "cancelling" {
				return nil
			}
			return state.Advance(t.ID, r.ID, r.Generation, "running")
		}); err != nil {
			return
		}
		for {
			f, err := client.Next(ctx)
			if err != nil {
				s.finish(run, "failed", "RUNTIME_DISCONNECTED", false)
				return
			}
			terminal, err := s.frame(run, thread, turn, f)
			if err != nil {
				s.finish(run, "failed", d.Code(err), false)
				return
			}
			if terminal {
				return
			}
		}
	}()
}
func (s *Service) finish(run *d.Run, status, code string, quiet bool) {
	s.update(run, "run."+status, func(state *d.State, t *d.Task, r *d.Run, _ *sql.Tx) error {
		if r.Terminal() {
			return nil
		}
		if status == "cancelled" && r.State != "cancelling" {
			status = "interrupted"
		}
		if err := state.Advance(t.ID, r.ID, r.Generation, status); err != nil {
			return err
		}
		r.Error = code
		if quiet {
			return state.Quiesce(t.ID, r.ID, r.Generation)
		}
		return nil
	})
}
func (s *Service) frame(run *d.Run, thread, turn string, f runtimeadapter.Frame) (bool, error) {
	var p struct {
		ThreadID string                      `json:"threadId"`
		TurnID   string                      `json:"turnId"`
		ItemID   string                      `json:"itemId"`
		Delta    string                      `json:"delta"`
		Turn     struct{ ID, Status string } `json:"turn"`
	}
	if json.Unmarshal(f.Params, &p) != nil {
		return false, d.Fail("PROTOCOL_ERROR")
	}
	if p.ThreadID != "" && p.ThreadID != thread {
		return false, s.quarantine(run, f, p.ThreadID, p.TurnID)
	}
	if p.TurnID != "" && p.TurnID != turn {
		return false, s.quarantine(run, f, p.ThreadID, p.TurnID)
	}
	if len(f.ID) > 0 {
		if run.EffectivePolicy == "read-only" {
			return false, d.Fail("UNSUPPORTED_APPROVAL")
		}
		kind := ""
		if f.Method == "item/commandExecution/requestApproval" {
			kind = "command"
		}
		if f.Method == "item/fileChange/requestApproval" {
			kind = "file_change"
		}
		if kind == "" {
			return false, d.Fail("UNSUPPORTED_APPROVAL")
		}
		return false, s.update(run, "approval.requested", func(state *d.State, t *d.Task, r *d.Run, _ *sql.Tx) error {
			return state.AddApproval(t.ID, r.ID, r.Generation, d.Approval{Kind: kind, Request: f.Params, ProviderID: f.ID})
		})
	}
	if f.Method == "item/agentMessage/delta" {
		if p.ThreadID != thread || p.TurnID != turn || p.ItemID == "" {
			return false, d.Fail("PROTOCOL_ERROR")
		}
		return false, s.update(run, "message.delta", func(_ *d.State, t *d.Task, r *d.Run, tx *sql.Tx) error {
			id := r.ID + ":" + p.ItemID
			var existing string
			err := tx.QueryRow(`SELECT body FROM messages WHERE id=?`, id).Scan(&existing)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			body := existing + p.Delta
			if len(body) > 8<<20 {
				return d.Fail("OUTPUT_LIMIT")
			}
			_, err = tx.Exec(`INSERT INTO messages(id,task_id,run_id,role,body,normalized,created_at) VALUES(?,?,?,'assistant',?,?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body,normalized=excluded.normalized`, id, t.ID, r.ID, body, sqlstore.Normalize(body), d.Now())
			return err
		})
	}
	if f.Method == "turn/completed" {
		if p.ThreadID != thread || p.Turn.ID != turn {
			return false, s.quarantine(run, f, p.ThreadID, p.Turn.ID)
		}
		status := "failed"
		switch p.Turn.Status {
		case "completed":
			status = "succeeded"
		case "interrupted":
			status = "cancelled"
		case "failed":
		default:
			return false, d.Fail("PROTOCOL_ERROR")
		}
		s.finish(run, status, "", false)
		return true, nil
	}
	return false, nil
}

func (s *Service) quarantine(run *d.Run, f runtimeadapter.Frame, thread, turn string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.Store.Update(ctx, "", nil, func(state *d.State, _ *sql.Tx) (any, []d.Event, error) {
		if _, _, err := state.Active(run.TaskID, run.ID, run.Generation); err != nil {
			return nil, nil, err
		}
		return true, []d.Event{{Type: "runtime.event_quarantined", TaskID: run.TaskID, RunID: run.ID, Generation: run.Generation, Payload: map[string]string{"method": f.Method, "provider_thread_id": thread, "provider_turn_id": turn}}}, nil
	})
	if len(f.ID) > 0 {
		s.mu.Lock()
		ref := s.live[run.ID]
		s.mu.Unlock()
		if ref != nil {
			ref.mu.Lock()
			client := ref.client
			ref.mu.Unlock()
			if client != nil {
				return client.Approval(f.ID, "decline")
			}
		}
	}
	return err
}
func (s *Service) stop(runID string) {
	s.mu.Lock()
	ref := s.live[runID]
	s.mu.Unlock()
	if ref == nil {
		return
	}
	ref.interrupt.Do(func() {
		ref.mu.Lock()
		client, thread, turn := ref.client, ref.thread, ref.turn
		ref.mu.Unlock()
		if client != nil && thread != "" && turn != "" {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			client.Interrupt(ctx, thread, turn)
			cancel()
		}
		select {
		case <-ref.done:
			return
		case <-time.After(3 * time.Second):
			ref.cancel()
			if client != nil {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				client.Close(ctx)
				cancel()
			}
		}
	})
}
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	ids := []string{}
	refs := []*runtimeRef{}
	for id, ref := range s.live {
		ids = append(ids, id)
		refs = append(refs, ref)
	}
	s.mu.Unlock()
	for _, id := range ids {
		go s.stop(id)
	}
	if s.History != nil {
		s.History.Cancel()
	}
	for _, ref := range refs {
		select {
		case <-ref.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.History != nil {
		return s.History.Close(ctx)
	}
	return nil
}
func (s *Service) Active() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.live) > 0 }
