import { useState, useEffect, useCallback, useRef, useMemo } from 'react'

// ── Types ──────────────────────────────────────────────────
//
// Flow-based grid: cards live in an ordered array; layout falls out from
// `index → (col, row)` with `col = i % cardsPerRow`. Drag-to-reorder splices
// the array; everything later in the array shifts naturally. There is no
// per-card geometry — width is uniform (cardsPerRow), height is uniform
// (cardsPerCol-derived). Tab order matches DOM order matches array order.

// GridRect is kept for back-compat with the rest of the app — it always
// describes a 1×1 cell now, and the (col, row) is derived from the card's
// index in the order array.
export interface GridRect {
  col: number  // 1-indexed
  row: number  // 1-indexed
  w: number    // always 1 in flow mode
  h: number    // always 1 in flow mode
}

export type CardMode = 'standard' | 'compact' | 'compact-minimal'

export interface DragState {
  id: string
  startPointer: { x: number; y: number }
  ghostOffset: { x: number; y: number }
}

const DEFAULT_CARDS_PER_ROW = 3
const DEFAULT_CARDS_PER_COL = 3
const DEFAULT_GAP = 8
const PADDING = 0
const MIN_CELL_PX = 16
const MIN_READABLE_CELL_W = 240

// ── Pure helpers ───────────────────────────────────────────

export function cardModeFor(pixelHeight: number): CardMode {
  if (pixelHeight >= 200) return 'standard'
  if (pixelHeight >= 120) return 'compact'
  return 'compact-minimal'
}

interface StoredLayout {
  cardsPerRow?: number
  cardsPerCol?: number
  gap?: number
  order?: string[]
  // Legacy bin-pack format — migrated by sorting entries row-major.
  settings?: { cols?: number; cardsPerRow?: number; cardsPerCol?: number; gap?: number }
  layouts?: Record<string, { col: number; row: number; w?: number; h?: number }>
}

function migrateLegacy(data: StoredLayout): {
  cardsPerRow: number
  cardsPerCol: number
  gap: number
  order: string[]
} {
  if (data.order) {
    return {
      cardsPerRow: data.cardsPerRow ?? DEFAULT_CARDS_PER_ROW,
      cardsPerCol: data.cardsPerCol ?? DEFAULT_CARDS_PER_COL,
      gap: data.gap ?? DEFAULT_GAP,
      order: data.order,
    }
  }
  // Legacy: derive an order from row-major sorted layouts so the user's
  // existing arrangement isn't randomized on first load after migration.
  if (data.layouts) {
    const sorted = Object.entries(data.layouts).sort(([, a], [, b]) =>
      a.row !== b.row ? a.row - b.row : a.col - b.col,
    )
    return {
      cardsPerRow: data.settings?.cardsPerRow ?? DEFAULT_CARDS_PER_ROW,
      cardsPerCol: data.settings?.cardsPerCol ?? DEFAULT_CARDS_PER_COL,
      gap: data.settings?.gap ?? DEFAULT_GAP,
      order: sorted.map(([id]) => id),
    }
  }
  return {
    cardsPerRow: DEFAULT_CARDS_PER_ROW,
    cardsPerCol: DEFAULT_CARDS_PER_COL,
    gap: DEFAULT_GAP,
    order: [],
  }
}

// ── Hook API ───────────────────────────────────────────────

export interface GridEngineSettings {
  cardsPerRow: number
  cardsPerCol: number
  gap: number
}

export interface GridEngine {
  // Order — committed sequence and active (preview-during-drag) sequence.
  order: string[]
  effectiveOrder: string[]
  previewOrder: string[] | null

  // Density knobs. `settings` is the effective responsive density; requestedSettings is the user preference.
  settings: GridEngineSettings
  requestedSettings: GridEngineSettings

  // Cell geometry derived from container size.
  cellW: number
  cellH: number
  gap: number

  // Single card-mode for all cells (derived from cellH); kept as a
  // function so the call site reads identically to the old per-id mode.
  getCardMode: (id: string) => CardMode

  // Back-compat: layouts derived from order so existing pixel-position
  // call sites in App.tsx (collapse/deploy animations) keep working.
  layouts: Record<string, GridRect>

