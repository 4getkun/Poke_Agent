// Package services provides higher-level features built on the core engine and store.
package services

import (
	"fmt"
	"log"
	"sync"
	"time"

	"pokegents/dashboard/server/store"
)

// WakeFunc tells an idle agent it has new messages waiting. The server
// dispatches through the Runtime registry — iterm2 types "check messages"
// into the TTY, chat sends an ACP prompt. Keeps messaging free of runtime
// imports.
type WakeFunc func(pgid string) error

// AgentLookupFunc returns agent state for nudge decisions.
// Injected by the server — keeps messaging free of state manager imports.
type AgentLookupFunc func(sessionID string) *AgentInfo

// IsSessionFocusedFunc checks if a terminal session is currently focused by
// the user. Only meaningful for iterm2 agents; nil for chat (chat agents
// have no TTY-focus concept).
type IsSessionFocusedFunc func(iTermSessionID, tty string) bool

// AgentInfo is the minimal agent state the nudger needs for its guards.
type AgentInfo struct {
	State          string
	IsAlive        bool
	LastUpdated    string
	TTY            string
	ITermSessionID string
}

// MessagingService consolidates message routing, delivery, budget, and nudging.
type MessagingService struct {
	store            store.MessageStore
	wake             WakeFunc
	getAgent         AgentLookupFunc
	isSessionFocused IsSessionFocusedFunc

	// Nudger state
	mu           sync.Mutex
	pending      map[string]*time.Timer // session_id → scheduled nudge
	lastNudge    map[string]time.Time   // session_id → last nudge time
	focusRetries map[string]int         // session_id → focus-defer retry count
	debounce     time.Duration
	batchDelay   time.Duration
	minIdle      time.Duration
	maxFocusRetries int
}

// NewMessagingService creates a messaging service with injected dependencies.
// `wake` may be nil at construction and set later via SetWake — useful when
// the runtime registry isn't built yet at messaging-service-init time.
func NewMessagingService(ms store.MessageStore, wake WakeFunc, getAgent AgentLookupFunc, isFocused IsSessionFocusedFunc) *MessagingService {
	return &MessagingService{
		store:            ms,
		wake:             wake,
		getAgent:         getAgent,
		isSessionFocused: isFocused,
		pending:          make(map[string]*time.Timer),
		lastNudge:        make(map[string]time.Time),
		focusRetries:     make(map[string]int),
		debounce:         5 * time.Second,
		batchDelay:       500 * time.Millisecond,
		minIdle:          1 * time.Second,
		maxFocusRetries:  3,
	}
}

// SetWake installs (or replaces) the wake callback. Safe to call after the
// service is constructed — used by the server to defer wiring until after
// the runtime registry is built.
func (s *MessagingService) SetWake(fn WakeFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wake = fn
}

// Send stores a message and returns whether the recipient should be nudged.
func (s *MessagingService) Send(from, fromName, to, toName, content string) (*store.Message, bool, error) {
	msg, err := s.store.Send(from, fromName, to, toName, content)
	if err != nil {
		return nil, false, err
	}

	// Determine if recipient needs a nudge
	needsNudge := false
	if agent := s.getAgent(to); agent != nil {
		needsNudge = agent.IsAlive && agent.State == "idle"
	}

	return msg, needsNudge, nil
}

// GetPending returns undelivered messages for a session.
func (s *MessagingService) GetPending(sessionID string) ([]store.Message, error) {
	return s.store.GetUndelivered(sessionID)
}

// Deliver marks undelivered messages as delivered and returns them.
// Called by the hook on UserPromptSubmit for systemMessage injection.
func (s *MessagingService) Deliver(sessionID string) ([]store.Message, error) {
	msgs, err := s.store.GetUndelivered(sessionID)
	if err != nil || len(msgs) == 0 {
		return nil, err
	}
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	if err := s.store.MarkDelivered(ids); err != nil {
		return nil, err
	}
	// Update delivered flag in returned messages
	for i := range msgs {
		msgs[i].Delivered = true
	}
	return msgs, nil
}

// Consume reads all messages and deletes their files.
// Called by the MCP check_messages tool.
func (s *MessagingService) Consume(sessionID string) ([]store.Message, error) {
	return s.store.Consume(sessionID)
}

// GetBudget returns the current message send count for a session.
func (s *MessagingService) GetBudget(sessionID string) (int, error) {
	return s.store.GetBudget(sessionID)
}

// ResetBudget resets the message send count to 0.
func (s *MessagingService) ResetBudget(sessionID string) error {
	return s.store.ResetBudget(sessionID)
}

// GetHistory returns recent message history for UI display.
func (s *MessagingService) GetHistory() ([]store.Message, error) {
	return s.store.GetHistory()
}

// GetConnections returns unique agent pairs that have communicated.
func (s *MessagingService) GetConnections() ([]store.Connection, error) {
	return s.store.GetConnections()
}

