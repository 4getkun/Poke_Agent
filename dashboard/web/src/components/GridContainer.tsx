import { useRef, useEffect, useState, useCallback } from 'react'
import type { GridEngine, GridRect } from '../hooks/useGridEngine'

// GridContainer — flow-based card grid.
//
// Cards are rendered in `engine.effectiveOrder` (preview-during-drag falls
// back to committed order). CSS Grid handles wrap; column count = cardsPerRow,
// row height = cellH. Drag-to-reorder splices the array; everything later
// shifts to the right and overflows to the next row. No per-card geometry,
// no collision repair, no resize.

interface GridContainerProps {
  engine: GridEngine
  /** Render a card given its id. The signature still passes a (rect, mode)
   *  triple for back-compat; rect is always 1×1 in flow mode. */
  children: (id: string, rect: GridRect, mode: ReturnType<GridEngine['getCardMode']>) => React.ReactNode
  /** Visible IDs in the grid. The engine derives its order from this list,
   *  but we still pass it explicitly so the consumer's filter (collapsed,
   *  ungrouped, town card) is the source of truth for what's visible. */
  agentIds: string[]
  showHeader: boolean
  showGridLines?: boolean
  onDropOnGroup?: (agentId: string, groupName: string) => void
  expandedGroups?: Set<string>
}

export function GridContainer({ engine, children, agentIds: _agentIds, showHeader: _showHeader, showGridLines, onDropOnGroup, expandedGroups }: GridContainerProps) {
  const { effectiveOrder, layouts, settings, cellH, gap, getCardMode, dragState, gridRef } = engine
  const isDragging = !!dragState

  const [ghostPos, setGhostPos] = useState<{ x: number; y: number } | null>(null)
  const [ghostSize, setGhostSize] = useState<{ w: number; h: number }>({ w: 0, h: 0 })
  const [dropTargetGroup, setDropTargetGroup] = useState<string | null>(null)

  const onDropOnGroupRef = useRef(onDropOnGroup)
  onDropOnGroupRef.current = onDropOnGroup
  const engineRef = useRef(engine)
  engineRef.current = engine
  const dragStateRef = useRef(dragState)
  dragStateRef.current = dragState

  // Document-level pointer move/up while dragging.
  useEffect(() => {
    if (!isDragging) return

    const onMove = (e: PointerEvent) => {
      const eng = engineRef.current
      const ds = dragStateRef.current
      if (!ds) return
      eng.updateDrag(e.clientX, e.clientY)
      setGhostPos({
        x: e.clientX - ds.ghostOffset.x,
        y: e.clientY - ds.ghostOffset.y,
      })
      // Check if dragging over a group container (only for non-group cards).
      if (!ds.id.startsWith('group:')) {
        const el = document.elementFromPoint(e.clientX, e.clientY)
        const groupEl = el?.closest('[data-group-name]') as HTMLElement | null
        setDropTargetGroup(groupEl?.dataset.groupName ?? null)
      }
    }

    const onUp = (e: PointerEvent) => {
      const eng = engineRef.current
      const ds = dragStateRef.current
      if (ds && !ds.id.startsWith('group:')) {
        const el = document.elementFromPoint(e.clientX, e.clientY)
        const groupEl = el?.closest('[data-group-name]') as HTMLElement | null
        if (groupEl?.dataset.groupName && onDropOnGroupRef.current) {
          eng.cancelDrag()
          onDropOnGroupRef.current(ds.id, groupEl.dataset.groupName)
          setGhostPos(null)
          setDropTargetGroup(null)
          return
        }
      }
      if (eng.dragState) eng.endDrag()
      setGhostPos(null)
      setDropTargetGroup(null)
    }

    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    return () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
    }
  }, [isDragging])

  // Snapshot ghost size from the dragged card on drag start.
  useEffect(() => {
    if (dragState) {
      setGhostSize({ w: engine.cellW, h: engine.cellH })
    }
  }, [dragState, engine.cellW, engine.cellH])

  // Separate expanded groups from regular grid items. Regular items (including
  // collapsed groups) flow in a single CSS grid so they fill all columns.
  // Expanded groups render as full-width blocks below the grid.
  const gridIds = effectiveOrder.filter(id => !(id.startsWith('group:') && expandedGroups?.has(id.slice(6))))
  const expandedIds = effectiveOrder.filter(id => id.startsWith('group:') && expandedGroups?.has(id.slice(6)))

  const gridStyle = {
    gridTemplateColumns: `repeat(${settings.cardsPerRow}, minmax(0, 1fr))`,
    gridAutoRows: cellH,
    gap,
    paddingLeft: gap,
    paddingRight: gap,
    position: 'relative' as const,
  }

  return (
    <div className="flex-1 min-h-0" style={{ overflowY: 'auto', overflowX: 'hidden', position: 'relative' }}>
      {/* Main grid — all items except expanded groups */}
      <div
        ref={gridRef}
        className="grid content-start items-start"
        style={{
          ...gridStyle,
          paddingTop: Math.min(gap, 8),
          paddingBottom: expandedIds.length > 0 ? 0 : Math.min(gap, 8),
        }}
      >
        {(isDragging || showGridLines) && (
          <div className="pointer-events-none" style={{
            position: 'absolute', inset: 0, zIndex: 1,
          }}>
            {Array.from({ length: settings.cardsPerRow - 1 }, (_, i) => {
              const x = gap + (i + 1) * (engine.cellW + gap) - gap / 2
              return (
                <div
                  key={`vline-${i}`}
                  style={{
                    position: 'absolute',
                    left: x,
                    top: 0,
                    bottom: 0,
                    width: 2,
                    backgroundImage: 'repeating-linear-gradient(180deg, var(--theme-panel-divider) 0px, var(--theme-panel-divider) 4px, transparent 4px, transparent 8px)',
                  }}
                />
              )
            })}
          </div>
        )}
        {gridIds.map(id => {
          const rect = layouts[id]
          if (!rect) return null
          const mode = getCardMode(id)
          const isDraggedCard = dragState?.id === id
          return (
            <GridCell
              key={id}
              id={id}
              rect={rect}
              cellW={engine.cellW}
              cellH={cellH}
              gap={gap}
              isDragging={isDraggedCard}
              engine={engine}
              isDropTarget={id.startsWith('group:') && dropTargetGroup === id.slice(6)}
            >
              {children(id, rect, mode)}
            </GridCell>
          )
        })}
      </div>

      {/* Expanded groups — full-width blocks below the main grid */}
      {expandedIds.map(id => {
        const rect = layouts[id]
        if (!rect) return null
        const mode = getCardMode(id)
        return (
          <div key={`expanded-${id}`} style={{ paddingLeft: gap, paddingRight: gap, paddingTop: gap / 2, paddingBottom: gap / 2 }}>
            {children(id, rect, mode)}
          </div>
        )
      })}

      {/* Drag ghost — floating element under the cursor while reordering. */}
      {isDragging && dragState && ghostPos && (
        <div
          className="fixed pointer-events-none z-50"
          style={{
            left: ghostPos.x,
            top: ghostPos.y,
            width: ghostSize.w,
            height: ghostSize.h,
            opacity: 0.7,
            transform: 'scale(1.02)',
            filter: 'brightness(1.1)',
          }}
        >
          {(() => {
            const rect = layouts[dragState.id]
            if (!rect) return null
            return children(dragState.id, rect, getCardMode(dragState.id))
          })()}
        </div>
      )}
    </div>
  )
}

