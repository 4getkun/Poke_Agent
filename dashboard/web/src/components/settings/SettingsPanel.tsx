import { ReactNode, useMemo, useState } from 'react'
import { DashboardSettings } from '../../hooks/useSettings'
import { installLaunchAgent, openSetupConfig, repairClaudeHooks, repairMcpMessaging, setSetupPreferences, SetupStatus } from '../../api'
import { GameModal } from '../GameModal'

interface SettingsPanelProps {
  settings: DashboardSettings
  defaults: DashboardSettings
  setupStatus?: SetupStatus | null
  onChange: (update: Partial<DashboardSettings>) => void
  onReset: () => void
  onClose: () => void
  onTestMessaging?: () => void
  onGridDragging?: (dragging: boolean) => void
  onOpenOnboarding?: () => void
  onOpenTownEditor?: () => void
}

type TabId = 'layout' | 'appearance' | 'shortcuts' | 'agents' | 'dev'

function SettingRow({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)] gap-4 items-start">
      <div className="min-w-0">
        <label className="block text-l theme-font-mono theme-text-secondary leading-snug">{label}</label>
        {hint && <p className="mt-1 text-m leading-relaxed theme-font-mono theme-text-faint">{hint}</p>}
      </div>
      <div className="min-w-0 flex items-center justify-start">{children}</div>
    </div>
  )
}

function Slider({ label, value, min, max, step, unit, onChange, onDragStart, onDragEnd }: {
  label: string; value: number; min: number; max: number; step: number; unit?: string
  onChange: (v: number) => void
  onDragStart?: () => void
  onDragEnd?: () => void
}) {
  return (
    <SettingRow label={label}>
      <div className="flex items-center gap-3 w-full max-w-[360px]">
      <input
        type="range"
        min={min} max={max} step={step} value={value}
        onChange={e => onChange(Number(e.target.value))}
        onPointerDown={() => onDragStart?.()}
        onPointerUp={() => onDragEnd?.()}
        className="flex-1 h-1.5 accent-accent-blue cursor-pointer"
      />
      <span className="text-l theme-font-mono theme-text-muted min-w-12 text-left">{value}{unit || ''}</span>
      </div>
    </SettingRow>
  )
}

function Toggle({ label, checked, onChange, hint }: {
  label: string; checked: boolean; onChange: (v: boolean) => void; hint?: string
}) {
  return (
    <SettingRow label={label} hint={hint}>
        <button
          onClick={() => onChange(!checked)}
          className={`w-8 h-4 rounded-full transition-colors relative shrink-0 ${checked ? 'bg-accent-green' : 'theme-bg-panel-subtle'}`}
          style={{ boxShadow: 'var(--theme-shadow-panel)' }}
        >
          <div
            className="absolute top-0.5 w-3 h-3 rounded-full bg-[var(--theme-text-primary)] transition-transform"
            style={{
              transform: checked ? 'translateX(16px)' : 'translateX(2px)',
              boxShadow: 'var(--theme-text-shadow-pixel)',
            }}
          />
        </button>
    </SettingRow>
  )
}

function OptionGroup<T extends string>({ label, value, options, onChange }: {
  label: string; value: T; options: { value: T; label: string }[]; onChange: (v: T) => void
}) {
  return (
    <SettingRow label={label}>
      <div className="flex gap-1 flex-wrap">
        {options.map(opt => (
          <button
            key={opt.value}
            onClick={() => onChange(opt.value)}
            className={`text-s theme-font-display uppercase pixel-shadow px-2 py-1 rounded transition-colors ${
              value === opt.value
                ? 'bg-accent-blue theme-text-primary'
                : 'theme-bg-panel-subtle theme-text-muted theme-bg-panel-hover'
            }`}
            style={{
              boxShadow: value === opt.value
                ? 'var(--theme-shadow-panel)'
                : 'none',
              textShadow: 'var(--theme-text-shadow-pixel)',
            }}
          >
            {opt.label.toUpperCase()}
          </button>
        ))}
      </div>
    </SettingRow>
  )
}

function TextSetting({ label, value, placeholder, hint, onChange }: {
  label: string; value: string; placeholder?: string; hint?: string; onChange: (v: string) => void
}) {
  return (
    <SettingRow label={label} hint={hint}>
      <input
        type="text"
        value={value}
        placeholder={placeholder}
        onChange={e => onChange(e.target.value)}
        className="w-full max-w-[390px] rounded border theme-border-subtle theme-bg-panel-muted theme-text-primary theme-font-mono text-l px-3 py-2 outline-none focus:border-accent-blue"
      />
    </SettingRow>
  )
}

