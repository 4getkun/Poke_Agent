package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"pokegents/dashboard/server/store"
)

type checkStatus struct {
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
	Path    string `json:"path,omitempty"`
}

type setupStatus struct {
	Complete             bool           `json:"complete"`
	DataDir              checkStatus    `json:"data_dir"`
	Config               checkStatus    `json:"config"`
	Version              string         `json:"version"`
	DashboardVersion     string         `json:"dashboard_version"`
	RootDir              string         `json:"root_dir"`
	ClaudeCLI            checkStatus    `json:"claude_cli"`
	ClaudeAuth           checkStatus    `json:"claude_auth"`
	Hooks                checkStatus    `json:"hooks"`
	StatusLine           checkStatus    `json:"status_line"`
	MCPMessaging         checkStatus    `json:"mcp_messaging"`
	NodeRuntime          checkStatus    `json:"node_runtime"`
	CodexBackend         checkStatus    `json:"codex_backend"`
	DefaultProject       checkStatus    `json:"default_project"`
	DefaultRole          checkStatus    `json:"default_role"`
	LaunchAgent          checkStatus    `json:"launch_agent"`
	ServerLifecycle      map[string]any `json:"server_lifecycle"`
	Backends             map[string]any `json:"backends"`
	Providers            map[string]any `json:"providers"`
	LifecycleMode        string         `json:"server_lifecycle_mode"`
	LaunchAgentInstalled bool           `json:"launch_agent_installed"`
	LaunchAgentRunning   bool           `json:"launch_agent_running"`
	Preferences          setupPrefs     `json:"preferences"`
	OnboardingComplete   bool           `json:"onboarding_complete"`
	DataDirExists        bool           `json:"data_dir_exists"`
	ConfigExists         bool           `json:"config_exists"`
	ClaudeHooks          string         `json:"claude_hooks"`
	RequiredActions      []string       `json:"required_actions,omitempty"`
}

type setupPrefs struct {
	DashboardOpenMode  string `json:"dashboard_open_mode"`
	DefaultInterface   string `json:"default_interface"`
	DefaultBackend     string `json:"default_backend"`
	DefaultProject     string `json:"default_project"`
	DefaultRole        string `json:"default_role"`
	EditorOpenCommand  string `json:"editor_open_command"`
	BrowserOpenCommand string `json:"browser_open_command"`
	OnboardingComplete bool   `json:"onboarding_complete"`
}

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	status := s.collectSetupStatus(r.Context())
	writeJSON(w, status)
}

