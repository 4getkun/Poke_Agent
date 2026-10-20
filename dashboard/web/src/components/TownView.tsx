import { useEffect, useMemo, useRef, useState, type MouseEvent as ReactMouseEvent } from 'react'
import { createPortal } from 'react-dom'
import { AgentState, AgentMessage, stableId } from '../types'
import { useSpriteAnimation } from './spriteAnimations'
import { BusyBubble, DoneBubble } from './MessageAnimations'
import { deriveAgentState } from '../hooks/useAgentState'
import { AgentMenu } from './AgentMenu'
import { SpritePicker } from './SpritePicker'
import { ProjectInfo, renameAgent, RoleInfo, setSprite } from '../api'
import { capsFor, useRuntimeCapabilities } from '../utils/runtimes'
import { PixelSprite } from './PixelSprite'

// ── Grid geometry ──────────────────────────────────────────
// Source town.png is 544×480. Town geometry is user-tunable from Dev
// Settings; keep module-level vars so the existing pathfinding helpers read
// the current cell/crop without threading geometry through every call.
const SOURCE_W = 544
const SOURCE_H = 480
let CELL = 16
let CROP_LEFT = 0
let CROP_TOP = 0
let CROP_RIGHT = SOURCE_W
let CROP_BOTTOM = SOURCE_H
let COLS = (CROP_RIGHT - CROP_LEFT) / CELL
let ROWS = (CROP_BOTTOM - CROP_TOP) / CELL
let MAP_W = COLS * CELL
let MAP_H = ROWS * CELL
let VISIBLE_MIN_COL = 0
let VISIBLE_MIN_ROW = 0
let VISIBLE_MAX_COL = COLS
let VISIBLE_MAX_ROW = ROWS
let VISIBLE_COLS = COLS
let VISIBLE_ROWS = ROWS

type ViewportSize = { w: number; h: number }

const DEFAULT_TOWN_MASK_34X30: readonly string[] = [
  '###############...################',
  '###############...################',
  '###############...################',
  '.###.##########...################',
  '.##############...################',
  '.######.#######...################',
  '........................##bbbbbbb#', // theme-audit-ignore map mask
  '.........................bbbbbbbb#',
  '.........................bbbbbbbb#',
  '#######..................bbbbbbbb#',
  '#######..................bbbbbbbb#',
  '#############............bbbbbbbb#',
  '#############............bbbbbbbb#',
  '###############.....####.bbbbbbbb#',
  '....#####..####.....#####bbbbbbbb#', // theme-audit-ignore map mask
  '....#####..#.##.....##############',
  '...######..####.....##############',
  '...........................#......',
  '..................................',
  '..................................',
  '....#######.........#######.....##',
  '....#######.........#######.###.##',
  '....#######..####...#######.....##',
  '.####.#####..####...#######.....##',
  '...########..####.....######...###',
  '.............###############...###',
  '.............###############...###',
  '.............##................###',
  '.##############................###',
  '..#...#...........................',
]

function defaultTownMask(): string[] {
  if (COLS === 34 && ROWS === 30) return [...DEFAULT_TOWN_MASK_34X30]
  return Array.from({ length: ROWS }, () => '.'.repeat(COLS))
}

function updateTownGeometry(settings: TownGeometrySettings) {
  const cell = Math.max(4, Math.round(settings.cellSize || 16))
  const left = Math.max(0, Math.min(SOURCE_W - cell, Math.round(settings.cropLeft || 0)))
  const top = Math.max(0, Math.min(SOURCE_H - cell, Math.round(settings.cropTop || 0)))
  const maxRight = Math.max(left + cell, Math.min(SOURCE_W, Math.round(settings.cropRight || SOURCE_W)))
  const maxBottom = Math.max(top + cell, Math.min(SOURCE_H, Math.round(settings.cropBottom || SOURCE_H)))
  const maxCols = Math.max(1, Math.floor((maxRight - left) / cell))
  const maxRows = Math.max(1, Math.floor((maxBottom - top) / cell))
  CELL = cell
  CROP_LEFT = left
  CROP_TOP = top
  COLS = maxCols
  ROWS = maxRows
  CROP_RIGHT = CROP_LEFT + COLS * CELL
  CROP_BOTTOM = CROP_TOP + ROWS * CELL
  MAP_W = COLS * CELL
  MAP_H = ROWS * CELL
  VISIBLE_MIN_COL = 0
  VISIBLE_MIN_ROW = 0
  VISIBLE_MAX_COL = COLS
  VISIBLE_MAX_ROW = ROWS
  VISIBLE_COLS = COLS
  VISIBLE_ROWS = ROWS
}

function updateVisibleBounds(available: ViewportSize | null, visualScale: number, cellOffsetX: number, cellOffsetY: number, mapShiftX: number) {
  const scale = Math.max(0.001, visualScale)
  const unscaledW = (available?.w ?? MAP_W * scale) / scale
  const unscaledH = (available?.h ?? MAP_H * scale) / scale

  // Keep agents on fully visible cells inside the clipped viewport.
  // Account for the tunable cell offset so debug-grid offsets do not let
  // sprites path to cells that are technically inside the crop but off-screen.
  VISIBLE_MIN_COL = Math.max(0, Math.min(COLS - 1, Math.ceil((-mapShiftX - cellOffsetX) / CELL)))
  VISIBLE_MIN_ROW = Math.max(0, Math.min(ROWS - 1, Math.ceil((-cellOffsetY) / CELL)))
  VISIBLE_MAX_COL = Math.max(
    VISIBLE_MIN_COL + 1,
    Math.min(COLS, Math.floor((unscaledW - mapShiftX - cellOffsetX) / CELL)),
  )
  VISIBLE_MAX_ROW = Math.max(
    VISIBLE_MIN_ROW + 1,
    Math.min(ROWS, Math.floor((unscaledH - cellOffsetY) / CELL)),
  )
  VISIBLE_COLS = VISIBLE_MAX_COL - VISIBLE_MIN_COL
  VISIBLE_ROWS = VISIBLE_MAX_ROW - VISIBLE_MIN_ROW
}

// agent sprites render at this fixed pixel size, regardless of CELL. The
// sprite intentionally overflows its parent cell button; alignment is done by
// absolute-positioning + marginLeft so the cell's small width doesn't squish
// the image (which a flexbox parent would do).
const SPRITE_PX = 72
const TINY_SPRITE_MAX_PX = 18

