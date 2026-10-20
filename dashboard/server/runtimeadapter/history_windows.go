//go:build windows

package runtimeadapter

import (
	"context"
	"encoding/json"
	"errors"

	"pokegents/dashboard/server/processhost"
)

type HistoryThread struct {
	Complete    bool            `json:"-"`
	ID          string          `json:"id"`
	Name        *string         `json:"name"`
	Preview     string          `json:"preview"`
	CWD         string          `json:"cwd"`
	CreatedAt   int64           `json:"createdAt"`
	UpdatedAt   int64           `json:"updatedAt"`
	Source      json.RawMessage `json:"source"`
	Status      json.RawMessage `json:"status"`
	HistoryMode string          `json:"historyMode"`
	Turns       []HistoryTurn   `json:"turns"`
}

type HistoryTurn struct {
	StartedAt *int64            `json:"startedAt"`
	ItemsView string            `json:"itemsView"`
	ID        string            `json:"id"`
	Status    string            `json:"status"`
	Items     []json.RawMessage `json:"items"`
}

type HistoryPage struct {
	Data       []HistoryThread `json:"data"`
	NextCursor string          `json:"nextCursor"`
}

func (c *Client) ListHistory(ctx context.Context, cwd, cursor string, archived bool) (HistoryPage, error) {
	params := map[string]any{
		"cwd":           cwd,
		"archived":      archived,
		"limit":         20,
		"sortKey":       "updated_at",
		"sortDirection": "desc",
		"sourceKinds":   []string{"cli", "vscode", "appServer"},
	}
	if cursor != "" {
		params["cursor"] = cursor
	}
	var page HistoryPage
	if err := c.Call(ctx, "thread/list", params, &page); err != nil {
		return HistoryPage{}, err
	}
	return page, nil
}

func (c *Client) ReadHistory(ctx context.Context, id string) (HistoryThread, error) {
	var response struct {
		Thread HistoryThread `json:"thread"`
	}
	if err := c.Call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": false}, &response); err != nil {
		return HistoryThread{}, err
	}
	if response.Thread.HistoryMode != "paginated" {
		if err := c.Call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": true}, &response); err != nil {
			return HistoryThread{}, err
		}
		return response.Thread, nil
	}
	return hydrateHistory(ctx, c.Call, response.Thread)
}

// Every page is read explicitly. Never treat a hydrated preview window as the
// entire thread. Budgets and repeated cursors fail without a partial import.
func hydrateHistory(ctx context.Context, call func(context.Context, string, any, any) error, thread HistoryThread) (HistoryThread, error) {
	thread.Turns = nil
	total := 0
	itemCount := 0
	seenTurns := map[string]bool{}
	pageLoop := func(method string, params map[string]any, consume func([]json.RawMessage) error) error {
		seen := map[string]bool{}
		for pages := 0; pages < 500; pages++ {
			var page struct {
				Data       []json.RawMessage `json:"data"`
				NextCursor string            `json:"nextCursor"`
			}
			if err := call(ctx, method, params, &page); err != nil {
				return err
			}
			if len(page.Data) > 100 || len(page.NextCursor) > 4096 {
				return errors.New("HISTORY_LIMIT")
			}
			for _, raw := range page.Data {
				total += len(raw)
			}
			if total > MaxFrame {
				return errors.New("HISTORY_LIMIT")
			}
			if err := consume(page.Data); err != nil {
				return err
			}
			if page.NextCursor == "" {
				return nil
			}
			if seen[page.NextCursor] {
				return errors.New("PROTOCOL_ERROR")
			}
			seen[page.NextCursor] = true
			params["cursor"] = page.NextCursor
		}
		return errors.New("HISTORY_LIMIT")
	}
	err := pageLoop("thread/turns/list", map[string]any{"threadId": thread.ID, "limit": 100, "sortDirection": "asc", "itemsView": "summary"}, func(data []json.RawMessage) error {
		for _, raw := range data {
			var turn HistoryTurn
			if json.Unmarshal(raw, &turn) != nil || turn.ID == "" || seenTurns[turn.ID] {
				return errors.New("PROTOCOL_ERROR")
			}
			seenTurns[turn.ID] = true
			turn.Items = nil
			thread.Turns = append(thread.Turns, turn)
			if len(thread.Turns) > 10000 {
				return errors.New("HISTORY_LIMIT")
			}
		}
		return nil
	})
	if err != nil {
		return HistoryThread{}, err
	}
	for i := range thread.Turns {
		turn := &thread.Turns[i]
		turn.ItemsView = "full"
		if turn.Status == "inProgress" {
			continue
		}
		err = pageLoop("thread/items/list", map[string]any{"threadId": thread.ID, "turnId": turn.ID, "limit": 100, "sortDirection": "asc"}, func(data []json.RawMessage) error {
			for _, raw := range data {
				var entry struct {
					TurnID string          `json:"turnId"`
					Item   json.RawMessage `json:"item"`
				}
				if json.Unmarshal(raw, &entry) != nil || entry.TurnID != turn.ID || len(entry.Item) == 0 {
					return errors.New("PROTOCOL_ERROR")
				}
				turn.Items = append(turn.Items, entry.Item)
				itemCount++
				if itemCount > 10000 {
					return errors.New("HISTORY_LIMIT")
				}
			}
			return nil
		})
		if err != nil {
			return HistoryThread{}, err
		}
	}
	thread.Complete = true
	return thread, nil
}

func StartAtHome(ctx context.Context, exe, cwd, home string) (*Client, error) {
	env := append(processhost.Environment(), "CODEX_HOME="+home)
	p, err := processhost.Start(exe, []string{"app-server", "--listen", "stdio://"}, cwd, env)
	return initialize(ctx, p, err)
}