// ── GridCell — wraps a card; runs FLIP when its index changes ──

interface GridCellProps {
  id: string
  rect: GridRect
  cellW: number
  cellH: number
  gap: number
  isDragging: boolean
  engine: GridEngine
  isDropTarget?: boolean
  fullRow?: boolean
  children: React.ReactNode
}

function GridCell({
  id,
  rect,
  cellW,
  cellH,
  gap,
  isDragging,
  engine,
  isDropTarget,
  fullRow,
  children,
}: GridCellProps) {
  const cellRef = useRef<HTMLDivElement>(null)
  const dragThreshold = useRef<{ startX: number; startY: number; started: boolean } | null>(null)
  const engineRef = useRef(engine)
  engineRef.current = engine

  // FLIP: animate from old (col, row) to new when the card's flow index
  // changes (someone reordered, or cardsPerRow changed and the card wrapped).
  const prevRect = useRef(rect)
  useEffect(() => {
    const prev = prevRect.current
    prevRect.current = rect
    if (!cellRef.current || isDragging) return
    if (prev.col === rect.col && prev.row === rect.row) return

    const dx = (prev.col - rect.col) * (cellW + gap)
    const dy = (prev.row - rect.row) * (cellH + gap)
    if (dx === 0 && dy === 0) return

    const el = cellRef.current
    el.style.transition = 'none'
    el.style.transform = `translate(${dx}px, ${dy}px)`
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        el.style.transition = 'transform 350ms cubic-bezier(0.2, 0, 0, 1)'
        el.style.transform = 'translate(0, 0)'
      })
    })
  }, [rect.col, rect.row, cellW, cellH, gap, isDragging])

  const onPointerDown = useCallback((e: React.PointerEvent) => {
    const el = e.target as HTMLElement
    if (
      el.tagName === 'INPUT' ||
      el.tagName === 'TEXTAREA' ||
      el.closest('[data-no-drag]') ||
      el.closest('button')
    ) return

    dragThreshold.current = { startX: e.clientX, startY: e.clientY, started: false }

    const onMove = (me: PointerEvent) => {
      if (!dragThreshold.current) return
      const dx = me.clientX - dragThreshold.current.startX
      const dy = me.clientY - dragThreshold.current.startY
      if (!dragThreshold.current.started && Math.abs(dx) + Math.abs(dy) > 5) {
        dragThreshold.current.started = true
        if (cellRef.current) {
          engineRef.current.startDrag(id, dragThreshold.current.startX, dragThreshold.current.startY, cellRef.current)
        }
      }
    }

    const onUp = () => {
      dragThreshold.current = null
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
    }

    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
  }, [id])

  const isGroup = id.startsWith('group:')

  return (
    <div
      ref={cellRef}
      className="relative"
      onPointerDown={onPointerDown}
      {...(isGroup ? { 'data-group-name': id.slice(6) } : {})}
      style={{
        // Pin the cell height so tall content (e.g. an agent with lots of
        // trace output) can't stretch the whole row. `grid-auto-rows` only
        // sets the *initial* track size; content can still grow it.
        // No `overflow: hidden` on the cell itself — that would clip the
        // card's outer drop shadow + glowActive ring (which extend a couple
        // px past the rounded corners) and the bottom-right would read as
        // square. AgentCard uses `overflow-visible` so sprite bubbles
        // can extend past the card top, so dropping the cell-level clip is safe.
        height: cellH,
        minHeight: 0,
        // Hide the source card while dragging; the ghost follows the cursor.
        visibility: isDragging ? 'hidden' : 'visible',
        transition: 'box-shadow 150ms',
        cursor: isDragging ? 'grabbing' : 'grab',
        boxShadow: isDropTarget ? 'inset 0 0 0 2px rgb(var(--theme-accent-green-rgb) / 0.7), 0 0 12px rgb(var(--theme-accent-green-rgb) / 0.3)' : undefined,
        borderRadius: isDropTarget ? 8 : undefined,
        userSelect: 'none',
      }}
    >
      {children}
    </div>
  )
}