func (s *Server) collectSetupStatus(ctx context.Context) setupStatus {
	root := resolvePokegentsRoot()
	paths := s.pathService()
	prefs := s.loadSetupPrefs()
	version := readVersion(root)
	defaultBackendID := "claude"
	backendCount := 0
	if s.backendStore != nil {
		defaultBackendID = s.backendStore.DefaultID()
		backendCount = len(s.backendStore.List())
	}
	st := setupStatus{
		Version:          version,
		DashboardVersion: version,
		RootDir:          root,
		ServerLifecycle: map[string]any{
			"port": s.port,
		},
		Backends: map[string]any{
			"default_id": defaultBackendID,
			"count":      backendCount,
		},
		Preferences:        prefs,
		OnboardingComplete: prefs.OnboardingComplete,
		Providers:          map[string]any{},
	}

	if info, err := os.Stat(s.dataDir); err == nil && info.IsDir() {
		st.DataDir = checkStatus{State: "ok", Path: s.dataDir}
		st.DataDirExists = true
	} else {
		st.DataDir = checkStatus{State: "missing", Path: s.dataDir, Message: "data directory does not exist"}
		st.RequiredActions = append(st.RequiredActions, "create-data-dir")
	}

	configPath := paths.ConfigPath()
	if fileExists(configPath) {
		st.Config = checkStatus{State: "ok", Path: configPath}
		st.ConfigExists = true
	} else {
		st.Config = checkStatus{State: "missing", Path: configPath, Message: "config.json missing"}
		st.RequiredActions = append(st.RequiredActions, "write-config")
	}

	for _, provider := range providerDefinitions(paths) {
		cli := checkStatus{State: "unknown"}
		if provider.DetectCLI != nil {
			cli = provider.DetectCLI(ctx)
		}
		auth := checkStatus{State: "unknown"}
		if provider.DetectAuth != nil {
			auth = provider.DetectAuth(ctx)
		}
		st.Providers[string(provider.ID)] = map[string]any{
			"id":          provider.ID,
			"displayName": provider.DisplayName,
			"cli":         cli,
			"auth":        auth,
			"commands":    provider.CLICommands,
		}
		switch provider.ID {
		case ProviderClaude:
			st.ClaudeCLI = cli
			st.ClaudeAuth = auth
			if cli.State == "ok" {
				st.MCPMessaging = detectMCP(ctx, root)
			} else {
				st.MCPMessaging = checkStatus{State: "unknown", Message: "claude CLI not found"}
			}
		case ProviderCodex:
			st.CodexBackend = cli
		}
	}

	st.Hooks, st.StatusLine = detectHooks(root, paths)
	st.ClaudeHooks = normalizeSetupState(st.Hooks.State)
	defaultProvider := providerFromBackendType(prefs.DefaultBackend, s.backendTypeForKey(prefs.DefaultBackend))
	_ = defaultProvider // provider-specific hook/MCP checks are repair signals, not first-run blockers.

	st.NodeRuntime = detectNode()
	st.DefaultProject = checkDirNonEmpty(filepath.Join(s.dataDir, "projects"), "no default project configured")
	st.DefaultRole = checkDirNonEmpty(filepath.Join(s.dataDir, "roles"), "no roles configured")
	st.LaunchAgent = detectLaunchAgent(root, paths)
	st.LaunchAgentInstalled = st.LaunchAgent.State == "installed" || st.LaunchAgent.State == "ok"
	st.LaunchAgentRunning = st.LaunchAgent.State == "ok"
	switch {
	case st.LaunchAgentRunning:
		st.LifecycleMode = "launch_agent"
	default:
		st.LifecycleMode = "manual"
	}
	if !validSurface(prefs.DefaultInterface) {
		st.RequiredActions = append(st.RequiredActions, "set-default-interface")
	}
	backendOK := false
	if s.backendStore != nil {
		_, backendOK = s.backendStore.Get(prefs.DefaultBackend)
	}
	if prefs.DefaultBackend == "" || !backendOK {
		st.RequiredActions = append(st.RequiredActions, "set-default-backend")
	}
	if st.DefaultProject.State != "ok" {
		st.RequiredActions = append(st.RequiredActions, "create-default-project")
	}
	if st.DefaultRole.State != "ok" {
		st.RequiredActions = append(st.RequiredActions, "install-default-roles")
	}
	st.Complete = st.DataDir.State == "ok" &&
		st.Config.State == "ok" &&
		st.DefaultProject.State == "ok" &&
		st.DefaultRole.State == "ok" &&
		validSurface(prefs.DefaultInterface) &&
		prefs.DefaultBackend != ""
	return st
}

