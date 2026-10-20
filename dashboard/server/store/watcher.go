package store

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// StoreWatcher monitors running/ and status/ directories for changes.
// It debounces rapid events (50ms) and runs a periodic reconciliation
// sweep (every 7s) to catch events missed by fsnotify (macOS kqueue
// can miss events under load).
type StoreWatcher struct {
	dataDir string
	watcher *fsnotify.Watcher
	done    chan struct{}

	mu        sync.Mutex
	listeners []chan FileEvent

	// Debounce: batch rapid events into a single notification
	pendingRunning bool
	pendingStatus  map[string]struct{} // session IDs with pending status events
	debounceTimer  *time.Timer

	// Reconciliation: periodic full scan to catch missed events
	lastRunningMods map[string]time.Time // filename → last mod time
	lastStatusMods  map[string]time.Time
}

// NewStoreWatcher creates a watcher for the given data directory.
func NewStoreWatcher(dataDir string) *StoreWatcher {
	return &StoreWatcher{
		dataDir:         dataDir,
		done:            make(chan struct{}),
		pendingStatus:   make(map[string]struct{}),
		lastRunningMods: make(map[string]time.Time),
		lastStatusMods:  make(map[string]time.Time),
	}
}

// Subscribe returns a channel that receives file change events.
// Call the returned function to unsubscribe.
func (sw *StoreWatcher) Subscribe() (<-chan FileEvent, func()) {
	ch := make(chan FileEvent, 64)
	sw.mu.Lock()
	sw.listeners = append(sw.listeners, ch)
	sw.mu.Unlock()

	cleanup := func() {
		sw.mu.Lock()
		defer sw.mu.Unlock()
		for i, l := range sw.listeners {
			if l == ch {
				sw.listeners = append(sw.listeners[:i], sw.listeners[i+1:]...)
				close(ch)
				break
			}
		}
	}
	return ch, cleanup
}

func (sw *StoreWatcher) emit(evt FileEvent) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	for _, ch := range sw.listeners {
		select {
		case ch <- evt:
		default:
			// Listener too slow, drop event
		}
	}
}

// Start begins watching directories and starts the reconciliation ticker.
func (sw *StoreWatcher) Start() error {
	var err error
	sw.watcher, err = fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	statusDir := filepath.Join(sw.dataDir, "status")
	runningDir := filepath.Join(sw.dataDir, "running")

	if err := sw.watcher.Add(statusDir); err != nil {
		log.Printf("store-watcher: cannot watch %s: %v", statusDir, err)
	}
	if err := sw.watcher.Add(runningDir); err != nil {
		log.Printf("store-watcher: cannot watch %s: %v", runningDir, err)
	}

	// Snapshot current state for reconciliation
	sw.snapshotMods()

	go sw.loop()
	go sw.reconcileLoop()
	return nil
}

// Stop shuts down the watcher.
func (sw *StoreWatcher) Stop() {
	close(sw.done)
	if sw.watcher != nil {
		sw.watcher.Close()
	}
}

func (sw *StoreWatcher) loop() {
	for {
		select {
		case <-sw.done:
			return
		case event, ok := <-sw.watcher.Events:
			if !ok {
				return
			}
			if !strings.HasSuffix(event.Name, ".json") {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}

			dir := filepath.Base(filepath.Dir(event.Name))
			evtType := "update"
			if event.Op&fsnotify.Create != 0 {
				evtType = "create"
			} else if event.Op&fsnotify.Remove != 0 {
				evtType = "delete"
			} else if event.Op&fsnotify.Rename != 0 {
				evtType = "rename"
			}

			sessionID := strings.TrimSuffix(filepath.Base(event.Name), ".json")

			switch dir {
			case "status":
				sw.mu.Lock()
				sw.pendingStatus[sessionID] = struct{}{}
				sw.mu.Unlock()
				sw.scheduleDebouncedFlush()
			case "running":
				sw.mu.Lock()
				sw.pendingRunning = true
				sw.mu.Unlock()
				sw.scheduleDebouncedFlush()
			default:
				// Direct emit for other directories
				sw.emit(FileEvent{Type: evtType, SessionID: sessionID, Path: event.Name})
			}

		case err, ok := <-sw.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("store-watcher error: %v", err)
		}
	}
}

func (sw *StoreWatcher) scheduleDebouncedFlush() {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if sw.debounceTimer != nil {
		sw.debounceTimer.Stop()
	}
	sw.debounceTimer = time.AfterFunc(50*time.Millisecond, sw.flushPending)
}

func (sw *StoreWatcher) flushPending() {
	sw.mu.Lock()
	runningChanged := sw.pendingRunning
	sw.pendingRunning = false
	statusSessions := make([]string, 0, len(sw.pendingStatus))
	for sid := range sw.pendingStatus {
		statusSessions = append(statusSessions, sid)
	}
	sw.pendingStatus = make(map[string]struct{})
	sw.mu.Unlock()

	if runningChanged {
		sw.emit(FileEvent{Type: "update", SessionID: "*", Path: filepath.Join(sw.dataDir, "running")})
	}
	for _, sid := range statusSessions {
		sw.emit(FileEvent{Type: "update", SessionID: sid, Path: filepath.Join(sw.dataDir, "status", sid+".json")})
	}
}

// reconcileLoop runs every 7 seconds and checks for file changes that
// fsnotify may have missed (macOS kqueue limitation under load).
func (sw *StoreWatcher) reconcileLoop() {
	ticker := time.NewTicker(7 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-sw.done:
			return
		case <-ticker.C:
			sw.reconcile()
		}
	}
}

func (sw *StoreWatcher) reconcile() {
	changed := false

	// Check running directory
	runDir := filepath.Join(sw.dataDir, "running")
	newRunMods := scanMods(runDir)
	if !modsEqual(sw.lastRunningMods, newRunMods) {
		sw.lastRunningMods = newRunMods
		sw.emit(FileEvent{Type: "update", SessionID: "*", Path: runDir})
		changed = true
	}

	// Check status directory
	statDir := filepath.Join(sw.dataDir, "status")
	newStatMods := scanMods(statDir)
	for name, modTime := range newStatMods {
		if prev, ok := sw.lastStatusMods[name]; !ok || !modTime.Equal(prev) {
			sid := strings.TrimSuffix(name, ".json")
			sw.emit(FileEvent{Type: "update", SessionID: sid, Path: filepath.Join(statDir, name)})
			changed = true
		}
	}
	// Check for deleted status files
	for name := range sw.lastStatusMods {
		if _, ok := newStatMods[name]; !ok {
			sid := strings.TrimSuffix(name, ".json")
			sw.emit(FileEvent{Type: "delete", SessionID: sid, Path: filepath.Join(statDir, name)})
			changed = true
		}
	}
	sw.lastStatusMods = newStatMods

	if changed {
		log.Printf("store-watcher: reconciliation detected missed changes")
	}
}

func (sw *StoreWatcher) snapshotMods() {
	sw.lastRunningMods = scanMods(filepath.Join(sw.dataDir, "running"))
	sw.lastStatusMods = scanMods(filepath.Join(sw.dataDir, "status"))
}

// scanMods returns a map of filename → mod time for all .json files in dir.
func scanMods(dir string) map[string]time.Time {
	entries, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil
	}
	m := make(map[string]time.Time, len(entries))
	for _, path := range entries {
		if info, err := statFile(path); err == nil {
			m[filepath.Base(path)] = info
		}
	}
	return m
}

func statFile(path string) (time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

func modsEqual(a, b map[string]time.Time) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || !v.Equal(bv) {
			return false
		}
	}
	return true
}
