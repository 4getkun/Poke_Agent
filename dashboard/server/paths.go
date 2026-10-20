package server

import (
	"os"
	"path/filepath"
)

// PathService centralizes filesystem locations that used to be scattered
// across setup, upload, transcript, and runtime code. Keep provider-owned
// homes explicit: Pokegents owns DataDir/UploadsDir; Claude/Codex own their
// native config and transcript directories.
type PathService struct {
	HomeDir string
	DataDir string
}

func NewPathService(dataDir string) PathService {
	home, _ := os.UserHomeDir()
	if dataDir == "" {
		dataDir = os.Getenv("POKEGENTS_DATA")
	}
	if dataDir == "" {
		dataDir = filepath.Join(home, ".pokegents")
	}
	return PathService{HomeDir: home, DataDir: dataDir}
}

func (s *Server) pathService() PathService {
	if s == nil {
		return NewPathService("")
	}
	if s.paths.DataDir != "" {
		return s.paths
	}
	return NewPathService(s.dataDir)
}

func (p PathService) ConfigPath() string {
	return filepath.Join(p.DataDir, "config.json")
}

func (p PathService) BackendsPath() string {
	return filepath.Join(p.DataDir, "backends.json")
}

func (p PathService) UploadsDir() string {
	return filepath.Join(p.DataDir, "uploads")
}

func (p PathService) ImageUploadDir(sessionID string) string {
	return filepath.Join(p.UploadsDir(), sessionID)
}

func (p PathService) LogsDir() string {
	return filepath.Join(p.DataDir, "logs")
}

func (p PathService) ClaudeConfigDir() string {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return v
	}
	return filepath.Join(p.HomeDir, ".claude")
}

func (p PathService) ClaudeSettingsPath() string {
	return filepath.Join(p.ClaudeConfigDir(), "settings.json")
}

func (p PathService) ClaudeProjectsDir() string {
	return filepath.Join(p.ClaudeConfigDir(), "projects")
}

func (p PathService) CodexConfigDir() string {
	if v := os.Getenv("CODEX_HOME"); v != "" {
		return v
	}
	return filepath.Join(p.HomeDir, ".codex")
}

func (p PathService) CodexConfigPath() string {
	return filepath.Join(p.CodexConfigDir(), "config.toml")
}

func (p PathService) CodexSessionsDir() string {
	return filepath.Join(p.CodexConfigDir(), "sessions")
}

func (p PathService) PokegentsCodexHomesGlob() string {
	return filepath.Join(p.DataDir, "codex-homes", "*", "sessions")
}

func (p PathService) LegacyCCSessionCodexHomesGlob() string {
	return filepath.Join(p.HomeDir, ".ccsession", "codex-homes", "*", "sessions")
}

func (p PathService) LaunchAgentsDir() string {
	return filepath.Join(p.HomeDir, "Library", "LaunchAgents")
}

func (p PathService) ITermDynamicProfilesDir() string {
	return filepath.Join(p.HomeDir, "Library", "Application Support", "iTerm2", "DynamicProfiles")
}
