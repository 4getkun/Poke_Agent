package server

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Notifier sends macOS notifications with per-session debounce.
type Notifier struct {
	mu            sync.Mutex
	lastSent      map[string]time.Time // session_id → last notification time
	debounce      time.Duration
	sink          NotificationSink
	spriteDir     string            // path to sprites directory
	sprites       []string          // sorted sprite names (no extension)
	overrides     map[string]string // session_id → sprite name
	overridesPath string
}

type NotificationSink interface {
	Send(title, body, iconPath string)
}

type MacNotificationSink struct{}

func NewNotifier(webDir, dataDir string) *Notifier {
	n := &Notifier{
		lastSent:      make(map[string]time.Time),
		debounce:      30 * time.Second,
		sink:          MacNotificationSink{},
		spriteDir:     filepath.Join(webDir, "sprites"),
		overridesPath: filepath.Join(dataDir, "sprite-overrides.json"),
	}
	n.loadSprites()
	n.loadOverrides()
	return n
}

func (n *Notifier) loadSprites() {
	entries, err := os.ReadDir(n.spriteDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".png") {
			n.sprites = append(n.sprites, strings.TrimSuffix(e.Name(), ".png"))
		}
	}
	sort.Strings(n.sprites)
}

func (n *Notifier) loadOverrides() {
	data, err := os.ReadFile(n.overridesPath)
	if err != nil {
		n.overrides = map[string]string{}
		return
	}
	var m map[string]string
	if json.Unmarshal(data, &m) != nil {
		n.overrides = map[string]string{}
		return
	}
	n.overrides = m
}

func (n *Notifier) spritePathForSession(ids ...string) string {
	if len(n.sprites) == 0 {
		return ""
	}
	n.loadOverrides()
	// Check overrides under all provided IDs
	sprite := ""
	for _, id := range ids {
		if s, ok := n.overrides[id]; ok && s != "" {
			sprite = s
			break
		}
	}
	if sprite == "" {
		// Hash the first stable ID.
		h := int32(0)
		for _, c := range ids[0] {
			h = ((h << 5) - h) + int32(c)
		}
		idx := int(math.Abs(float64(h))) % len(n.sprites)
		sprite = n.sprites[idx]
	}
	path := filepath.Join(n.spriteDir, sprite+".png")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return ""
}

// MaybeNotify sends a macOS notification if the event is notification-worthy
// and the debounce period has elapsed for that session.
func (n *Notifier) MaybeNotify(evt HookEvent, agent *AgentState) {
	if agent == nil {
		return
	}

	agentName := agent.DisplayName
	if agentName == "" {
		agentName = agent.ProfileName
	}

	var title, body string

	switch evt.HookEventName {
	case "Stop":
		title = agentName
		body = truncate(evt.LastAssistantMessage, 100)
		if body == "" {
			body = "Agent finished its task"
		}
	case "Notification":
		if evt.NotificationType != "idle_prompt" {
			return
		}
		title = agentName
		body = "Waiting for your response"
	case "PermissionRequest":
		if agent.Interface == "chat" {
			return
		}
		title = agentName
		body = fmt.Sprintf("Needs permission for %s", evt.ToolName)
	default:
		return
	}

	n.mu.Lock()
	last, exists := n.lastSent[evt.SessionID]
	if exists && time.Since(last) < n.debounce {
		n.mu.Unlock()
		return
	}
	n.lastSent[evt.SessionID] = time.Now()
	n.mu.Unlock()

	// Use the agent's actual sprite if available (single source of truth),
	// fall back to hash-based lookup for legacy agents without a sprite field.
	spritePath := ""
	if agent.Sprite != "" {
		p := filepath.Join(n.spriteDir, agent.Sprite+".png")
		if _, err := os.Stat(p); err == nil {
			spritePath = p
		}
	}
	if spritePath == "" {
		spriteIDs := []string{evt.SessionID}
		if agent.PokegentID != "" {
			spriteIDs = []string{agent.PokegentID, evt.SessionID}
		}
		spritePath = n.spritePathForSession(spriteIDs...)
	}
	go n.sink.Send(title, body, spritePath)
}

func (MacNotificationSink) Send(title, body, iconPath string) {
	// Prefer terminal-notifier (supports custom icons), fall back to osascript
	if tn, err := exec.LookPath("terminal-notifier"); err == nil {
		args := []string{"-title", title, "-message", body, "-group", "pokegents"}
		if iconPath != "" {
			scaled := scaleSprite(iconPath)
			args = append(args, "-appIcon", scaled, "-contentImage", scaled)
		}
		exec.Command(tn, args...).Run()
		return
	}
	script := fmt.Sprintf(
		`display notification %q with title %q`,
		body, title,
	)
	exec.Command("osascript", "-e", script).Run()
}

func scaleSprite(path string) string {
	tmp := filepath.Join(os.TempDir(), "pokegents-notif-sprite.png")
	err := exec.Command("python3", "-c", fmt.Sprintf(`
from PIL import Image
img = Image.open(%q).convert("RGBA")
bbox = img.getbbox()
if bbox:
    img = img.crop(bbox)
img = img.resize((img.width * 4, img.height * 4), Image.NEAREST)
img.save(%q)
`, path, tmp)).Run()
	if err != nil {
		return path
	}
	return tmp
}