// Wide sprites (reuniclus 46×27) look tiny with objectFit:contain in a square
// box because height shrinks to preserve aspect ratio. We detect wide sprites
// and scale their container so they render at the same visual prominence.
const spriteSizeCache: Record<string, { w: number; h: number; bounds?: { minX: number; minY: number; maxX: number; maxY: number } }> = {}
function spriteRenderSize(sprite: string): { w: number; h: number } {
  const cached = spriteSizeCache[sprite]
  if (cached && Math.max(cached.w, cached.h) <= TINY_SPRITE_MAX_PX) {
    // Some overworld sprites are intentionally tiny (e.g. Lotad). Don't blow
    // those up to card-avatar size on the minimap.
    return { w: cached.w, h: cached.h }
  }
  if (cached && cached.w > cached.h * 1.4) {
    return { w: Math.round(SPRITE_PX * (cached.w / cached.h)), h: SPRITE_PX }
  }
  return { w: SPRITE_PX, h: SPRITE_PX }
}
function preloadSpriteSize(sprite: string, onLoad?: () => void) {
  if (spriteSizeCache[sprite]) return
  const img = new window.Image()
  img.onload = () => {
    spriteSizeCache[sprite] = { w: img.naturalWidth, h: img.naturalHeight, bounds: computeAlphaBounds(img) }
    onLoad?.()
  }
  img.src = `/sprites/${sprite}.png`
}

function computeAlphaBounds(img: HTMLImageElement): { minX: number; minY: number; maxX: number; maxY: number } | undefined {
  const w = img.naturalWidth
  const h = img.naturalHeight
  if (!w || !h) return undefined
  const canvas = document.createElement('canvas')
  canvas.width = w
  canvas.height = h
  const ctx = canvas.getContext('2d', { willReadFrequently: true })
  if (!ctx) return undefined
  ctx.drawImage(img, 0, 0)
  const data = ctx.getImageData(0, 0, w, h).data
  let minX = w, minY = h, maxX = -1, maxY = -1
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      if (data[(y * w + x) * 4 + 3] > 8) {
        if (x < minX) minX = x
        if (x > maxX) maxX = x
        if (y < minY) minY = y
        if (y > maxY) maxY = y
      }
    }
  }
  return maxX >= 0 ? { minX, minY, maxX, maxY } : undefined
}

// Default mask: everything walkable. The crop already removes the
// trees/mountains border, so within the visible playable area the user paints
// obstacles (buildings, pond) and zones with the brush toolbar in debug mode.
// Module-level mutable copy of the mask. Hydrated from `/api/town-mask` on
// mount (if a saved version exists) and mutated in place when the user paints
// in debug mode. Pathfinding/spawn helpers read from this rather than the
// constant so paints take effect immediately without threading state through
// every setInterval closure.
let mutableMask: string[] = defaultTownMask()

// Walkable = anything that isn't a wall. Path and busy cells all let sprites
// pass through; only `#` blocks.
function walkable(col: number, row: number): boolean {
  if (col < 0 || col >= COLS || row < 0 || row >= ROWS) return false
  const line = mutableMask[row]
  if (!line) return false
  return line[col] !== '#'
}

function visible(col: number, row: number): boolean {
  return col >= VISIBLE_MIN_COL && col < VISIBLE_MAX_COL && row >= VISIBLE_MIN_ROW && row < VISIBLE_MAX_ROW
}

function visibleWalkable(col: number, row: number): boolean {
  return visible(col, row) && walkable(col, row)
}

// ── Cell-type vocabulary ───────────────────────────────────
//   '#' — blocked (not walkable)
//   '.' — walkable/default path
//   'b' — busy destination area
// Anything non-wall is walkable. Idle agents can wander anywhere walkable.

export type Brush = 'walkable' | 'block' | 'busy'

export const BRUSH_CHARS: Record<Brush, string> = {
  walkable: '.', block: '#', busy: 'b',
}

const BRUSH_COLORS: Record<Brush, string> = {
  walkable: 'var(--theme-accent-green)',
  block:    'var(--theme-accent-red)',
  busy:     'var(--theme-accent-blue)',
}

const BRUSH_LABELS: Record<Brush, string> = {
  walkable: 'PATH', block: 'WALL', busy: 'BUSY',
}

function cellChar(col: number, row: number): string {
  const line = mutableMask[row]
  if (!line) return '#'
  const ch = line[col] || '#'
  // Back-compat: old masks used 1/2/3 for busy and i for idle. Collapse them
  // into the simplified vocabulary at render/runtime.
  if (ch === '1' || ch === '2' || ch === '3') return 'b'
  if (ch === 'i') return '.'
  return ch
}

function colorForChar(ch: string): string {
  switch (ch) {
    case '#': return 'color-mix(in srgb, var(--theme-accent-red) 35%, transparent)'
    case 'b': return 'color-mix(in srgb, var(--theme-accent-blue) 28%, transparent)'
    default:  return 'transparent'
  }
}

function borderForChar(ch: string): string {
  switch (ch) {
    case '#': return '1px solid color-mix(in srgb, var(--theme-accent-red) 70%, transparent)'
    case 'b': return '1px solid color-mix(in srgb, var(--theme-accent-blue) 65%, transparent)'
    default:  return '1px solid var(--theme-panel-divider)'
  }
}

function listCells(matchChars: Set<string>): Cell[] {
  const out: Cell[] = []
  for (let r = VISIBLE_MIN_ROW; r < VISIBLE_MAX_ROW; r++) {
    const line = mutableMask[r] || ''
    for (let c = VISIBLE_MIN_COL; c < VISIBLE_MAX_COL; c++) {
      const ch = cellChar(c, r)
      if (matchChars.has(ch)) out.push({ col: c, row: r })
    }
  }
  return out
}

function allBusyCells(): Cell[] {
  return listCells(new Set(['b']))
}

function isBusyCell(c: Cell | null | undefined): boolean {
  return !!c && cellChar(c.col, c.row) === 'b'
}

// Hard-coded fallback when nothing is painted — front of the agent Center.
const FALLBACK_CENTER: Cell = { col: 12, row: 15 }

function fallbackCell(): Cell {
  return {
    col: Math.max(VISIBLE_MIN_COL, Math.min(VISIBLE_MAX_COL - 1, FALLBACK_CENTER.col)),
    row: Math.max(VISIBLE_MIN_ROW, Math.min(VISIBLE_MAX_ROW - 1, FALLBACK_CENTER.row)),
  }
}

function clampCell(c: Cell | null | undefined): Cell | null {
  if (!c) return null
  return {
    col: Math.max(VISIBLE_MIN_COL, Math.min(VISIBLE_MAX_COL - 1, c.col)),
    row: Math.max(VISIBLE_MIN_ROW, Math.min(VISIBLE_MAX_ROW - 1, c.row)),
  }
}

function nearestVisibleWalkable(c: Cell | null | undefined): Cell | null {
  const start = clampCell(c)
  if (!start) return null
  if (visibleWalkable(start.col, start.row)) return start
  const maxRadius = Math.max(VISIBLE_COLS, VISIBLE_ROWS)
  for (let radius = 1; radius <= maxRadius; radius++) {
    for (let row = start.row - radius; row <= start.row + radius; row++) {
      for (let col = start.col - radius; col <= start.col + radius; col++) {
        if (Math.abs(col - start.col) !== radius && Math.abs(row - start.row) !== radius) continue
        if (visibleWalkable(col, row)) return { col, row }
      }
    }
  }
  return null
}

function keepVisibleWalkable(c: Cell | null | undefined): Cell | null {
  return nearestVisibleWalkable(c) || randomWalkableCell()
}

