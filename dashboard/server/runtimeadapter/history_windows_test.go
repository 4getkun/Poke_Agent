//go:build windows

package runtimeadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestHydrateHistoryPagesTurnsAndItems(t *testing.T) {
	ctx := context.Background()
	var calls []struct {
		method string
		params map[string]any
	}
	call := func(_ context.Context, method string, raw any, out any) error {
		params := raw.(map[string]any)
		calls = append(calls, struct {
			method string
			params map[string]any
		}{method, params})
		var result any
		switch method {
		case "thread/turns/list":
			if len(calls) == 1 {
				result = map[string]any{"data": []any{map[string]any{"id": "turn-1", "status": "completed"}}, "nextCursor": "turn-page-2"}
			} else {
				result = map[string]any{"data": []any{map[string]any{"id": "turn-2", "status": "completed"}}}
			}
		case "thread/items/list":
			turnID := params["turnId"].(string)
			page, _ := params["cursor"].(string)
			if turnID == "turn-1" && page == "" {
				result = map[string]any{"data": []any{map[string]any{"turnId": turnID, "item": map[string]any{"id": "u1", "type": "userMessage"}}}, "nextCursor": "item-page-2"}
			} else if turnID == "turn-1" {
				result = map[string]any{"data": []any{map[string]any{"turnId": turnID, "item": map[string]any{"id": "a1", "type": "agentMessage"}}}}
			} else {
				result = map[string]any{"data": []any{map[string]any{"turnId": turnID, "item": map[string]any{"id": "a2", "type": "agentMessage"}}}}
			}
		default:
			t.Fatalf("unexpected method %q", method)
		}
		b, _ := json.Marshal(result)
		return json.Unmarshal(b, out)
	}
	thread, err := hydrateHistory(ctx, call, HistoryThread{ID: "thread-1", HistoryMode: "paginated"})
	if err != nil {
		t.Fatal(err)
	}
	if !thread.Complete || len(thread.Turns) != 2 || len(thread.Turns[0].Items) != 2 || len(thread.Turns[1].Items) != 1 {
		t.Fatalf("incomplete hydration: complete=%t turns=%d items=%v", thread.Complete, len(thread.Turns), []int{len(thread.Turns[0].Items), len(thread.Turns[1].Items)})
	}
	first := calls[0].params
	if calls[0].method != "thread/turns/list" || first["threadId"] != "thread-1" || first["sortDirection"] != "asc" || first["itemsView"] != "summary" || first["limit"] != 100 {
		t.Fatalf("unexpected turn list params: %#v", first)
	}
	if calls[1].params["cursor"] != "turn-page-2" {
		t.Fatalf("turn cursor not advanced: %#v", calls[1].params)
	}
	for _, c := range calls[2:] {
		if c.method != "thread/items/list" || c.params["threadId"] != "thread-1" || c.params["sortDirection"] != "asc" || c.params["limit"] != 100 {
			t.Fatalf("unexpected item list params: %s %#v", c.method, c.params)
		}
	}
	if calls[2].params["turnId"] != "turn-1" || calls[3].params["turnId"] != "turn-1" || calls[3].params["cursor"] != "item-page-2" || calls[4].params["turnId"] != "turn-2" {
		t.Fatalf("item pagination escaped its turn: %#v", calls[2:])
	}
}

func TestHydrateHistoryRejectsInvalidPages(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(context.Context, string, any, any) error
		want string
	}{
		{"repeated cursor", func(_ context.Context, _ string, _ any, out any) error {
			return json.Unmarshal([]byte(`{"data":[],"nextCursor":"same"}`), out)
		}, "PROTOCOL_ERROR"},
		{"invalid turn id", func(_ context.Context, _ string, _ any, out any) error {
			return json.Unmarshal([]byte(`{"data":[{"status":"completed"}]}`), out)
		}, "PROTOCOL_ERROR"},
		{"oversize page", func(_ context.Context, _ string, _ any, out any) error {
			return json.Unmarshal([]byte(`{"data":[`+fmt.Sprintf("%q", strings.Repeat("x", MaxFrame))+`]}`), out)
		}, "HISTORY_LIMIT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := hydrateHistory(context.Background(), tc.call, HistoryThread{ID: "thread-1", HistoryMode: "paginated"})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error=%v want %s", err, tc.want)
			}
		})
	}
}

func TestHydrateHistoryRejectsTurnCountLimit(t *testing.T) {
	page := 0
	call := func(_ context.Context, method string, _ any, out any) error {
		if method != "thread/turns/list" {
			t.Fatalf("unexpected item fetch after turn limit: %s", method)
		}
		start := page * 100
		end := start + 100
		if end > 10001 {
			end = 10001
		}
		data := make([]map[string]any, 0, end-start)
		for i := start; i < end; i++ {
			data = append(data, map[string]any{"id": fmt.Sprintf("turn-%05d", i), "status": "inProgress"})
		}
		page++
		var result any = map[string]any{"data": data}
		if end < 10001 {
			result = map[string]any{"data": data, "nextCursor": fmt.Sprintf("cursor-%d", page)}
		}
		b, _ := json.Marshal(result)
		return json.Unmarshal(b, out)
	}
	_, err := hydrateHistory(context.Background(), call, HistoryThread{ID: "thread-1", HistoryMode: "paginated"})
	if err == nil || err.Error() != "HISTORY_LIMIT" {
		t.Fatalf("error=%v want HISTORY_LIMIT", err)
	}
}

func TestHydrateHistoryRejectsItemCountLimit(t *testing.T) {
	itemsPage := 0
	call := func(_ context.Context, method string, raw any, out any) error {
		params := raw.(map[string]any)
		var result any
		switch method {
		case "thread/turns/list":
			result = map[string]any{"data": []any{map[string]any{"id": "turn-1", "status": "completed"}}}
		case "thread/items/list":
			start := itemsPage * 100
			end := start + 100
			if end > 10001 {
				end = 10001
			}
			data := make([]map[string]any, 0, end-start)
			turnID, _ := params["turnId"].(string)
			for i := start; i < end; i++ {
				data = append(data, map[string]any{"turnId": turnID, "item": map[string]any{"id": fmt.Sprintf("item-%05d", i), "type": "agentMessage"}})
			}
			itemsPage++
			result = map[string]any{"data": data}
			if end < 10001 {
				result = map[string]any{"data": data, "nextCursor": fmt.Sprintf("cursor-%d", itemsPage)}
			}
		default:
			t.Fatalf("unexpected method %q", method)
		}
		b, _ := json.Marshal(result)
		return json.Unmarshal(b, out)
	}
	_, err := hydrateHistory(context.Background(), call, HistoryThread{ID: "thread-1", HistoryMode: "paginated"})
	if err == nil || err.Error() != "HISTORY_LIMIT" {
		t.Fatalf("error=%v want HISTORY_LIMIT", err)
	}
}
