//go:build windows

package localapp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"pokegents/dashboard/server/processhost"
	d "pokegents/dashboard/server/taskdomain"
)

func TestRecoveryExitedOwnerFixture(t *testing.T) {
	if os.Getenv("POKEGENTS_RECOVERY_OWNER_FIXTURE") != "1" {
		return
	}
	owner, err := processhost.NewOwnership()
	if err != nil {
		os.Exit(2)
	}
	data, _ := json.Marshal(owner)
	fmt.Println(string(data))
	os.Exit(0)
}

func TestRecoveryRequiresOSProofAndHumanEffectEvidence(t *testing.T) {
	server := newHTTPTestServer(t)
	s := server.Service
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "-test.run=^TestRecoveryExitedOwnerFixture$")
	cmd.Env = append(processhost.Environment(), "POKEGENTS_RECOVERY_OWNER_FIXTURE=1")
	data, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var released processhost.Ownership
	if json.Unmarshal(data, &released) != nil || processhost.VerifyReleased(released) != nil {
		t.Fatal("exited test owner identity was not verified")
	}
	active, err := processhost.NewOwnership()
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Store.Update(context.Background(), "", nil, func(state *d.State, tx *sql.Tx) (any, []d.Event, error) {
		for _, kind := range []string{"released", "active", "legacy"} {
			state.Projects[kind] = &d.Project{ID: kind, Name: "synthetic", Identity: kind}
			spec := d.Spec{ProjectID: kind, Version: 1, Objective: "synthetic recovery", AllowedScope: "synthetic fixture", ApprovalProfile: "read-only", Criteria: []d.Criterion{{ID: "recovery", Procedure: "observe stop", Expected: "no automatic retry", Required: true}}}
			var owner json.RawMessage
			if kind == "released" {
				owner, _ = json.Marshal(released)
			} else if kind == "active" {
				owner, _ = json.Marshal(active)
			}
			state.Tasks[kind] = &d.Task{ID: kind, State: "DOING", Version: 1, CurrentRunID: kind + "-run", Spec: spec}
			state.Runs[kind+"-run"] = &d.Run{ID: kind + "-run", TaskID: kind, State: "running", Generation: 1, Snapshot: spec}
			if owner != nil {
				if _, err := tx.Exec(`INSERT INTO runtime_instances VALUES(?,?,?,?)`, kind+"-run", kind+"-run", 1, string(owner)); err != nil {
					return nil, nil, err
				}
			}
		}
		state.Effects["effect"] = &d.Effect{ID: "effect", RunID: "released-run", Outcome: "pending", Description: "synthetic operation only"}
		return true, nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	invoke := func(action, task string, version int64, payload any) (json.RawMessage, error) {
		body, _ := json.Marshal(payload)
		return s.Execute(context.Background(), Command{RequestID: d.ID(), Action: action, TaskID: task, ExpectedVersion: version, Data: body})
	}
	for _, task := range []string{"active", "legacy", "released"} {
		if _, err := invoke("recovery.confirm", task, 2, map[string]string{"reason": "reviewed"}); d.Code(err) != "RECOVERY_REQUIRED" {
			t.Fatalf("%s accepted without OS proof/effect evidence: %v", task, err)
		}
	}
	for _, payload := range []map[string]string{
		{"effect_id": "effect", "outcome": "succeeded", "evidence": ""},
		{"effect_id": "effect", "outcome": "unknown", "evidence": "checked"},
		{"effect_id": "missing", "outcome": "failed", "evidence": "checked"},
	} {
		if _, err := invoke("recovery.resolve_effect", "released", 2, payload); err == nil {
			t.Fatal("invalid resolution accepted")
		}
	}
	payload := map[string]string{"effect_id": "effect", "outcome": "failed", "evidence": "Observed the synthetic destination; no change occurred."}
	if _, err := invoke("recovery.resolve_effect", "released", 2, payload); err != nil {
		t.Fatal(err)
	}
	// A stale request must not clear the block or overwrite evidence.
	if _, err := invoke("recovery.confirm", "released", 2, map[string]string{"reason": "checked"}); d.Code(err) != "STALE_VERSION" {
		t.Fatalf("stale recovery accepted: %v", err)
	}
	if _, err := invoke("recovery.confirm", "released", 3, map[string]string{"reason": ""}); d.Code(err) != "REASON_REQUIRED" {
		t.Fatalf("empty reason accepted: %v", err)
	}
	// Reopening after an interrupted recovery must retain the result and block.
	if err := s.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke("recovery.confirm", "released", 3, map[string]string{"reason": "Stopped owner and operation result checked."}); err != nil {
		t.Fatal(err)
	}
	state, err := s.Store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Tasks["released"].State != "READY" || state.Tasks["released"].Block != "" || !state.Runs["released-run"].WorkspaceQuiet || state.Effects["effect"].Outcome != "failed" || state.Effects["effect"].EvidenceRef != payload["evidence"] || s.Active() {
		t.Fatal("recovery did not preserve evidence and return to READY without execution")
	}
	if len(state.Recovery) != 1 || state.Tasks["legacy"].Block != "RECOVERY_REQUIRED" || state.Tasks["active"].Block != "RECOVERY_REQUIRED" {
		t.Fatal("recovery record or independent blocked tasks changed")
	}
	publicState, _ := json.Marshal(state)
	if bytes.Contains(publicState, []byte(released.JobName)) || bytes.Contains(publicState, []byte(released.Created)) || bytes.Contains(publicState, []byte("runtime_owner")) {
		t.Fatal("private process ownership leaked into public state")
	}
}