function keepVisibleWalkableNonBusy(c: Cell | null | undefined): Cell | null {
  const start = keepVisibleWalkable(c)
  if (start && !isBusyCell(start)) return start
  return nearestVisibleWalkableNonBusy(start) || randomWalkableCell({ avoidBusy: true })
}

function nearestVisibleWalkableNonBusy(c: Cell | null | undefined): Cell | null {
  const start = clampCell(c)
  if (!start) return null
  if (visibleWalkable(start.col, start.row) && !isBusyCell(start)) return start
  const maxRadius = Math.max(VISIBLE_COLS, VISIBLE_ROWS)
  for (let radius = 1; radius <= maxRadius; radius++) {
    for (let row = start.row - radius; row <= start.row + radius; row++) {
      for (let col = start.col - radius; col <= start.col + radius; col++) {
        if (Math.abs(col - start.col) !== radius && Math.abs(row - start.row) !== radius) continue
        if (visibleWalkable(col, row) && cellChar(col, row) !== 'b') return { col, row }
      }
    }
  }
  return null
}

// ── Pathfinding (BFS on the walkable grid) ─────────────────

type Cell = { col: number; row: number }

function cellKey(c: Cell): string { return `${c.col},${c.row}` }
function manhattan(a: Cell, b: Cell): number { return Math.abs(a.col - b.col) + Math.abs(a.row - b.row) }

function neighbours(c: Cell): Cell[] {
  return [
    { col: c.col + 1, row: c.row },
    { col: c.col - 1, row: c.row },
    { col: c.col, row: c.row + 1 },
    { col: c.col, row: c.row - 1 },
  ].filter(n => visibleWalkable(n.col, n.row))
}

function idleNeighbours(c: Cell): Cell[] {
  return neighbours(c).filter(n => !isBusyCell(n))
}

/** Return next cell to step toward target, or null if no path. */
function stepToward(from: Cell, to: Cell, opts: { avoidBusy?: boolean } = {}): Cell | null {
  if (from.col === to.col && from.row === to.row) return null
  const start = keepVisibleWalkable(from)
  const target = keepVisibleWalkable(to)
  if (!start || !target) return null
  if (start.col === target.col && start.row === target.row) return null
  const queue: Cell[] = [start]
  const parent = new Map<string, Cell>()
  parent.set(cellKey(start), start)
  while (queue.length > 0) {
    const cur = queue.shift()!
    if (cur.col === target.col && cur.row === target.row) {
      // Walk back via parents to find first step
      let step = cur
      while (parent.get(cellKey(step)) && cellKey(parent.get(cellKey(step))!) !== cellKey(start)) {
        step = parent.get(cellKey(step))!
      }
      return step
    }
    for (const n of neighbours(cur)) {
      if (opts.avoidBusy && isBusyCell(n) && cellKey(n) !== cellKey(target)) continue
      if (!parent.has(cellKey(n))) {
        parent.set(cellKey(n), cur)
        queue.push(n)
      }
    }
  }
  return null
}

// Random spawn point.
function randomWalkableCell(opts: { avoidBusy?: boolean } = {}): Cell {
  for (let i = 0; i < 200; i++) {
    const c = VISIBLE_MIN_COL + Math.floor(Math.random() * VISIBLE_COLS)
    const r = VISIBLE_MIN_ROW + Math.floor(Math.random() * VISIBLE_ROWS)
    if (visibleWalkable(c, r) && (!opts.avoidBusy || cellChar(c, r) !== 'b')) return { col: c, row: r }
  }
  if (opts.avoidBusy) {
    const nonBusy = nearestVisibleWalkableNonBusy(fallbackCell())
    if (nonBusy) return nonBusy
  }
  return keepVisibleWalkable(fallbackCell()) || fallbackCell()
}

// ── Sprite model ───────────────────────────────────────────

interface TownSprite {
  id: string            // pokegent_id / stableId
  sprite: string
  displayName: string
  agentState: AgentState['state']  // 'idle', 'busy', etc.
  taskGroup: string
  pos: Cell
  target: Cell | null
  facing: 'left' | 'right'
  nextMoveAt: number
  // Duration the CSS transform-transition uses to animate the current move.
  // Tracked per-sprite so cross-zone transit (busy↔idle journeys) animates at
  // STEP_MS_TRANSIT while idle-ambling stays at STEP_MS_IDLE — without this
  // the render would fall back to a state-only ternary and visuals would
  // desync from the real step timing.
  stepMs: number
  // Mail delivery: sprint to recipient then return to original position
  mailTarget?: Cell | null
  mailReturn?: Cell | null
}

function isBusy(s: AgentState['state']) {
  return s === 'busy' || s === 'needs_input' || s === 'permission' || s === 'waiting'
}

// ── Component ──────────────────────────────────────────────

interface TownViewProps {
  agents: AgentState[]
  onSelect: (agent: AgentState) => void
  selectedId: string | null
  debug?: boolean
  newMessage?: AgentMessage | null
  geometry?: Partial<TownGeometrySettings>
  editorOpen?: boolean
  onCloseEditor?: () => void
  onSaveGeometry?: (geometry: TownGeometrySettings) => void
  projects?: ProjectInfo[]
  roles?: RoleInfo[]
  existingGroups?: string[]
}

interface TownGeometrySettings {
  scale: number
  cellSize: number
  cellOffsetX: number
  cellOffsetY: number
  cropLeft: number
  cropTop: number
  cropRight: number
  cropBottom: number
}

function normalizedGeometry(geometry?: Partial<TownGeometrySettings>): TownGeometrySettings {
  return {
    scale: geometry?.scale ?? 1,
    cellSize: geometry?.cellSize ?? 16,
    cellOffsetX: geometry?.cellOffsetX ?? 0,
    cellOffsetY: geometry?.cellOffsetY ?? 0,
    cropLeft: geometry?.cropLeft ?? 0,
    cropTop: geometry?.cropTop ?? 0,
    cropRight: geometry?.cropRight ?? SOURCE_W,
    cropBottom: geometry?.cropBottom ?? SOURCE_H,
  }
}

function TownEditorSlider({ label, value, min, max, step, unit, onChange }: {
  label: string
  value: number
  min: number
  max: number
  step: number
  unit?: string
  onChange: (value: number) => void
}) {
  return (
    <div style={{ display: 'grid', gridTemplateColumns: '72px 1fr 54px', alignItems: 'center', gap: 8 }}>
      <label style={{ color: 'var(--theme-text-secondary)', fontSize: 'var(--theme-type-m)' }}>{label}</label>
      <input
        type="range"
        min={min}
        max={max}
        step={step}
        value={value}
        onChange={e => onChange(Number(e.target.value))}
        style={{ width: '100%' }}
      />
      <span style={{ color: 'var(--theme-text-muted)', fontSize: 'var(--theme-type-m)', textAlign: 'right' }}>{value}{unit || ''}</span>
    </div>
  )
}

