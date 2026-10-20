package server

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pokegents/dashboard/server/store"
)

// fixupStatusFiles re-keys status files whose `session_id` field equals
// the file basename (i.e. the chat backend wrote pokegent_id where it
// should have written the Claude conversation UUID). Looks up the
// agent's actual Claude session_id from the matching running file and
// rewrites. Idempotent — safe across restarts. Removable once nobody has
// status files written by chat-backend pre-runtime-parity.
func fixupStatusFiles(dataDir string) {
	statusDir := filepath.Join(dataDir, "status")
	runningDir := filepath.Join(dataDir, "running")
	entries, err := os.ReadDir(statusDir)
	if err != nil {
		return
	}
	fixed := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		basename := strings.TrimSuffix(name, ".json")
		path := filepath.Join(statusDir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var sf store.StatusFile
		if err := json.Unmarshal(raw, &sf); err != nil {
			continue
		}
		if sf.SessionID != basename {
			continue // already keyed correctly
		}
		// Find running file with this pokegent_id and harvest its session_id.
		matches, _ := filepath.Glob(filepath.Join(runningDir, "*-"+basename+".json"))
		if len(matches) == 0 {
			continue // no live agent for this status file
		}
		rsRaw, err := os.ReadFile(matches[0])
		if err != nil {
			continue
		}
		var rs struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(rsRaw, &rs); err != nil {
			continue
		}
		if rs.SessionID == "" || rs.SessionID == basename {
			continue
		}
		sf.SessionID = rs.SessionID
		if err := writeStatusFile(dataDir, basename, sf); err != nil {
			continue
		}
		fixed++
	}
	if fixed > 0 {
		log.Printf("status: fixed %d chat status file(s) with mis-keyed session_id", fixed)
	}
}

// writeStatusFile is the canonical Go-side writer for ~/.pokegents/status/{key}.json.
// Both runtime backends emit the same StatusFile shape so dashboard state.go
// and the frontend never need to branch on which runtime produced the agent.
// (The bash hooks' jq-based writer in hooks/status-update.sh emits the same
// schema — keep the field set in sync there too.)
func writeStatusFile(dataDir, key string, sf store.StatusFile) error {
	if sf.Timestamp == "" {
		sf.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Join(dataDir, "status")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, key+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
