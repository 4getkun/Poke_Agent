package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildMigrationContextReadsClaudeTranscript(t *testing.T) {
	dir := t.TempDir()
	transcriptPath := filepath.Join(dir, "claude-session.jsonl")
	writeJSONLLines(t, transcriptPath,
		`{"type":"system","cwd":"/tmp/pokegents-work"}`,
		`{"type":"user","uuid":"u1","timestamp":"2026-05-06T10:00:00Z","message":{"role":"user","content":"please preserve this claude user request"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-05-06T10:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"claude assistant answer to carry forward"}]}}`,
	)

	s := &Server{state: NewStateManager(t.TempDir(), dir)}
	got := s.buildMigrationContext("Pika", "/tmp/pokegents-work", transcriptPath)

	assertContainsAll(t, got,
		"## Migrated Conversation Context",
		`same pokegents agent "Pika" after a backend migration`,
		"Your current working directory is /tmp/pokegents-work.",
		"The previous backend transcript is at "+transcriptPath+".",
		"Do not say you lack prior history solely because the backend changed.",
		"User: please preserve this claude user request",
		"Assistant: claude assistant answer to carry forward",
	)
}

func TestBuildMigrationContextReadsCodexTranscript(t *testing.T) {
	dir := t.TempDir()
	transcriptPath := filepath.Join(dir, "codex-session.jsonl")
	writeJSONLLines(t, transcriptPath,
		`{"type":"session_meta","timestamp":"2026-05-06T10:00:00Z","payload":{"id":"codex-session","cwd":"/tmp/codex-work"}}`,
		`{"type":"response_item","timestamp":"2026-05-06T10:00:01Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"please preserve this codex user request"}]}}`,
		`{"type":"response_item","timestamp":"2026-05-06T10:00:02Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"codex assistant answer to carry forward"}]}}`,
	)

	s := &Server{state: NewStateManager(t.TempDir(), dir)}
	got := s.buildMigrationContext("Charmander", "/tmp/codex-work", transcriptPath)

	assertContainsAll(t, got,
		"## Migrated Conversation Context",
		`same pokegents agent "Charmander" after a backend migration`,
		"User: please preserve this codex user request",
		"Assistant: codex assistant answer to carry forward",
	)
}

func TestBuildMigrationContextIsBoundedToRecentContext(t *testing.T) {
	dir := t.TempDir()
	transcriptPath := filepath.Join(dir, "large-session.jsonl")
	var lines []string
	lines = append(lines, `{"type":"system","cwd":"/tmp/pokegents-work"}`)
	for i := 0; i < 80; i++ {
		lines = append(lines,
			fmt.Sprintf(`{"type":"user","uuid":"u-%03d","timestamp":"2026-05-06T10:00:00Z","message":{"role":"user","content":"old verbose request %03d %s"}}`, i, i, strings.Repeat("x", 400)),
			fmt.Sprintf(`{"type":"assistant","uuid":"a-%03d","timestamp":"2026-05-06T10:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"newest answer marker %03d %s"}]}}`, i, i, strings.Repeat("y", 500)),
		)
	}
	writeJSONLLines(t, transcriptPath, lines...)

	s := &Server{state: NewStateManager(t.TempDir(), dir)}
	got := s.buildMigrationContext("Squirtle", "/tmp/pokegents-work", transcriptPath)

	if len(got) > 17000 {
		t.Fatalf("migration context too large: got %d chars, want <= 17000", len(got))
	}
	if !strings.Contains(got, "truncated to latest context") {
		t.Fatalf("expected truncation marker in large migration context")
	}
	if !strings.Contains(got, "newest answer marker 079") {
		t.Fatalf("expected newest turns to survive truncation")
	}
	if strings.Contains(got, "old verbose request 000") {
		t.Fatalf("oldest turns should be truncated first")
	}
}

func writeJSONLLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript %s: %v", path, err)
	}
}

func assertContainsAll(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Fatalf("expected migration context to contain %q\n\ncontext:\n%s", want, got)
		}
	}
}