// Mail-carry fields (mailTarget, mailReturn) live on TownSprite.
// When set, the sprite sprints to the recipient and back using the normal
// movement tick — no separate clone component needed.

// Per-state step durations. Idle pokes amble (slower step + longer wander
// cooldowns); during state transitions and once at a busy station they move
// fast so the trip between zones reads as a sprint, not a saunter.
const STEP_MS_IDLE = 225       // each idle-wander step within the idle area
const STEP_MS_TRANSIT = 30    // cross-zone travel: idle→busy or busy→idle
const TRANSIT_STEPS_PER_TICK = 1 // keep transit to one cell per CSS transition; multi-cell updates read as teleporting
const IDLE_COOLDOWN_MIN = 850
const IDLE_COOLDOWN_MAX = 1400

// Movement tick rate. Must be ≤ the smallest STEP_MS we ever schedule —
// otherwise the loop becomes the floor and a sprite that "should" step every
// Keep the town card cheap. 30ms was effectively a 33fps React setState loop
// over every agent and made typing/clicking laggy in Chrome.
const TICK_MS = 30

export function TownView({ agents, onSelect, selectedId, debug = false, newMessage, geometry, editorOpen = false, onCloseEditor, onSaveGeometry, projects, roles, existingGroups }: TownViewProps) {
  const wrapRef = useRef<HTMLDivElement>(null)
  const [available, setAvailable] = useState<ViewportSize | null>(null)
  const [draftGeometry, setDraftGeometry] = useState<TownGeometrySettings>(() => normalizedGeometry(geometry))
  const savedGeometryKey = `${geometry?.scale ?? 1}:${geometry?.cellSize ?? 16}:${geometry?.cellOffsetX ?? 0}:${geometry?.cellOffsetY ?? 0}:${geometry?.cropLeft ?? 0}:${geometry?.cropTop ?? 0}:${geometry?.cropRight ?? SOURCE_W}:${geometry?.cropBottom ?? SOURCE_H}`
  useEffect(() => {
    if (editorOpen) setDraftGeometry(normalizedGeometry(geometry))
  }, [editorOpen, savedGeometryKey])
  const effectiveGeometry = editorOpen ? draftGeometry : normalizedGeometry(geometry)
  updateTownGeometry(effectiveGeometry)
  const manualScale = Math.max(0.1, effectiveGeometry.scale ?? 1)
  const fitScale = available
    ? Math.max(available.w / MAP_W, available.h / MAP_H)
    : 1
  // Always fill the town viewport. The editor Scale is a zoom multiplier on
  // top of fit-to-card, not a way to shrink below the available card space.
  const visualScale = fitScale * Math.max(1, manualScale)
  const cellOffsetX = effectiveGeometry.cellOffsetX ?? 0
  const cellOffsetY = effectiveGeometry.cellOffsetY ?? 0
  const viewportW = available?.w ?? MAP_W * visualScale
  const viewportH = available?.h ?? MAP_H * visualScale
  // Anchor the scaled map to the right edge of the viewport so horizontal
  // shrinking crops the left side first. The busy area lives on the right.
  const mapShiftX = Math.min(0, (viewportW / visualScale) - MAP_W)
  updateVisibleBounds(available, visualScale, cellOffsetX, cellOffsetY, mapShiftX)
  const geometryKey = `${CELL}:${CROP_LEFT}:${CROP_TOP}:${CROP_RIGHT}:${CROP_BOTTOM}:${visualScale}:${cellOffsetX}:${cellOffsetY}:${mapShiftX}:${VISIBLE_MIN_COL}:${VISIBLE_MIN_ROW}:${VISIBLE_MAX_COL}:${VISIBLE_MAX_ROW}`
  const [sprites, setSprites] = useState<Record<string, TownSprite>>({})
  const spritesRef = useRef(sprites)
  spritesRef.current = sprites

  // Mail-carry: when an agent sends a message, sprint to recipient then return
  const seenMsgIds = useRef(new Set<string>())

  useEffect(() => {
    if (!newMessage || seenMsgIds.current.has(newMessage.id)) return
    seenMsgIds.current.add(newMessage.id)
    const cur = spritesRef.current
    const fromSprite = Object.values(cur).find(s => s.id === newMessage.from)
    const toSprite = Object.values(cur).find(s => s.id === newMessage.to)
    if (!fromSprite || !toSprite) return
    setSprites(prev => {
      const s = prev[newMessage.from]
      if (!s) return prev
      return { ...prev, [newMessage.from]: {
        ...s,
        mailTarget: { ...toSprite.pos },
        mailReturn: { ...s.pos },
        target: { ...toSprite.pos },
        nextMoveAt: Date.now(),
        stepMs: STEP_MS_TRANSIT,
      }}
    })
  }, [newMessage])

  // Walkable mask, fetched from server on mount.
  const [mask, setMask] = useState<string[]>(() => mutableMask)
  // Gate sprite spawning on the mask being loaded so a fresh refresh doesn't
  // scatter sprites onto cells the user has painted as walls.
  const [maskReady, setMaskReady] = useState(false)
  useEffect(() => {
    fetch('/api/town-mask')
      .then(r => (r.status === 204 ? null : r.json()))
      .then(data => {
        if (
          data &&
          Array.isArray(data.mask) &&
          data.mask.length === ROWS &&
          data.mask.every((row: unknown) => typeof row === 'string' && row.length === COLS)
        ) {
          mutableMask = data.mask
          setMask(data.mask)
        } else {
          const next = defaultTownMask()
          mutableMask = next
          setMask(next)
        }
      })
      .catch(() => { /* keep default */ })
      .finally(() => setMaskReady(true))
  }, [geometryKey])

  useEffect(() => {
    if (mask.length !== ROWS || mask.some(row => row.length !== COLS)) {
      const next = defaultTownMask()
      mutableMask = next
      setMask(next)
      setSprites({})
    }
  }, [geometryKey, mask])

  const persistTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const saveMaskNow = (next: string[]) => {
    if (persistTimer.current) {
      clearTimeout(persistTimer.current)
      persistTimer.current = null
    }
    fetch('/api/town-mask', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ cols: COLS, rows: ROWS, mask: next }),
    }).catch(() => {})
  }

  // Active brush + drag-paint plumbing. paintingRef tracks whether the mouse
  // is held; activeBrush picks which char to paint with. Default to `block`
  // so the user's first click on a default-walkable cell does something
  // visible (otherwise walkable→walkable is a no-op and the UI looks broken).
  const [activeBrush, setActiveBrush] = useState<Brush>('block')
  const paintingRef = useRef(false)
  const activeBrushRef = useRef(activeBrush)
  activeBrushRef.current = activeBrush
  const [editorPos, setEditorPos] = useState({ x: 12, y: 12 })
  const editorDragRef = useRef<{ dx: number; dy: number } | null>(null)
  const [savedFlash, setSavedFlash] = useState(false)
  const [menuAgent, setMenuAgent] = useState<AgentState | null>(null)
  const [menuPos, setMenuPos] = useState({ x: 0, y: 0 })
  const [spritePickerAgent, setSpritePickerAgent] = useState<AgentState | null>(null)
  const allCaps = useRuntimeCapabilities()

  useEffect(() => {
    const move = (e: MouseEvent) => {
      const drag = editorDragRef.current
      if (!drag) return
      setEditorPos({ x: Math.max(0, e.clientX - drag.dx), y: Math.max(0, e.clientY - drag.dy) })
    }
    const up = () => { editorDragRef.current = null }
    document.addEventListener('mousemove', move)
    document.addEventListener('mouseup', up)
    return () => {
      document.removeEventListener('mousemove', move)
      document.removeEventListener('mouseup', up)
    }
  }, [])

  // Stop painting on mouseup anywhere — robust against the user dragging out
  // of the map and releasing.
  useEffect(() => {
    if (!debug) return
    const stop = () => { paintingRef.current = false }
    document.addEventListener('mouseup', stop)
    return () => document.removeEventListener('mouseup', stop)
  }, [debug])

  const paintCell = (c: number, r: number) => {
    setMask(prev => {
      const next = [...prev]
      const line = next[r]
      if (!line || c < 0 || c >= COLS) return prev
      const ch = BRUSH_CHARS[activeBrushRef.current]
      if (line[c] === ch) return prev
      next[r] = line.substring(0, c) + ch + line.substring(c + 1)
      mutableMask = next
      return next
    })
  }

  // Sync sprites with the agents list. Gated on `maskReady` so sprites don't
  // spawn before the saved mask loads — otherwise a refresh would scatter
  // them onto cells the user has since painted as walls.
  useEffect(() => {
    if (!maskReady) return
    setSprites(prev => {
      const next: Record<string, TownSprite> = {}
      const now = Date.now()

      for (const a of agents) {
        const id = stableId(a)
        const taskGroup = a.task_group || ''
        const existing = prev[id]
        const newState = a.state
        const wasBusy = existing ? isBusy(existing.agentState) : false
        const nowBusy = isBusy(newState)
        const nowDone = newState === 'done'

        if (existing) {
          let target = existing.target
          let resetIdleCooldown = false
          // Re-target on busy/idle transition. Random pick (not [0]) so
          // multiple agents flipping busy at once spread across painted tiles
          // rather than all converging on the same first-listed cell. The
          // tick re-checks each frame, so this is just a snappy first hint.
          const stateFlipped = nowBusy !== wasBusy
          if (nowBusy && !wasBusy) {
            const busyCells = allBusyCells()
            target = busyCells.length > 0
              ? busyCells[Math.floor(Math.random() * busyCells.length)]
              : fallbackCell()
          } else if (!nowBusy && wasBusy) {
            target = isBusyCell(existing.pos) ? nearestVisibleWalkableNonBusy(existing.pos) : null
          }
          // If existing pos is on a wall (e.g. mask just got repainted),
          // re-spawn the sprite somewhere walkable.
          let pos = keepVisibleWalkable(existing.pos) || randomWalkableCell()
          if (nowDone) {
            target = null
          } else if (nowBusy) {
            target = keepVisibleWalkable(target)
          } else {
            target = target ? keepVisibleWalkableNonBusy(target) : null
            // Idle agents should only amble locally. Clear stale/random
            // long-distance targets from older logic or geometry changes so
            // they don't glide across the entire town like they're busy.
            if (target && !isBusyCell(pos) && manhattan(pos, target) > 3) {
              target = null
              resetIdleCooldown = true
            }
          }
          const nextMoveAt = stateFlipped
            ? (target ? now : now + randomCooldown())
            : resetIdleCooldown
              ? now + randomCooldown()
              : existing.nextMoveAt
          next[id] = {
            ...existing,
            pos,
            sprite: a.sprite || 'pokeball',
            displayName: a.display_name || a.profile_name,
            agentState: newState,
            taskGroup,
            target,
            // On state flip, cancel any pending idle cooldown so the agent
            // starts moving toward the new zone NOW. Previously a sprite
            // mid-amble could be sitting on a 7s wander cooldown when its
            // state changed — making the entire transition feel laggy even
            // though the per-step speed was fast.
            nextMoveAt,
          }
        } else {
          const spawn = nowBusy ? randomWalkableCell() : randomWalkableCell({ avoidBusy: true })
          const busyCells = nowBusy ? allBusyCells() : []
          // Random pick (not [0]) so freshly-spawned busy agents distribute
          // across painted busy tiles instead of all converging on the first.
          const initialBusyTarget = busyCells.length > 0
            ? busyCells[Math.floor(Math.random() * busyCells.length)]
            : fallbackCell()
          next[id] = {
            id,
            sprite: a.sprite || 'pokeball',
            displayName: a.display_name || a.profile_name,
            agentState: newState,
            taskGroup,
            pos: spawn,
            target: nowDone ? null : (nowBusy ? initialBusyTarget : null),
            facing: 'right',
            nextMoveAt: nowBusy ? now + 300 + Math.random() * 600 : now + randomCooldown(),
            stepMs: nowBusy ? STEP_MS_TRANSIT : STEP_MS_IDLE,
          }
        }
      }
      return next
    })
  }, [agents, maskReady, geometryKey, mask])

  // Movement tick — every STEP_MS, advance sprites by one cell toward target.
  useEffect(() => {
    const interval = setInterval(() => {
      setSprites(prev => {
        const now = Date.now()
        const next: Record<string, TownSprite> = { ...prev }
        let changed = false

        // Pool of painted busy cells. Busy agents pick
        // any free cell from the pool so multiple busy sprites distribute
        // instead of stacking on the first tile.
        const busyPool = allBusyCells()
        const claimedBusy = new Set<string>()

        for (const id of Object.keys(next)) {
          const s = next[id]
          const busy = isBusy(s.agentState)
          const done = s.agentState === 'done'

          const isDelivering = !!s.mailTarget || !!s.mailReturn
          const safePos = busy
            ? keepVisibleWalkable(s.pos)
            : keepVisibleWalkableNonBusy(s.pos)
          if (safePos && (safePos.col !== s.pos.col || safePos.row !== s.pos.row)) {
            const step = stepToward(s.pos, safePos, { avoidBusy: !busy && !isDelivering })
            if (step) {
              const facing: 'left' | 'right' = step.col < s.pos.col ? 'left' : step.col > s.pos.col ? 'right' : s.facing
              next[id] = {
                ...s,
                pos: step,
                facing,
                target: safePos,
                nextMoveAt: now + STEP_MS_TRANSIT,
                stepMs: STEP_MS_TRANSIT,
              }
            } else {
              next[id] = {
                ...s,
                pos: safePos,
                target: busy ? s.target : null,
                nextMoveAt: now,
                stepMs: STEP_MS_TRANSIT,
              }
            }
            changed = true
            continue
          }

          if (done && !isDelivering) {
            if (s.target) {
              next[id] = { ...s, target: null, stepMs: STEP_MS_IDLE }
              changed = true
            }
            continue
          }

          if (busy && !isDelivering) {
            const targetIsBusy = s.target ? cellChar(s.target.col, s.target.row) === 'b' : false

            if (busyPool.length === 0) {
              // No busy stations painted — fall back to a fixed cell.
              s.target = fallbackCell()
            } else if (!targetIsBusy) {
              // Pick a RANDOM free busy cell so agents distribute across the
              // painted area. (Was first-found, which funneled all overflow
              // agents onto busyPool[0].)
              const free = busyPool.filter(c =>
                !claimedBusy.has(cellKey(c)) && !isOccupied(next, c, id))
              const pick = free.length > 0
                ? free[Math.floor(Math.random() * free.length)]
                : busyPool[Math.floor(Math.random() * busyPool.length)]
              s.target = pick
              claimedBusy.add(cellKey(pick))
            } else if (s.target) {
              claimedBusy.add(cellKey(s.target))
            }
          }

          // Step delay:
          //  - busy traveling toward a busy cell: sprint
          //  - idle: amble anywhere walkable
          // The two transit modes share STEP_MS_TRANSIT — both feel like a
          // dash between zones, vs the slow STEP_MS_IDLE wander.
          const stepDelay = busy || isDelivering
            ? STEP_MS_TRANSIT
            : STEP_MS_IDLE

          // If at target:
          if (s.target && s.pos.col === s.target.col && s.pos.row === s.target.row) {
            // Mail delivery: arrived at recipient → sprint back to origin
            if (s.mailTarget && s.pos.col === s.mailTarget.col && s.pos.row === s.mailTarget.row && s.mailReturn) {
              next[id] = { ...s, target: s.mailReturn, mailTarget: undefined, mailReturn: undefined, nextMoveAt: now, stepMs: STEP_MS_TRANSIT }
              changed = true
              continue
            }
            // Mail return complete: clear and resume normal behavior
            if (!busy) {
              if (isBusyCell(s.pos)) {
                const escape = pickIdleStep(s.pos) || randomWalkableCell({ avoidBusy: true })
                next[id] = { ...s, target: escape, nextMoveAt: now, stepMs: STEP_MS_TRANSIT }
                changed = true
                continue
              }
              // Idle wander complete. Pause for a few seconds before picking
              // another one/two-step amble so idle agents don't look busy.
              next[id] = { ...s, target: null, nextMoveAt: now + randomCooldown(), stepMs: STEP_MS_IDLE }
              changed = true
            }
            // Busy + arrived at station: stay put (no pacing).
            continue
          }

          // If we have a target, step closer (pathfinding)
          if (s.target && now >= s.nextMoveAt) {
            const isTransit = stepDelay === STEP_MS_TRANSIT
            const steps = isTransit ? TRANSIT_STEPS_PER_TICK : 1
            let cur = s
            let stepped = false
            for (let i = 0; i < steps; i++) {
              if (!cur.target || (cur.pos.col === cur.target.col && cur.pos.row === cur.target.row)) break
              const step = stepToward(cur.pos, cur.target, { avoidBusy: !busy && !isDelivering })
              if (!step) break
              const facing: 'left' | 'right' = step.col < cur.pos.col ? 'left' : step.col > cur.pos.col ? 'right' : cur.facing
              cur = { ...cur, pos: step, facing }
              stepped = true
            }
            if (stepped) {
              next[id] = { ...cur, nextMoveAt: now + stepDelay, stepMs: stepDelay }
              changed = true
            } else {
              // No path — wait and retry
              next[id] = { ...s, nextMoveAt: now + 800 }
              changed = true
            }
            continue
          }

          // No target, idle — schedule a wander
          if (!s.target && !busy && now >= s.nextMoveAt) {
            if (isBusyCell(s.pos)) {
              const escape = pickIdleStep(s.pos) || randomWalkableCell({ avoidBusy: true })
              next[id] = { ...s, target: escape, nextMoveAt: now, stepMs: STEP_MS_TRANSIT }
              changed = true
              continue
            }
            const next_ = pickIdleWanderTarget(s.pos)
            if (next_) {
              next[id] = {
                ...s,
                target: next_,
                nextMoveAt: now + randomCooldown(),
                stepMs: stepDelay,
              }
              changed = true
            }
          }
        }

        return changed ? next : prev
      })
    }, TICK_MS)
    return () => clearInterval(interval)
  }, [geometryKey])

  // Measure parent viewport. This only clips what portion of the already-defined
  // town grid is visible; it must not affect cell/crop logic or persisted mask
  // dimensions.
  useEffect(() => {
    const measure = () => {
      const parent = wrapRef.current?.parentElement
      if (!parent) return
      const w = parent.clientWidth
      const h = parent.clientHeight
      if (w < 10 || h < 10) return
      // Leave room for the town frame so it doesn't get clipped by parent overflow.
      const PAD = 16
      const next = { w: Math.max(1, w - PAD), h: Math.max(1, h - PAD) }
      setAvailable(prev => (prev && prev.w === next.w && prev.h === next.h) ? prev : next)
    }
    // Two rAFs so flexbox has time to settle before first measure
    requestAnimationFrame(() => requestAnimationFrame(measure))
    const ro = new ResizeObserver(measure)
    const parent = wrapRef.current?.parentElement
    if (parent) ro.observe(parent)
    window.addEventListener('resize', measure)
    return () => { ro.disconnect(); window.removeEventListener('resize', measure) }
  }, [geometry?.cellSize, geometry?.cropLeft, geometry?.cropTop, geometry?.cropRight, geometry?.cropBottom])

  const spriteList = useMemo(() => Object.values(sprites), [sprites])

  const openAgentMenu = (e: ReactMouseEvent, id: string) => {
    e.preventDefault()
    e.stopPropagation()
    const agent = agents.find(a => stableId(a) === id)
    if (!agent) return
    setMenuAgent(agent)
    setMenuPos({ x: e.clientX, y: e.clientY })
  }

  return (
    <div
      ref={wrapRef}
      className="relative flex items-center justify-center overflow-hidden"
      style={{
        width: viewportW,
        height: viewportH,
        maxWidth: '100%',
        maxHeight: '100%',
        // Tiny frame around the map image itself — the inner box-shadow used to
        // do this but got clipped by overflow-hidden. Border lives on the outer
        // wrapper now so it actually renders.
        border: '3px solid var(--theme-panel-border)',
        boxShadow: 'var(--theme-shadow-panel)',
        borderRadius: 4,
      }}
    >
      {editorOpen && (
        <div
          data-no-drag
          style={{
            position: 'fixed',
            top: editorPos.y,
            left: editorPos.x,
            zIndex: 10000,
            width: 280,
            background: 'var(--theme-panel-bg)',
            padding: 8,
            borderRadius: 6,
            border: '1px solid var(--theme-panel-border)',
            fontFamily: 'var(--theme-font-mono)',
            fontSize: 'var(--theme-type-m)',
            userSelect: 'none',
            boxShadow: 'var(--theme-shadow-strong)',
          }}
          onMouseDown={(e) => e.stopPropagation()}
        >
          <div
            style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8, cursor: 'move' }}
            onMouseDown={(e) => {
              e.stopPropagation()
              const rect = (e.currentTarget.parentElement as HTMLElement).getBoundingClientRect()
              editorDragRef.current = { dx: e.clientX - rect.left, dy: e.clientY - rect.top }
            }}
          >
            <strong style={{ color: 'var(--theme-text-primary)', fontSize: 'var(--theme-type-l)', flex: 1 }}>Town editor</strong>
            <button
              onClick={(e) => { e.stopPropagation(); onCloseEditor?.() }}
              style={{ color: 'var(--theme-text-secondary)', border: '1px solid var(--theme-panel-divider)', borderRadius: 3, padding: '1px 6px' }}
            >
              ×
            </button>
          </div>

          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 4, marginBottom: 10 }}>
            {(['walkable','block','busy'] as Brush[]).map(b => {
              const active = activeBrush === b
              return (
                <button
                  key={b}
                  onClick={(e) => { e.stopPropagation(); setActiveBrush(b) }}
                  title={`Paint as ${BRUSH_LABELS[b]} (${BRUSH_CHARS[b]})`}
                  style={{
                    padding: '4px 5px',
                    border: active ? '1px solid var(--theme-accent-yellow)' : '1px solid var(--theme-panel-divider)',
                    background: active ? BRUSH_COLORS[b] : 'transparent',
                    color: active ? 'var(--theme-text-primary)' : 'var(--theme-text-secondary)',
                    fontFamily: 'var(--theme-font-mono)',
                    fontSize: 'var(--theme-type-m)',
                    cursor: 'pointer',
                    borderRadius: 2,
                    letterSpacing: 0.5,
                  }}
                >
                  {BRUSH_LABELS[b]}
                </button>
              )
            })}
          </div>

          <div style={{ display: 'grid', gap: 6, marginBottom: 10 }}>
            <TownEditorSlider label="Scale" value={draftGeometry.scale} min={0.5} max={3} step={0.05} unit="×" onChange={scale => setDraftGeometry(g => ({ ...g, scale }))} />
            <TownEditorSlider label="Cell" value={draftGeometry.cellSize} min={8} max={32} step={1} unit="px" onChange={cellSize => setDraftGeometry(g => ({ ...g, cellSize }))} />
            <TownEditorSlider label="Offset X" value={draftGeometry.cellOffsetX} min={-64} max={64} step={1} unit="px" onChange={cellOffsetX => setDraftGeometry(g => ({ ...g, cellOffsetX }))} />
            <TownEditorSlider label="Offset Y" value={draftGeometry.cellOffsetY} min={-64} max={64} step={1} unit="px" onChange={cellOffsetY => setDraftGeometry(g => ({ ...g, cellOffsetY }))} />
          </div>

          <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <button
              onClick={(e) => {
                e.stopPropagation()
                saveMaskNow(mask)
                onSaveGeometry?.(draftGeometry)
                setSavedFlash(true)
                window.setTimeout(() => setSavedFlash(false), 1200)
              }}
              className="gba-button"
              style={{ fontSize: 'var(--theme-type-m)', padding: '5px 8px', flex: 1 }}
            >
              Save town config
            </button>
            {savedFlash && <span style={{ color: 'var(--theme-accent-green)', fontSize: 'var(--theme-type-m)' }}>saved</span>}
          </div>
        </div>
      )}
      <div
        style={{
          width: MAP_W,
          height: MAP_H,
          transform: `scale(${visualScale})`,
          transformOrigin: 'top left',
          position: 'absolute',
          left: mapShiftX * visualScale,
          top: 0,
          imageRendering: 'pixelated',
          // background-image with negative position is the most robust way to
          // render a cropped sub-region of an image — naturally clipped to
          // element bounds, no img/overflow shenanigans.
          backgroundImage: `url(/town.png)`,
          backgroundPosition: `-${CROP_LEFT}px -${CROP_TOP}px`,
          backgroundSize: `${SOURCE_W}px ${SOURCE_H}px`,
          backgroundRepeat: 'no-repeat',
          overflow: 'hidden',
        }}
      >

        {/* Debug overlay: cell-type mask. Cells are drag-paintable with the
            currently selected brush; colors mirror the brush palette so what
            you paint is what shows.
            data-no-drag stops the parent grid cell from interpreting the drag
            as a card move (GridCell scans closest('[data-no-drag]')). */}
        {debug && mask && (
          <div data-no-drag style={{ position: 'absolute', inset: 0 }}>
            {Array.from({ length: VISIBLE_ROWS }).map((_, rowIdx) => {
              const r = VISIBLE_MIN_ROW + rowIdx
              return Array.from({ length: VISIBLE_COLS }).map((__, colIdx) => {
                const c = VISIBLE_MIN_COL + colIdx
                const ch = cellChar(c, r)
                return (
                  <div
                    key={`dbg-${c}-${r}`}
                    onMouseDown={(e) => {
                      e.stopPropagation(); e.preventDefault()
                      paintingRef.current = true
                      paintCell(c, r)
                    }}
                    onMouseEnter={() => {
                      if (paintingRef.current) paintCell(c, r)
                    }}
                    title={`(${c},${r}) [${ch}] — drag to paint with ${BRUSH_LABELS[activeBrush]}`}
                    style={{
                      position: 'absolute',
                      left: cellOffsetX + c * CELL,
                      top: cellOffsetY + r * CELL,
                      width: CELL,
                      height: CELL,
                      background: colorForChar(ch),
                      border: borderForChar(ch),
                      boxSizing: 'border-box',
                      fontFamily: 'var(--theme-font-mono)',
                      fontSize: 'var(--theme-type-m)',
                      color: 'var(--theme-text-inverse)',
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'center',
                      cursor: 'crosshair',
                      pointerEvents: 'auto',
                      userSelect: 'none',
                    }}
                  >
                    {ch === '.' || ch === '#' ? '' : ch.toUpperCase()}
                  </div>
                )
              })
            })}
          </div>
        )}

        {/* Sprites */}
        {spriteList.map(s => {
          const px = cellOffsetX + s.pos.col * CELL
          const py = cellOffsetY + s.pos.row * CELL
          const selected = selectedId === s.id
          const atTarget = !s.target || (s.pos.col === s.target.col && s.pos.row === s.target.row)
          const inTransit = !atTarget && s.stepMs === STEP_MS_TRANSIT
          const agentStatus = deriveAgentState({ state: s.agentState })
          const isDone = agentStatus.isDone
          const glow: 'busy' | 'done' | null = isDone ? 'done' : agentStatus.isBusy ? 'busy' : null
          return (
            <button
              key={s.id}
              onClick={(e) => { e.stopPropagation(); spriteClick(agents, s.id, onSelect) }}
              onContextMenuCapture={(e) => openAgentMenu(e, s.id)}
              onContextMenu={(e) => openAgentMenu(e, s.id)}
              data-no-drag
              title={s.displayName}
              className="group"
              style={{
                position: 'absolute',
                left: 0,
                top: 0,
                width: CELL,
                height: CELL,
                transform: `translate(${px}px, ${py}px)`,
                transition: `transform ${s.stepMs ?? (isBusy(s.agentState) ? STEP_MS_TRANSIT : STEP_MS_IDLE)}ms linear`,
                cursor: 'pointer',
                zIndex: Math.floor(py),
                padding: 0,
                border: 0,
                background: 'transparent',
              }}
            >
              {/* Selection ring */}
              {selected && (
                <div
                  style={{
                    position: 'absolute',
                    inset: -2,
                    borderRadius: '50%',
                    boxShadow: '0 0 0 2px var(--theme-accent-yellow), 0 0 12px rgb(var(--theme-accent-yellow-rgb) / 0.6)',
                    pointerEvents: 'none',
                  }}
                />
              )}
              <TownSpritePoke
                sprite={s.sprite}
                state={s.agentState}
                facing={s.facing}
                inTransit={inTransit}
                attention={isDone}
                glow={glow}
                busyBubble={isBusy(s.agentState)}
                doneBubble={isDone}
              />
            </button>
          )
        })}


      </div>

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
          existingGroups={existingGroups}
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

    </div>
  )
}