func (s *Server) handleSetupApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RepairHooks        bool `json:"repair_hooks"`
		RepairMCP          bool `json:"repair_mcp"`
		InstallLaunchAgent bool `json:"install_launch_agent"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	result := map[string]any{}
	if err := s.ensureBaseConfig(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	result["base_config"] = "ok"
	if res, err := s.installDefaultRoles(); err == nil {
		result["roles"] = res
	}
	if body.RepairHooks {
		if res, err := repairClaudeHooks(resolvePokegentsRoot(), s.pathService()); err != nil {
			http.Error(w, "repair hooks: "+err.Error(), http.StatusInternalServerError)
			return
		} else {
			result["hooks"] = res
		}
	}
	if body.RepairMCP {
		if res, err := repairMCPRegistration(r.Context(), resolvePokegentsRoot()); err != nil {
			http.Error(w, "repair mcp: "+err.Error(), http.StatusInternalServerError)
			return
		} else {
			result["mcp"] = res
		}
	}
	if body.InstallLaunchAgent {
		if path, err := installDashboardLaunchAgent(resolvePokegentsRoot(), s.pathService(), s.port, true); err != nil {
			http.Error(w, "install launch agent: "+err.Error(), http.StatusInternalServerError)
			return
		} else {
			result["launch_agent"] = path
		}
	}
	writeJSON(w, map[string]any{"ok": true, "result": result})
}

func (s *Server) handleSetupPreferences(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DashboardOpenMode  string `json:"dashboard_open_mode"`
		DefaultInterface   string `json:"default_interface"`
		DefaultBackend     string `json:"default_backend"`
		DefaultProject     string `json:"default_project"`
		DefaultRole        string `json:"default_role"`
		EditorOpenCommand  string `json:"editor_open_command"`
		BrowserOpenCommand string `json:"browser_open_command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.DefaultInterface != "" && !validSurface(req.DefaultInterface) {
		http.Error(w, "default_interface must be chat or terminal", http.StatusBadRequest)
		return
	}
	if req.DefaultBackend != "" {
		if err := s.ensureBackend(req.DefaultBackend); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.backendStore.SetDefault(req.DefaultBackend); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	defaultInterface := any(nil)
	if req.DefaultInterface != "" {
		defaultInterface = runtimeNameForSurface(req.DefaultInterface)
	}
	patch := map[string]any{
		"dashboard_open_mode":  nonEmpty(req.DashboardOpenMode),
		"default_interface":    defaultInterface,
		"default_backend":      nonEmpty(req.DefaultBackend),
		"default_project":      nonEmpty(req.DefaultProject),
		"default_role":         nonEmpty(req.DefaultRole),
		"editor_open_command":  nonEmpty(req.EditorOpenCommand),
		"browser_open_command": nonEmpty(req.BrowserOpenCommand),
	}
	if err := s.patchConfig(patch); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, s.collectSetupStatus(r.Context()))
}

func (s *Server) handleSetupOnboardingComplete(w http.ResponseWriter, r *http.Request) {
	if err := s.patchConfig(map[string]any{"onboarding_complete": true}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, s.collectSetupStatus(r.Context()))
}

func (s *Server) handleSetupDefaultRoles(w http.ResponseWriter, r *http.Request) {
	res, err := s.installDefaultRoles()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "result": res})
}

func (s *Server) handleSetupDefaultProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Title string `json:"title"`
		CWD   string `json:"cwd"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Name == "" {
		body.Name = "current"
	}
	if body.CWD == "" {
		body.CWD = os.Getenv("POKEGENTS_INSTALL_CWD")
	}
	if body.CWD == "" {
		body.CWD, _ = os.Getwd()
	}
	if body.CWD == "" {
		body.CWD = "~"
	}
	if body.Title == "" {
		body.Title = filepath.Base(body.CWD)
		if body.Title == "." || body.Title == string(filepath.Separator) || body.Title == "" {
			body.Title = "Current Project"
		}
	}
	if err := writeProjectConfig(filepath.Join(s.dataDir, "projects"), body.Name, body.Title, body.CWD); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.patchConfig(map[string]any{"default_project": body.Name}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, s.collectSetupStatus(r.Context()))
}

func (s *Server) handleSetupOpenConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target  string `json:"target"`
		Command string `json:"command"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	target := strings.TrimSpace(body.Target)
	paths := s.pathService()
	path := paths.ConfigPath()
	switch target {
	case "", "pokegents", "config":
	case "backends":
		if s.backendStore != nil {
			_ = s.backendStore.EnsureReadableDefaults()
		}
		path = paths.BackendsPath()
	case "claude":
		path = paths.ClaudeSettingsPath()
	case "codex":
		path = paths.CodexConfigPath()
	default:
		http.Error(w, "unknown config target", http.StatusBadRequest)
		return
	}
	command := strings.TrimSpace(body.Command)
	if command == "" {
		command = s.loadSetupPrefs().EditorOpenCommand
	}
	if err := openInEditor(path, command); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "path": path})
}

