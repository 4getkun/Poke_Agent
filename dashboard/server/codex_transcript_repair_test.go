package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepairCodexTranscriptMissingCustomToolOutputs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	call := map[string]any{
		"timestamp": "2026-05-07T07:00:00Z",
		"type":      "response_item",
		"payload": map[string]any{
			"type":    "custom_tool_call",
			"status":  "completed",
			"call_id": "call_missing",
			"name":    "apply_patch",
			"input":   "*** Begin Patch\n*** End Patch\n",
		},
	}
	user := map[string]any{
		"timestamp": "2026-05-07T07:01:00Z",
		"type":      "response_item",
		"payload": map[string]any{
			"type":    "message",
			"role":    "user",
			"content": []map[string]any{{"type": "input_text", "text": "next turn"}},
		},
	}
	var b strings.Builder
	for _, frame := range []map[string]any{call, user} {
		line, err := json.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	repaired, err := repairCodexTranscriptMissingCustomToolOutputs(path)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if repaired != 1 {
		t.Fatalf("repaired = %d, want 1", repaired)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"custom_tool_call_output"`) || !strings.Contains(text, "call_missing") || !strings.Contains(text, "pokegents repaired transcript") {
		t.Fatalf("repaired transcript missing synthetic output:\n%s", text)
	}
	backups, err := filepath.Glob(path + ".bak.missing-tool-output.*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("backup count = %d err=%v", len(backups), err)
	}

	repaired, err = repairCodexTranscriptMissingCustomToolOutputs(path)
	if err != nil {
		t.Fatalf("second repair: %v", err)
	}
	if repaired != 0 {
		t.Fatalf("second repaired = %d, want 0", repaired)
	}
}
