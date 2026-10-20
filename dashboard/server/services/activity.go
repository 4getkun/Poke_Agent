package services

import (
	"strings"

	"pokegents/dashboard/server/store"
)

// ActivityService provides activity log tracking and overlap detection.
type ActivityService struct {
	store store.ActivityStore
}

// NewActivityService creates an activity service.
func NewActivityService(as store.ActivityStore) *ActivityService {
	return &ActivityService{store: as}
}

// RecordTurn appends a file-change entry to the project's activity log.
// Called after an agent's turn completes (Stop event).
func (s *ActivityService) RecordTurn(projectHash, sessionID, agentName string, files []string) error {
	if len(files) == 0 || projectHash == "" {
		return nil
	}
	entry := store.ActivityEntry{
		SessionID: sessionID,
		AgentName: agentName,
		Files:     strings.Join(files, ", "),
	}
	return s.store.Append(projectHash, entry)
}

// GetRecent returns new activity entries from other agents since the
// session's last read position. Updates the read position.
func (s *ActivityService) GetRecent(projectHash, sessionID string, maxEntries int) ([]store.ActivityEntry, error) {
	lastLine, err := s.store.GetLastReadLine(projectHash, sessionID)
	if err != nil {
		lastLine = 0
	}

	entries, totalLines, err := s.store.GetSince(projectHash, lastLine)
	if err != nil {
		return nil, err
	}

	// Update read position
	if totalLines > lastLine {
		_ = s.store.SetLastReadLine(projectHash, sessionID, totalLines)
	}

	// Filter out own session and limit
	var result []store.ActivityEntry
	for _, e := range entries {
		if e.SessionID == sessionID {
			continue
		}
		result = append(result, e)
		if maxEntries > 0 && len(result) >= maxEntries {
			break
		}
	}

	return result, nil
}

// DetectOverlaps checks if any recent activity entries from other agents
// modified files that the current agent is also working on.
func (s *ActivityService) DetectOverlaps(entries []store.ActivityEntry, myFiles []string) []store.ActivityEntry {
	if len(entries) == 0 || len(myFiles) == 0 {
		return nil
	}

	var overlaps []store.ActivityEntry
	for _, entry := range entries {
		for _, myFile := range myFiles {
			if myFile == "" {
				continue
			}
			if strings.Contains(entry.Files, myFile) {
				overlaps = append(overlaps, entry)
				break
			}
		}
	}
	return overlaps
}

// GetProjectHash computes a filesystem-safe hash from a working directory path.
func GetProjectHash(cwd string) string {
	if cwd == "" {
		return "default"
	}
	h := strings.ReplaceAll(cwd, "/", "-")
	if len(h) > 0 && h[0] == '-' {
		h = h[1:]
	}
	return h
}
