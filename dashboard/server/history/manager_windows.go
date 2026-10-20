//go:build windows

package history

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"pokegents/dashboard/server/processhost"
	"pokegents/dashboard/server/runtimeadapter"
	"pokegents/dashboard/server/sqlstore"
	d "pokegents/dashboard/server/taskdomain"
)

type Config struct{ Executable, Home string }
type Info struct {
	Available      bool   `json:"available"`
	Executable     string `json:"executable_path"`
	Home           string `json:"home_path"`
	RuntimeVersion string `json:"runtime_version"`
	Reason         string `json:"reason"`
}
type client interface {
	ListHistory(context.Context, string, string, bool) (runtimeadapter.HistoryPage, error)
	ReadHistory(context.Context, string) (runtimeadapter.HistoryThread, error)
	Close(context.Context) error
}
type Manager struct {
	Store   *sqlstore.Store
	config  Config
	mu      sync.Mutex
	closing bool
	ctx     context.Context
	cancel  context.CancelFunc
	workers sync.WaitGroup
	slot    chan struct{}
	start   func(context.Context, string) (client, error)
}

func New(store *sqlstore.Store, config Config) *Manager {
	if config.Executable == "" {
		root, _ := windows.KnownFolderPath(windows.FOLDERID_RoamingAppData, 0)
		config.Executable = filepath.Join(root, "npm", "node_modules", "@openai", "codex", "node_modules", "@openai", "codex-win32-x64", "vendor", "x86_64-pc-windows-msvc", "bin", "codex.exe")
	}
	if config.Home == "" {
		config.Home = os.Getenv("CODEX_HOME")
	}
	if config.Home == "" {
		root, _ := windows.KnownFolderPath(windows.FOLDERID_Profile, 0)
		config.Home = filepath.Join(root, ".codex")
	}
	if home, err := processhost.Workspace(config.Home); err == nil {
		config.Home = home.ResolvedPath
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{Store: store, config: config, ctx: ctx, cancel: cancel, slot: make(chan struct{}, 1)}
	m.start = func(ctx context.Context, cwd string) (client, error) {
		if _, err := runtimeadapter.Probe(ctx, config.Executable, cwd); err != nil {
			return nil, d.Fail("HISTORY_UNAVAILABLE")
		}
		return runtimeadapter.StartAtHome(ctx, config.Executable, cwd, config.Home)
	}
	return m
}
func (m *Manager) Info() Info {
	info := Info{Executable: m.config.Executable, Home: m.config.Home, RuntimeVersion: runtimeadapter.SupportedVersion, Reason: "HISTORY_UNAVAILABLE"}
	if !filepath.IsAbs(info.Executable) || !strings.EqualFold(filepath.Ext(info.Executable), ".exe") {
		return info
	}
	if file, err := os.Stat(info.Executable); err != nil || !file.Mode().IsRegular() {
		return info
	}
	if _, err := processhost.Workspace(info.Home); err != nil {
		return info
	}
	info.Available = true
	info.Reason = ""
	return info
}
func (m *Manager) Cancel() { m.mu.Lock(); m.closing = true; m.cancel(); m.mu.Unlock() }
func (m *Manager) Close(ctx context.Context) error {
	m.Cancel()
	done := make(chan struct{})
	go func() { m.workers.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *Manager) begin(ctx context.Context) (context.Context, func(), error) {
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return nil, nil, d.Fail("APP_CLOSING")
	}
	m.workers.Add(1)
	m.mu.Unlock()
	op, cancel := context.WithTimeout(ctx, 20*time.Second)
	stop := context.AfterFunc(m.ctx, cancel)
	return op, func() { stop(); cancel(); m.workers.Done() }, nil
}
func (m *Manager) withClient(ctx context.Context, project *d.Project, fn func(client) error) (result error) {
	if !m.Info().Available {
		return d.Fail("HISTORY_UNAVAILABLE")
	}
	current, err := processhost.Workspace(project.DisplayPath)
	if err != nil || current.Identity != project.Identity {
		return d.Fail("WORKSPACE_CHANGED")
	}
	select {
	case m.slot <- struct{}{}:
		defer func() { <-m.slot }()
	default:
		return d.Fail("HISTORY_BUSY")
	}
	c, err := m.start(ctx, current.ResolvedPath)
	if err != nil {
		return err
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := c.Close(stop); err != nil {
			result = d.Fail("HISTORY_UNAVAILABLE")
		}
		if result != nil {
			if result.Error() == "HISTORY_LIMIT" {
				result = d.Fail("HISTORY_LIMIT")
			}
		}
	}()
	return fn(c)
}
func (m *Manager) List(ctx context.Context, project *d.Project, cursor string, archived bool) (Page, error) {
	ctx, end, err := m.begin(ctx)
	if err != nil {
		return Page{}, err
	}
	defer end()
	if len(cursor) > 4096 {
		return Page{}, d.Fail("INVALID_REQUEST")
	}
	page := Page{Items: []Summary{}}
	err = m.withClient(ctx, project, func(c client) error {
		result, err := c.ListHistory(ctx, project.ResolvedPath, cursor, archived)
		if err != nil {
			return err
		}
		if len(result.Data) > 20 || len(result.NextCursor) > 4096 {
			return d.Fail("HISTORY_LIMIT")
		}
		for _, thread := range result.Data {
			if uuid(thread.ID) && sameWorkspace(thread.CWD, project) && supportedSource(thread.Source) {
				page.Items = append(page.Items, summary(thread))
			}
		}
		if result.NextCursor != "" {
			page.NextCursor = &result.NextCursor
		}
		return nil
	})
	return page, err
}
func sameWorkspace(cwd string, project *d.Project) bool {
	return filepath.IsAbs(cwd) && strings.EqualFold(filepath.Clean(cwd), filepath.Clean(project.ResolvedPath))
}
func (m *Manager) read(ctx context.Context, project *d.Project, id string) (Preview, error) {
	if !uuid(id) {
		return Preview{}, d.Fail("INVALID_HISTORY_ID")
	}
	var preview Preview
	err := m.withClient(ctx, project, func(c client) error {
		thread, err := c.ReadHistory(ctx, id)
		if err != nil {
			return err
		}
		if thread.ID != id || !sameWorkspace(thread.CWD, project) || !supportedSource(thread.Source) {
			return d.Fail("INVALID_HISTORY_ID")
		}
		preview, err = makePreview(thread)
		return err
	})
	return preview, err
}
func (m *Manager) Preview(ctx context.Context, project *d.Project, id string) (Preview, error) {
	ctx, end, err := m.begin(ctx)
	if err != nil {
		return Preview{}, err
	}
	defer end()
	return m.read(ctx, project, id)
}
