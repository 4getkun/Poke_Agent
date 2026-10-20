import { ReactNode, useMemo, useState } from 'react'
import {
  SetupStatus,
  createDefaultProject,
  installDefaultRoles,
  openSetupAuth,
  openSetupConfig,
  setSetupPreferences,
} from '../../api'
import { GameModal } from '../GameModal'

type StepId = 'welcome' | 'auth' | 'project' | 'done'

interface OnboardingModalProps {
  status: SetupStatus | null
  onClose: () => void
  onRefresh: () => void
}

const STEPS: { id: StepId; title: string }[] = [
  { id: 'welcome', title: 'Welcome' },
  { id: 'auth', title: 'Agent auth' },
  { id: 'project', title: 'Project defaults' },
  { id: 'done', title: 'Done' },
]

function Pill({ ok, label }: { ok?: boolean; label: string }) {
  return (
    <span className={`inline-flex items-center rounded border px-2 py-1 text-xs theme-font-display uppercase leading-none ${
      ok
        ? 'border-accent-green/40 bg-accent-green/15 text-accent-green'
        : 'border-accent-red/35 bg-accent-red/10 text-accent-red'
    }`}>
      {label}: {ok ? 'OK' : 'NEEDS SETUP'}
    </span>
  )
}

function Card({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="gba-card p-4 space-y-3" style={{ boxShadow: 'var(--theme-shadow-panel)' }}>
      <h3 className="text-s theme-font-display uppercase text-accent-yellow pixel-shadow">{title}</h3>
      <div className="setup-readable text-l leading-relaxed theme-font-mono theme-text-secondary space-y-3">{children}</div>
    </section>
  )
}

function statusText(value: boolean | string | { state?: string; message?: string; path?: string } | undefined) {
  if (value === undefined || value === null) return 'unknown'
  if (typeof value === 'boolean') return value ? 'ok' : 'missing'
  if (typeof value === 'object') return value.state || value.message || 'unknown'
  return value
}

function statusOk(value: unknown) {
  if (value === true) return true
  if (typeof value === 'string') return value === 'ok' || value === 'current' || value === 'available' || value === 'running' || value === 'installed'
  if (value && typeof value === 'object' && 'state' in value) {
    const state = String((value as { state?: string }).state || '')
    return state === 'ok' || state === 'current' || state === 'available' || state === 'running' || state === 'installed'
  }
  return false
}