func (s *Server) handleSetupOpenAuth(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Backend string `json:"backend"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	backend := strings.TrimSpace(body.Backend)
	if backend == "" {
		backend = s.loadSetupPrefs().DefaultBackend
	}
	instructions := map[string]any{
		"backend": backend,
		"commands": []string{
			"claude",
			"codex",
		},
		"message": "Run the relevant CLI login/auth command in your terminal, then refresh setup status.",
	}
	if providerFromBackendKey(backend) == "codex" {
		instructions["commands"] = []string{"codex"}
	} else if backend == "claude" || providerFromBackendKey(backend) == "claude" {
		instructions["commands"] = []string{"claude"}
	}
	writeJSON(w, instructions)
}

func (s *Server) handleSetupRepairHooks(w http.ResponseWriter, _ *http.Request) {
	res, err := repairClaudeHooks(resolvePokegentsRoot(), s.pathService())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "result": res})
}

func (s *Server) handleSetupRepairMCP(w http.ResponseWriter, r *http.Request) {
	res, err := repairMCPRegistration(r.Context(), resolvePokegentsRoot())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "result": res})
}

func (s *Server) handleSetupInstallLaunchAgent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RunAtLoad *bool `json:"run_at_load"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	runAtLoad := true
	if body.RunAtLoad != nil {
		runAtLoad = *body.RunAtLoad
	}
	path, err := installDashboardLaunchAgent(resolvePokegentsRoot(), s.pathService(), s.port, runAtLoad)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "path": path})
}

func (s *Server) handleSetupOpenAtLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	path, err := installDashboardLaunchAgent(resolvePokegentsRoot(), s.pathService(), s.port, body.Enabled)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "enabled": body.Enabled, "path": path})
}

func (s *Server) ensureBaseConfig() error {
	for _, dir := range []string{"profiles", "projects", "roles", "history", "running", "status", "messages", "logs"} {
		if err := os.MkdirAll(filepath.Join(s.dataDir, dir), 0o755); err != nil {
			return err
		}
	}
	cfgPath := s.pathService().ConfigPath()
	if !fileExists(cfgPath) {
		cfg := map[string]any{
			"port":                s.port,
			"dashboard_open_mode": "browser",
			"default_interface":   "chat",
			"default_backend":     "claude",
			"default_project":     "current",
			"default_role":        "implementer",
			"skip_permissions":    false,
			"onboarding_complete": false,
		}
		data, _ := json.MarshalIndent(cfg, "", "  ")
		if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
			return err
		}
	} else if err := s.patchConfigDefaults(map[string]any{
		"dashboard_open_mode": "browser",
		"default_interface":   "chat",
		"default_backend":     "claude",
		"default_project":     "current",
		"default_role":        "implementer",
	}); err != nil {
		return err
	}
	// BackendStore construction already ensures the default backend file exists.
	if s.backendStore != nil {
		_ = s.backendStore.List()
	}
	return nil
}

func (s *Server) loadSetupPrefs() setupPrefs {
	defaultBackend := "claude"
	if s.backendStore != nil {
		defaultBackend = s.backendStore.DefaultID()
	}
	prefs := setupPrefs{
		DashboardOpenMode:  "browser",
		DefaultInterface:   "chat",
		DefaultBackend:     defaultBackend,
		DefaultProject:     "current",
		DefaultRole:        "implementer",
		EditorOpenCommand:  "code {path}",
		BrowserOpenCommand: `open -a "Google Chrome" {url}`,
	}
	raw, err := os.ReadFile(s.pathService().ConfigPath())
	if err != nil {
		if prefs.DefaultBackend == "" {
			prefs.DefaultBackend = "claude"
		}
		return prefs
	}
	var cfg map[string]any
	if json.Unmarshal(raw, &cfg) != nil {
		return prefs
	}
	if v, ok := cfg["dashboard_open_mode"].(string); ok && v != "" {
		prefs.DashboardOpenMode = v
	}
	if v, ok := cfg["default_interface"].(string); ok && v != "" {
		prefs.DefaultInterface = v
	}
	if v, ok := cfg["default_backend"].(string); ok && v != "" {
		prefs.DefaultBackend = v
	}
	if v, ok := cfg["default_project"].(string); ok && v != "" {
		prefs.DefaultProject = v
	}
	if v, ok := cfg["default_role"].(string); ok && v != "" {
		prefs.DefaultRole = v
	}
	if v, ok := cfg["editor_open_command"].(string); ok && v != "" {
		prefs.EditorOpenCommand = v
	}
	if v, ok := cfg["browser_open_command"].(string); ok && v != "" {
		prefs.BrowserOpenCommand = v
	}
	if v, ok := cfg["onboarding_complete"].(bool); ok {
		prefs.OnboardingComplete = v
	}
	if prefs.DefaultBackend == "" {
		prefs.DefaultBackend = "claude"
	}
	return prefs
}

