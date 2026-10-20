import { useState, useRef, useEffect } from 'react'
import { createPortal } from 'react-dom'
import { AgentState, stableId } from '../types'
import { AgentCard, GROUP_COLORS } from './AgentCard'
import { hashString } from './CreatureIcon'
import { PixelSprite } from './PixelSprite'
import { focusAgent, releaseTaskGroup, assignTaskGroup, ProjectInfo, RoleInfo } from '../api'
import type { CardMode } from '../hooks/useGridEngine'

export type GroupViewMode = 'collapsed' | 'single' | 'expanded'

interface GroupContainerProps {
  name: string
  members: AgentState[]
  viewMode: 'single' | 'expanded'
  pageIndex: number
  onSetViewMode: (mode: GroupViewMode) => void
  onSetPageIndex: (index: number) => void
  onMinimize: () => void
  cols: number
  cardMode: CardMode
  pixelW: number
  pixelH: number
  singleCardPixelW?: number
  singleCardPixelH?: number
  readingAgents: Set<string>
  projects: ProjectInfo[]
  roles: RoleInfo[]
  existingGroups?: string[]
  isConnecting?: boolean
}

function statusColor(state: string): string {
  if (state === 'busy') return 'var(--theme-status-busy)'
  if (state === 'needs_input') return 'var(--theme-status-needs-input)'
  if (state === 'error') return 'var(--theme-status-error)'
  // Phase 2: done collapsed into idle — no separate done color
  return 'var(--theme-status-idle)'
}

function statusLabel(state: string): string {
  if (state === 'busy') return 'ATK'
  if (state === 'needs_input') return 'WAIT'
  if (state === 'error') return 'PSN'
  // Phase 2: done collapsed into idle — no separate done label
  return 'SLP'
}

function formatTime(lastUpdated?: string): string {
  if (!lastUpdated) return ''
  const secs = Math.max(0, (Date.now() - new Date(lastUpdated).getTime()) / 1000)
  if (secs < 60) return `${Math.floor(secs)}s`
  if (secs < 3600) return `${Math.floor(secs / 60)}m`
  return `${Math.floor(secs / 3600)}h${Math.floor((secs % 3600) / 60)}m`
}

function sortMembers(members: AgentState[]): AgentState[] {
  return [...members].sort((a, b) => {
    const aCoord = a.role?.toLowerCase().includes('coordinator') ? 0 : 1
    const bCoord = b.role?.toLowerCase().includes('coordinator') ? 0 : 1
    if (aCoord !== bCoord) return aCoord - bCoord
    return (a.created_at || '').localeCompare(b.created_at || '')
  })
}

function getSprite(agent: AgentState): string {
  return agent.sprite || 'pokeball'
}

const HEADER_H = 24

/** Compact 1-row member entry: sprite + name + status pill + time */
function MemberRow({ agent, sprite, isActive, onClick }: {
  agent: AgentState; sprite: string; isActive: boolean; onClick: () => void
}) {
  const time = formatTime(agent.last_updated)
  return (
    <button
      onClick={onClick}
      className={`flex items-center gap-1.5 w-full px-1.5 py-0.5 rounded transition-colors ${isActive ? 'theme-bg-panel-subtle' : 'theme-bg-panel-hover'}`}
      style={{ minHeight: 20 }}
    >
      <div className="shrink-0 flex items-center justify-center" style={{ width: 16, height: 16 }}>
        <PixelSprite sprite={sprite} scale={0.5} alt="" />
      </div>
      <span className="text-s theme-font-display theme-text-secondary truncate flex-1 text-left">
        {agent.display_name || agent.profile_name}
      </span>
      <span
        className={`text-xs theme-font-display theme-text-primary px-1 py-px rounded-full leading-none shrink-0${agent.state === 'busy' ? ' animate-pulse-soft' : ''}`}
        style={{ backgroundColor: statusColor(agent.state), textShadow: 'var(--theme-text-shadow-pixel)' }}
      >{statusLabel(agent.state)}</span>
      {time && <span className="text-xs theme-font-mono theme-text-faint shrink-0">{time}</span>}
    </button>
  )
}