  // Drag.
  dragState: DragState | null
  startDrag: (id: string, pointerX: number, pointerY: number, cardEl: HTMLElement) => void
  updateDrag: (pointerX: number, pointerY: number) => void
  endDrag: () => void
  cancelDrag: () => void

  // Grid container ref + geometry helpers.
  gridRef: React.RefObject<HTMLDivElement | null>
  gridRectToPixels: (rect: GridRect) => DOMRect
  indexOf: (id: string) => number

  // Profiles (shape unchanged — just stores order + cardsPerRow/Col + gap).
  saveProfile: (name: string) => Promise<void>
  loadProfile: (name: string) => Promise<void>
  deleteProfile: (name: string) => Promise<void>
  listProfiles: () => Promise<string[]>
}

export function useGridEngine(
  agentIds: string[],
  initial?: { cardsPerRow?: number; cardsPerCol?: number; gap?: number },
): GridEngine {
  const [cardsPerRow, setCardsPerRow] = useState<number>(
    initial?.cardsPerRow ?? DEFAULT_CARDS_PER_ROW,
  )
  const [cardsPerCol, setCardsPerCol] = useState<number>(
    initial?.cardsPerCol ?? DEFAULT_CARDS_PER_COL,
  )
  const [gap, setGap] = useState<number>(initial?.gap ?? DEFAULT_GAP)
  const [order, setOrder] = useState<string[]>([])
  const [previewOrder, setPreviewOrder] = useState<string[] | null>(null)
  const [dragState, setDragState] = useState<DragState | null>(null)
  const gridRef = useRef<HTMLDivElement | null>(null)
  const loadedRef = useRef(false)

  // Sync external knob changes from App.tsx (settings panel).
  useEffect(() => {
    if (initial?.cardsPerRow !== undefined) setCardsPerRow(initial.cardsPerRow)
  }, [initial?.cardsPerRow])
  useEffect(() => {
    if (initial?.cardsPerCol !== undefined) setCardsPerCol(initial.cardsPerCol)
  }, [initial?.cardsPerCol])
  useEffect(() => {
    if (initial?.gap !== undefined) setGap(initial.gap)
  }, [initial?.gap])

  // ── Cell dimensions — observed from the scroll container ──
  const [dims, setDims] = useState(() => ({
    cellW: Math.max(MIN_CELL_PX, (window.innerWidth - PADDING - (gap * 2) - (cardsPerRow - 1) * gap) / cardsPerRow),
    cellH: 220,
    effectiveCardsPerRow: cardsPerRow,
  }))

  useEffect(() => {
    const measure = () => {
      const el = gridRef.current
      const container = el?.parentElement
      if (!container) return
      const w = container.clientWidth
      const h = container.clientHeight
      const usableW = Math.max(0, w - (gap * 2))
      // User setting is a maximum column count. If the available width shrinks
      // (e.g. chat panel is open), reduce columns so cards keep a readable
      // minimum width instead of crushing content.
      const maxReadableCols = Math.max(1, Math.floor((usableW + gap) / (MIN_READABLE_CELL_W + gap)))
      const effectiveCardsPerRow = Math.max(1, Math.min(cardsPerRow, maxReadableCols))
      const cellW = Math.max(MIN_CELL_PX, (usableW - (effectiveCardsPerRow - 1) * gap) / effectiveCardsPerRow)
      // cardsPerCol determines visible rows: shrink cells so exactly N rows
      // fit. Extra cards wrap below the fold and the container scrolls.
      // GridContainer adds vertical padding to leave room for card glows. That
      // padding participates in the scroll height, so include it in the row
      // math; otherwise "3 rows" becomes 3 rows + padding and a tiny scrollbar
      // clips the bottom row.
      const verticalPadding = Math.min(gap, 8) * 2
      const cellH = Math.max(MIN_CELL_PX, (h - verticalPadding - (cardsPerCol - 1) * gap) / cardsPerCol)
      setDims(prev => (
        Math.abs(prev.cellW - cellW) < 1 &&
        Math.abs(prev.cellH - cellH) < 1 &&
        prev.effectiveCardsPerRow === effectiveCardsPerRow
      ) ? prev : { cellW, cellH, effectiveCardsPerRow })
    }
    requestAnimationFrame(() => requestAnimationFrame(measure))
    const ro = new ResizeObserver(measure)
    const container = gridRef.current?.parentElement
    if (container) ro.observe(container)
    window.addEventListener('resize', measure)
    return () => { ro.disconnect(); window.removeEventListener('resize', measure) }
  }, [cardsPerRow, cardsPerCol, gap])

  // ── Persistence (debounced) ──
  const persistTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const persist = useCallback(
    (nextOrder: string[], nextCpr: number, nextCpc: number, nextGap: number) => {
      if (persistTimer.current) clearTimeout(persistTimer.current)
      persistTimer.current = setTimeout(() => {
        fetch('/api/grid-layout', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            cardsPerRow: nextCpr,
            cardsPerCol: nextCpc,
            gap: nextGap,
            order: nextOrder,
          }),
        }).catch(() => {})
      }, 500)
    },
    [],
  )

  // ── Initial load (one-shot) ──
  useEffect(() => {
    fetch('/api/grid-layout')
      .then(r => (r.ok ? r.json() : null))
      .then((data: StoredLayout | null) => {
        if (data) {
          const m = migrateLegacy(data)
          // App.tsx-provided initial knobs win over stored values — the
          // settings panel is the source of truth at session level.
          if (initial?.cardsPerRow === undefined) setCardsPerRow(m.cardsPerRow)
          if (initial?.cardsPerCol === undefined) setCardsPerCol(m.cardsPerCol)
          if (initial?.gap === undefined) setGap(m.gap)
          setOrder(m.order)
        }
        loadedRef.current = true
      })
      .catch(() => {
        loadedRef.current = true
      })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // ── Sync agentIds → order (append new, prune missing) ──
  const agentIdSet = useMemo(() => new Set(agentIds), [agentIds])
  useEffect(() => {
    if (!loadedRef.current) return
    setOrder(prev => {
      const kept = prev.filter(id => agentIdSet.has(id))
      const seen = new Set(kept)
      const appended = [...kept]
      for (const id of agentIds) if (!seen.has(id)) appended.push(id)
      const same = appended.length === prev.length && appended.every((id, i) => prev[i] === id)
      if (same) return prev
      persist(appended, cardsPerRow, cardsPerCol, gap)
      return appended
    })
  }, [agentIds, agentIdSet, cardsPerRow, cardsPerCol, gap, persist])

  // ── Card mode (single value, derived from cellH) ──
  const effectiveCardsPerRow = dims.effectiveCardsPerRow
  const cardMode = useMemo(() => cardModeFor(dims.cellH), [dims.cellH])
  const getCardMode = useCallback((_id: string) => cardMode, [cardMode])

  // ── Effective (preview-during-drag or committed) order ──
  const effectiveOrder = previewOrder ?? order

  const indexOf = useCallback((id: string) => effectiveOrder.indexOf(id), [effectiveOrder])

  // Back-compat: build a layouts map from the effective order so existing
  // pixel-positioning call sites in App.tsx keep working without a rewrite.
  const layouts = useMemo<Record<string, GridRect>>(() => {
    const out: Record<string, GridRect> = {}
    for (let i = 0; i < effectiveOrder.length; i++) {
      const id = effectiveOrder[i]
      out[id] = {
        col: (i % effectiveCardsPerRow) + 1,
        row: Math.floor(i / effectiveCardsPerRow) + 1,
        w: 1,
        h: 1,
      }
    }
    return out
  }, [effectiveOrder, effectiveCardsPerRow])

  const gridRectToPixels = useCallback(
    (rect: GridRect): DOMRect => {
      const gridEl = gridRef.current
      const bounds = gridEl?.getBoundingClientRect() ?? new DOMRect(0, 0, window.innerWidth, window.innerHeight)
      const x = bounds.left + (rect.col - 1) * (dims.cellW + gap)
      const y = bounds.top + (rect.row - 1) * (dims.cellH + gap) - (gridEl?.scrollTop ?? 0)
      return new DOMRect(x, y, dims.cellW, dims.cellH)
    },
    [dims.cellW, dims.cellH, gap],
  )

  // ── Drag-to-reorder ────────────────────────────────────────

  const indexFromPointer = useCallback(
    (px: number, py: number, orderLen: number): number => {
      const el = gridRef.current
      if (!el) return 0
      const rect = el.getBoundingClientRect()
      const x = px - rect.left
      const y = py - rect.top + el.scrollTop
      const colSpan = dims.cellW + gap
      const rowSpan = dims.cellH + gap
      const col = Math.max(0, Math.min(effectiveCardsPerRow - 1, Math.floor(x / colSpan)))
      const row = Math.max(0, Math.floor(y / rowSpan))
      const i = row * effectiveCardsPerRow + col
      return Math.max(0, Math.min(orderLen - 1, i))
    },
    [effectiveCardsPerRow, dims.cellW, dims.cellH, gap],
  )

  const lastDropIdx = useRef(-1)

  const startDrag = useCallback((id: string, px: number, py: number, cardEl: HTMLElement) => {
    const cardRect = cardEl.getBoundingClientRect()
    lastDropIdx.current = -1
    setDragState({
      id,
      startPointer: { x: px, y: py },
      ghostOffset: { x: px - cardRect.left, y: py - cardRect.top },
    })
  }, [])

  const updateDrag = useCallback(
    (px: number, py: number) => {
      if (!dragState) return
      const targetIdx = indexFromPointer(px, py, order.length)
      if (targetIdx === lastDropIdx.current) return
      lastDropIdx.current = targetIdx
      const fromIdx = order.indexOf(dragState.id)
      if (fromIdx < 0 || fromIdx === targetIdx) {
        setPreviewOrder(null)
        return
      }
      const next = [...order]
      next.splice(fromIdx, 1)
      next.splice(targetIdx, 0, dragState.id)
      setPreviewOrder(next)
    },
    [dragState, order, indexFromPointer],
  )

  const endDrag = useCallback(() => {
    if (previewOrder) {
      setOrder(previewOrder)
      persist(previewOrder, cardsPerRow, cardsPerCol, gap)
    }
    setPreviewOrder(null)
    setDragState(null)
    lastDropIdx.current = -1
  }, [previewOrder, cardsPerRow, cardsPerCol, gap, persist])

  const cancelDrag = useCallback(() => {
    setPreviewOrder(null)
    setDragState(null)
    lastDropIdx.current = -1
  }, [])

  // Escape cancels drag.
  useEffect(() => {
    if (!dragState) return
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') cancelDrag()
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [dragState, cancelDrag])

  // ── Profiles ───────────────────────────────────────────────

  const saveProfile = useCallback(
    async (name: string) => {
      await fetch(`/api/grid-profiles/${encodeURIComponent(name)}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ cardsPerRow, cardsPerCol, gap, order }),
      })
    },
    [order, cardsPerRow, cardsPerCol, gap],
  )

  const loadProfile = useCallback(
    async (name: string) => {
      const res = await fetch(`/api/grid-profiles/${encodeURIComponent(name)}`)
      if (!res.ok) return
      const data: StoredLayout = await res.json()
      const m = migrateLegacy(data)
      setCardsPerRow(m.cardsPerRow)
      setCardsPerCol(m.cardsPerCol)
      setGap(m.gap)
      // Keep saved order for known agents, append unknowns at the end.
      const known = new Set(m.order)
      const merged = m.order.filter(id => agentIdSet.has(id))
      for (const id of agentIds) if (!known.has(id)) merged.push(id)
      setOrder(merged)
      persist(merged, m.cardsPerRow, m.cardsPerCol, m.gap)
    },
    [agentIds, agentIdSet, persist],
  )

  const deleteProfile = useCallback(async (name: string) => {
    await fetch(`/api/grid-profiles/${encodeURIComponent(name)}`, { method: 'DELETE' })
  }, [])

  const listProfiles = useCallback(async (): Promise<string[]> => {
    const res = await fetch('/api/grid-profiles')
    if (!res.ok) return []
    const data = await res.json()
    return data.profiles || []
  }, [])

  return {
    order,
    effectiveOrder,
    previewOrder,
    settings: { cardsPerRow: effectiveCardsPerRow, cardsPerCol, gap },
    requestedSettings: { cardsPerRow, cardsPerCol, gap },
    cellW: dims.cellW,
    cellH: dims.cellH,
    gap,
    getCardMode,
    layouts,
    dragState,
    startDrag,
    updateDrag,
    endDrag,
    cancelDrag,
    gridRef,
    gridRectToPixels,
    indexOf,
    saveProfile,
    loadProfile,
    deleteProfile,
    listProfiles,
  }
}
