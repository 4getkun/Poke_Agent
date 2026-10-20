import { useState, useEffect, useMemo, useRef, Component, ErrorInfo, ReactNode, type MouseEvent as ReactMouseEvent } from 'react'
import { createPortal } from 'react-dom'
import { useSSE } from './hooks/useSSE'
import { useGridEngine } from './hooks/useGridEngine'
import { fetchSessions, focusAgent, fetchProfiles, fetchProjectList, fetchRoleList, shutdownAgent, dismissEphemeral, assignTaskGroup, fetchSetupStatus, completeOnboarding, renameAgent, setSprite, ProfileInfo, ProjectInfo, RoleInfo, SetupStatus } from './api'
import { AgentState, AgentMessage, stableId } from './types'
import type { Entry } from './types/chat'
import { AgentCard, GROUP_COLORS } from './components/AgentCard'
import { GridContainer } from './components/GridContainer'
import { GroupContainer } from './components/GroupContainer'
import { SessionBrowser } from './components/SessionBrowser'
import { TownView } from './components/TownView'
import { ChatPanel } from './components/ChatPanel'
import { useChatWebSockets } from './hooks/useChatWebSocket'
import { hashString } from './components/CreatureIcon'
import { useMessageAnimations, DeliveryOverlay } from './components/MessageAnimations'
import { useSettings } from './hooks/useSettings'
import { SettingsPanel } from './components/settings/SettingsPanel'
import { OnboardingModal } from './components/onboarding/OnboardingModal'
import { LaunchModal } from './components/LaunchModal'
import { PokeballAnimationLayer, usePokeballAnimations } from './components/PokeballAnimation'
import { AgentMenu } from './components/AgentMenu'
import { PixelSprite } from './components/PixelSprite'
import { SpritePicker } from './components/SpritePicker'
import { capsFor, useRuntimeCapabilities } from './utils/runtimes'
import { formatToolActivityText } from './utils/toolAdapters'

export class DashboardErrorBoundary extends Component<{children: ReactNode}, {error: Error | null}> {
  state = { error: null as Error | null }
  static getDerivedStateFromError(error: Error) { return { error } }
  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('Dashboard crash:', error, info.componentStack)
  }
  render() {
    if (this.state.error) {
      return (
        <div style={{ padding: 20, color: 'var(--theme-accent-red)', background: 'var(--theme-app-bg)', height: '100vh', fontFamily: 'var(--theme-font-mono)', overflow: 'auto' }}>
          <h2>Dashboard crashed</h2>
          <pre style={{ whiteSpace: 'pre-wrap', fontSize: 'var(--theme-type-l)' }}>{this.state.error.message}{'\n'}{this.state.error.stack}</pre>
          <button onClick={() => { this.setState({ error: null }); window.location.reload() }}
            style={{ marginTop: 16, padding: '8px 16px', background: '#333', color: '#fff', border: 'none', cursor: 'pointer' }}>
            Reload
          </button>
        </div>
      )
    }
    return this.props.children
  }
}


