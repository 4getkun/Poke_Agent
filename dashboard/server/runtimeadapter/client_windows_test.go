//go:build windows && integration

package runtimeadapter

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// Opt-in integration test. Only a public greeting is sent; no repository files,
// credentials or private context are included in the prompt.
func TestRealCodexReadOnlyGreeting(t *testing.T) {
	exe := os.Getenv("POKEGENTS_TEST_CODEX_EXE")
	if exe == "" {
		t.Fatal("explicit native Codex path required for real integration")
	}
	cwd := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	version, err := Probe(ctx, exe, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if version != SupportedVersion {
		t.Fatal(version)
	}
	c, err := Start(ctx, exe, cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if err := c.Close(stop); err != nil {
			t.Error(err)
		}
	}()
	thread, err := c.Open(ctx, cwd, "read-only", "", "")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := c.Turn(ctx, thread, "ツールを使わず、日本語で「こんにちは」とだけ返答してください。")
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for {
		f, err := c.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var p struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Delta    string `json:"delta"`
			Turn     struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turn"`
		}
		if err = json.Unmarshal(f.Params, &p); err != nil {
			t.Fatal(err)
		}
		if len(f.ID) > 0 {
			if err = c.Approval(f.ID, "decline"); err != nil {
				t.Fatal(err)
			}
		}
		if f.Method == "item/agentMessage/delta" && p.ThreadID == thread && p.TurnID == turn {
			text += p.Delta
		}
		if f.Method == "turn/completed" && p.ThreadID == thread && p.Turn.ID == turn {
			if p.Turn.Status != "completed" {
				t.Fatalf("terminal=%s", p.Turn.Status)
			}
			if text == "" {
				t.Fatal("no streamed output")
			}
			t.Logf("native Codex %s: initialize/open/turn/stream/complete succeeded; output bytes=%d", version, len(text))
			return
		}
	}
}