// ── Nudger ──────────────────────────────────────────────────────────────

// QueueNudge schedules a "check messages" nudge for an idle/done agent.
// If the agent is busy, no nudge is scheduled (the hook will deliver).
// If already queued, the timer resets (batches rapid messages).
func (s *MessagingService) QueueNudge(sessionID string) {
	agent := s.getAgent(sessionID)
	if agent == nil || !agent.IsAlive {
		return
	}

	if agent.State == "busy" || agent.State == "needs_input" || agent.State == "error" {
		log.Printf("nudger: skip %s — state is %s", sessionID[:8], agent.State)
		return
	}
	log.Printf("nudger: queuing nudge for %s (state=%s)", sessionID[:8], agent.State)

	s.mu.Lock()
	defer s.mu.Unlock()

	if t, ok := s.pending[sessionID]; ok {
		t.Stop()
	}

	s.pending[sessionID] = time.AfterFunc(s.batchDelay, func() {
		s.executeNudge(sessionID)
	})
}

func (s *MessagingService) executeNudge(sessionID string) {
	s.mu.Lock()
	delete(s.pending, sessionID)

	if last, ok := s.lastNudge[sessionID]; ok && time.Since(last) < s.debounce {
		log.Printf("nudger: skip %s — debounced (last nudge %v ago)", sessionID[:8], time.Since(last))
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	agent := s.getAgent(sessionID)
	if agent == nil || !agent.IsAlive {
		log.Printf("nudger: skip %s — agent nil or dead", sessionID[:8])
		return
	}

	if agent.State != "idle" {
		log.Printf("nudger: skip %s — state changed to %s", sessionID[:8], agent.State)
		return
	}

	// Don't nudge if agent just changed state (user might be typing)
	if agent.LastUpdated != "" {
		if t, err := time.Parse(time.RFC3339, agent.LastUpdated); err == nil {
			if time.Since(t) < s.minIdle {
				log.Printf("nudger: defer %s — only idle for %v (need %v)", sessionID[:8], time.Since(t), s.minIdle)
				s.mu.Lock()
				s.pending[sessionID] = time.AfterFunc(s.minIdle, func() {
					s.executeNudge(sessionID)
				})
				s.mu.Unlock()
				return
			}
		}
	}

	// Iterm2-specific guard: if the user is actively typing in the agent's
	// terminal, defer the nudge (don't clobber their in-progress prompt).
	// Only meaningful when we have a TTY/iTerm sid — chat agents have neither
	// and skip this branch entirely. After maxFocusRetries deferrals, nudge
	// anyway (Ctrl+U makes it safe).
	hasTTY := agent.ITermSessionID != "" || agent.TTY != ""
	if hasTTY && s.isSessionFocused != nil && s.isSessionFocused(agent.ITermSessionID, agent.TTY) {
		s.mu.Lock()
		retries := s.focusRetries[sessionID]
		if retries < s.maxFocusRetries {
			s.focusRetries[sessionID] = retries + 1
			log.Printf("nudger: defer %s — session focused (retry %d/%d)", sessionID[:8], retries+1, s.maxFocusRetries)
			s.pending[sessionID] = time.AfterFunc(2*time.Second, func() {
				s.executeNudge(sessionID)
			})
			s.mu.Unlock()
			return
		}
		// Max retries hit — nudge anyway, clear retry counter
		delete(s.focusRetries, sessionID)
		log.Printf("nudger: focus-retry limit reached for %s — nudging anyway", sessionID[:8])
		s.mu.Unlock()
	}

	s.mu.Lock()
	s.lastNudge[sessionID] = time.Now()
	delete(s.focusRetries, sessionID)
	wake := s.wake
	s.mu.Unlock()

	short := sessionID
	if len(short) > 8 {
		short = short[:8]
	}
	log.Printf("nudger: NUDGING %s", short)

	if wake == nil {
		log.Printf("nudger: no wake func wired for %s", short)
		return
	}
	if err := wake(sessionID); err != nil {
		log.Printf("nudger: wake error for %s: %v", short, err)
	}
}

// HasPendingNudge returns true if a nudge is scheduled for this session.
func (s *MessagingService) HasPendingNudge(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.pending[sessionID]
	return ok
}

// NudgeIfPending checks if a session has undelivered messages and queues a nudge.
// Called when an agent transitions to done/idle.
func (s *MessagingService) NudgeIfPending(sessionID string) {
	msgs, err := s.store.GetUndelivered(sessionID)
	if err != nil || len(msgs) == 0 {
		return
	}
	s.QueueNudge(sessionID)
}

// FormatMessages returns a display string for a set of messages.
func FormatMessages(msgs []store.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	result := ""
	for i, m := range msgs {
		if i > 0 {
			result += "\n---\n"
		}
		from := m.FromName
		if from == "" {
			from = m.From
		}
		result += fmt.Sprintf("[Message from %s]: %s", from, m.Content)
	}
	return result
}
