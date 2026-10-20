package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// MessageManager manages inter-agent messages.
type MessageStore struct {
	mu      sync.Mutex
	dataDir string
	history []Message // recent message history for UI
}

func NewMessageStore(dataDir string) *MessageStore {
	msgDir := filepath.Join(dataDir, "messages")
	os.MkdirAll(msgDir, 0755)
	ms := &MessageStore{
		dataDir: dataDir,
		history: make([]Message, 0),
	}
	ms.loadHistory()
	return ms
}

// Send stores a message in the recipient's mailbox for hook delivery.
func (ms *MessageStore) Send(from, fromName, to, toName, content string) (*Message, error) {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	msg := Message{
		ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
		From:      from,
		FromName:  fromName,
		To:        to,
		ToName:    toName,
		Content:   content,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Delivered: false,
	}

	// Write to recipient's mailbox directory
	mailbox := filepath.Join(ms.dataDir, "messages", to)
	os.MkdirAll(mailbox, 0755)

	data, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}

	msgFile := filepath.Join(mailbox, msg.ID+".json")
	if err := os.WriteFile(msgFile, data, 0644); err != nil {
		return nil, err
	}

	// Add to history
	ms.history = append(ms.history, msg)
	if len(ms.history) > 200 {
		ms.history = ms.history[len(ms.history)-200:]
	}
	ms.saveHistory()

	return &msg, nil
}

// GetPending returns undelivered messages for a session WITHOUT deleting them.
func (ms *MessageStore) GetPending(sessionID string) []Message {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	var pending []Message
	for _, msg := range ms.readMailbox(sessionID) {
		if !msg.Delivered {
			pending = append(pending, msg)
		}
	}
	return pending
}

// DeliverPending marks undelivered messages as delivered and returns them.
// Called by the hook on UserPromptSubmit — injects content via systemMessage.
// Files stay on disk so check_messages can still find them as a fallback.
func (ms *MessageStore) DeliverPending(sessionID string) []Message {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	allMessages := ms.readMailbox(sessionID)
	mailbox := filepath.Join(ms.dataDir, "messages", sessionID)

	var delivered []Message
	for _, msg := range allMessages {
		if msg.Delivered {
			continue
		}
		msg.Delivered = true
		delivered = append(delivered, msg)

		// Rewrite the file with delivered: true
		data, err := json.Marshal(msg)
		if err == nil {
			os.WriteFile(filepath.Join(mailbox, msg.ID+".json"), data, 0644)
		}
	}
	return delivered
}

// ConsumePending returns undelivered messages and removes them from the mailbox.
// Called by the MCP check_messages tool — the agent explicitly acknowledged receipt.
func (ms *MessageStore) ConsumePending(sessionID string) []Message {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	messages := ms.readMailbox(sessionID)

	// Delete consumed messages from mailbox
	mailbox := filepath.Join(ms.dataDir, "messages", sessionID)
	for _, msg := range messages {
		os.Remove(filepath.Join(mailbox, msg.ID+".json"))
	}

	// Update history
	if len(messages) > 0 {
		for _, msg := range messages {
			for i := range ms.history {
				if ms.history[i].ID == msg.ID {
					ms.history[i].Delivered = true
					break
				}
			}
		}
		ms.saveHistory()
	}

	return messages
}

func (ms *MessageStore) readMailbox(sessionID string) []Message {
	mailbox := filepath.Join(ms.dataDir, "messages", sessionID)
	entries, err := os.ReadDir(mailbox)
	if err != nil {
		return nil
	}

	var messages []Message
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		path := filepath.Join(mailbox, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var msg Message
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		messages = append(messages, msg)
	}

	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Timestamp < messages[j].Timestamp
	})

	return messages
}

// GetHistory returns recent message history.
func (ms *MessageStore) GetHistory() []Message {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	result := make([]Message, len(ms.history))
	copy(result, ms.history)
	return result
}

// GetConnections returns unique agent pairs that have communicated.
func (ms *MessageStore) GetConnections() []AgentConnection {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	type pairKey struct{ a, b string }
	pairs := make(map[pairKey]*AgentConnection)

	for _, msg := range ms.history {
		// Normalize pair order
		a, b := msg.From, msg.To
		an, bn := msg.FromName, msg.ToName
		if a > b {
			a, b = b, a
			an, bn = bn, an
		}
		key := pairKey{a, b}
		if conn, ok := pairs[key]; ok {
			conn.MessageCount++
			if msg.Timestamp > conn.LastMessage {
				conn.LastMessage = msg.Timestamp
			}
		} else {
			pairs[key] = &AgentConnection{
				AgentA:       a,
				AgentB:       b,
				AgentAName:   an,
				AgentBName:   bn,
				MessageCount: 1,
				LastMessage:  msg.Timestamp,
			}
		}
	}

	result := make([]AgentConnection, 0, len(pairs))
	for _, conn := range pairs {
		result = append(result, *conn)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].LastMessage > result[j].LastMessage
	})
	return result
}

// --- persistence ---

func (ms *MessageStore) historyPath() string {
	return filepath.Join(ms.dataDir, "messages", "_history.json")
}

func (ms *MessageStore) loadHistory() {
	data, err := os.ReadFile(ms.historyPath())
	if err != nil {
		return
	}
	json.Unmarshal(data, &ms.history)
}

func (ms *MessageStore) saveHistory() {
	data, _ := json.Marshal(ms.history)
	os.WriteFile(ms.historyPath(), data, 0644)
}