function entryTime(ts?: number): string {
  return new Date(ts || Date.now()).toLocaleTimeString([], { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function livePreviewFromChat(agent: AgentState, entries: Entry[], wsBusy?: boolean, busySince?: string | null): AgentState {
  if (!wsBusy) return agent
  let lastUserIdx = -1
  for (let i = entries.length - 1; i >= 0; i--) {
    if (entries[i].kind === 'user') { lastUserIdx = i; break }
  }
  const currentTurn = lastUserIdx >= 0 ? entries.slice(lastUserIdx + 1) : entries
  const prompt = lastUserIdx >= 0 && entries[lastUserIdx].kind === 'user'
    ? (entries[lastUserIdx] as Extract<Entry, { kind: 'user' }>).text
    : agent.user_prompt
  const feed = currentTurn.flatMap(e => {
    if (e.kind === 'tool') return [{ time: entryTime(e.ts), type: 'tool', text: formatToolActivityText(e.data) }]
    if (e.kind === 'assistant') {
      const out: { time: string; type: string; text: string }[] = []
      if (e.thoughts?.trim()) out.push({ time: entryTime(e.ts), type: 'thinking', text: e.thoughts.trim().slice(-280) })
      if (e.text?.trim()) out.push({ time: entryTime(e.ts), type: 'text', text: e.text.trim().slice(-280) })
      return out
    }
    if (e.kind === 'system') return [{ time: entryTime(e.ts), type: 'thinking', text: e.text }]
    return []
  }).slice(-8)
  const last = feed[feed.length - 1]
  return {
    ...agent,
    state: 'busy',
    busy_since: busySince || agent.busy_since,
    user_prompt: prompt,
    last_summary: feed.length ? '' : '',
    last_trace: '',
    activity_feed: feed,
    card_preview: {
      state: 'busy',
      phase: last ? (last.type === 'tool' ? 'tool' : 'streaming') : 'thinking',
      prompt,
      text: feed.length ? undefined : 'Working...',
      feed,
      updated_at: new Date().toISOString(),
    },
  }
}

const STATUS_PILLS: Record<string, { label: string; bg: string; pulse?: boolean }> = {
  idle:        { label: 'SLP',  bg: '#788890' },
  done:        { label: 'OK',   bg: '#58a868' },
  busy:        { label: 'ATK',  bg: '#e87848', pulse: true },
  needs_input: { label: 'WAIT', bg: '#d84848', pulse: true },
  error:       { label: 'PSN',  bg: '#a858a8', pulse: true },
  starting:    { label: 'NEW',  bg: '#5898c8', pulse: true },
}

function formatInactive(lastUpdated?: string): string {
  if (!lastUpdated) return ''
  const secs = Math.max(0, (Date.now() - new Date(lastUpdated).getTime()) / 1000)
  if (secs < 60) return `${Math.floor(secs)}s`
  if (secs < 3600) return `${Math.floor(secs / 60)}m`
  return `${Math.floor(secs / 3600)}h${Math.floor((secs % 3600) / 60)}m`
}

function CollapsedBubble({ agent, sprite, onExpand, bubbleRef, onMenu }: {
  agent: AgentState; sprite: string; onExpand: () => void
  bubbleRef?: (el: HTMLDivElement | null) => void
  onMenu?: (e: ReactMouseEvent) => void
}) {
  const [hovered, setHovered] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  const hoverTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const st = STATUS_PILLS[agent.state] || STATUS_PILLS.idle
  const dur = formatInactive(agent.last_updated)

  return (
    <div
      ref={(el) => { ref.current = el; bubbleRef?.(el) }}
      className="relative"
      onMouseEnter={() => { hoverTimer.current = setTimeout(() => setHovered(true), 300) }}
      onMouseLeave={() => { if (hoverTimer.current) clearTimeout(hoverTimer.current); setHovered(false) }}
      onContextMenu={(e) => {
        e.preventDefault()
        e.stopPropagation()
        setHovered(false)
        onMenu?.(e)
      }}
    >
      <button
        onClick={() => { setHovered(false); onExpand() }}
        onContextMenu={(e) => {
          e.preventDefault()
          e.stopPropagation()
          setHovered(false)
          onMenu?.(e)
        }}
        className="relative flex items-center justify-center group" style={{ width: 32, height: 32 }}
        title={`${agent.display_name} — click to expand`}
      >
        <img
          src="/sprites/pokeball.png"
          alt=""
          className="absolute opacity-50 group-hover:opacity-80 transition-opacity"
          style={{ imageRendering: 'pixelated', width: 32, height: 32 }}
        />
        <PixelSprite
          sprite={sprite}
          alt=""
          scale={0.8}
          className="relative z-10"
          style={{ filter: 'grayscale(0.4) brightness(0.8)', transition: 'filter 0.15s' }}
        />
      </button>

      {/* Hover preview card */}
      {hovered && (
        <div className="absolute top-full left-1/2 -translate-x-1/2 mt-2 z-50 pointer-events-none"
          style={{ animation: 'fadeIn 0.15s ease' }}
        >
          <div className="gba-card rounded-lg px-3 py-2.5 border theme-border-subtle min-w-[180px] max-w-[220px]"
            style={{ boxShadow: '0 4px 20px rgba(0,0,0,0.6)' }}
          >
            <div className="flex items-center gap-2 mb-1.5">
              <div className="shrink-0 flex items-center justify-center" style={{ width: 28, height: 28 }}><PixelSprite sprite={sprite} alt="" scale={0.875} /></div>
              <div className="flex-1 min-w-0">
                <div className="text-s theme-font-display theme-text-primary pixel-shadow truncate">{agent.display_name}</div>
                <div className="flex items-center gap-1.5 mt-0.5">
                  <span
                    className={`text-xs theme-font-display theme-text-primary px-1 py-px rounded-full leading-none ${st.pulse ? 'animate-pulse-soft' : ''}`}
                    style={{ backgroundColor: st.bg, textShadow: '1px 1px 0 rgba(0,0,0,0.4)' }}
                  >{st.label}</span>
                  {dur && <span className="text-xs theme-font-mono theme-text-faint">{dur}</span>}
                </div>
              </div>
            </div>
            {agent.detail && (
              <div className="text-s theme-font-mono theme-text-muted truncate mt-1 border-t theme-border-subtle pt-1">{agent.detail}</div>
            )}
            {agent.user_prompt && (
              <div className="text-s theme-font-mono theme-text-faint truncate mt-1">{agent.user_prompt.slice(0, 80)}</div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

function CollapsedGroupBubble({ name, members, sprite, onExpand, bubbleRef }: {
  name: string; members: AgentState[]; sprite: string; onExpand: () => void
  bubbleRef?: (el: HTMLDivElement | null) => void
}) {
  const [hovered, setHovered] = useState(false)
  const hoverTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const ref = useRef<HTMLDivElement>(null)
  const idx = Math.abs(hashString(name)) % GROUP_COLORS.length
  const [r, g, b] = GROUP_COLORS[idx]

  return (
    <div
      ref={(el) => { ref.current = el; bubbleRef?.(el) }}
      className="relative"
      onMouseEnter={() => { hoverTimer.current = setTimeout(() => setHovered(true), 300) }}
      onMouseLeave={() => { if (hoverTimer.current) clearTimeout(hoverTimer.current); setHovered(false) }}
    >
      <button
        onClick={() => { setHovered(false); onExpand() }}
        className="relative flex items-center justify-center group" style={{ width: 32, height: 32 }}
        title={`${name} — ${members.length} agents — click to open`}
      >
        <img
          src="/sprites/pokeball.png"
          alt=""
          className="absolute opacity-50 group-hover:opacity-80 transition-opacity"
          style={{ imageRendering: 'pixelated', width: 32, height: 32 }}
        />
        <PixelSprite
          sprite={sprite}
          alt=""
          scale={0.8}
          className="relative z-10"
          style={{ filter: 'grayscale(0.4) brightness(0.8)', transition: 'filter 0.15s' }}
        />
        {/* Count badge */}
        <span
          className="absolute -bottom-1 -right-1.5 z-20 text-xs theme-font-display theme-text-primary rounded-full px-0.5 leading-tight"
          style={{ background: `rgb(${r},${g},${b})`, textShadow: '1px 1px 0 rgba(0,0,0,0.5)' }}
        >{members.length}</span>
      </button>

      {/* Hover tooltip */}
      {hovered && (
        <div className="absolute top-full left-1/2 -translate-x-1/2 mt-2 z-50 pointer-events-none"
          style={{ animation: 'fadeIn 0.15s ease' }}
        >
          <div className="gba-card rounded-lg px-3 py-2 border theme-border-subtle min-w-[140px] max-w-[200px]"
            style={{ boxShadow: '0 4px 20px rgba(0,0,0,0.6)' }}
          >
            <div className="text-s theme-font-display pixel-shadow uppercase mb-1" style={{ color: `rgb(${r},${g},${b})` }}>{name}</div>
            {members.map(m => {
              const st = STATUS_PILLS[m.state] || STATUS_PILLS.idle
              return (
                <div key={m.session_id} className="flex items-center gap-1.5 py-0.5">
                  <span
                    className={`text-xs theme-font-display theme-text-primary px-1 py-px rounded-full leading-none ${st.pulse ? 'animate-pulse-soft' : ''}`}
                    style={{ backgroundColor: st.bg, textShadow: '1px 1px 0 rgba(0,0,0,0.4)' }}
                  >{st.label}</span>
                  <span className="text-s theme-font-display theme-text-muted truncate">{m.display_name || m.profile_name}</span>
                </div>
              )
            })}
          </div>
        </div>
      )}
    </div>
  )
}

export default function App() {
  const { settings, setSettings, reset: resetSettings, DEFAULTS } = useSettings()
  const { agents: sseAgents, newMessage, connected } = useSSE()
  const [agents, setAgents] = useState<AgentState[]>([])
  const [showBrowser, setShowBrowser] = useState(false)
  const [showSettings, setShowSettings] = useState(false)
  const [showTownEditor, setShowTownEditor] = useState(false)
  const [setupStatus, setSetupStatus] = useState<SetupStatus | null>(null)
  const [showOnboarding, setShowOnboarding] = useState(false)
  const [serverRestarting, setServerRestarting] = useState(false)
  const [chatAgentId, setChatAgentId] = useState<string | null>(() => {
    try { return localStorage.getItem('pokegents-chat-agent') || null } catch { return null }
  })
  const [shortcutOverlay, setShortcutOverlay] = useState(false)
  const [showLauncher, setShowLauncher] = useState(false)
  const [menuAgent, setMenuAgent] = useState<AgentState | null>(null)
  const [menuPos, setMenuPos] = useState({ x: 0, y: 0 })
  const [spritePickerAgent, setSpritePickerAgent] = useState<AgentState | null>(null)
  const allCaps = useRuntimeCapabilities()
  const chatConnections = useChatWebSockets(agents, chatAgentId)
  useEffect(() => {
    try {
      if (chatAgentId) localStorage.setItem('pokegents-chat-agent', chatAgentId)
      else localStorage.removeItem('pokegents-chat-agent')
    } catch { /* ignore */ }
  }, [chatAgentId])
  // Auto-open ChatPanel when an agent is migrated to chat — fired by AgentCard.
  // Also fires on re-click of the same chat card (the card's inner output area
  // stopPropagates, so the GridCell's onClick never reaches App — everything
  // routes through this custom event instead).
  const chatAgentIdRef = useRef(chatAgentId)
  chatAgentIdRef.current = chatAgentId
  useEffect(() => {
    const restartHandler = () => setServerRestarting(true)
    window.addEventListener('server-restart-requested', restartHandler)
    return () => window.removeEventListener('server-restart-requested', restartHandler)
  }, [])

  useEffect(() => {
    if (connected) setServerRestarting(false)
  }, [connected])

  useEffect(() => {
    const handler = (e: Event) => {
      const detail = (e as CustomEvent).detail as { pokegentId?: string }
      if (!detail?.pokegentId) return
      if (chatAgentIdRef.current === detail.pokegentId) {
        // Same agent re-clicked — ping the panel to flash.
        window.dispatchEvent(new Event('chat-panel-ping'))
      } else {
        setChatAgentId(detail.pokegentId)
      }
    }
    window.addEventListener('open-chat-panel', handler)
    return () => window.removeEventListener('open-chat-panel', handler)
  }, [])
  // Auto-close the chat panel when the targeted agent is no longer chat-backed.
  // Covers two cases: (1) user migrated away from chat → we should close the
  // panel; (2) localStorage has a stale chatAgentId from a previous session
  // pointing at an iterm2 agent → don't render an SSE-subscribed panel that
  // 404s. We only clear AFTER the agents list has loaded so a transient
  // empty agentMap on first paint doesn't nuke a valid chat panel.
  useEffect(() => {
    if (!chatAgentId || agents.length === 0) return
    const target = agents.find(a =>
      (a.pokegent_id && a.pokegent_id === chatAgentId) ||
      a.session_id === chatAgentId,
    )
    if (target && target.interface !== 'chat') setChatAgentId(null)
  }, [chatAgentId, agents])
  // Resizable chat panel — drag the divider between grid and panel to reflow.
  // Persisted to localStorage so the user's preferred width survives reloads.
  const [chatPanelWidth, setChatPanelWidth] = useState<number>(() => {
    try {
      const v = parseInt(localStorage.getItem('pokegents-chat-panel-width') || '', 10)
      if (Number.isFinite(v) && v >= 280 && v <= 1400) return v
    } catch { /* ignore */ }
    return 520
  })
  useEffect(() => {
    try { localStorage.setItem('pokegents-chat-panel-width', String(chatPanelWidth)) } catch { /* ignore */ }
  }, [chatPanelWidth])
  const dragWidthRef = useRef<{ startX: number; startWidth: number } | null>(null)
  const onChatDividerPointerDown = (e: React.PointerEvent) => {
    e.preventDefault()
    dragWidthRef.current = { startX: e.clientX, startWidth: chatPanelWidth }
    const onMove = (ev: PointerEvent) => {
      const ref = dragWidthRef.current
      if (!ref) return
      // Drag right → divider moves right → panel shrinks (its left edge moves right).
      // Convention: width = startWidth - dx (so dragging RIGHT makes panel narrower).
      const dx = ev.clientX - ref.startX
      const next = Math.max(280, Math.min(window.innerWidth - 200, ref.startWidth - dx))
      setChatPanelWidth(next)
    }
    const onUp = () => {
      dragWidthRef.current = null
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
    }
    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
  }
  // CMD-hold overlay: shows numbered shortcuts on agent cards.
  // CMD+1..5 switches to the agent at that visual grid position.
  const gridIdsRef = useRef<string[]>([])
  const agentMapRef = useRef<Record<string, AgentState>>({})
  useEffect(() => {
    const isEditable = (target: EventTarget | null) => {
      const el = target as HTMLElement | null
      return !!el && (
        el.tagName === 'INPUT' ||
        el.tagName === 'TEXTAREA' ||
        el.isContentEditable
      )
    }
    const onDown = (e: KeyboardEvent) => {
      const hasSelection = !!window.getSelection()?.toString()
      // Do not rerender the dashboard while the user is copying selected text.
      // The shortcut-number overlay is only useful for navigation; showing it
      // on Cmd/Cmd+C can clear the browser selection in the chat transcript.
      if (hasSelection) return

      // CMD+1..5 → open agent at that visual grid position.
      if (e.metaKey && e.key >= '1' && e.key <= '5') {
        e.preventDefault()
        const idx = parseInt(e.key) - 1
        const ids = gridIdsRef.current
        const agentIds = ids.filter(id => !id.startsWith('town') && !id.startsWith('group:'))
        const targetId = agentIds[idx]
        if (targetId) {
          const agent = agentMapRef.current[targetId]
          if (agent?.interface === 'chat') {
            setChatAgentId(targetId)
          } else if (agent) {
            focusAgent(targetId)
          }
        }
        return
      }

      // CMD+N → launcher/new agent.
      if (e.metaKey && e.key.toLowerCase() === 'n') {
        e.preventDefault()
        setShowLauncher(true)
        return
      }

      // CMD+P → PC box.
      if (e.metaKey && e.key.toLowerCase() === 'p') {
        e.preventDefault()
        setShowBrowser(true)
        return
      }

      // CMD+F is handled by ChatPanel when one is open; prevent browser find
      // so search is scoped to the selected agent transcript.
      if (e.metaKey && e.key.toLowerCase() === 'f') {
        if (chatAgentIdRef.current) e.preventDefault()
        return
      }

      // Escape → settings, unless user is typing or another modal/menu is open.
      if (e.key === 'Escape' && !isEditable(e.target)) {
        const anyOverlayOpen =
          showBrowser || showSettings || showLauncher || showOnboarding ||
          showTownEditor || !!menuAgent || !!spritePickerAgent
        if (!anyOverlayOpen) {
          e.preventDefault()
          setShowSettings(true)
        }
      }
    }
    const onUp = (e: KeyboardEvent) => {
      if (e.key === 'Meta' || !e.metaKey) setShortcutOverlay(false)
    }
    const onBlur = () => setShortcutOverlay(false)
    window.addEventListener('keydown', onDown)
    window.addEventListener('keyup', onUp)
    window.addEventListener('blur', onBlur)
    return () => {
      window.removeEventListener('keydown', onDown)
      window.removeEventListener('keyup', onUp)
      window.removeEventListener('blur', onBlur)
    }
  }, [showBrowser, showSettings, showLauncher, showOnboarding, showTownEditor, menuAgent, spritePickerAgent])
  const [messages, setMessages] = useState<AgentMessage[]>([])
  const [collapsedIds, setCollapsedIds] = useState<Set<string>>(() => new Set())
  const collapsedInitialized = useRef(false)
  const [groupViewModes, setGroupViewModes] = useState<Record<string, 'collapsed' | 'single' | 'expanded'>>(() => {
    try {
      const raw = JSON.parse(localStorage.getItem('pokegents-group-view-modes') || '{}')
      const out: Record<string, 'collapsed' | 'single' | 'expanded'> = {}
      for (const [k, v] of Object.entries(raw)) {
        out[k] = v === 'collapsed' ? 'collapsed' : v === 'expanded' ? 'expanded' : v === 'single' ? 'single' : 'expanded'
      }
      return out
    } catch { return {} }
  })
  const [groupPageIndex, setGroupPageIndex] = useState<Record<string, number>>({})
  const { animations, triggerRecall, triggerDeploy, onComplete: onAnimComplete } = usePokeballAnimations()
  const bubbleRefs = useRef<Map<string, HTMLDivElement>>(new Map())
  const [profiles, setProfiles] = useState<ProfileInfo[]>([])
  const [projects, setProjects] = useState<ProjectInfo[]>([])
  const [roles, setRoles] = useState<RoleInfo[]>([])
  const [gridSliderDragging, setGridSliderDragging] = useState(false)

  // Restore from localStorage AFTER first agents load (so we know which IDs are valid)
  useEffect(() => {
    if (collapsedInitialized.current || agents.length === 0) return
    collapsedInitialized.current = true
    try {
      const saved: string[] = JSON.parse(localStorage.getItem('pokegents-collapsed') || '[]')
      const validIds = new Set(agents.map(a => stableId(a)))
      const restored = saved.filter(id => validIds.has(id))
      if (restored.length > 0) {
        setCollapsedIds(new Set(restored))
      }
    } catch { /* ignore */ }
  }, [agents])

  // Persist collapsed state (skip the initial empty set)
  useEffect(() => {
    if (!collapsedInitialized.current) return
    localStorage.setItem('pokegents-collapsed', JSON.stringify([...collapsedIds]))
  }, [collapsedIds])

  // Persist group view modes
  useEffect(() => {
    localStorage.setItem('pokegents-group-view-modes', JSON.stringify(groupViewModes))
  }, [groupViewModes])

  // Auto-collapse new groups on first appearance (seed existing groups on first load)
  const knownGroupsRef = useRef<Set<string>>(new Set())
  const knownGroupsInitialized = useRef(false)
  useEffect(() => {
    const currentGroups = new Set(agents.filter(a => a.task_group).map(a => a.task_group!))
    if (!knownGroupsInitialized.current && currentGroups.size > 0) {
      knownGroupsInitialized.current = true
      knownGroupsRef.current = currentGroups
      return // don't treat existing groups as new on first load
    }
    for (const g of currentGroups) {
      if (!knownGroupsRef.current.has(g)) {
        knownGroupsRef.current.add(g)
        setGroupViewModes(prev => ({ ...prev, [g]: 'expanded' }))
      }
    }
  }, [agents])

  // Track manually expanded agents so auto-collapse doesn't immediately re-collapse them
  const manualExpandRef = useRef<Set<string>>(
    (() => { try { return new Set<string>(JSON.parse(localStorage.getItem('pokegents-manual-expand') || '[]')) } catch { return new Set<string>() } })()
  )

  const persistManualExpand = () => {
    localStorage.setItem('pokegents-manual-expand', JSON.stringify([...manualExpandRef.current]))
  }

  const cardRefs = useRef<Map<string, HTMLDivElement>>(new Map())
  // useMessageAnimations moved below getSpriteForId (needs it as dependency)
  // Layout computed after gridIds (below)
  // showHeader computed after layout (below)

  const refreshSetupStatus = async () => {
    const status = await fetchSetupStatus()
    setSetupStatus(status)
    return status
  }

  const closeOnboarding = async () => {
    setShowOnboarding(false)
    try {
      const status = await completeOnboarding()
      if (status) setSetupStatus(status)
    } catch {
      // Do not trap the user in onboarding if persistence fails; Settings can reopen it.
    }
  }

  useEffect(() => {
    fetchSessions().then(setAgents).catch(() => {})
    fetchProfiles().then(p => setProfiles(p.sort((a, b) => a.title.localeCompare(b.title)))).catch(() => {})
    fetchProjectList().then(p => setProjects(p.sort((a, b) => a.title.localeCompare(b.title)))).catch(() => {})
    fetchRoleList().then(r => setRoles(r.sort((a, b) => a.title.localeCompare(b.title)))).catch(() => {})
    refreshSetupStatus().then(status => {
      if (status && status.onboarding_complete === false) setShowOnboarding(true)
    }).catch(() => {})
    // Fallback session poll for cases where SSE reconnects late.
    const fallbackPoll = setInterval(() => {
      fetchSessions().then(fresh => {
        setAgents(prev => {
          if (fresh.length !== prev.length) return fresh
          for (let i = 0; i < fresh.length; i++) {
            if (fresh[i].session_id !== prev[i].session_id ||
                fresh[i].state !== prev[i].state ||
                fresh[i].detail !== prev[i].detail ||
                fresh[i].user_prompt !== prev[i].user_prompt ||
                fresh[i].display_name !== prev[i].display_name) return fresh
          }
          return prev
        })
      }).catch(() => {})
    }, 10000)
    return () => { clearInterval(fallbackPoll) }
  }, [])

  useEffect(() => {
    if (sseAgents.length > 0) setAgents(sseAgents)
  }, [sseAgents])

  // Instantly append new messages from SSE (no poll delay)
  useEffect(() => {
    if (newMessage) {
      setMessages(prev => prev.some(m => m.id === newMessage.id) ? prev : [...prev, newMessage])
    }
  }, [newMessage])

  // Split agents into grouped and ungrouped
  const { grouped, ungrouped } = useMemo(() => {
    const grouped: Record<string, AgentState[]> = {}
    const ungrouped: AgentState[] = []
    agents.forEach(a => {
      if (a.task_group) {
        (grouped[a.task_group] ??= []).push(a)
      } else {
        ungrouped.push(a)
      }
    })
    // Sort ephemeral agents to appear immediately after their parent.
    // Helper: reorder a list so ephemerals follow their parent
    function sortWithEphemerals(list: AgentState[]): AgentState[] {
      const result: AgentState[] = []
      const ephByParent: Record<string, AgentState[]> = {}
      for (const a of list) {
        if (a.ephemeral && a.parent_session_id) {
          (ephByParent[a.parent_session_id] ??= []).push(a)
        }
      }
      for (const a of list) {
        if (a.ephemeral) continue
        result.push(a)
        const children = ephByParent[a.session_id] || ephByParent[a.pokegent_id || ''] || []
        result.push(...children)
      }
      // Append orphaned ephemerals at the end
      for (const a of list) {
        if (a.ephemeral && !result.includes(a)) result.push(a)
      }
      return result
    }
    // Apply to both grouped and ungrouped
    for (const key of Object.keys(grouped)) {
      grouped[key] = sortWithEphemerals(grouped[key])
    }
    return { grouped, ungrouped: sortWithEphemerals(ungrouped) }
  }, [agents])

  // Ungrouped: filter collapsed for pokéball bubbles
  const visibleUngrouped = useMemo(() => ungrouped.filter(a => !collapsedIds.has(stableId(a))), [ungrouped, collapsedIds])
  const collapsedAgents = useMemo(() => ungrouped.filter(a => collapsedIds.has(stableId(a))), [ungrouped, collapsedIds])

  const existingGroupNames = useMemo(() => Object.keys(grouped).sort(), [grouped])

  // Collapsed group names (rendered as bubbles, not grid items)
  const collapsedGroupNames = useMemo(() =>
    Object.keys(grouped).filter(g => (groupViewModes[g] || 'collapsed') === 'collapsed'),
    [grouped, groupViewModes]
  )

  // Grid IDs: town (optional) + open group virtual IDs + ungrouped agent IDs.
  const gridIds = useMemo(() => {
    const ids: string[] = []
    if (settings.showTownCard) ids.push('town')
    for (const groupName of Object.keys(grouped).sort()) {
      if ((groupViewModes[groupName] || 'collapsed') !== 'collapsed') {
        ids.push(`group:${groupName}`)
      }
    }
    for (const a of visibleUngrouped) {
      ids.push(stableId(a))
    }
    return ids
  }, [grouped, visibleUngrouped, groupViewModes, settings.showTownCard])

  // Flow-grid density knobs. Cards are uniform 1×1 cells; cardsPerRow drives
  // CSS grid columns, cardsPerCol determines how many rows fit before scroll.
  // Drag reorders the array; the rest just falls out of CSS.
  const gridEngine = useGridEngine(gridIds, {
    cardsPerRow: settings.cardsPerRow ?? 3,
    cardsPerCol: settings.cardsPerCol ?? 3,
    gap: settings.cardGap ?? 8,
  })
  // Keep gridIdsRef in sync for the CMD+N keydown handler closure.
  // Use effectiveOrder (visual grid order) not gridIds (insertion order).
  const visualOrder = gridEngine.effectiveOrder
  useEffect(() => { gridIdsRef.current = visualOrder }, [visualOrder])

  // Show header when cells are tall enough for standard mode
  const showHeader = gridEngine.cellH >= 140
  const isCompact = gridEngine.cellH < 120
  const isServerConnecting = serverRestarting || (!connected && agents.length > 0)


  const getSpriteForId = useMemo(() => {
    const m: Record<string, string> = {}
    for (const a of agents) {
      if (a.sprite) {
        m[a.session_id] = a.sprite
        if (a.pokegent_id) m[a.pokegent_id] = a.sprite
      }
    }
    return (id: string) => m[id] || 'pokeball'
  }, [agents])

  const { deliveries, hiddenSprites, readingAgents, triggerTestDelivery } = useMessageAnimations(messages, cardRefs, getSpriteForId)

  // Keyboard shortcut: / to search
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === '/' && !showBrowser && !(e.target instanceof HTMLInputElement) && !(e.target instanceof HTMLTextAreaElement)) {
        e.preventDefault()
        setShowBrowser(true)
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [showBrowser])


  // Agent lookup for rendering cards in GridContainer — keyed by stableId (pokegent_id)
  // and also by session_id for backward compat with grid layouts saved before migration
  const agentMap = useMemo(() => {
    const m: Record<string, AgentState> = {}
    for (const a of agents) {
      m[stableId(a)] = a
      if (a.session_id !== stableId(a)) m[a.session_id] = a
    }
    return m
  }, [agents])
  const shortcutById = useMemo(() => {
    const out: Record<string, string> = {}
    visualOrder
      .filter(id => !id.startsWith('town') && !id.startsWith('group:') && !!agentMap[id])
      .slice(0, 5)
      .forEach((id, idx) => { out[id] = String(idx + 1) })
    return out
  }, [visualOrder, agentMap])
  // Keep agentMapRef in sync for the CMD+N keydown handler.
  useEffect(() => { agentMapRef.current = agentMap }, [agentMap])

  return (
    <>
    <div className="h-screen flex flex-col p-3 overflow-hidden relative z-10">
      {/* Header — always visible */}
      {(
        <div className="flex items-center shrink-0 mb-2">
          <div className="flex items-center gap-3 pl-2">
            <h1 className="text-m theme-font-display theme-text-primary pixel-shadow">POKéGENTS</h1>
            <span className="text-s theme-font-display theme-text-muted">
              {agents.length - collapsedAgents.length} active{collapsedAgents.length > 0 && <>, {collapsedAgents.length} idle</>}
              <span className={`ml-1.5 ${connected ? 'text-accent-green' : 'text-accent-red'}`}>●</span>
            </span>
          </div>
          {/* Collapsed pokeballs + group bubbles inline */}
          {(collapsedAgents.length > 0 || collapsedGroupNames.length > 0) && (
            <div className="flex items-center gap-1.5 ml-6">
          {/* Group bubbles */}
          {collapsedGroupNames.map(groupName => {
            const members = grouped[groupName] || []
            const coord = members.find(m => m.role?.toLowerCase().includes('coordinator'))
            const primaryAgent = coord || members[0]
            const sprite = primaryAgent ? getSpriteForId(primaryAgent.session_id) : 'pokeball'
            return (
              <CollapsedGroupBubble
                key={`group:${groupName}`}
                name={groupName}
                members={members}
                sprite={sprite}
                bubbleRef={(el) => {
                  if (el) bubbleRefs.current.set(`group:${groupName}`, el)
                  else bubbleRefs.current.delete(`group:${groupName}`)
                }}
                onExpand={() => {
                  const bubbleEl = bubbleRefs.current.get(`group:${groupName}`)
                  if (bubbleEl) {
                    const bubbleRect = bubbleEl.getBoundingClientRect()
                    const bubbleSource = {
                      x: bubbleRect.left + bubbleRect.width / 2,
                      y: bubbleRect.top + bubbleRect.height / 2,
                    }
                    const groupId = `group:${groupName}`
                    // Card lands at its current index in the flow order — or
                    // at the end if not yet placed. Either way it's a single
                    // 1×1 cell, so the geometry is just (col, row) from index.
                    const cpr = gridEngine.settings.cardsPerRow
                    const idx = gridEngine.indexOf(groupId)
                    const landingIdx = idx >= 0 ? idx : gridEngine.order.length
                    const targetRect = gridEngine.gridRectToPixels({
                      col: (landingIdx % cpr) + 1,
                      row: Math.floor(landingIdx / cpr) + 1,
                      w: 1, h: 1,
                    })
                    triggerDeploy(groupId, sprite, bubbleSource, targetRect, () => {},
                      () => setGroupViewModes(prev => ({ ...prev, [groupName]: 'single' })))
                  } else {
                    setGroupViewModes(prev => ({ ...prev, [groupName]: 'single' }))
                  }
                }}
              />
            )
          })}
          {collapsedAgents.map(agent => {
            const aid = stableId(agent)
            const sprite = getSpriteForId(aid)
            return (
              <CollapsedBubble
                key={aid}
                agent={agent}
                sprite={sprite}
                bubbleRef={(el) => {
                  if (el) bubbleRefs.current.set(aid, el)
                  else bubbleRefs.current.delete(aid)
                }}
                onMenu={(e) => {
                  setMenuAgent(agent)
                  setMenuPos({ x: e.clientX, y: e.clientY })
                }}
                onExpand={() => {
                  const bubbleEl = bubbleRefs.current.get(aid)
                  if (bubbleEl) {
                    const bubbleRect = bubbleEl.getBoundingClientRect()
                    const bubbleSource = {
                      x: bubbleRect.left + bubbleRect.width / 2,
                      y: bubbleRect.top + bubbleRect.height / 2,
                    }
                    // Card lands at its current index in the flow order — or
                    // at the end if not yet placed. Either way it's a 1×1.
                    const cpr = gridEngine.settings.cardsPerRow
                    const idx = gridEngine.indexOf(aid)
                    const landingIdx = idx >= 0 ? idx : gridEngine.order.length
                    const targetRect = gridEngine.gridRectToPixels({
                      col: (landingIdx % cpr) + 1,
                      row: Math.floor(landingIdx / cpr) + 1,
                      w: 1, h: 1,
                    })
                    const doExpand = () => {
                      manualExpandRef.current.add(aid); persistManualExpand()
                      setCollapsedIds(prev => { const next = new Set(prev); next.delete(aid); return next })
                    }
                    triggerDeploy(aid, sprite, bubbleSource, targetRect, () => {}, doExpand)
                  } else {
                    manualExpandRef.current.add(aid); persistManualExpand()
                    setCollapsedIds(prev => { const next = new Set(prev); next.delete(aid); return next })
                  }
                }}
              />
            )
          })}
            </div>
          )}
          <div className="flex-1" />
          <div className="flex items-center gap-2">
            <button
              onClick={() => setShowLauncher(true)}
              className="gba-button text-s theme-font-display px-3 py-1.5 transition-colors"
            >
              NEW AGENT
            </button>
            <button
              onClick={() => setShowBrowser(true)}
              className="gba-button text-s theme-font-display px-3 py-1.5 transition-colors"
            >
              PC BOX
            </button>
            <button
              onClick={() => setShowSettings(true)}
              className="gba-button text-s theme-font-display px-2.5 py-1.5 transition-colors"
              title="Settings"
            >
              SETTINGS
            </button>
          </div>
        </div>
      )}

      {/* Body: grid (left) + optional ChatPanel (right split-pane) */}
      <div className="flex-1 min-h-0 flex gap-3">
       <div className="flex-1 min-w-0 flex flex-col">
      {agents.length === 0 && !settings.showTownCard ? (
        <div className="flex-1 flex items-center justify-center">
          <div className="gba-dialog text-center px-8 py-6">
            <p className="text-m theme-font-display text-gba-dialog-border">No avatar in party</p>
            <p className="text-s theme-font-display text-gba-dialog-border/60 mt-3">
              Start with <span className="text-gba-card">pokegents &lt;profile&gt;</span>
            </p>
          </div>
        </div>
      ) : (
        <GridContainer
          engine={gridEngine}
          agentIds={gridIds}
          showHeader={showHeader}
          showGridLines={gridSliderDragging}
          expandedGroups={new Set(Object.entries(groupViewModes).filter(([, v]) => v === 'expanded').map(([k]) => k))}
          onDropOnGroup={async (agentId, groupName) => {
            await assignTaskGroup(agentId, groupName)
          }}
        >
          {(id, rect, cardMode) => {
            // Town card — special grid item that renders the live town map.
            // Wrapped in gba-card so it has the same blue chrome as agent cards
            // instead of floating naked on the page background.
            if (id === 'town') {
              return (
                <div
                  className="gba-card h-full w-full flex items-center justify-center overflow-hidden"
                  style={{ padding: 'var(--card-padding, 16px)' }}
                  data-no-drag-children="false"
                >
                  <TownView
                    agents={agents}
                    onSelect={(a) => focusAgent(stableId(a))}
                    selectedId={null}
                    debug={showTownEditor}
                    newMessage={newMessage}
                    geometry={{
                      scale: settings.townScale,
                      cellSize: settings.townCellSize,
                      cellOffsetX: settings.townCellOffsetX,
                      cellOffsetY: settings.townCellOffsetY,
                      cropLeft: settings.townCropLeft,
                      cropTop: settings.townCropTop,
                      cropRight: settings.townCropRight,
                      cropBottom: settings.townCropBottom,
                    }}
                    editorOpen={showTownEditor}
                    onCloseEditor={() => setShowTownEditor(false)}
                    onSaveGeometry={(g) => setSettings({
                      townScale: g.scale,
                      townCellSize: g.cellSize,
                      townCellOffsetX: g.cellOffsetX,
                      townCellOffsetY: g.cellOffsetY,
                      townCropLeft: g.cropLeft,
                      townCropTop: g.cropTop,
                      townCropRight: g.cropRight,
                      townCropBottom: g.cropBottom,
                    })}
                    projects={projects}
                    roles={roles}
                    existingGroups={existingGroupNames}
                  />
                </div>
              )
            }
            // Group container
            if (id.startsWith('group:')) {
              const groupName = id.slice(6)
              const members = grouped[groupName]
              if (!members) return null
              const gvm = groupViewModes[groupName] || 'expanded'
              const isExpanded = gvm === 'expanded'
              const pixelW = isExpanded ? gridEngine.cellW * settings.cardsPerRow + gridEngine.gap * (settings.cardsPerRow - 1) : gridEngine.cellW
              const pixelH = isExpanded ? 0 : gridEngine.cellH
              return (
                <GroupContainer
                  name={groupName}
                  members={members}
                  viewMode={isExpanded ? 'expanded' : 'single'}
                  pageIndex={groupPageIndex[groupName] || 0}
                  onSetViewMode={(mode) => {
                    setGroupViewModes(prev => {
                      const next = { ...prev, [groupName]: mode === 'collapsed' ? 'collapsed' as const : mode === 'expanded' ? 'expanded' as const : 'single' as const }
                      localStorage.setItem('pokegents-group-view-modes', JSON.stringify(next))
                      return next
                    })
                  }}
                  onSetPageIndex={(idx) => setGroupPageIndex(prev => ({ ...prev, [groupName]: idx }))}
                  onMinimize={() => {
                    const coord = members.find(m => m.role?.toLowerCase().includes('coordinator'))
                    const primaryAgent = coord || members[0]
                    const groupSprite = primaryAgent ? getSpriteForId(primaryAgent.session_id) : 'pokeball'
                    const cardRect = gridEngine.gridRectToPixels(rect)
                    const spriteCx = cardRect.left + cardRect.width / 2
                    const spriteCy = cardRect.top + 40
                    const existingBubbles = bubbleRefs.current.size
                    const bubbleTarget = { x: 12 + existingBubbles * 36 + 16, y: showHeader ? 56 : 16 }
                    triggerRecall(`group:${groupName}`, groupSprite, cardRect, bubbleTarget, () => {
                      setGroupViewModes(prev => ({ ...prev, [groupName]: 'collapsed' }))
                    }, { spriteCx, spriteCy })
                  }}
                  cols={isExpanded ? settings.cardsPerRow : 1}
                  cardMode={cardMode}
                  pixelW={pixelW}
                  pixelH={pixelH}
                  readingAgents={readingAgents}
                  projects={projects}
                  roles={roles}
                  existingGroups={existingGroupNames}
                  isConnecting={isServerConnecting}
                />
              )
            }

            // Regular agent card
            const agent = agentMap[id]
            if (!agent) return null
            const aid = stableId(agent)
            const isChat = agent.interface === 'chat'
            const isActiveChatTarget = isChat && chatAgentId === aid
            const chatConn = isChat ? chatConnections.getConnection(aid) : null
            const isConnecting = isServerConnecting || (isChat && isActiveChatTarget && (!chatConn || !chatConn.streamReady))
            const cardAgent = chatConn ? livePreviewFromChat(agent, chatConn.entries, chatConn.wsBusy, chatConn.busySince) : agent
            return (
              <AgentCard
                agent={cardAgent}
                isConnecting={isConnecting}
                onClick={() => {
                  if (isChat) {
                    if (chatAgentId === aid) {
                      // Same agent re-clicked — ping the panel to flash.
                      window.dispatchEvent(new Event('chat-panel-ping'))
                    } else {
                      setChatAgentId(aid)
                    }
                  } else {
                    focusAgent(aid)
                  }
                }}
                glowActive={isActiveChatTarget}
                quickSendPrompt={isChat && chatConn ? async (text) => {
                  setChatAgentId(aid)
                  window.dispatchEvent(new Event('chat-panel-ping'))
                  await chatConn.sendPrompt(text)
                } : undefined}
                quickInputDisabled={isChat ? (!chatConn?.streamReady || chatConn.reconfiguring) : undefined}
                quickInputPlaceholder={isChat
                  ? (chatConn?.reconfiguring
                    ? 'Reconfiguring… hang tight'
                    : chatConn?.streamReady
                      ? `Ask ${agent.display_name || 'agent'}…`
                      : 'Connecting…')
                  : undefined}
                quickInputBusy={isChat ? (chatConn?.wsBusy ?? agent.state === 'busy') : undefined}
                quickInputSlashCommands={isChat}
                mode={cardMode}
                shortcutLabel={shortcutById[aid]}
                shortcutVisible={shortcutOverlay}
                spriteOverride={agent.sprite}
                isReading={readingAgents.has(aid) || readingAgents.has(agent.session_id)}
                hideSprite={hiddenSprites.has(aid) || hiddenSprites.has(agent.session_id)}
                projects={projects}
                roles={roles}
                onDismiss={agent.ephemeral ? () => dismissEphemeral(aid) : undefined}
                existingGroups={existingGroupNames}
                onCollapse={() => {
                  const cardEl = cardRefs.current.get(aid)
                  if (cardEl) {
                    const spriteEl = cardEl.querySelector('.creature-sprite') as HTMLElement | null
                    const spriteRect = spriteEl?.getBoundingClientRect()
                    const cardRect = spriteRect
                      ? new DOMRect(cardEl.getBoundingClientRect().left, cardEl.getBoundingClientRect().top,
                          cardEl.getBoundingClientRect().width, cardEl.getBoundingClientRect().height)
                      : cardEl.getBoundingClientRect()
                    const animRect = new DOMRect(
                      cardRect.left, cardRect.top, cardRect.width, cardRect.height
                    )
                    const spriteCx = spriteRect ? spriteRect.left + spriteRect.width / 2 : cardRect.left + cardRect.width - 40
                    const spriteCy = spriteRect ? spriteRect.top + spriteRect.height / 2 : cardRect.top + 32
                    const sprite = getSpriteForId(aid)
                    const existingBubbles = bubbleRefs.current.size
                    const bubbleTarget = { x: 12 + existingBubbles * 36 + 16, y: showHeader ? 56 : 16 }
                    triggerRecall(aid, sprite, animRect, bubbleTarget, () => {
                      setCollapsedIds(prev => new Set([...prev, aid]))
                    }, { spriteCx, spriteCy })
                  } else {
                    setCollapsedIds(prev => new Set([...prev, aid]))
                  }
                }}
                cardRef={(el) => {
                  if (el) {
                    cardRefs.current.set(aid, el)
                  } else {
                    cardRefs.current.delete(aid)
                  }
                }}
              />
            )
          }}
        </GridContainer>
      )}
       </div>{/* end grid column */}

       {/* Right split-pane: always present so grid doesn't resize when toggling chat.
           Shows ChatPanel when an agent is selected, empty placeholder otherwise. */}
       {/* Resize handle */}
       <div
         role="separator"
         aria-orientation="vertical"
         title="Drag to resize chat panel"
         onPointerDown={onChatDividerPointerDown}
         className="group/divider shrink-0 -mx-1.5 px-1 cursor-col-resize relative z-20 select-none"
         style={{ width: 6 }}
       >
         <div className="h-full w-px mx-auto theme-bg-panel-subtle group-hover/divider:bg-accent-blue transition-colors" />
       </div>
       <div className="shrink-0 min-h-0" style={{ width: chatPanelWidth }}>
         {(() => {
           const chatAgent = chatAgentId ? agentMap[chatAgentId] : null
           if (chatAgent) {
             const conn = chatConnections.getConnection(chatAgent.pokegent_id || chatAgent.session_id)
             return <ChatPanel agent={chatAgent} connection={conn} onClose={() => setChatAgentId(null)} />
           }
           return (
             <div className="h-full w-full flex items-center justify-center gba-card" style={{ borderRadius: 8, background: 'linear-gradient(180deg, #3a78b0 0%, #2e6498 30%, #1f4878 100%)' }}>
               <div className="text-center theme-text-faint">
                 <div className="text-s theme-font-display pixel-shadow">No agent selected</div>
                 <div className="text-s theme-font-display mt-1">Click a chat agent to open</div>
               </div>
             </div>
           )
         })()}
       </div>
      </div>{/* end body flex-row */}

      </div>{/* end ROOT flex container */}

      <DeliveryOverlay deliveries={deliveries} />
      <PokeballAnimationLayer animations={animations} onComplete={onAnimComplete} />
      {showBrowser && <SessionBrowser
        onClose={() => setShowBrowser(false)}
        activePokegentIds={new Set(agents.map(a => stableId(a)))}
        onResume={(id) => setCollapsedIds(prev => { const next = new Set(prev); next.delete(id); return next })}
      />}
      {showSettings && (
        <SettingsPanel
          settings={settings}
          defaults={DEFAULTS}
          setupStatus={setupStatus}
          onChange={setSettings}
          onReset={resetSettings}
          onClose={() => setShowSettings(false)}
          onTestMessaging={triggerTestDelivery}
          onGridDragging={setGridSliderDragging}
          onOpenOnboarding={() => {
            setShowSettings(false)
            setShowOnboarding(true)
            refreshSetupStatus().catch(() => {})
          }}
          onOpenTownEditor={() => {
            setSettings({ showTownCard: true })
            setShowSettings(false)
            setShowTownEditor(true)
          }}
        />
      )}
      {showLauncher && <LaunchModal projects={projects} roles={roles} agents={agents} onClose={() => setShowLauncher(false)} />}
      {menuAgent && createPortal(
        <AgentMenu
          x={menuPos.x}
          y={menuPos.y}
          agent={menuAgent}
          capabilities={capsFor(allCaps, menuAgent.interface)}
          onClose={() => setMenuAgent(null)}
          onRename={async () => {
            const current = menuAgent.display_name || menuAgent.profile_name || 'Agent'
            setMenuAgent(null)
            const next = window.prompt('Rename agent', current)
            const trimmed = next?.trim()
            if (trimmed && trimmed !== current) {
              await renameAgent(menuAgent.pokegent_id || menuAgent.session_id, trimmed)
            }
          }}
          onChangeSprite={() => {
            setSpritePickerAgent(menuAgent)
            setMenuAgent(null)
          }}
          projects={projects}
          roles={roles}
          existingGroups={existingGroupNames}
        />,
        document.body
      )}
      {spritePickerAgent && createPortal(
        <SpritePicker
          currentSprite={spritePickerAgent.sprite || 'pokeball'}
          onSelect={async (sprite) => {
            await setSprite(spritePickerAgent.session_id, sprite)
            setSpritePickerAgent(null)
          }}
          onClose={() => setSpritePickerAgent(null)}
        />,
        document.body
      )}
      {showOnboarding && (
        <OnboardingModal
          status={setupStatus}
          onClose={closeOnboarding}
          onRefresh={async () => { await refreshSetupStatus() }}
        />
      )}
    </>
  )
}
