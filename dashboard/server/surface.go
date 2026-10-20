package server

import "strings"

const (
	SurfaceChat     = "chat"
	SurfaceTerminal = "terminal"

	// LegacySurfaceITerm2 is accepted for existing state/config. Public UI
	// should prefer "terminal"; the iTerm2 implementation is just one terminal
	// provider.
	LegacySurfaceITerm2 = "iterm2"
)

func normalizeSurface(surface string) string {
	switch strings.ToLower(strings.TrimSpace(surface)) {
	case "", SurfaceChat:
		return SurfaceChat
	case SurfaceTerminal, LegacySurfaceITerm2:
		return SurfaceTerminal
	default:
		return surface
	}
}

func runtimeNameForSurface(surface string) string {
	if strings.TrimSpace(surface) == "" {
		// Existing persisted terminal agents often have an empty interface.
		return LegacySurfaceITerm2
	}
	normalized := normalizeSurface(surface)
	if normalized == SurfaceTerminal {
		return LegacySurfaceITerm2
	}
	return normalized
}

func validSurface(surface string) bool {
	switch strings.ToLower(strings.TrimSpace(surface)) {
	case SurfaceChat, SurfaceTerminal, LegacySurfaceITerm2:
		return true
	default:
		return false
	}
}
