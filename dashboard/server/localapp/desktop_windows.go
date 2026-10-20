//go:build windows

package localapp

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode"
)

// DesktopPreferences are opt-in; absent/invalid saved values stay disabled.
type DesktopPreferences struct {
	Notifications bool `json:"notifications"`
	Background    bool `json:"background"`
}

type DesktopBridge interface {
	ConfigureDesktop(background, notifications bool, revision int64) error
	Notify(id, title, body string) error
}

type desktopObservation struct {
	seen        map[string]bool
	initialized bool
}

// WatchDesktop never changes a task, runs a command, or marks a notification
// read. Failed/suppressed OS delivery leaves the durable in-app inbox intact.
func (s *Service) WatchDesktop(ctx context.Context, bridge DesktopBridge) {
	observation := &desktopObservation{}
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		observation.poll(ctx, s, bridge)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func (o *desktopObservation) poll(ctx context.Context, s *Service, bridge DesktopBridge) {
	state, revision, raw, err := s.Store.View(ctx)
	if err != nil {
		return
	}
	var settings struct {
		Desktop DesktopPreferences `json:"desktop"`
		Locale  string             `json:"locale"`
	}
	// A malformed preference document must not accidentally enable residency.
	if json.Unmarshal(raw, &settings) != nil {
		settings.Desktop = DesktopPreferences{}
	}
	available := bridge.ConfigureDesktop(settings.Desktop.Background, settings.Desktop.Notifications, revision) == nil
	ids := make([]string, 0, len(state.Notifications))
	for id := range state.Notifications {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := state.Notifications[ids[i]], state.Notifications[ids[j]]
		if a.At == b.At {
			return ids[i] < ids[j]
		}
		return a.At < b.At
	})
	current := make(map[string]bool, len(ids))
	for _, id := range ids {
		current[id] = true
		n := state.Notifications[id]
		if !o.initialized || o.seen[id] || !available || !settings.Desktop.Notifications || n.Read {
			continue
		}
		task := state.Tasks[n.TaskID]
		if task == nil {
			continue
		}
		label := notificationLabel(n.Kind, settings.Locale == "en-US")
		if label == "" {
			continue
		}
		name := safeNotificationName(task.Spec.Objective)
		if name == "" {
			name = "Pokegents"
		}
		// Only state and the saved target name leave the application. No chat,
		// approval payload, diagnostic, process metadata or workspace path.
		_ = bridge.Notify(n.ID, "Pokegents", label+"\n"+name)
	}
	o.seen, o.initialized = current, true
}

func notificationLabel(kind string, english bool) string {
	var ja, en string
	switch kind {
	case "succeeded", "review":
		ja, en = "完了", "Completed"
	case "approval":
		ja, en = "承認待ち", "Approval required"
	case "failed", "interrupted":
		ja, en = "エラー", "Error"
	default:
		return ""
	}
	if english {
		return en
	}
	return ja
}

func safeNotificationName(name string) string {
	runes := []rune(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, name))
	if len(runes) > 80 {
		runes = append(runes[:79], '…')
	}
	return strings.TrimSpace(string(runes))
}
