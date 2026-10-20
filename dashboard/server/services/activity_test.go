package services

import (
	"testing"

	"pokegents/dashboard/server/store"
)

func newTestActivity(t *testing.T) *ActivityService {
	dir := t.TempDir()
	fs := store.NewFileStore(dir)
	return NewActivityService(fs.Activity)
}

func TestRecordTurn(t *testing.T) {
	svc := newTestActivity(t)

	// Empty files → no-op
	err := svc.RecordTurn("proj-1", "session-a", "Agent A", nil)
	if err != nil {
		t.Fatalf("RecordTurn with no files: %v", err)
	}

	// Record a real turn
	err = svc.RecordTurn("proj-1", "session-a", "Agent A", []string{"server.go", "models.go"})
	if err != nil {
		t.Fatalf("RecordTurn: %v", err)
	}

	// Should appear in GetRecent for other sessions
	entries, err := svc.GetRecent("proj-1", "session-b", 10)
	if err != nil {
		t.Fatalf("GetRecent: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
}

func TestGetRecentFiltersSelf(t *testing.T) {
	svc := newTestActivity(t)

	svc.RecordTurn("proj-filter", "session-a", "Agent A", []string{"a.go"})
	svc.RecordTurn("proj-filter", "session-b", "Agent B", []string{"b.go"})

	// Session A should only see B's entry
	entries, _ := svc.GetRecent("proj-filter", "session-a", 10)
	if len(entries) != 1 {
		t.Fatalf("expected 1 (only B's), got %d", len(entries))
	}

	// Session B: first call gets both lines (position starts at 0),
	// but filters out own → should see only A's entry
	entries, _ = svc.GetRecent("proj-filter", "session-b", 10)
	if len(entries) != 1 {
		t.Fatalf("expected 1 (only A's), got %d", len(entries))
	}
}

func TestGetRecentMaxEntries(t *testing.T) {
	svc := newTestActivity(t)

	for i := 0; i < 10; i++ {
		svc.RecordTurn("proj-1", "other", "Other", []string{"file.go"})
	}

	entries, _ := svc.GetRecent("proj-1", "me", 3)
	if len(entries) != 3 {
		t.Errorf("expected 3 entries (maxEntries=3), got %d", len(entries))
	}
}

func TestGetRecentTracksPosition(t *testing.T) {
	svc := newTestActivity(t)

	svc.RecordTurn("proj-1", "other", "Other", []string{"a.go"})

	// First read: 1 entry
	entries, _ := svc.GetRecent("proj-1", "me", 10)
	if len(entries) != 1 {
		t.Fatalf("first read: expected 1, got %d", len(entries))
	}

	// Second read (no new entries): 0
	entries, _ = svc.GetRecent("proj-1", "me", 10)
	if len(entries) != 0 {
		t.Errorf("second read: expected 0 new, got %d", len(entries))
	}

	// Add more, third read: only new ones
	svc.RecordTurn("proj-1", "other", "Other", []string{"b.go"})
	entries, _ = svc.GetRecent("proj-1", "me", 10)
	if len(entries) != 1 {
		t.Errorf("third read: expected 1 new, got %d", len(entries))
	}
}

func TestDetectOverlaps(t *testing.T) {
	svc := newTestActivity(t)

	entries := []store.ActivityEntry{
		{SessionID: "other-1", AgentName: "Agent A", Files: "server.go, models.go"},
		{SessionID: "other-2", AgentName: "Agent B", Files: "frontend.tsx"},
	}

	// My files overlap with Agent A
	overlaps := svc.DetectOverlaps(entries, []string{"server.go"})
	if len(overlaps) != 1 {
		t.Fatalf("expected 1 overlap, got %d", len(overlaps))
	}
	if overlaps[0].AgentName != "Agent A" {
		t.Errorf("overlap should be Agent A, got %s", overlaps[0].AgentName)
	}

	// No overlap
	overlaps = svc.DetectOverlaps(entries, []string{"utils.go"})
	if len(overlaps) != 0 {
		t.Errorf("expected 0 overlaps, got %d", len(overlaps))
	}

	// Empty inputs
	overlaps = svc.DetectOverlaps(nil, []string{"server.go"})
	if len(overlaps) != 0 {
		t.Errorf("nil entries should return 0 overlaps")
	}
	overlaps = svc.DetectOverlaps(entries, nil)
	if len(overlaps) != 0 {
		t.Errorf("nil files should return 0 overlaps")
	}
}

func TestGetProjectHash(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"/home/user/Projects", "home-user-Projects"},
		{"", "default"},
		{"/", ""},
	}
	for _, tt := range tests {
		got := GetProjectHash(tt.input)
		if got != tt.expected {
			t.Errorf("GetProjectHash(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