function StatusRow({ label, value }: { label: string; value: boolean | string | { state?: string; message?: string; path?: string } | undefined }) {
  const state = value && typeof value === 'object' ? (value.state || value.message || 'Unknown') : value
  const display = typeof state === 'boolean' ? (state ? 'Ok' : 'Missing') : String(state || 'Unknown')
  const ok = state === true || state === 'ok' || state === 'current' || state === 'running' || state === 'installed' || state === 'available'
  const warn = state === false || state === 'missing' || state === 'stale' || state === 'error'
  return (
    <div className="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)] gap-4 text-l theme-font-mono leading-snug">
      <span className="theme-text-muted">{label}</span>
      <span className={`${ok ? 'text-accent-green' : warn ? 'text-accent-red' : 'theme-text-faint'} truncate`}>{display}</span>
    </div>
  )
}

function launchAgentStatus(status?: SetupStatus | null) {
  if (!status) return undefined
  if (status.launch_agent) return status.launch_agent
  if (status.launch_agent_running) return 'running'
  if (status.launch_agent_installed) return 'installed'
  return false
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="space-y-3.5">
      <h4 className="text-s theme-font-display uppercase text-accent-yellow pixel-shadow">{title.toUpperCase()}</h4>
      {children}
    </section>
  )
}

export function SettingsPanel({ settings, defaults, setupStatus, onChange, onReset, onClose, onTestMessaging, onGridDragging, onOpenOnboarding, onOpenTownEditor }: SettingsPanelProps) {
  const [tab, setTab] = useState<TabId>('layout')
  const tabs = useMemo<{ id: TabId; label: string }[]>(() => [
    { id: 'layout', label: 'Layout' },
    { id: 'appearance', label: 'Theme' },
    { id: 'shortcuts', label: 'Keys' },
    { id: 'agents', label: 'Configs' },
    { id: 'dev', label: 'Dev' },
  ], [])

  return (
    <GameModal title="Settings" onClose={onClose} width="min(760px, 96vw)" height="min(640px, 92vh)" scanlines={false}>
      <div
        className="p-4 flex flex-col min-h-0"
        style={{
          borderRadius: '0 0 8px 8px',
          overflow: 'hidden',
          flex: 1,
          background: 'var(--theme-chat-panel-bg)',
          border: '2px solid var(--theme-panel-border)',
          color: 'var(--theme-panel-text)',
          fontFamily: 'var(--theme-font-mono)',
          boxShadow: 'var(--theme-shadow-panel)',
        }}
      >
        <div
          className="grid shrink-0 border-b theme-border-subtle"
          style={{
            gridTemplateColumns: `repeat(${tabs.length}, minmax(0, 1fr))`,
            marginLeft: -16,
            marginRight: -16,
            marginTop: -16,
          }}
        >
          {tabs.map(t => {
            const active = tab === t.id
            return (
              <button
                key={t.id}
                onClick={() => setTab(t.id)}
                className={`relative text-m theme-font-display uppercase pixel-shadow px-3 py-3 transition-colors border-r last:border-r-0 theme-border-subtle theme-text-primary ${
                  active
                    ? 'theme-bg-panel-muted'
                    : 'bg-transparent theme-bg-panel-hover'
                }`}
              >
                {t.label.toUpperCase()}
                {active && <span className="absolute left-0 right-0 -bottom-px h-[2px] bg-accent-yellow" />}
              </button>
            )
          })}
        </div>

        <div className="settings-readable space-y-5 overflow-auto pr-1 pt-4 min-h-0 flex-1 text-l theme-font-mono leading-relaxed">
          {tab === 'layout' && (
            <>
              <Section title="Grid density">
                <Slider
                  label="Number of columns"
                  value={settings.cardsPerRow}
                  min={1} max={8} step={1}
                  onChange={v => onChange({ cardsPerRow: v })}
                  onDragStart={() => onGridDragging?.(true)}
                  onDragEnd={() => onGridDragging?.(false)}
                />
                <Slider
                  label="Number of rows"
                  value={settings.cardsPerCol}
                  min={1} max={6} step={1}
                  onChange={v => onChange({ cardsPerCol: v })}
                  onDragStart={() => onGridDragging?.(true)}
                  onDragEnd={() => onGridDragging?.(false)}
                />
              </Section>
              <Section title="Card spacing">
                <Slider label="Card gap" value={settings.cardGap} min={0} max={24} step={1} unit="px" onChange={v => onChange({ cardGap: v })} />
                <Slider label="Card padding" value={settings.cardPadding} min={0} max={24} step={1} unit="px" onChange={v => onChange({ cardPadding: v })} />
              </Section>
              <Section title="World">
                <Toggle label="Show town card" checked={settings.showTownCard} onChange={v => onChange({ showTownCard: v })} />
              </Section>
              <Section title="Reset">
                <SettingRow label="Layout defaults" hint="Restore layout density, card spacing, and card padding. Other settings stay unchanged.">
                  <button
                    onClick={() => onChange({
                      cardsPerRow: defaults.cardsPerRow,
                      cardsPerCol: defaults.cardsPerCol,
                      cardGap: defaults.cardGap,
                      cardPadding: defaults.cardPadding,
                    })}
                    className="inline-flex gba-button text-s theme-font-display uppercase pixel-shadow px-3 py-2 transition-colors"
                  >
                    RESET LAYOUT
                  </button>
                </SettingRow>
              </Section>
            </>
          )}

          {tab === 'appearance' && (
            <>
              <Section title="Theme">
                <OptionGroup
                  label="Colorway"
                  value={settings.theme}
                  options={[
                    { value: 'fire-red', label: 'Retro' },
                    { value: 'classic', label: 'Classic' },
                    { value: 'vscode', label: 'VS Code Dark' },
                    { value: 'vscode-light', label: 'VS Code Light' },
                  ]}
                  onChange={v => onChange({ theme: v })}
                />
                <Toggle label="Scanlines" checked={settings.scanlines} onChange={v => onChange({ scanlines: v })} />
              </Section>
              <Section title="Text Size">
                <Slider label="Agent card output" value={settings.agentCardOutputFontSize} min={8} max={18} step={1} unit="px" onChange={v => onChange({ agentCardOutputFontSize: v })} />
                <Slider label="Chat panel output" value={settings.chatPanelOutputFontSize} min={8} max={20} step={1} unit="px" onChange={v => onChange({ chatPanelOutputFontSize: v })} />
              </Section>
            </>
          )}

          {tab === 'shortcuts' && (
            <>
              <Section title="Global shortcuts">
                <div className="rounded theme-bg-panel-subtle border theme-border-subtle p-3 space-y-2">
                  {[
                    ['/', 'Open PC Box search'],
                    ['Cmd + 1..9', 'Focus agent by grid position'],
                    ['Esc', 'Close modal / cancel focused overlay'],
                    ['Enter', 'Send prompt'],
                    ['Shift + Enter', 'New line in prompt'],
                  ].map(([key, desc]) => (
                    <div key={key} className="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)] gap-4 text-l theme-font-mono">
                      <span className="theme-text-muted">{desc}</span>
                      <kbd className="justify-self-start text-s theme-font-display uppercase theme-text-secondary theme-bg-panel-muted border theme-border-subtle rounded px-1.5 py-1 pixel-shadow">{key.toUpperCase()}</kbd>
                    </div>
                  ))}
                </div>
                <p className="text-l leading-relaxed theme-font-mono theme-text-faint">
                  First pass is discoverability-only. Configurable bindings will move these into the shortcut registry/API from the spec.
                </p>
              </Section>
            </>
          )}

          {tab === 'agents' && (
            <>
              <Section title="Open defaults">
                <TextSetting
                  label="Open files command"
                  value={settings.editorOpenCommand}
                  placeholder="code {path}"
                  hint="Use {path}; if omitted, the file path is appended."
                  onChange={v => {
                    onChange({ editorOpenCommand: v })
                    setSetupPreferences({ editor_open_command: v }).catch(() => {})
                  }}
                />
                <TextSetting
                  label="Open URLs command"
                  value={settings.browserOpenCommand}
                  placeholder={'open -a "Google Chrome" {url}'}
                  hint="Use {url}; if omitted, the URL is appended."
                  onChange={v => {
                    onChange({ browserOpenCommand: v })
                    setSetupPreferences({ browser_open_command: v }).catch(() => {})
                  }}
                />
              </Section>
              <Section title="Config files">
                <SettingRow label="Backend models" hint="Edit Pokegents backend entries: provider type, default model, effort, and env.">
                  <button onClick={() => openSetupConfig('backends', settings.editorOpenCommand)} className="inline-flex gba-button text-s theme-font-display uppercase pixel-shadow px-3 py-2 transition-colors">
                    OPEN BACKENDS.JSON
                  </button>
                </SettingRow>
                <SettingRow label="Claude config" hint="Provider-owned Claude Code settings. Use for Claude-specific auth/settings.">
                  <button onClick={() => openSetupConfig('claude', settings.editorOpenCommand)} className="inline-flex gba-button text-s theme-font-display uppercase pixel-shadow px-3 py-2 transition-colors">
                    OPEN CLAUDE SETTINGS
                  </button>
                </SettingRow>
                <SettingRow label="Codex config" hint="Provider-owned Codex config.toml. Use for Codex CLI defaults.">
                  <button onClick={() => openSetupConfig('codex', settings.editorOpenCommand)} className="inline-flex gba-button text-s theme-font-display uppercase pixel-shadow px-3 py-2 transition-colors">
                    OPEN CODEX CONFIG
                  </button>
                </SettingRow>
                <p className="text-l leading-relaxed theme-font-mono theme-text-faint">
                  Prefer backends.json for Pokegents launch choices. Set model entries to exact provider model IDs; legacy aliases are only accepted for old configs.
                </p>
              </Section>
              <Section title="Setup">
                {onOpenOnboarding && (
                  <button onClick={onOpenOnboarding} className="inline-flex gba-button text-s theme-font-display uppercase pixel-shadow px-3 py-2 transition-colors">
                    OPEN FIRST-RUN SETUP
                  </button>
                )}
              </Section>
            </>
          )}

          {tab === 'dev' && (
            <>
              <Section title="Repair">
                <div className="rounded theme-bg-panel-subtle border theme-border-subtle p-3 space-y-1.5">
                  <StatusRow label="Claude hooks" value={setupStatus?.hooks || setupStatus?.claude_hooks} />
                  <StatusRow label="Claude MCP" value={setupStatus?.mcp_messaging} />
                  <StatusRow label="Claude CLI" value={setupStatus?.claude_cli} />
                  <StatusRow label="Codex CLI" value={setupStatus?.codex_backend} />
                </div>
                <div className="flex flex-wrap gap-2">
                  <button onClick={() => repairClaudeHooks().catch(err => alert(String(err)))} className="inline-flex gba-button text-s theme-font-display uppercase pixel-shadow px-3 py-2 transition-colors">
                    REPAIR HOOKS
                  </button>
                  <button onClick={() => repairMcpMessaging().catch(err => alert(String(err)))} className="inline-flex gba-button text-s theme-font-display uppercase pixel-shadow px-3 py-2 transition-colors">
                    REPAIR MCP
                  </button>
                  <button onClick={() => installLaunchAgent().catch(err => alert(String(err)))} className="inline-flex gba-button text-s theme-font-display uppercase pixel-shadow px-3 py-2 transition-colors">
                    INSTALL LAUNCHAGENT
                  </button>
                </div>
                <p className="text-l leading-relaxed theme-font-mono theme-text-faint">
                  MCP is only needed for Claude Code agent-to-agent messaging. It is a repair/integration check, not a first-run blocker.
                </p>
              </Section>
              <Section title="Debug">
                {onOpenTownEditor && (
                  <button onClick={onOpenTownEditor} className="inline-flex gba-button text-s theme-font-display uppercase pixel-shadow px-3 py-2 transition-colors">
                    SHOW TOWN EDITOR
                  </button>
                )}
                {onTestMessaging && (
                  <button onClick={onTestMessaging} className="inline-flex gba-button text-s theme-font-display uppercase pixel-shadow px-3 py-2 transition-colors">
                    TEST MESSAGING
                  </button>
                )}
              </Section>
              <Section title="Server">
                <div className="rounded theme-bg-panel-subtle border theme-border-subtle p-3 space-y-1.5">
                  <StatusRow label="Version" value={setupStatus?.dashboard_version} />
                  <StatusRow label="Lifecycle" value={setupStatus?.server_lifecycle_mode} />
                  <StatusRow label="LaunchAgent" value={launchAgentStatus(setupStatus)} />
                </div>
                <button
                  onClick={() => {
                    if (!confirm('Rebuild and restart the dashboard server? All agents will reattach automatically.')) return
                    window.dispatchEvent(new Event('server-restart-requested'))
                    fetch('/api/server/restart', { method: 'POST' }).catch(() => {})
                  }}
                  className="inline-flex text-s theme-font-display uppercase pixel-shadow px-3 py-2 transition-colors rounded border border-accent-red/40 text-accent-red/70 hover:bg-accent-red/10"
                >
                  REBUILD + RESTART SERVER
                </button>
              </Section>
            </>
          )}
        </div>


      </div>
    </GameModal>
  )
}
