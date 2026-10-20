import { useEffect, useRef, useState, useMemo, type MouseEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { AgentState } from '../types'
import { Entry } from '../types/chat'
import { setSprite, fetchProjectList, fetchRoleList, ProjectInfo, RoleInfo, restartBackend } from '../api'
import { PromptInput } from './PromptInput'
import { StateBadge, AgentLifecycleState } from './StateBadge'
import { ChatStatusBar } from './ChatStatusBar'
import { AgentMenu } from './AgentMenu'
import { SpritePicker } from './SpritePicker'
import { useRuntimeCapabilities, capsFor } from '../utils/runtimes'
import { CreatureIcon } from './CreatureIcon'
import { useSpriteAnimation } from './spriteAnimations'
import { HealthBar, ProfilePill, RolePill, TaskGroupPill } from './AgentCard'
import { BusyBubble, DoneBubble } from './MessageAnimations'
import { useAgentRename } from '../hooks/useAgentRename'
import { DebugModal } from './ChatDebugModal'
import { SearchBar, countSearchMatches } from './ChatSearch'
import { ChatPanelSprite, ChatPanelDropdown } from './ChatPanelChrome'
import { EntryRow, ThinkingIndicator, lastEntryIsCurrentAssistantMessage, inflightThoughts, PromptNav, ToolCallSummaryBlock, isLocalCommandArtifact } from './ChatTranscript'
import { FilesView, CommandsView } from './PanelViews'
import { ChatConnectionState, ChatConnectionActions } from '../hooks/useChatWebSocket'

// Right-side split panel for chat-backed pokegents (`agent.interface === 'chat'`).
// Connection management (SSE, event processing, prompt submission) lives in
// useChatConnections hook (called once in App.tsx) so the connection survives
// when this panel unmounts/remounts on agent switch. This component is now a
// thin render wrapper.

interface ChatPanelProps {
  agent: AgentState
  connection: (ChatConnectionState & ChatConnectionActions) | null
  onClose: () => void
}

export function ChatPanel({ agent, onClose, connection }: ChatPanelProps) {
  const pokegentId = agent.pokegent_id || agent.session_id

  const entries = connection?.entries ?? []
  const streamReady = connection?.streamReady ?? false
  const title = connection?.title ?? ''
  const queuedMessages = connection?.queuedMessages ?? []
  const bgShells = connection?.bgShells ?? new Map()
  const reconfiguring = connection?.reconfiguring ?? false
  const debugLog = connection?.debugLog ?? []

  // UI-only state — local to this panel, not persisted across unmount
  const [menuOpen, setMenuOpen] = useState(false)
  const [menuPos, setMenuPos] = useState({ x: 0, y: 0 })
  const [showSpritePicker, setShowSpritePicker] = useState(false)
  const rename = useAgentRename(agent.pokegent_id || agent.session_id, agent.display_name || '')
  const [projects, setProjects] = useState<ProjectInfo[]>([])
  const [roles, setRoles] = useState<RoleInfo[]>([])
  const scrollRef = useRef<HTMLDivElement>(null)
  const renameInputRef = useRef<HTMLInputElement>(null)
  const [panelView, setPanelView] = useState<'chat' | 'files' | 'commands'>('chat')
  const filesScrollRef = useRef<HTMLDivElement>(null)
  const commandsScrollRef = useRef<HTMLDivElement>(null)
  const allCaps = useRuntimeCapabilities()
  const caps = capsFor(allCaps, agent.interface)

  // Timestamps: false | true | 'debug' (debug shows table borders)
  const [showTimestamps, setShowTimestamps] = useState<boolean | 'debug'>(() => localStorage.getItem('pokegents-show-timestamps') !== 'false')

  // Debug panel
  const [debugOpen, setDebugOpen] = useState(false)

  // Search state
  const [searchOpen, setSearchOpen] = useState(false)
  const [searchQuery, setSearchQuery] = useState('')
  const [searchMatchIdx, setSearchMatchIdx] = useState(0)
  const searchInputRef = useRef<HTMLInputElement>(null)

  const backendDeadRaw = agent.interface === 'chat' && !agent.is_alive
  const [backendDeadSince, setBackendDeadSince] = useState<number | null>(null)
  const [backendDeadGraceElapsed, setBackendDeadGraceElapsed] = useState(false)
  const [restartPending, setRestartPending] = useState(false)
  const isBusy = connection?.wsBusy ?? agent.state === 'busy'
  const busySinceTs = connection?.busySince || agent.busy_since
  const isCompacting = connection?.compacting ?? false
  const bgToolIds = useMemo(() => new Set(bgShells.keys()), [bgShells])
  useEffect(() => {
    if (!backendDeadRaw || reconfiguring) {
      setBackendDeadSince(null)
      setBackendDeadGraceElapsed(false)
      return
    }
    const started = Date.now()
    setBackendDeadSince(started)
    setBackendDeadGraceElapsed(false)
    const t = setTimeout(() => setBackendDeadGraceElapsed(true), 8000)
    return () => clearTimeout(t)
  }, [backendDeadRaw, reconfiguring, pokegentId])
  const backendDead = backendDeadRaw && !reconfiguring && backendDeadSince != null && backendDeadGraceElapsed

  const lifecycle: AgentLifecycleState = reconfiguring
    ? 'reconfiguring'
    : backendDead
    ? 'error'
    : !streamReady
    ? 'connecting'
    : isBusy
      ? 'busy'
      : agent.state === 'error'
        ? 'error'
        : agent.state === 'needs_input'
          ? 'needs_input'
          : 'idle'

  // Scroll all views to bottom on panel open (pokegentId change) or tab switch.
  useEffect(() => {
    requestAnimationFrame(() => {
      const ref = panelView === 'chat' ? scrollRef : panelView === 'files' ? filesScrollRef : commandsScrollRef
      if (ref.current) ref.current.scrollTop = ref.current.scrollHeight
    })
  }, [panelView, pokegentId])

  // Lazy-load projects/roles for the AgentMenu submenus.
  useEffect(() => {
    fetchProjectList().then(setProjects).catch(() => {})
    fetchRoleList().then(setRoles).catch(() => {})
  }, [])

  useEffect(() => {
    if (rename.isRenaming) {
      renameInputRef.current?.focus()
      renameInputRef.current?.select()
    }
  }, [rename.isRenaming])

  // Sticky-bottom auto-scroll
  const STICKY_BOTTOM_PX = 80
  const stickToBottomRef = useRef(true)
  useEffect(() => {
    stickToBottomRef.current = true
    setPanelView('chat')
    if (scrollRef.current) scrollRef.current.scrollTop = scrollRef.current.scrollHeight
  }, [pokegentId])
  function handleScroll() {
    const el = scrollRef.current
    if (!el) return
    const distanceFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight
    stickToBottomRef.current = distanceFromBottom <= STICKY_BOTTOM_PX
  }
  function handleTranscriptMouseDownCapture(e: MouseEvent<HTMLDivElement>) {
    if (e.button !== 0) return
    const target = e.target as HTMLElement | null
    if (!target) return
    if (target.closest('button, a, input, textarea, [contenteditable="true"], [data-selectable-text]')) return
    // Blank padding/gaps between transcript blocks should not be selectable.
    // Starting a drag there makes Chromium anchor selection to the previous
    // text node, which feels like the previous block was selected by mistake.
    e.preventDefault()
  }
  useEffect(() => {
    if (!stickToBottomRef.current) return
    if (scrollRef.current) scrollRef.current.scrollTop = scrollRef.current.scrollHeight
  }, [entries])

  // Keyboard shortcuts
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'f' && (e.ctrlKey || e.metaKey)) {
        e.preventDefault()
        setSearchOpen(true)
        setTimeout(() => searchInputRef.current?.focus(), 0)
        return
      }
      if (e.key === 'Escape' && searchOpen) {
        setSearchOpen(false)
        setSearchQuery('')
        return
      }
      if (e.key === 'c' && e.ctrlKey && !e.metaKey) {
        if (window.getSelection()?.toString()) return
        e.preventDefault()
        connection?.cancel()
        if (queuedMessages.length > 0) {
          connection?.appendSystemEntry('Cancelled — sending next queued message.')
        } else {
          connection?.appendSystemEntry('Interrupted. What would you like me to do?')
        }
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [searchOpen, connection, queuedMessages.length])

  // Brief yellow glow flash on panel open / agent switch / re-click
  const [glowFlash, setGlowFlash] = useState(false)
  const [flashKey, setFlashKey] = useState(0)
  useEffect(() => {
    setGlowFlash(true)
    const timer = setTimeout(() => setGlowFlash(false), 500)
    return () => clearTimeout(timer)
  }, [pokegentId, flashKey])
  useEffect(() => {
    const handler = () => {
      setPanelView('chat')
      setFlashKey(k => k + 1)
    }
    window.addEventListener('chat-panel-ping', handler)
    return () => window.removeEventListener('chat-panel-ping', handler)
  }, [])

  // Delegate actions to connection (with null guards)
  const submitText = async (text: string) => {
    setPanelView('chat')
    await (connection?.sendPrompt ?? (async () => {}))(text)
  }
  const cancel = connection?.cancel ?? (async () => {})
  const decidePermission = connection?.decidePermission ?? (async () => {})
  const retryMessage = connection?.retryMessage ?? (async () => {})
  const activeBusyPromptIndex = useMemo(() => {
    if (!isBusy) return -1
    for (let i = entries.length - 1; i >= 0; i--) {
      const entry = entries[i]
      if (entry.kind !== 'user') continue
      if (isLocalCommandArtifact(entry.text)) continue
      return i
    }
    return -1
  }, [entries, isBusy])

  const transcriptRows = useMemo(() => {
    const rows: ReactNode[] = []
    for (let i = 0; i < entries.length; i++) {
      const entry = entries[i]
      const shouldCollapseTool = entry.kind === 'tool' && (!isBusy || activeBusyPromptIndex < 0 || i < activeBusyPromptIndex)
      if (shouldCollapseTool) {
        const group: Extract<Entry, { kind: 'tool' }>[] = [entry]
        while (i + 1 < entries.length) {
          const next = entries[i + 1]
          const nextShouldCollapse = next.kind === 'tool' && (!isBusy || activeBusyPromptIndex < 0 || (i + 1) < activeBusyPromptIndex)
          if (!nextShouldCollapse) break
          group.push(next as Extract<Entry, { kind: 'tool' }>)
          i++
        }
        rows.push(
          <ToolCallSummaryBlock
            key={`tool-summary-${group[0].id}-${group[group.length - 1].id}`}
            entries={group}
            backgroundedToolIds={bgToolIds}
            showTimestamps={showTimestamps}
          />,
        )
        continue
      }
      rows.push(
        <EntryRow
          key={entry.id}
          entry={entry}
          onDecidePermission={decidePermission}
          onRetry={retryMessage}
          searchQuery={searchQuery}
          backgroundedToolIds={bgToolIds}
          showTimestamps={showTimestamps}
        />,
      )
    }
    return rows
  }, [entries, isBusy, activeBusyPromptIndex, decidePermission, retryMessage, searchQuery, bgToolIds, showTimestamps])

  return (
    <div
      className="h-full w-full flex flex-col gba-card overflow-visible"
      style={{
        borderRadius: 'var(--theme-radius-card)',
        background: 'var(--theme-chat-panel-bg)',
        borderColor: isBusy ? 'var(--theme-card-selected-border)' : undefined,
        boxShadow: glowFlash
          ? '0 0 0 2px rgb(var(--theme-accent-yellow-rgb) / 0.5), 0 0 16px rgb(var(--theme-accent-yellow-rgb) / 0.25)'
          : isBusy
            ? '0 0 0 4px rgb(var(--theme-accent-orange-rgb) / 0.68), 0 0 24px rgb(var(--theme-accent-orange-rgb) / 0.38)'
            : 'none',
        transition: 'box-shadow 0.4s ease, border-color 0.4s ease',
      }}
    >
      {/* Header — matches AgentCard layout: sprite box + name + HP bar + pills + dropdown */}
      <div
        className="px-3 py-2 border-b theme-border-subtle shrink-0"
        onContextMenu={(e) => { e.preventDefault(); setMenuPos({ x: e.clientX, y: e.clientY }); setMenuOpen(true) }}
      >
        <div className="flex items-center gap-3">
          {/* Sprite with background box + animations */}
          <div
            onClick={() => setShowSpritePicker(true)}
            className="cursor-pointer hover:brightness-125 relative shrink-0 overflow-visible"
            style={{ width: 32, height: 32 }}
          >
            <div className="absolute inset-0 theme-bg-panel-muted rounded-lg" />
            <div className={`relative ${useSpriteAnimation(agent.state || 'idle', true)}`}>
              <CreatureIcon sessionId={agent.session_id} size={32} noGlow={false} doneFlash={false} spriteOverride={agent.sprite} noBg />
              <BusyBubble isBusy={isBusy} />
              <DoneBubble isDone={false} />
            </div>
          </div>
          {/* Name + HP bar */}
          <div className="flex-1 min-w-0">
            <div className="flex items-start justify-between gap-2">
              <div className="flex-1 min-w-0">
                {rename.isRenaming ? (
                  <input
                    ref={renameInputRef}
                    value={rename.newName}
                    onChange={(e) => rename.setNewName(e.target.value)}
                    onBlur={rename.submitRename}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') rename.submitRename()
                      if (e.key === 'Escape') rename.cancelRename()
                    }}
                    className="text-s theme-font-display theme-text-primary bg-transparent border-b theme-border-subtle outline-none w-full pixel-shadow"
                  />
                ) : (
                  <div className="flex items-center gap-1.5">
                    <h3
                      className="text-s theme-font-display theme-text-primary truncate cursor-pointer hover:text-accent-yellow pixel-shadow"
                      onClick={() => rename.startRename()}
                    >
                      {agent.display_name || 'chat'}
                    </h3>
                    <StateBadge state={lifecycle} busySince={busySinceTs} compact />
                  </div>
                )}
                <HealthBar tokens={agent.context_tokens} window={agent.context_window} />
              </div>
              <div className={`flex flex-col items-end gap-0.5 shrink-0 ${!agent.task_group && !agent.role ? 'justify-center' : ''}`}>
                {agent.task_group && <TaskGroupPill name={agent.task_group} />}
                {agent.role && <RolePill name={agent.role} />}
                <ProfilePill name={agent.project || agent.profile_name} color={agent.project_color || agent.color} />
              </div>
            </div>
          </div>
          {/* Divider + dropdown */}
          <div className="w-px self-stretch theme-bg-panel-subtle shrink-0" />
          <ChatPanelDropdown
            onSearch={() => { setSearchOpen(o => !o); if (!searchOpen) setTimeout(() => searchInputRef.current?.focus(), 0) }}
            onMenu={(e) => { setMenuPos({ x: e.clientX, y: e.clientY }); setMenuOpen(true) }}
            onCancel={caps.can_cancel ? () => {
              cancel()
              connection?.appendSystemEntry(queuedMessages.length > 0 ? 'Cancelled — sending next queued message.' : 'Interrupted. What would you like me to do?')
            } : undefined}
            onClose={onClose}
            searchOpen={searchOpen}
            onDebug={() => setDebugOpen(true)}
            showTimestamps={showTimestamps}
            onToggleTimestamps={() => {
              const next = !showTimestamps
              setShowTimestamps(next)
              localStorage.setItem('pokegents-show-timestamps', String(next))
            }}
          />
        </div>
      </div>

      {backendDead && (
        <div className="mx-2 mt-2 shrink-0 rounded-md border border-accent-red/40 bg-accent-red/15 px-3 py-2 flex items-center gap-3">
          <div className="flex-1 min-w-0">
            <div className="text-m theme-font-display text-accent-red pixel-shadow">BACKEND OFFLINE</div>
            <div className="text-m theme-font-mono theme-text-muted truncate">ACP process exited. Restart just this agent backend to recover.</div>
          </div>
          <button
            type="button"
            disabled={restartPending}
            onClick={async () => {
              setRestartPending(true)
              try {
                await restartBackend(pokegentId)
                connection?.appendSystemEntry('Restarting backend…')
              } catch (err) {
                connection?.appendSystemEntry(`Backend restart failed: ${err instanceof Error ? err.message : String(err)}`)
                setRestartPending(false)
              }
            }}
            className="gba-button px-2.5 py-1 text-s theme-font-display theme-text-primary disabled:opacity-50"
          >
            {restartPending ? 'RESTARTING…' : 'RESTART BACKEND'}
          </button>
        </div>
      )}

      {/* Tab bar */}
      <div className="flex items-end gap-1.5 px-2 pt-1.5 shrink-0">
        {(['chat', 'files', 'commands'] as const).map(tab => {
          const active = panelView === tab
          return (
            <button
              key={tab}
              onClick={() => setPanelView(tab)}
              className={`relative px-3 py-1.5 text-s theme-font-display uppercase pixel-shadow transition-colors border border-b-0 theme-border-subtle rounded-t-md ${
                active
                  ? 'theme-text-primary theme-bg-panel-muted'
                  : 'theme-text-secondary theme-bg-panel-subtle theme-hover-text-primary'
              }`}
            >
              {tab.toUpperCase()}
              {active && <span className="absolute left-0 right-0 bottom-0 h-[2px] bg-accent-yellow" />}
            </button>
          )
        })}
      </div>

      {/* Main content area */}
      <div className="flex-1 min-h-0 px-2 pt-0 pb-2 relative">
        {panelView === 'chat' && <>
          {searchOpen && (
            <SearchBar
              query={searchQuery}
              onQueryChange={(q) => { setSearchQuery(q); setSearchMatchIdx(0) }}
              matchCount={searchQuery ? countSearchMatches(entries, searchQuery) : 0}
              matchIdx={searchMatchIdx}
              onNext={() => setSearchMatchIdx(i => {
                const total = countSearchMatches(entries, searchQuery)
                return total ? (i + 1) % total : 0
              })}
              onPrev={() => setSearchMatchIdx(i => {
                const total = countSearchMatches(entries, searchQuery)
                return total ? (i - 1 + total) % total : 0
              })}
              onClose={() => { setSearchOpen(false); setSearchQuery('') }}
              inputRef={searchInputRef}
            />
          )}
          <div
            ref={scrollRef}
            onScroll={handleScroll}
            onMouseDownCapture={handleTranscriptMouseDownCapture}
            className="chat-panel-output h-full overflow-y-auto overflow-x-hidden rounded-md px-3 pt-2.5 pb-10 space-y-1.5 theme-font-mono"
            style={{
              background: 'var(--theme-chat-bg)',
              boxShadow: 'var(--theme-shadow-panel)',
              fontSize: 'var(--chat-panel-output-font-size, 13px)',
            }}
          >
            {!streamReady && (
              <div className="text-m theme-font-mono theme-text-muted">Connecting…</div>
            )}
            {streamReady && entries.length === 0 && (
              <div className="text-m theme-font-mono theme-text-faint">Ready. Type a prompt below.</div>
            )}
            {transcriptRows}
            {isBusy && (() => {
              const lastEntry = entries[entries.length - 1]
              const lastIsActiveTool = lastEntry?.kind === 'tool' &&
                (lastEntry.data.status === 'pending' || lastEntry.data.status === 'in_progress')
              if (lastIsActiveTool) return null
              if (lastEntryIsCurrentAssistantMessage(entries)) return null
              const indicator = (
                <ThinkingIndicator
                  busySince={busySinceTs}
                  thoughts={inflightThoughts(entries)}
                  label={isCompacting ? 'Compacting' : 'Thinking'}
                />
              )
              if (!showTimestamps) return indicator
              return (
                <table className="w-full border-collapse"><tbody><tr>
                  <td className={`align-top w-0 whitespace-nowrap pr-1 ${showTimestamps === 'debug' ? 'border theme-border-danger' : ''}`}>
                    <span className="text-s theme-font-mono theme-text-faint tabular-nums select-none">{new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })}</span>
                  </td>
                  <td className={`align-top ${showTimestamps === 'debug' ? 'border theme-border-subtle' : ''}`}>
                    {indicator}
                  </td>
                </tr></tbody></table>
              )
            })()}
          </div>
          <ChatPanelSprite sprite={agent.sprite} state={agent.state} />
        </>}
        {panelView === 'files' && <FilesView entries={entries} showTimestamps={showTimestamps} scrollRef={filesScrollRef} />}
        {panelView === 'commands' && <CommandsView entries={entries} showTimestamps={showTimestamps} scrollRef={commandsScrollRef} />}
      </div>

      {/* Queued messages — stacked above input, same visual treatment */}
      {queuedMessages.length > 0 && (
        <div className="shrink-0 px-2 pt-1.5 space-y-1">
          <div className="flex items-center gap-2 px-0.5">
            <span className="text-m theme-font-mono theme-text-faint">
              {queuedMessages.length} queued
            </span>
            <div className="flex-1 h-px theme-bg-panel-subtle" />
            <button
              type="button"
              onClick={() => connection?.flushQueue()}
              className="text-s theme-font-display uppercase pixel-shadow theme-text-faint theme-hover-text-secondary transition-colors"
            >CLEAR</button>
          </div>
          {queuedMessages.map((msg, i) => (
            <div key={i} className="flex items-start gap-1">
              <textarea
                data-no-drag
                rows={1}
                value={msg}
                onChange={(e) => connection?.updateQueuedMessage(i, e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Escape') e.currentTarget.blur()
                  if (e.key === 'Enter' && !e.shiftKey) {
                    e.preventDefault()
                    e.currentTarget.blur()
                  }
                }}
                className="flex-1 min-w-0 gba-dialog-dark text-m leading-snug theme-font-mono px-2.5 py-1 theme-text-secondary theme-placeholder-input outline-none resize-none box-border focus:border-accent-blue"
                style={{ minHeight: 24, maxHeight: 96 }}
              />
              <button
                type="button"
                onClick={() => connection?.removeQueuedMessage(i)}
                className="shrink-0 text-s theme-font-display theme-text-faint theme-hover-text-primary px-1 py-1 transition-colors"
                title="Remove queued message"
              >×</button>
            </div>
          ))}
        </div>
      )}

      {/* Input — shared component handles auto-grow, Enter/Shift+Enter,
          and image paste (works for both runtimes via /api/sessions/{id}/image). */}
      <PromptInput
        sessionId={pokegentId}
        onSend={submitText}
        variant="chat"
        showSendButton
        autoFocus={streamReady}
        disabled={!streamReady || reconfiguring || backendDead}
        placeholder={
          reconfiguring
            ? 'Reconfiguring…  hang tight'
            : backendDead
              ? 'Backend offline — restart backend to continue'
              : streamReady
                ? `Ask ${agent.display_name || 'agent'}…  (Enter to send, Shift+Enter for newline)`
                : 'Connecting…'
        }
        isBusy={isBusy}
      />

      <ChatStatusBar agent={agent} shells={Array.from(bgShells.values())}>
        <PromptNav entries={entries} scrollRef={scrollRef} />
      </ChatStatusBar>

      {menuOpen && createPortal(
        <AgentMenu
          x={menuPos.x}
          y={menuPos.y}
          agent={agent}
          capabilities={caps}
          projects={projects}
          roles={roles}
          onClose={() => setMenuOpen(false)}
          onRename={() => { setMenuOpen(false); rename.startRename() }}
          onChangeSprite={() => { setMenuOpen(false); setShowSpritePicker(true) }}
        />,
        document.body,
      )}
      {showSpritePicker && createPortal(
        <SpritePicker
          currentSprite={agent.sprite || 'pokeball'}
          onSelect={async (sprite) => { await setSprite(agent.session_id, sprite) }}
          onClose={() => setShowSpritePicker(false)}
        />,
        document.body,
      )}
      {debugOpen && createPortal(
        <DebugModal
          agent={agent}
          pokegentId={pokegentId}
          streamReady={streamReady}
          queuedMessages={queuedMessages}
          bgShells={bgShells}
          debugLog={debugLog}
          onClose={() => setDebugOpen(false)}
          onForceIdle={async () => { connection?.forceIdle() }}
          onRespawnAcp={async () => { connection?.respawnAcp() }}
          onReconnectSse={() => { connection?.reconnectSSE() }}
          onReloadTranscript={() => { connection?.reloadTranscript() }}
          onFlushQueue={() => { connection?.flushQueue() }}
          onClearBgTasks={() => { connection?.clearBgTasks() }}
          showTimestamps={showTimestamps}
          onToggleDebugBorders={() => {
            setShowTimestamps(prev => prev === 'debug' ? true : 'debug')
          }}
        />,
        document.body,
      )}
    </div>
  )
}

// (Extracted components live in ChatTranscript.tsx, PanelViews.tsx,
// ChatSearch.tsx, ChatPanelChrome.tsx, ChatDebugModal.tsx)