func (s *Server) patchConfig(patch map[string]any) error {
	return s.patchConfigWithMode(patch, false)
}

func (s *Server) patchConfigDefaults(patch map[string]any) error {
	return s.patchConfigWithMode(patch, true)
}

func (s *Server) patchConfigWithMode(patch map[string]any, defaultsOnly bool) error {
	if err := os.MkdirAll(s.dataDir, 0o755); err != nil {
		return err
	}
	path := s.pathService().ConfigPath()
	cfg := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
	}
	for k, v := range patch {
		if v == nil {
			continue
		}
		if defaultsOnly {
			if existing, ok := cfg[k]; ok && existing != nil && existing != "" {
				continue
			}
		}
		cfg[k] = v
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Server) installDefaultRoles() (map[string]any, error) {
	dir := filepath.Join(s.dataDir, "roles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	roles := map[string]store.RoleConfig{
		"implementer": {
			Title:        "Implementer",
			Emoji:        "🛠️",
			SystemPrompt: "You are an implementer agent. Make focused code changes, follow existing patterns, validate your work, and report changed files plus validation commands.",
		},
		"reviewer": {
			Title:        "Reviewer",
			Emoji:        "👀",
			SystemPrompt: "You are a code reviewer agent. Review changes for correctness, edge cases, consistency, and spec adherence. Be specific and actionable.",
		},
		"researcher": {
			Title:        "Researcher",
			Emoji:        "🧪",
			SystemPrompt: "You are a research agent. Explore, investigate, and summarize findings with evidence before recommending changes.",
		},
		"pm": {
			Title:        "PM",
			Emoji:        "📋",
			SystemPrompt: "You are a product manager agent. Clarify requirements, sequence work, and coordinate agents. Do not write code unless explicitly asked.",
		},
	}
	installed := []string{}
	for name, role := range roles {
		path := filepath.Join(dir, name+".json")
		if fileExists(path) {
			continue
		}
		data, err := json.MarshalIndent(role, "", "  ")
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return nil, err
		}
		installed = append(installed, name)
	}
	_ = s.patchConfigDefaults(map[string]any{"default_role": "implementer"})
	return map[string]any{"installed": installed, "dir": dir}, nil
}

func writeProjectConfig(dir, name, title, cwd string) error {
	if name == "" {
		return fmt.Errorf("project name is required")
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("project name must not contain path separators")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	p := store.ProjectConfig{
		Title:   title,
		Color:   [3]int{100, 180, 255},
		CWD:     cwd,
		AddDirs: []string{},
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name+".json"), data, 0o644)
}

func (s *Server) ensureBackend(id string) error {
	switch id {
	case "", "claude":
		if _, ok := s.backendStore.Get("claude"); !ok {
			return s.backendStore.EnsureReadableDefaults()
		}
		return nil
	case "codex":
		if _, ok := s.backendStore.Get("codex"); !ok {
			return s.backendStore.EnsureReadableDefaults()
		}
		return nil
	case "codex-acp":
		if _, ok := s.backendStore.Get("codex"); !ok {
			return s.backendStore.EnsureReadableDefaults()
		}
		return nil
	default:
		if _, ok := s.backendStore.Get(id); !ok {
			return fmt.Errorf("unknown backend %q", id)
		}
		return nil
	}
}

