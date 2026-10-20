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

func TestRealCodexCommandApprovalDenyAndAllow(t *testing.T) {
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
	thread, err := client.Open(ctx, cwd, "workspace-write", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ file, decision string }{{"denied-marker.txt", "decline"}, {"allowed-marker.txt", "accept"}} {
		marker := filepath.Join(cwd, test.file)
		turn, err := client.Turn(ctx, thread, "この空のテスト用フォルダーだけを使います。shell command toolで PowerShell の Set-Content -LiteralPath './"+test.file+"' -Value 'ok' を実行してください。apply_patchや他のファイル編集ツールは使わないでください。承認で拒否されたら代替手段を試さず終了してください。")
		if err != nil {
			t.Fatal(err)
		}
		requests := 0
		for {
			f, err := client.Next(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var p struct {
				ThreadID string                      `json:"threadId"`
				TurnID   string                      `json:"turnId"`
				Turn     struct{ ID, Status string } `json:"turn"`
			}
			if err = json.Unmarshal(f.Params, &p); err != nil {
				t.Fatal(err)
			}
			if len(f.ID) > 0 {
				if p.ThreadID != thread || p.TurnID != turn {
					if err = client.Approval(f.ID, "decline"); err != nil {
						t.Fatal(err)
					}
					continue
				}
				if _, err = os.Stat(marker); err == nil {
					t.Fatal("marker was written before explicit approval")
				}
				requests++
				if err = client.Approval(f.ID, test.decision); err != nil {
					t.Fatal(err)
				}
			}
			if f.Method == "turn/completed" && p.ThreadID == thread && p.Turn.ID == turn {
				if requests == 0 {
					t.Fatalf("%s: no approval request observed", test.decision)
				}
				_, statErr := os.Stat(marker)
				if test.decision == "decline" && !os.IsNotExist(statErr) {
					t.Fatal("denied command changed the marker")
				}
				if test.decision == "accept" {
					if p.Turn.Status != "completed" {
						t.Fatalf("allowed turn=%s", p.Turn.Status)
					}
					b, err := os.ReadFile(marker)
					if err != nil || len(b) == 0 {
						t.Fatalf("allowed marker missing: %v", err)
					}
				}
				t.Logf("%s: explicit approval requests=%d, marker expectation verified", test.decision, requests)
				break
			}
		}
	}
}
