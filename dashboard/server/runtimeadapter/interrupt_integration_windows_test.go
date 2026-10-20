//go:build windows && integration

package runtimeadapter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRealCodexReadOnlyEnforcementAndInterrupt(t *testing.T) {
	exe := os.Getenv("POKEGENTS_TEST_CODEX_EXE")
	if exe == "" {
		t.Fatal("explicit native Codex exe required")
	}
	cwd := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client, err := Start(ctx, exe, cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, c := context.WithTimeout(context.Background(), 4*time.Second)
		defer c()
		if e := client.Close(stop); e != nil {
			t.Error(e)
		}
	}()
	thread, err := client.Open(ctx, cwd, "read-only", "", "")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := client.Turn(ctx, thread, "これはread-only sandboxの否定試験です。shell command toolでPowerShellの Set-Content -LiteralPath './must-not-exist.txt' -Value 'not allowed' を一回だけ試み、拒否されたら他の手段は試さず結果を短く報告してください。")
	if err != nil {
		t.Fatal(err)
	}
	commandAttempts := 0
	for {
		f, e := client.Next(ctx)
		if e != nil {
			t.Fatal(e)
		}
		var p struct {
			ThreadID string `json:"threadId"`
			Item     struct {
				Type string `json:"type"`
			} `json:"item"`
			Turn struct{ ID, Status string } `json:"turn"`
		}
		json.Unmarshal(f.Params, &p)
		if f.Method == "item/started" && p.Item.Type == "commandExecution" {
			commandAttempts++
		}
		if len(f.ID) > 0 {
			client.Approval(f.ID, "decline")
			t.Fatal("read-only never policy requested approval")
		}
		if f.Method == "turn/completed" && p.ThreadID == thread && p.Turn.ID == turn {
			break
		}
	}
	if commandAttempts == 0 {
		t.Fatal("no command execution attempt observed; read-only enforcement is unverified")
	}
	if _, err = os.Stat(filepath.Join(cwd, "must-not-exist.txt")); !os.IsNotExist(err) {
		t.Fatal("read-only turn wrote to workspace")
	}
	turn, err = client.Turn(ctx, thread, "ツールを使わず、1から10000までの数を一つずつ日本語で説明してください。省略せず長く回答してください。")
	if err != nil {
		t.Fatal(err)
	}
	interrupted := false
	var sent time.Time
	for {
		f, e := client.Next(ctx)
		if e != nil {
			t.Fatal(e)
		}
		var p struct {
			ThreadID string                      `json:"threadId"`
			TurnID   string                      `json:"turnId"`
			Turn     struct{ ID, Status string } `json:"turn"`
		}
		json.Unmarshal(f.Params, &p)
		if !interrupted && f.Method == "item/agentMessage/delta" && p.ThreadID == thread && p.TurnID == turn {
			sent = time.Now()
			callCtx, c := context.WithTimeout(ctx, time.Second)
			err = client.Interrupt(callCtx, thread, turn)
			c()
			if err != nil {
				t.Fatal(err)
			}
			interrupted = true
		}
		if f.Method == "turn/completed" && p.ThreadID == thread && p.Turn.ID == turn {
			if !interrupted || p.Turn.Status != "interrupted" {
				t.Fatalf("status=%s interrupted request=%v", p.Turn.Status, interrupted)
			}
			if elapsed := time.Since(sent); elapsed > 3*time.Second {
				t.Fatalf("interrupt took %v", elapsed)
			}
			t.Logf("readOnly+never rejected workspace write; native interrupt terminal latency=%s", time.Since(sent).Round(time.Millisecond))
			break
		}
	}
}