// Per-sprite animator. Pulled into its own component so each sprite owns its
// own useSpriteAnimation cycle (the hook holds setTimeout state — sharing it
// would mean every sprite hops on the same beat). Reuses the same animation
// registry as the agent-card preview, so card-busy and town-busy match.
function TownSpritePoke({ sprite, state, facing, inTransit, attention, glow, busyBubble, doneBubble }: {
  sprite: string
  state: AgentState['state']
  facing: 'left' | 'right'
  inTransit: boolean
  attention: boolean
  glow: 'busy' | 'done' | null
  busyBubble: boolean
  doneBubble: boolean
}) {
  const animClass = useSpriteAnimation(state, !inTransit)
  const cls = inTransit ? 'sprite-transit-hop' : attention ? 'sprite-hop-loop' : animClass
  const [, setSizeVersion] = useState(0)
  const sz = spriteRenderSize(sprite)
  const natural = spriteSizeCache[sprite]
  const centerScaleX = natural ? sz.w / natural.w : 1
  const centerScaleY = natural ? sz.h / natural.h : 1
  const visualBounds = natural?.bounds
  // Town sprites are positioned by their feet. PixelSprite's default vertical
  // alpha-centering is great for cards/PC boxes, but it makes the minimap art
  // appear about a tile above its collision cell for sprites with uneven
  // transparent padding. Keep horizontal alpha-centering, and instead shift the
  // transparent canvas down just enough that the visible feet sit on the cell
  // bottom.
  const footShiftY = visualBounds ? Math.round((natural!.h - visualBounds.maxY - 1) * centerScaleY) : 0
  const visualTop = visualBounds
    ? Math.max(-8, Math.min(sz.h - 8, Math.round(visualBounds.minY * centerScaleY + footShiftY)))
    : 0
  useEffect(() => { preloadSpriteSize(sprite, () => setSizeVersion(v => v + 1)) }, [sprite])
  return (
    <div
      className={cls}
      style={{
        position: 'absolute',
        bottom: 0,
        left: '50%',
        marginLeft: -sz.w / 2,
        width: sz.w,
        height: sz.h,
        pointerEvents: 'none',
      }}
    >
      {(busyBubble || doneBubble) && (
        <div
          style={{
            position: 'absolute',
            top: visualTop,
            left: 0,
            right: 0,
            height: 0,
            transform: 'scale(0.85)',
            transformOrigin: 'top center',
            zIndex: 2,
            pointerEvents: 'none',
          }}
        >
          <BusyBubble isBusy={busyBubble} />
          <DoneBubble isDone={doneBubble} />
        </div>
      )}
      <PixelSprite
        sprite={sprite}
        alt=""
        centerScaleX={centerScaleX}
        centerScaleY={0}
        shiftY={footShiftY}
        flipX={facing === 'right'}
        shadow="panel"
        style={{
          width: '100%',
          height: '100%',
          objectFit: 'contain',
          objectPosition: 'bottom',
          filter: glow === 'done'
            ? 'drop-shadow(0 0 1px rgb(var(--theme-accent-green-rgb) / 0.95)) drop-shadow(0 0 5px rgb(var(--theme-accent-green-rgb) / 0.85)) drop-shadow(0 0 12px rgb(var(--theme-accent-green-rgb) / 0.55)) drop-shadow(1px 2px 0 var(--theme-panel-muted-bg))'
            : glow === 'busy'
              ? 'drop-shadow(0 0 1px rgb(var(--theme-accent-red-rgb) / 0.95)) drop-shadow(0 0 5px rgb(var(--theme-accent-red-rgb) / 0.85)) drop-shadow(0 0 12px rgb(var(--theme-accent-red-rgb) / 0.55)) drop-shadow(1px 2px 0 var(--theme-panel-muted-bg))'
              : 'drop-shadow(1px 2px 0 var(--theme-panel-muted-bg))',
        }}
      />
    </div>
  )
}


