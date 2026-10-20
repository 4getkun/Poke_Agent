import { useState, useEffect, useRef } from 'react'
import { createPortal } from 'react-dom'
import { GameModal } from './GameModal'
import { ProjectInfo, RoleInfo, BackendInfo, launchPokegent, setSprite, renameAgent, fetchSessions, fetchBackends, fetchSetupStatus } from '../api'
import { AgentState } from '../types'
import { POKEMON_SPRITES } from './sprites'
import { PixelSprite } from './PixelSprite'
import { SpritePicker } from './SpritePicker'

interface LaunchModalProps {
  projects: ProjectInfo[]
  roles: RoleInfo[]
  agents: AgentState[]
  onClose: () => void
}

function GbaDropdown<T extends { key: string; label: string; color?: [number, number, number]; sprite?: string }>({ label, value, options, onChange, allowNone, disabled }: {
  label: string
  value: string
  options: T[]
  onChange: (key: string) => void
  allowNone?: boolean
  disabled?: boolean
}) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const handler = (e: MouseEvent) => { if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false) }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [open])

  const selected = options.find(o => o.key === value)

  return (
    <div ref={ref} className="relative">
      <label className="text-s theme-font-display theme-text-muted pixel-shadow block mb-1">{label}</label>
      <button
        onClick={() => !disabled && setOpen(v => !v)}
        disabled={disabled}
        className="w-full flex items-center justify-between gba-dialog-dark px-2 py-1.5 text-s theme-font-mono theme-text-primary hover:brightness-110 focus:border-accent-blue transition-colors disabled:opacity-50"
        style={{ background: 'var(--theme-dropdown-bg)', borderColor: 'var(--theme-dropdown-border)' }}
      >
        <span className="flex items-center gap-1.5">
          {selected?.sprite && (
            <div className="w-4 h-4 flex items-center justify-center overflow-visible"><PixelSprite sprite={selected.sprite} scale={0.5} alt="" /></div>
          )}
          {selected?.color && (
            <span className="w-2 h-2 rounded-sm shrink-0" style={{ background: `rgb(${selected.color[0]},${selected.color[1]},${selected.color[2]})` }} />
          )}
          {selected ? selected.label : 'None'}
        </span>
        <span className="theme-text-faint text-s">{open ? '▲' : '▼'}</span>
      </button>
      {open && (
        <div
          className="absolute top-full left-0 right-0 mt-0.5 gba-dropdown-panel z-50 py-0.5 max-h-[200px] overflow-y-auto"
        >
          {allowNone !== false && (
            <button
              onClick={() => { onChange(''); setOpen(false) }}
              className={`w-full text-left px-2 py-1 text-s theme-font-mono theme-bg-dropdown-hover transition-colors ${!value ? 'text-accent-yellow' : 'theme-text-muted'}`}
            >
              None
            </button>
          )}
          {options.map(o => (
            <button
              key={o.key}
              onClick={() => { onChange(o.key); setOpen(false) }}
              className={`w-full text-left px-2 py-1 text-s theme-font-mono transition-colors flex items-center gap-1.5 ${value === o.key ? 'text-accent-yellow theme-bg-dropdown-active' : 'theme-text-primary theme-bg-dropdown-hover'}`}
            >
              {o.sprite && (
                <div className="w-4 h-4 flex items-center justify-center overflow-visible"><PixelSprite sprite={o.sprite} scale={0.5} alt="" /></div>
              )}
              {o.color && (
                <span className="w-2 h-2 rounded-sm shrink-0" style={{ background: `rgb(${o.color[0]},${o.color[1]},${o.color[2]})` }} />
              )}
              {o.label}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}


function pokemonDisplayName(sprite: string): string {
  return sprite
    .split(/[-_]/g)
    .filter(Boolean)
    .map(part => part.charAt(0).toUpperCase() + part.slice(1))
    .join(' ')
}

function backendKindForBackend(backend?: BackendInfo): 'claude' | 'codex' {
  const haystack = `${backend?.id || ''} ${backend?.name || ''} ${backend?.type || ''}`.toLowerCase()
  return haystack.includes('codex') || haystack.includes('gpt') || haystack.includes('openai') ? 'codex' : 'claude'
}

function backendForKind(backends: BackendInfo[], kind: 'claude' | 'codex'): BackendInfo | undefined {
  return backends.find(b => b.default && backendKindForBackend(b) === kind)
    || backends.find(b => backendKindForBackend(b) === kind)
}

function backendIdForKind(backends: BackendInfo[], kind: 'claude' | 'codex'): string | undefined {
  return backendForKind(backends, kind)?.id || (kind === 'claude' ? 'claude' : 'codex')
}

function modelOptionsForBackend(backend?: BackendInfo): { key: string; label: string; model?: string; effort?: string }[] {
  const models = backend?.models || {}
  const entries = Object.entries(models)
  const options = entries.length > 0
    ? entries.map(([key, cfg]) => ({
      key,
      label: cfg.name || cfg.model || key,
      model: cfg.model,
      effort: cfg.effort,
    }))
    : backendKindForBackend(backend) === 'claude'
      ? [
        { key: 'sonnet-4-6', label: 'Sonnet 4.6', model: 'claude-sonnet-4-6' },
        { key: 'opus-4-7', label: 'Opus 4.7', model: 'claude-opus-4-7' },
        { key: 'opus-4-6', label: 'Opus 4.6 (1M)', model: 'claude-opus-4-6[1m]' },
        { key: 'haiku-4-5', label: 'Haiku 4.5', model: 'haiku' },
      ]
      : [
        { key: 'default', label: 'Provider default', model: '' },
      ]

  return options.sort((a, b) => {
    if (a.key === backend?.default_model) return -1
    if (b.key === backend?.default_model) return 1
    return a.label.localeCompare(b.label)
  })
}

export function LaunchModal({ projects, roles, agents: _agents, onClose }: LaunchModalProps) {
  const [randomSprite] = useState(() => POKEMON_SPRITES[Math.floor(Math.random() * POKEMON_SPRITES.length)])
  const [selectedRole, setSelectedRole] = useState('implementer')
  const [selectedProject, setSelectedProject] = useState('current')
  const [name, setName] = useState(() => pokemonDisplayName(randomSprite))
  const [sprite, setSelectedSprite] = useState('')
  const [showSpritePicker, setShowSpritePicker] = useState(false)
  const [launching, setLaunching] = useState(false)
  const [backends, setBackends] = useState<BackendInfo[]>([])
  const [backendKind, setBackendKind] = useState<'claude' | 'codex'>('claude')
  const [selectedModel, setSelectedModel] = useState('')
  const [iface, setIface] = useState<'terminal' | 'chat'>(() => {
    try {
      const stored = localStorage.getItem('pokegents-launch-interface')
      return stored === 'terminal' || stored === 'iterm2' ? 'terminal' : 'chat'
    }
    catch { return 'chat' }
  })
  useEffect(() => {
    try { localStorage.setItem('pokegents-launch-interface', iface) } catch { /* ignore */ }
  }, [iface])

  // Fetch setup defaults + backends on mount
  useEffect(() => {
    fetchSetupStatus().then(status => {
      const prefInterface = status?.preferences?.default_interface
      if (prefInterface === 'chat') setIface('chat')
      else if (prefInterface === 'terminal' || prefInterface === 'iterm2') setIface('terminal')
    }).catch(() => {})
    fetchBackends().then(list => {
      setBackends(list)
      const def = list.find(b => b.default)
      if (def) {
        setBackendKind(backendKindForBackend(def))
        setSelectedModel(def.default_model || Object.keys(def.models || {})[0] || '')
      }
    })
  }, [])


  useEffect(() => {
    if (selectedRole && roles.length > 0 && !roles.some(r => r.name === selectedRole)) {
      setSelectedRole(roles.find(r => r.name === 'implementer')?.name || roles[0]?.name || '')
    }
  }, [roles, selectedRole])

  useEffect(() => {
    if (selectedProject && projects.length > 0 && !projects.some(p => p.name === selectedProject)) {
      setSelectedProject(projects.find(p => p.name === 'current')?.name || projects[0]?.name || '')
    }
  }, [projects, selectedProject])

  const canLaunch = selectedProject || selectedRole

  const roleOptions = roles.map(r => ({ key: r.name, label: r.title }))
  const projectOptions = projects.map(p => ({ key: p.name, label: p.title, color: p.color }))
  const selectedBackend = backendForKind(backends, backendKind)
  const modelOptions = modelOptionsForBackend(selectedBackend)
  const selectedModelConfig = modelOptions.find(m => m.key === selectedModel)

  useEffect(() => {
    const backend = backendForKind(backends, backendKind)
    const options = modelOptionsForBackend(backend)
    if (options.length === 0) {
      if (selectedModel) setSelectedModel('')
      return
    }
    if (!selectedModel || !options.some(o => o.key === selectedModel)) {
      setSelectedModel(backend?.default_model || options[0].key)
    }
  }, [backends, backendKind, selectedModel])

  const displaySprite = sprite || randomSprite

  const handleLaunch = async () => {
    if (!canLaunch || launching) return
    setLaunching(true)

    const wantSprite = displaySprite
    const wantName = name.trim()

    let resp
    try {
      // Unified launch — server mints pokegent_id and pre-writes the running
      // file before invoking the launcher. Returns the pokegent_id we can use
      // to apply sprite/name overrides without polling-by-exclusion.
      resp = await launchPokegent({
        role: selectedRole || undefined,
        project: selectedProject || undefined,
        name: wantName || undefined,
        sprite: wantSprite || undefined,
        task_group: undefined,
        interface: iface,
        agent_backend: backendIdForKind(backends, backendKind),
        model: selectedModelConfig?.model || undefined,
        effort: selectedModelConfig?.effort || undefined,
      })
    } catch (err) {
      console.error('launch failed', err)
      alert(`Launch failed: ${err instanceof Error ? err.message : String(err)}`)
      setLaunching(false)
      return
    }

    // Wait for pokegent.sh to overwrite the placeholder with real session info,
    // then apply user's name + sprite. Keyed by pokegent_id from launch response —
    // no more polling-by-exclusion.
    const pokegentId = resp.pokegent_id
    {
      for (let i = 0; i < 40; i++) {
        await new Promise(r => setTimeout(r, 500))
        const fresh = await fetchSessions()
        const newAgent = fresh.find(a => a.pokegent_id === pokegentId && a.session_id && a.is_alive)
        if (newAgent) {
          if (wantSprite) await setSprite(newAgent.session_id, wantSprite)
          if (wantName) await renameAgent(newAgent.pokegent_id || newAgent.session_id, wantName)
          break
        }
      }
    }


    setLaunching(false)
    onClose()
  }

  return (
    <GameModal
      title="New Agent"
      onClose={onClose}
      width="min(320px, 96vw)"
      height="auto"
      maxHeight="92vh"
      scanlines={false}
    >
      <div
        className="p-3 flex flex-col gap-2"
        style={{
          borderRadius: '0 0 8px 8px',
          overflow: 'visible',
          background: 'var(--theme-chat-panel-bg)',
          border: '2px solid var(--theme-panel-border)',
          color: 'var(--theme-panel-text)',
          fontFamily: 'var(--theme-font-mono)',
          boxShadow: 'var(--theme-shadow-panel)',
        }}
      >
        {/* Name & Pokemon */}
        <div className="flex gap-2 items-end">
          <div className="flex-1 min-w-0">
            <label className="text-s theme-font-display theme-text-muted pixel-shadow block mb-1">Name</label>
            <input
              type="text"
              value={name}
              onChange={e => setName(e.target.value)}
              placeholder="Pokemon name"
              className="w-full gba-dialog-dark px-2 py-1.5 text-s theme-font-mono theme-text-primary theme-placeholder-input outline-none focus:border-accent-blue transition-colors"
            />
          </div>
          <div>
            <label className="text-s theme-font-display theme-text-muted pixel-shadow block mb-1">Sprite</label>
            <button
              onClick={() => setShowSpritePicker(true)}
              className="gba-dialog-dark px-2 py-1 hover:brightness-110 focus:border-accent-blue transition-colors flex items-center gap-1"
            >
              <div className="w-5 h-5 flex items-center justify-center overflow-visible">
                <PixelSprite sprite={displaySprite} scale={1} alt="" />
              </div>
              <span className="theme-text-faint text-s">▼</span>
            </button>
          </div>
        </div>

        {/* Role & Project side by side */}
        <div className="flex gap-2">
          <div className="flex-1 min-w-0">
            <GbaDropdown label="Role" value={selectedRole} options={roleOptions} onChange={setSelectedRole} />
          </div>
          <div className="flex-1 min-w-0">
            <GbaDropdown label="Project" value={selectedProject} options={projectOptions} onChange={setSelectedProject} />
          </div>
        </div>

        <div className="border-t theme-border-subtle my-0.5" />

        <GbaDropdown
          label="Interface"
          value={iface}
          options={[
            { key: 'chat', label: 'Pokegent Chat' },
            { key: 'terminal', label: 'Terminal' },
          ]}
          onChange={key => setIface(key as 'terminal' | 'chat')}
          allowNone={false}
        />

        <div className="flex gap-2">
          <div className="flex-1 min-w-0">
            <GbaDropdown
              label="Backend"
              value={backendKind}
              options={[
                { key: 'claude', label: 'Claude' },
                { key: 'codex', label: 'Codex' },
              ]}
              onChange={key => { setBackendKind(key as 'claude' | 'codex'); setSelectedModel('') }}
              allowNone={false}
            />
          </div>
          <div className="flex-1 min-w-0">
            <GbaDropdown
              label="Model"
              value={selectedModel}
              options={modelOptions.length > 0 ? modelOptions : [{ key: '', label: selectedBackend?.name ? `${selectedBackend.name} default` : 'Default' }]}
              onChange={setSelectedModel}
              allowNone={false}
              disabled={modelOptions.length === 0}
            />
          </div>
        </div>

        <div className="flex gap-2 pt-3">
          <button
            onClick={onClose}
            disabled={launching}
            className="gba-button text-s theme-font-display px-2.5 py-1.5 transition-colors opacity-80"
          >
            Cancel
          </button>
          <button
            onClick={handleLaunch}
            disabled={!canLaunch || launching}
            className={`flex-1 gba-button text-s theme-font-display px-2.5 py-1.5 transition-colors ${
              !canLaunch ? 'opacity-30 cursor-not-allowed' : ''
            }`}
          >
            {launching ? 'Launching...' : 'Launch'}
          </button>
        </div>
      </div>

      {showSpritePicker && createPortal(
        <SpritePicker
          currentSprite={displaySprite}
          onSelect={(s) => { setSelectedSprite(s); setShowSpritePicker(false) }}
          onClose={() => setShowSpritePicker(false)}
        />,
        document.body
      )}
    </GameModal>
  )
}