func (s *Server) handleOpenExternal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind    string `json:"kind"`
		Target  string `json:"target"`
		Command string `json:"command"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	kind := strings.TrimSpace(strings.ToLower(body.Kind))
	target := strings.TrimSpace(body.Target)
	command := strings.TrimSpace(body.Command)
	prefs := s.loadSetupPrefs()
	switch kind {
	case "file":
		if command == "" {
			command = prefs.EditorOpenCommand
		}
	case "url":
		if command == "" {
			command = prefs.BrowserOpenCommand
		}
	default:
		http.Error(w, "unknown open kind", http.StatusBadRequest)
		return
	}
	if err := runOpenCommand(command, target, kind); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func runOpenCommand(command, target, kind string) error {
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("%s target is required", kind)
	}
	if strings.TrimSpace(command) == "" {
		if kind == "url" {
			command = `open -a "Google Chrome" {url}`
		} else {
			command = "code {path}"
		}
	}
	escaped := shellQuote(target)
	cmdline := command
	replaced := false
	for _, placeholder := range []string{"{target}", "{path}", "{file}", "{url}"} {
		if strings.Contains(cmdline, placeholder) {
			cmdline = strings.ReplaceAll(cmdline, placeholder, escaped)
			replaced = true
		}
	}
	if !replaced {
		cmdline += " " + escaped
	}
	cmd := exec.Command("sh", "-lc", cmdline)
	cmd.Env = append(os.Environ(),
		"POKEGENTS_OPEN_TARGET="+target,
		"POKEGENTS_OPEN_KIND="+kind,
	)
	return cmd.Start()
}

func openInEditor(path string, command string) error {
	if path == "" {
		return fmt.Errorf("config path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if !fileExists(path) {
		initial := []byte("{}\n")
		if strings.EqualFold(filepath.Ext(path), ".toml") {
			initial = []byte("# Pokegents opens this provider-owned config for convenience.\n# Example:\n# model = \"your-model-id\"\n\n")
		}
		if err := os.WriteFile(path, initial, 0o644); err != nil {
			return err
		}
	}
	return runOpenCommand(command, path, "file")
}

func normalizeSetupState(state string) string {
	if state == "ok" {
		return "current"
	}
	if state == "" {
		return "unknown"
	}
	return state
}

func nonEmpty(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return strings.TrimSpace(v)
}

func detectClaudeAuth(ctx context.Context) checkStatus {
	// `claude auth status` is available in recent Claude Code builds; keep this
	// best-effort and short so setup status never hangs the dashboard.
	out, err := runShort(ctx, 2*time.Second, "claude", "auth", "status")
	if err != nil {
		return checkStatus{State: "unknown", Message: strings.TrimSpace(out)}
	}
	low := strings.ToLower(out)
	if strings.Contains(low, "authenticated") || strings.Contains(low, "logged in") {
		return checkStatus{State: "ok", Message: strings.TrimSpace(out)}
	}
	return checkStatus{State: "unknown", Message: strings.TrimSpace(out)}
}

func detectMCP(ctx context.Context, root string) checkStatus {
	out, err := runShort(ctx, 3*time.Second, "claude", "mcp", "list")
	if err != nil {
		return checkStatus{State: "unknown", Message: strings.TrimSpace(out)}
	}
	if !strings.Contains(out, "pokegents-messaging") {
		return checkStatus{State: "missing", Message: "pokegents-messaging is not registered"}
	}
	serverPath := filepath.Join(root, "mcp", "server.js")
	if root != "" && !strings.Contains(out, serverPath) {
		return checkStatus{State: "stale", Path: serverPath, Message: "registered MCP may point at a different install root"}
	}
	return checkStatus{State: "ok", Path: serverPath}
}

func detectHooks(root string, paths PathService) (checkStatus, checkStatus) {
	settingsPath := paths.ClaudeSettingsPath()
	statusCmd := filepath.Join(root, "hooks", "status-update.sh")
	statusLineCmd := filepath.Join(root, "hooks", "statusline.sh")
	if !fileExists(settingsPath) {
		return checkStatus{State: "missing", Path: settingsPath, Message: "Claude settings file missing"}, checkStatus{State: "missing", Path: settingsPath}
	}
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		return checkStatus{State: "error", Path: settingsPath, Message: err.Error()}, checkStatus{State: "error", Path: settingsPath, Message: err.Error()}
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return checkStatus{State: "error", Path: settingsPath, Message: err.Error()}, checkStatus{State: "error", Path: settingsPath, Message: err.Error()}
	}
	hookCount := countCommand(cfg["hooks"], statusCmd)
	hookState := checkStatus{State: "missing", Path: statusCmd, Message: "status-update hook not registered"}
	if hookCount >= len(pokegentsHookEvents()) {
		hookState = checkStatus{State: "ok", Path: statusCmd, Message: fmt.Sprintf("%d events registered", hookCount)}
	} else if hookCount > 0 {
		hookState = checkStatus{State: "stale", Path: statusCmd, Message: fmt.Sprintf("%d/%d events registered", hookCount, len(pokegentsHookEvents()))}
	}
	statusLineState := checkStatus{State: "missing", Path: statusLineCmd, Message: "statusLine command not registered"}
	if sl, _ := cfg["statusLine"].(map[string]any); sl != nil && sl["command"] == statusLineCmd {
		statusLineState = checkStatus{State: "ok", Path: statusLineCmd}
	}
	return hookState, statusLineState
}

func detectNode() checkStatus {
	node, nerr := exec.LookPath("node")
	npm, perr := exec.LookPath("npm")
	if nerr == nil && perr == nil {
		return checkStatus{State: "ok", Path: node, Message: "npm: " + npm}
	}
	return checkStatus{State: "missing", Message: "node and npm are required for ACP/MCP adapters"}
}

func detectCodexBackend() checkStatus {
	if path, err := exec.LookPath("codex"); err == nil {
		return checkStatus{State: "ok", Path: path}
	}
	if path, err := exec.LookPath("npx"); err == nil {
		return checkStatus{State: "available", Path: path, Message: "codex can be launched through npx"}
	}
	return checkStatus{State: "missing", Message: "codex CLI/npx not found"}
}

func checkDirNonEmpty(path, missingMsg string) checkStatus {
	entries, err := os.ReadDir(path)
	if err != nil {
		return checkStatus{State: "missing", Path: path, Message: missingMsg}
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			count++
		}
	}
	if count == 0 {
		return checkStatus{State: "missing", Path: path, Message: missingMsg}
	}
	return checkStatus{State: "ok", Path: path, Message: fmt.Sprintf("%d file(s)", count)}
}

func detectLaunchAgent(root string, paths PathService) checkStatus {
	path := filepath.Join(paths.LaunchAgentsDir(), "com.pokegents.dashboard.plist")
	if !fileExists(path) {
		return checkStatus{State: "missing", Path: path}
	}
	state := "installed"
	msg := "LaunchAgent plist exists"
	if runtime.GOOS == "darwin" {
		uid := os.Getuid()
		out, err := runShort(context.Background(), time.Second, "launchctl", "print", fmt.Sprintf("gui/%d/com.pokegents.dashboard", uid))
		if err == nil {
			state, msg = "ok", strings.TrimSpace(firstLine(out))
		}
	}
	_ = root
	return checkStatus{State: state, Path: path, Message: msg}
}

func repairClaudeHooks(root string, paths PathService) (map[string]any, error) {
	if root == "" {
		return nil, fmt.Errorf("could not resolve POKEGENTS_ROOT")
	}
	settingsPath := paths.ClaudeSettingsPath()
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return nil, err
	}
	cfg := map[string]any{}
	if raw, err := os.ReadFile(settingsPath); err == nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	backupPath := settingsPath + ".bak." + time.Now().Format("20060102-150405")
	if fileExists(settingsPath) {
		if raw, err := os.ReadFile(settingsPath); err == nil {
			_ = os.WriteFile(backupPath, raw, 0o600)
		}
	} else {
		backupPath = ""
	}
	statusCmd := filepath.Join(root, "hooks", "status-update.sh")
	statusLineCmd := filepath.Join(root, "hooks", "statusline.sh")
	ephCmd := filepath.Join(root, "hooks", "ephemeral-track.sh")
	for _, hook := range []string{statusCmd, statusLineCmd, ephCmd} {
		if fileExists(hook) {
			_ = os.Chmod(hook, 0o755)
		}
	}
	mergeHooks(cfg, statusCmd, ephCmd)
	cfg["statusLine"] = map[string]any{"type": "command", "command": statusLineCmd}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	tmp := settingsPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, settingsPath); err != nil {
		return nil, err
	}
	return map[string]any{"settings": settingsPath, "backup": backupPath, "hook": statusCmd, "status_line": statusLineCmd}, nil
}

func mergeHooks(cfg map[string]any, statusCmd, ephCmd string) {
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		cfg["hooks"] = hooks
	}
	for event, matcher := range pokegentsHookEvents() {
		appendHook(hooks, event, matcher, statusCmd)
	}
	appendHook(hooks, "SubagentStart", "", ephCmd)
	appendHook(hooks, "SubagentStop", "", ephCmd)
	appendHook(hooks, "PreToolUse", "Agent", ephCmd)
}

func pokegentsHookEvents() map[string]string {
	return map[string]string{"UserPromptSubmit": "", "PreToolUse": "", "PostToolUse": "", "PostToolUseFailure": "", "Stop": "", "StopFailure": "", "PermissionRequest": "", "Notification": "idle_prompt", "SessionStart": "", "SessionEnd": ""}
}

func appendHook(hooks map[string]any, event, matcher, cmd string) {
	if cmd == "" {
		return
	}
	arr, _ := hooks[event].([]any)
	for _, item := range arr {
		if countCommand(item, cmd) > 0 {
			hooks[event] = arr
			return
		}
	}
	entry := map[string]any{"matcher": matcher, "hooks": []any{map[string]any{"type": "command", "command": cmd, "timeout": float64(5)}}}
	hooks[event] = append(arr, entry)
}

func countCommand(v any, cmd string) int {
	if cmd == "" || v == nil {
		return 0
	}
	count := 0
	switch x := v.(type) {
	case map[string]any:
		if x["command"] == cmd {
			count++
		}
		for _, val := range x {
			count += countCommand(val, cmd)
		}
	case []any:
		for _, val := range x {
			count += countCommand(val, cmd)
		}
	case string:
		if x == cmd {
			count++
		}
	}
	return count
}

func repairMCPRegistration(ctx context.Context, root string) (map[string]any, error) {
	if root == "" {
		return nil, fmt.Errorf("could not resolve POKEGENTS_ROOT")
	}
	serverPath := filepath.Join(root, "mcp", "server.js")
	if !fileExists(serverPath) {
		return nil, fmt.Errorf("MCP server not found at %s", serverPath)
	}
	if _, err := exec.LookPath("node"); err != nil {
		return nil, fmt.Errorf("node not found on PATH")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		return nil, fmt.Errorf("claude not found on PATH")
	}
	out, err := runShort(ctx, 15*time.Second, "claude", "mcp", "add", "-s", "user", "pokegents-messaging", "--", "node", serverPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(out))
	}
	return map[string]any{"server": serverPath, "output": strings.TrimSpace(out)}, nil
}

func installDashboardLaunchAgent(root string, paths PathService, port int, runAtLoad bool) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("LaunchAgent install is only supported on macOS")
	}
	if root == "" {
		return "", fmt.Errorf("could not resolve POKEGENTS_ROOT")
	}
	bin := filepath.Join(root, "dashboard", "pokegents-dashboard")
	if !fileExists(bin) {
		return "", fmt.Errorf("dashboard binary not found at %s", bin)
	}
	path := filepath.Join(paths.LaunchAgentsDir(), "com.pokegents.dashboard.plist")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	dataDir := paths.DataDir
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.pokegents.dashboard</string>
  <key>ProgramArguments</key><array><string>%s</string><string>serve</string><string>--port</string><string>%d</string></array>
  <key>EnvironmentVariables</key><dict><key>POKEGENTS_ROOT</key><string>%s</string><key>POKEGENTS_DATA</key><string>%s</string></dict>
  <key>RunAtLoad</key><%s/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, xmlEscape(bin), port, xmlEscape(root), xmlEscape(dataDir), boolPlist(runAtLoad), xmlEscape(filepath.Join(dataDir, "logs", "dashboard.out.log")), xmlEscape(filepath.Join(dataDir, "logs", "dashboard.err.log")))
	if err := os.MkdirAll(paths.LogsDir(), 0o755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(plist), 0o644)
}

func runShort(parent context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	ctx := parent
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

func readVersion(root string) string {
	for _, p := range []string{filepath.Join(root, "VERSION"), filepath.Join(root, "dashboard", "VERSION")} {
		if raw, err := os.ReadFile(p); err == nil {
			return strings.TrimSpace(string(raw))
		}
	}
	return "dev"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
func boolPlist(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;")
	return r.Replace(s)
}