export function OnboardingModal({ status, onClose, onRefresh }: OnboardingModalProps) {
  const [stepIdx, setStepIdx] = useState(0)
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const step = STEPS[stepIdx]

  const dataOk = Boolean((status?.data_dir_exists ?? statusOk(status?.data_dir)) && (status?.config_exists ?? statusOk(status?.config)))
  const authOk = statusOk(status?.claude_auth) || statusOk(status?.claude_cli) || statusOk(status?.codex_backend)
  const projectOk = statusOk(status?.default_project)
  const roleOk = statusOk(status?.default_role)
  const defaultInterface = status?.preferences?.default_interface || 'chat'
  const defaultBackend = status?.preferences?.default_backend || 'claude'

  const summary = useMemo(() => [
    { label: 'Data/config', ok: dataOk },
    { label: 'Agent auth', ok: authOk },
    { label: 'Project', ok: projectOk },
    { label: 'Roles', ok: roleOk },
  ], [dataOk, authOk, projectOk, roleOk])

  const run = async (label: string, fn: () => Promise<unknown>) => {
    setBusy(label)
    setError(null)
    try {
      await fn()
      await onRefresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(null)
    }
  }

  return (
    <GameModal title="Pokégents Setup" onClose={onClose} width="min(760px, 96vw)" height="min(640px, 92vh)" zIndex={80} scanlines={false}>
      <div
        className="flex min-h-0 flex-1 flex-col overflow-hidden"
        style={{
          borderRadius: '0 0 8px 8px',
          background: 'var(--theme-chat-panel-bg)',
          border: '2px solid var(--theme-panel-border)',
          color: 'var(--theme-panel-text)',
          fontFamily: 'var(--theme-font-mono)',
          boxShadow: 'var(--theme-shadow-panel)',
        }}
      >
        <div className="px-4 pt-3 pb-2 border-b theme-border-subtle shrink-0 theme-bg-panel-muted">
          <p className="text-l theme-font-mono theme-text-secondary leading-relaxed">
            First-time setup is just defaults + agent auth. Repair tools for hooks, MCP, and server lifecycle live under Settings → Dev/Repair.
          </p>
        </div>

        <div className="p-4 grid grid-cols-[180px_1fr] gap-4 min-h-0 overflow-hidden flex-1">
          <aside className="min-h-0 overflow-auto space-y-1 rounded-md border theme-border-subtle theme-bg-panel-muted p-2">
            {STEPS.map((s, idx) => (
              <button
                key={s.id}
                onClick={() => setStepIdx(idx)}
                className={`w-full rounded px-2.5 py-2 text-left transition-colors ${idx === stepIdx ? 'bg-accent-blue theme-text-primary' : 'theme-bg-panel-subtle theme-text-muted theme-bg-panel-hover'}`}
                style={{ boxShadow: idx === stepIdx ? 'var(--theme-shadow-panel)' : 'none' }}
              >
                <span className="flex items-center gap-2 min-w-0">
                  <span className="text-xs theme-font-display leading-none opacity-80">{idx + 1}</span>
                  <span className="text-l theme-font-mono truncate">{s.title}</span>
                </span>
              </button>
            ))}
            <div className="pt-3 space-y-1.5">
              {summary.map(item => <Pill key={item.label} ok={item.ok} label={item.label} />)}
            </div>
          </aside>

          <main className="min-h-0 overflow-auto pr-1 space-y-4">
            {step.id === 'welcome' && (
              <Card title="SMART DEFAULTS">
                <p>Pokegents creates its own data/config defaults automatically. You should only need to pick where agents run and which provider to use first.</p>
                <div className="rounded-md border theme-border-subtle theme-bg-panel-muted p-3 space-y-2">
                  <p>Launch surface: <span className="theme-text-primary uppercase">{defaultInterface === 'chat' ? 'dashboard chat' : 'terminal'}</span></p>
                  <p>Default backend: <span className="theme-text-primary uppercase">{defaultBackend}</span></p>
                  <div className="flex flex-wrap gap-2">
                    <button disabled={!!busy} onClick={() => run('interface-dashboard', () => setSetupPreferences({ default_interface: 'chat' }))} className="gba-button text-s theme-font-display px-3 py-2 disabled:opacity-50">USE DASHBOARD</button>
                    <button disabled={!!busy} onClick={() => run('interface-terminal', () => setSetupPreferences({ default_interface: 'terminal' }))} className="gba-button text-s theme-font-display px-3 py-2 disabled:opacity-50">USE TERMINAL</button>
                    <button disabled={!!busy} onClick={() => run('backend-claude', () => setSetupPreferences({ default_backend: 'claude' }))} className="gba-button text-s theme-font-display px-3 py-2 disabled:opacity-50">USE CLAUDE</button>
                    <button disabled={!!busy} onClick={() => run('backend-codex', () => setSetupPreferences({ default_backend: 'codex' }))} className="gba-button text-s theme-font-display px-3 py-2 disabled:opacity-50">USE CODEX</button>
                  </div>
                </div>
              </Card>
            )}

            {step.id === 'auth' && (
              <Card title="AGENT AUTH">
                <p>Authenticate the providers you want to use. Claude Code subscription auth and Codex config are provider-owned; Pokegents just detects them and opens the right files/help.</p>
                <div className="rounded-md border theme-border-subtle theme-bg-panel-muted p-3 space-y-1.5">
                  <p>Claude CLI: <span className="theme-text-primary">{statusText(status?.claude_cli)}</span></p>
                  <p>Claude auth: <span className="theme-text-primary">{statusText(status?.claude_auth)}</span></p>
                  <p>Codex backend: <span className="theme-text-primary">{statusText(status?.codex_backend)}</span></p>
                </div>
                <div className="flex flex-wrap gap-2">
                  <button disabled={!!busy} onClick={() => run('auth-help', () => openSetupAuth(defaultBackend))} className="gba-button text-s theme-font-display px-3 py-2 disabled:opacity-50">AUTH HELP</button>
                  <button disabled={!!busy} onClick={() => run('open-backends', () => openSetupConfig('backends'))} className="gba-button text-s theme-font-display px-3 py-2 disabled:opacity-50">EDIT BACKENDS</button>
                  <button disabled={!!busy} onClick={() => run('open-claude-config', () => openSetupConfig('claude'))} className="gba-button text-s theme-font-display px-3 py-2 disabled:opacity-50">OPEN CLAUDE CONFIG</button>
                  <button disabled={!!busy} onClick={() => run('open-codex-config', () => openSetupConfig('codex'))} className="gba-button text-s theme-font-display px-3 py-2 disabled:opacity-50">OPEN CODEX CONFIG</button>
                </div>
              </Card>
            )}

            {step.id === 'project' && (
              <Card title="PROJECT DEFAULTS">
                <p>Default roles/project are just starter metadata so New Agent has useful defaults. You can edit or ignore them later.</p>
                <p>Default project: <span className="theme-text-primary">{statusText(status?.default_project)}</span></p>
                <p>Default role: <span className="theme-text-primary">{statusText(status?.default_role)}</span></p>
                <div className="flex flex-wrap gap-2">
                  <button disabled={!!busy} onClick={() => run('roles', installDefaultRoles)} className="gba-button text-s theme-font-display px-3 py-2 disabled:opacity-50">INSTALL ROLES</button>
                  <button disabled={!!busy} onClick={() => run('project', () => createDefaultProject({ name: 'current' }))} className="gba-button text-s theme-font-display px-3 py-2 disabled:opacity-50">CREATE PROJECT</button>
                  <button disabled={!!busy} onClick={() => run('open-config', () => openSetupConfig('pokegents'))} className="gba-button text-s theme-font-display px-3 py-2 disabled:opacity-50">OPEN CONFIG</button>
                </div>
              </Card>
            )}

            {step.id === 'done' && (
              <Card title="DONE">
                <p>You can start using Pokegents now. If something looks broken later, use Settings → Dev/Repair for hooks, MCP messaging, logs, and server lifecycle.</p>
                <div className="flex flex-wrap gap-2 pt-1">
                  <Pill ok={dataOk} label="Data/config" />
                  <Pill ok={authOk} label="Auth" />
                  <Pill ok={projectOk} label="Project" />
                </div>
              </Card>
            )}

            {busy && <p className="text-l theme-font-mono text-accent-yellow">Working: {busy}…</p>}
            {error && <p className="text-l theme-font-mono text-accent-red">{error}</p>}
          </main>
        </div>

        <div className="p-4 border-t theme-border-subtle theme-bg-panel-muted flex items-center justify-between shrink-0">
          <button disabled={stepIdx === 0} onClick={() => setStepIdx(i => Math.max(0, i - 1))} className="gba-button text-s theme-font-display px-3 py-2 disabled:opacity-40">BACK</button>
          <div className="text-s theme-font-display theme-text-faint pixel-shadow">{step.title.toUpperCase()} · {stepIdx + 1}/{STEPS.length}</div>
          <button onClick={() => stepIdx === STEPS.length - 1 ? onClose() : setStepIdx(i => Math.min(STEPS.length - 1, i + 1))} className="gba-button text-s theme-font-display px-3 py-2">
            {stepIdx === STEPS.length - 1 ? 'FINISH' : 'NEXT'}
          </button>
        </div>
      </div>
    </GameModal>
  )
}
