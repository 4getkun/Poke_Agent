import { useEffect, useRef, useState } from 'react'
import { AgentState } from '../types'
import {
  focusAgent, checkAgentMessages, spawnClone, shutdownAgent,
  assignRole, assignProject, assignTaskGroup, migrateInterface,
  switchBackend, fetchBackends, setRuntimeConfig,
  RuntimeCapabilities, ProjectInfo, RoleInfo, BackendInfo,
} from '../api'
import { GameModal } from './GameModal'

interface AgentMenuProps {
  x: number
  y: number
  agent: AgentState
  capabilities: RuntimeCapabilities
  onClose: () => void
  onRename: () => void
  onChangeSprite: () => void
  onCollapse?: () => void
  onAssignStatus?: (msg: string) => void
  projects?: ProjectInfo[]
  roles?: RoleInfo[]
  existingGroups?: string[]
}

function SwitchDropdown({ label, value, options, onChange }: {
  label: string
  value: string
  options: { key: string; label: string }[]
  onChange: (key: string) => void
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
        onClick={() => setOpen(v => !v)}
        className="w-full flex items-center justify-between gba-dialog-dark px-2 py-1.5 text-s theme-font-mono theme-text-primary hover:brightness-110 focus:border-accent-blue transition-colors"
        style={{ background: 'var(--theme-dropdown-bg)', borderColor: 'var(--theme-dropdown-border)' }}
      >
        <span>{selected ? selected.label : 'None'}</span>
        <span className="theme-text-faint text-s">{open ? '▲' : '▼'}</span>
      </button>
      {open && (
        <div className="absolute top-full left-0 right-0 mt-0.5 gba-dropdown-panel z-50 py-0.5 max-h-[200px] overflow-y-auto">
          {options.map(o => (
            <button
              key={o.key}
              onClick={() => { onChange(o.key); setOpen(false) }}
              className={`w-full text-left px-2 py-1 text-s theme-font-mono transition-colors ${value === o.key ? 'text-accent-yellow theme-bg-dropdown-active' : 'theme-text-primary theme-bg-dropdown-hover'}`}
            >
              {o.label}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

function SwitchRuntimeModal({ agent, onClose, onAssignStatus }: {
  agent: AgentState
  onClose: () => void
  onAssignStatus?: (msg: string) => void
}) {
  const agentId = agent.pokegent_id || agent.session_id
  const [backends, setBackends] = useState<BackendInfo[]>([])
  const [iface, setIface] = useState<'chat' | 'iterm2'>(agent.interface as 'chat' | 'iterm2' || 'chat')
  const [backendId, setBackendId] = useState(agent.agent_backend || '')
  const [modelId, setModelId] = useState('')
  const [applying, setApplying] = useState(false)

  useEffect(() => {
    fetchBackends().then(list => {
      setBackends(list)
      if (!backendId && list.length > 0) {
        const def = list.find(b => b.default) || list[0]
        setBackendId(def.id)
      }
    })
  }, [])

  const selectedBackend = backends.find(b => b.id === backendId)
  const models = selectedBackend?.models ? Object.entries(selectedBackend.models) : []
  const currentModelKey = models.find(([, m]) => agent.model?.toLowerCase().includes(m.model.toLowerCase()))?.[0]

  async function apply() {
    setApplying(true)
    try {
      if (iface !== agent.interface) {
        const result = await migrateInterface(agentId, iface)
        if (iface === 'chat') {
          window.dispatchEvent(new CustomEvent('open-chat-panel', {
            detail: { pokegentId: result.pokegent_id },
          }))
        }
        onAssignStatus?.(`Switched to ${iface}`)
      }
      if (backendId !== (agent.agent_backend || '') && iface === 'chat') {
        await switchBackend(agentId, backendId)
        onAssignStatus?.(`Switching backend to ${selectedBackend?.name}...`)
      }
      if (modelId && iface === 'chat') {
        const modelEntry = models.find(([k]) => k === modelId)
        if (modelEntry) {
          await setRuntimeConfig(agentId, modelEntry[1].model, '')
        }
      }
      onClose()
    } catch (err) {
      alert(`Switch failed: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setApplying(false)
    }
  }

  return (
    <GameModal
      title="Switch Runtime"
      onClose={onClose}
      width="min(320px, 96vw)"
      height="auto"
      maxHeight="92vh"
      scanlines={false}
      zIndex={10001}
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
        <SwitchDropdown
          label="Interface"
          value={iface}
          options={[
            { key: 'chat', label: 'Pokegent Chat' },
            { key: 'iterm2', label: 'Terminal (iTerm2)' },
          ]}
          onChange={key => setIface(key as 'chat' | 'iterm2')}
        />

        {iface === 'chat' && (
          <div className="flex gap-2">
            <div className="flex-1 min-w-0">
              <SwitchDropdown
                label="Backend"
                value={backendId}
                options={backends.map(b => ({ key: b.id, label: b.name + (b.default ? ' (default)' : '') }))}
                onChange={key => { setBackendId(key); setModelId('') }}
              />
            </div>
            <div className="flex-1 min-w-0">
              {models.length > 0 && (
                <SwitchDropdown
                  label="Model"
                  value={modelId || selectedBackend?.default_model || currentModelKey || ''}
                  options={models.map(([key, m]) => ({ key, label: m.name || m.model }))}
                  onChange={setModelId}
                />
              )}
            </div>
          </div>
        )}

        <div className="text-s theme-font-mono theme-text-warning leading-snug">
          Restarts the agent. History is preserved.
        </div>

        <div className="flex gap-2 pt-0.5">
          <button
            onClick={onClose}
            disabled={applying}
            className="gba-button text-s theme-font-display px-2.5 py-1.5 transition-colors opacity-80"
          >
            Cancel
          </button>
          <button
            onClick={apply}
            disabled={applying}
            className={`flex-1 gba-button text-s theme-font-display px-2.5 py-1.5 transition-colors ${applying ? 'opacity-30 cursor-not-allowed' : ''}`}
          >
            {applying ? 'Applying...' : 'Apply'}
          </button>
        </div>
      </div>
    </GameModal>
  )
}

export function AgentMenu({
  x, y, agent, capabilities, onClose, onRename, onChangeSprite, onCollapse,
  projects, roles, existingGroups, onAssignStatus,
}: AgentMenuProps) {
  const [submenu, setSubmenu] = useState<'role' | 'project' | 'group' | null>(null)
  const [newGroupName, setNewGroupName] = useState('')
  const newGroupRef = useRef<HTMLInputElement>(null)
  const [showSwitchModal, setShowSwitchModal] = useState(false)
  const agentId = agent.pokegent_id || agent.session_id

  const showStatus = (res: { status: string }, label: string) => {
    if (!onAssignStatus) return
    if (res.status === 'relaunching') onAssignStatus(`Saved — relaunching to load ${label}...`)
    else if (res.status === 'queued') onAssignStatus(`Saved — will relaunch to load ${label} when idle`)
    else if (res.status === 'updated') onAssignStatus(`Set ${label}`)
  }

  useEffect(() => {
    const keyHandler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') { if (submenu) setSubmenu(null); else onClose() }
    }
    document.addEventListener('keydown', keyHandler)
    return () => document.removeEventListener('keydown', keyHandler)
  }, [onClose, submenu])

  const menuWidth = 200
  const subMenuWidth = 150
  const flipSub = x + menuWidth + subMenuWidth > window.innerWidth
  const menuStyle: React.CSSProperties = {
    position: 'fixed',
    left: flipSub ? Math.max(0, x - menuWidth) : Math.min(x, window.innerWidth - menuWidth),
    top: Math.min(y, window.innerHeight - 300),
    zIndex: 10000,
  }
  const subPos = flipSub ? 'right-full mr-1' : 'left-full ml-1'

  type MenuItem = { label: string; icon: string; action: () => void }
  const items: MenuItem[] = []
  if (capabilities.can_focus) {
    items.push({ label: 'Go to terminal', icon: '⌨', action: () => { focusAgent(agentId); onClose() } })
  }
  items.push({ label: 'Check messages', icon: '💬', action: () => { checkAgentMessages(agent.session_id); onClose() } })
  items.push({ label: 'Rename', icon: '✏️', action: onRename })
  items.push({ label: 'Change avatar', icon: '🔄', action: onChangeSprite })
  if (capabilities.can_clone) {
    items.push({ label: 'Spawn clone', icon: '🧬', action: () => { spawnClone(agent.pokegent_id || agent.session_id); onClose() } })
  }
  if (onCollapse) {
    items.push({ label: 'Collapse', icon: '📌', action: () => { onCollapse(); onClose() } })
  }

  if (showSwitchModal) {
    return <SwitchRuntimeModal agent={agent} onClose={() => { setShowSwitchModal(false); onClose() }} onAssignStatus={onAssignStatus} />
  }

  return (
    <>
      <div
        className="fixed inset-0"
        style={{ zIndex: 9999 }}
        onClick={onClose}
        onContextMenu={(e) => { e.preventDefault(); onClose() }}
      />
      <div style={menuStyle}>
        <div className="gba-dropdown-panel py-1 min-w-[190px]">
        {items.map((item) => (
          <button
            key={item.label}
            onClick={(e) => { e.stopPropagation(); item.action() }}
            className="w-full text-left px-3 py-1.5 text-s theme-font-display theme-text-primary theme-bg-dropdown-hover theme-hover-text-primary flex items-center gap-2 transition-colors pixel-shadow"
          >
            <span className="w-4 text-center">{item.icon}</span>
            {item.label}
          </button>
        ))}

        {/* Role/Project assignment */}
        {(roles && roles.length > 0 || projects && projects.length > 0) && (
          <>
            <div className="border-t theme-border-subtle my-1" />
            <div className="px-3 py-1.5 text-m theme-font-mono theme-text-warning leading-snug">
              Role/project changes relaunch the agent to load the profile.
            </div>
            {roles && roles.length > 0 && (
              <div className="relative">
                <button
                  onClick={(e) => { e.stopPropagation(); setSubmenu(submenu === 'role' ? null : 'role') }}
                  className="w-full text-left px-3 py-1.5 text-s theme-font-display theme-text-primary theme-bg-dropdown-hover theme-hover-text-primary flex items-center gap-2 transition-colors pixel-shadow"
                >
                  <span className="w-4 text-center">🎭</span>
                  {agent.role ? `Role: ${agent.role}` : 'Assign role'}
                  <span className="ml-auto theme-text-faint">▸</span>
                </button>
                {submenu === 'role' && (
                  <div className={`absolute top-0 ${subPos} gba-dropdown-panel py-1 min-w-[140px]`}>
                    {agent.role && (
                      <button
                        onClick={async (e) => { e.stopPropagation(); const res = await assignRole(agentId, ''); showStatus(res, 'no role'); onClose() }}
                        className="w-full text-left px-3 py-1.5 text-s theme-font-display theme-text-faint theme-bg-dropdown-hover transition-colors pixel-shadow italic"
                      >
                        None
                      </button>
                    )}
                    {roles.map(r => (
                      <button
                        key={r.name}
                        onClick={async (e) => { e.stopPropagation(); const res = await assignRole(agentId, r.name); showStatus(res, r.title); onClose() }}
                        className={`w-full text-left px-3 py-1.5 text-s theme-font-display transition-colors pixel-shadow flex items-center gap-1.5 ${agent.role === r.name ? 'text-accent-yellow theme-bg-dropdown-active' : 'theme-text-primary theme-bg-dropdown-hover'}`}
                      >
                        <span>{r.emoji}</span>
                        <span>{r.title}</span>
                      </button>
                    ))}
                  </div>
                )}
              </div>
            )}
            {projects && projects.length > 0 && (
              <div className="relative">
                <button
                  onClick={(e) => { e.stopPropagation(); setSubmenu(submenu === 'project' ? null : 'project') }}
                  className="w-full text-left px-3 py-1.5 text-s theme-font-display theme-text-primary theme-bg-dropdown-hover theme-hover-text-primary flex items-center gap-2 transition-colors pixel-shadow"
                >
                  <span className="w-4 text-center">📁</span>
                  {agent.project ? `Project: ${agent.project}` : 'Assign project'}
                  <span className="ml-auto theme-text-faint">▸</span>
                </button>
                {submenu === 'project' && (
                  <div className={`absolute top-0 ${subPos} gba-dropdown-panel py-1 min-w-[140px]`}>
                    {agent.project && (
                      <button
                        onClick={async (e) => { e.stopPropagation(); const res = await assignProject(agentId, ''); showStatus(res, 'no project'); onClose() }}
                        className="w-full text-left px-3 py-1.5 text-s theme-font-display theme-text-faint theme-bg-dropdown-hover transition-colors pixel-shadow italic"
                      >
                        None
                      </button>
                    )}
                    {projects.map(p => (
                      <button
                        key={p.name}
                        onClick={async (e) => { e.stopPropagation(); const res = await assignProject(agentId, p.name); showStatus(res, p.title); onClose() }}
                        className={`w-full text-left px-3 py-1.5 text-s theme-font-display transition-colors pixel-shadow flex items-center gap-1.5 ${agent.project === p.name ? 'text-accent-yellow theme-bg-dropdown-active' : 'theme-text-primary theme-bg-dropdown-hover'}`}
                      >
                        <span className="w-2 h-2 rounded-sm shrink-0" style={{ background: `rgb(${p.color[0]},${p.color[1]},${p.color[2]})` }} />
                        <span>{p.title}</span>
                      </button>
                    ))}
                  </div>
                )}
              </div>
            )}
          </>
        )}

        {/* Group assignment */}
        <div className="relative">
          <button
            onClick={(e) => { e.stopPropagation(); setSubmenu(submenu === 'group' ? null : 'group') }}
            className="w-full text-left px-3 py-1.5 text-s theme-font-display theme-text-primary theme-bg-dropdown-hover theme-hover-text-primary flex items-center gap-2 transition-colors pixel-shadow"
          >
            <span className="w-4 text-center">📦</span>
            {agent.task_group ? `Group: ${agent.task_group}` : 'Assign group'}
            <span className="ml-auto theme-text-faint">▸</span>
          </button>
          {submenu === 'group' && (
            <div className={`absolute top-0 ${subPos} gba-dropdown-panel py-1 min-w-[140px]`}>
              {agent.task_group && (
                <button
                  onClick={async (e) => { e.stopPropagation(); await assignTaskGroup(agentId, ''); onAssignStatus?.('Removed from group'); onClose() }}
                  className="w-full text-left px-3 py-1.5 text-s theme-font-display theme-text-faint theme-bg-dropdown-hover transition-colors pixel-shadow italic"
                >
                  None
                </button>
              )}
              {(existingGroups || []).map(g => (
                <button
                  key={g}
                  onClick={async (e) => { e.stopPropagation(); await assignTaskGroup(agentId, g); onAssignStatus?.(`Group: ${g}`); onClose() }}
                  className={`w-full text-left px-3 py-1.5 text-s theme-font-display transition-colors pixel-shadow ${agent.task_group === g ? 'text-accent-yellow theme-bg-dropdown-active' : 'theme-text-primary theme-bg-dropdown-hover'}`}
                >
                  {g}
                </button>
              ))}
              <div className="border-t theme-border-subtle my-1" />
              <form
                className="px-2 py-1 flex gap-1"
                onClick={(e) => e.stopPropagation()}
                onSubmit={async (e) => {
                  e.preventDefault()
                  e.stopPropagation()
                  const name = newGroupName.trim()
                  if (!name) return
                  await assignTaskGroup(agentId, name)
                  onAssignStatus?.(`Group: ${name}`)
                  onClose()
                }}
              >
                <input
                  ref={newGroupRef}
                  value={newGroupName}
                  onChange={(e) => setNewGroupName(e.target.value)}
                  onKeyDown={(e) => e.stopPropagation()}
                  placeholder="New group..."
                  className="flex-1 theme-bg-panel-muted border theme-border-subtle rounded px-1.5 py-0.5 text-s theme-font-display theme-text-primary outline-none focus-theme-border-subtle"
                  style={{ minWidth: 0 }}
                  autoFocus
                />
                <button
                  type="submit"
                  className="text-s theme-font-display theme-text-muted theme-hover-text-primary px-1"
                >+</button>
              </form>
            </div>
          )}
        </div>

        <div className="border-t theme-border-subtle my-1" />
        <button
          onClick={(e) => { e.stopPropagation(); setShowSwitchModal(true) }}
          className="w-full text-left px-3 py-1.5 text-s theme-font-display theme-text-primary theme-bg-dropdown-hover flex items-center gap-2 transition-colors pixel-shadow"
        >
          <span className="w-4 text-center">⇄</span>
          Switch runtime
        </button>
        <div className="border-t theme-border-subtle my-1" />
        <button
          onClick={(e) => { e.stopPropagation(); shutdownAgent(agentId); onClose() }}
          className="w-full text-left px-3 py-1.5 text-s theme-font-display text-accent-red theme-bg-dropdown-hover flex items-center gap-2 transition-colors pixel-shadow"
        >
          <span className="w-4 text-center">⏻</span>
          Release
        </button>
        </div>
      </div>
    </>
  )
}