// ── Helpers ────────────────────────────────────────────────

// Idle wander step. Idle agents can wander anywhere walkable.
function pickIdleStep(from: Cell): Cell | null {
  const ns = idleNeighbours(from)
  if (ns.length === 0) return null
  return ns[Math.floor(Math.random() * ns.length)]
}

function pickIdleWanderTarget(from: Cell): Cell | null {
  const dirs = [
    { col: 1, row: 0 },
    { col: -1, row: 0 },
    { col: 0, row: 1 },
    { col: 0, row: -1 },
  ].sort(() => Math.random() - 0.5)
  const r = Math.random()
  const steps = r < 0.5 ? 1 : r < 0.85 ? 2 : 3
  for (const d of dirs) {
    let cur = from
    let target: Cell | null = null
    for (let i = 0; i < steps; i++) {
      const next = { col: cur.col + d.col, row: cur.row + d.row }
      if (!visibleWalkable(next.col, next.row) || isBusyCell(next)) break
      target = next
      cur = next
    }
    if (target) return target
  }
  return pickIdleStep(from)
}

function randomCooldown(): number {
  return IDLE_COOLDOWN_MIN + Math.random() * (IDLE_COOLDOWN_MAX - IDLE_COOLDOWN_MIN)
}

function isOccupied(sprites: Record<string, TownSprite>, cell: Cell, selfId: string): boolean {
  for (const id in sprites) {
    if (id === selfId) continue
    const s = sprites[id]
    if ((s.target?.col === cell.col && s.target?.row === cell.row) ||
        (s.pos.col === cell.col && s.pos.row === cell.row)) return true
  }
  return false
}

function spriteClick(agents: AgentState[], id: string, onSelect: (a: AgentState) => void) {
  const a = agents.find(x => stableId(x) === id)
  if (a) onSelect(a)
}