export function GroupContainer({
  name, members: rawMembers, viewMode, pageIndex, onSetViewMode, onSetPageIndex, onMinimize,
  cols, cardMode, pixelW, pixelH, singleCardPixelW, singleCardPixelH,
  readingAgents, projects, roles, existingGroups, isConnecting,
}: GroupContainerProps) {
  const [confirmRelease, setConfirmRelease] = useState(false)
  const [ctxMenu, setCtxMenu] = useState<{ x: number; y: number } | null>(null)
  const confirmTimer = useRef<ReturnType<typeof setTimeout>>(undefined)

  useEffect(() => {
    return () => { if (confirmTimer.current) clearTimeout(confirmTimer.current) }
  }, [])

  const handleRelease = () => {
    if (!confirmRelease) {
      setConfirmRelease(true)
      confirmTimer.current = setTimeout(() => setConfirmRelease(false), 3000)
      return
    }
    setConfirmRelease(false)
    releaseTaskGroup(name)
  }

  const idx = Math.abs(hashString(name)) % GROUP_COLORS.length
  const [r, g, b] = GROUP_COLORS[idx]
  const members = sortMembers(rawMembers)
  const safePageIndex = Math.min(pageIndex, members.length - 1)
  const contentH = pixelH - HEADER_H
  const innerCardW = singleCardPixelW || pixelW
  const INNER_GAP = 8
  const PAD = 8
  const subCols = innerCardW < pixelW ? Math.max(1, Math.floor((pixelW - PAD) / innerCardW)) : 1
  const cardRows = Math.ceil(members.length / subCols)
  const innerCardH = cardRows > 0 ? Math.floor((contentH - PAD - (cardRows - 1) * INNER_GAP) / cardRows) : 180

  const renderCard = (agent: AgentState, hideGroupTag = false) => (
    <AgentCard
      key={stableId(agent)}
      agent={agent}
      onClick={() => focusAgent(stableId(agent))}
      mode={cardMode}
      spriteOverride={agent.sprite}
      isReading={readingAgents.has(stableId(agent)) || readingAgents.has(agent.session_id)}
      hideGroupTag={hideGroupTag}
      projects={projects}
      roles={roles}
      existingGroups={existingGroups}
      isConnecting={isConnecting}
    />
  )

  // Other members (not the active one) for the compact list
  const otherMembers = members.filter((_, i) => i !== safePageIndex)

  return (
    <div
      className="rounded-lg h-full flex flex-col overflow-hidden min-w-0"
      style={{
        background: `rgba(${r}, ${g}, ${b}, 0.20)`,
        border: `1px solid rgba(${r}, ${g}, ${b}, 0.55)`,
        borderLeft: `3px solid rgba(${r}, ${g}, ${b}, 0.85)`,
      }}
      onContextMenu={(e) => { e.preventDefault(); setCtxMenu({ x: e.clientX, y: e.clientY }) }}
    >
      {/* Header bar */}
      <div className="flex items-center gap-1.5 px-2 select-none shrink-0" style={{ height: HEADER_H, background: `rgba(${r}, ${g}, ${b}, 0.15)` }}>
        <button
          className="text-[10px] theme-text-muted theme-hover-text-primary transition-colors px-0.5"
          onClick={(e) => { e.stopPropagation(); onSetViewMode(viewMode === 'single' ? 'expanded' : 'single') }}
          title={viewMode === 'single' ? 'Expand all' : 'Single view'}
        >{viewMode === 'single' ? '▶' : '▼'}</button>

        <span
          className="text-s theme-font-display uppercase truncate theme-text-primary"
        >{name}</span>
        <span className="text-s theme-font-display theme-text-faint shrink-0">{members.length}</span>

        <div className="flex items-center gap-1 ml-auto" onClick={e => e.stopPropagation()}>
          {viewMode === 'single' && (
            <>
              <button
                className="text-[10px] theme-text-faint theme-hover-text-primary transition-colors disabled:theme-text-faint px-0.5"
                onClick={() => onSetPageIndex(Math.max(0, safePageIndex - 1))}
                disabled={safePageIndex === 0}
              >◀</button>
              <button
                className="text-[10px] theme-text-faint theme-hover-text-primary transition-colors disabled:theme-text-faint px-0.5"
                onClick={() => onSetPageIndex(Math.min(members.length - 1, safePageIndex + 1))}
                disabled={safePageIndex >= members.length - 1}
              >▶</button>
            </>
          )}

          <button
            className="w-3 h-3 rounded-full bg-accent-red/60 hover:bg-accent-red/80 transition-colors flex items-center justify-center text-[9px] theme-font-display theme-text-primary leading-none"
            style={{ boxShadow: 'var(--theme-text-shadow-pixel)' }}
            onClick={onMinimize}
            title="Minimize"
          >−</button>
        </div>
      </div>

      {/* Single mode: active card + compact member list */}
      {viewMode === 'single' && contentH > 0 && (
        <div className="flex-1 flex flex-col min-h-0 min-w-0 px-1 pb-1 gap-1">
          {/* Active agent card */}
          <div className="flex-1 min-h-0 min-w-0">
            {members[safePageIndex] && renderCard(members[safePageIndex])}
          </div>

          {/* Compact member list — other agents */}
          {otherMembers.length > 0 && (
            <div className="shrink-0 flex flex-col gap-px overflow-y-auto" style={{ maxHeight: Math.min(otherMembers.length * 22, 88) }}>
              {otherMembers.map((m) => {
                const sprite = getSprite(m)
                const memberIdx = members.findIndex(x => stableId(x) === stableId(m))
                return (
                  <MemberRow
                    key={stableId(m)}
                    agent={m}
                    sprite={sprite}
                    isActive={false}
                    onClick={() => onSetPageIndex(memberIdx)}
                  />
                )
              })}
            </div>
          )}
        </div>
      )}

      {/* Expanded mode: sub-grid using the parent grid's column count */}
      {(() => {
        const isExpanded = viewMode === 'expanded'
        const expandedCols = Math.max(1, cols)
        const innerGap = 4
        const cardH = pixelH || 200
        const expandedRows = Math.ceil(members.length / expandedCols)
        const targetH = expandedRows * cardH + (expandedRows - 1) * innerGap + 8
        return (
          <div
            className="min-w-0 overflow-hidden"
            style={{
              maxHeight: isExpanded ? targetH : 0,
              opacity: isExpanded ? 1 : 0,
              transition: 'max-height 300ms ease-out, opacity 200ms ease-out',
            }}
          >
            <div
              style={{
                display: 'grid',
                gridTemplateColumns: `repeat(${expandedCols}, 1fr)`,
                gridAutoRows: cardH,
                gap: innerGap,
                padding: '4px 4px 4px',
              }}
            >
              {members.map(agent => (
                <div key={stableId(agent)} className="min-w-0 min-h-0 overflow-hidden">
                  {renderCard(agent, true)}
                </div>
              ))}
            </div>
          </div>
        )
      })()}

      {/* Context menu */}
      {ctxMenu && createPortal(
        <>
          <div className="fixed inset-0" style={{ zIndex: 9999 }} onClick={() => setCtxMenu(null)} onContextMenu={(e) => { e.preventDefault(); setCtxMenu(null) }} />
          <div style={{ position: 'fixed', left: ctxMenu.x, top: ctxMenu.y, zIndex: 10000 }}>
            <div className="gba-panel py-1 min-w-[160px]">
              <button
                onClick={() => {
                  Promise.all(members.map(m => assignTaskGroup(stableId(m), '')))
                  setCtxMenu(null)
                }}
                className="w-full text-left px-3 py-1.5 text-s theme-font-display theme-text-primary theme-bg-panel-hover flex items-center gap-2 transition-colors pixel-shadow"
              >
                <span className="w-4 text-center">💨</span>
                Disperse group
              </button>
              <button
                onClick={(e) => { e.stopPropagation(); handleRelease(); setCtxMenu(null) }}
                className="w-full text-left px-3 py-1.5 text-s theme-font-display text-accent-red theme-bg-panel-hover flex items-center gap-2 transition-colors pixel-shadow"
              >
                <span className="w-4 text-center">⏻</span>
                Release all
              </button>
            </div>
          </div>
        </>,
        document.body
      )}
    </div>
  )
}
