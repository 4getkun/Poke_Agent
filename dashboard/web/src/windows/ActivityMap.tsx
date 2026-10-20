import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type { Approval, Run, Snapshot, Task } from './api'
import type { Locale } from './i18n'
import './activity-map.css'
import { ACTOR_X_MAX, ACTOR_X_MIN, bubbleForActor, bubbleFramePath, clampActorX } from './bubbleGeometry.js'
import { useSpriteCenterOffset } from '../utils/spriteCentering'

const SPRITES = ['slowking', 'hawlucha', 'bulbasaur', 'pikachu', 'squirtle', 'charmander'] as const
const MAX_MAP_ACTORS = 24
const WALK_STEPS = 18

interface ActivityPreview { body: string; role: string; created_at: string }
interface ActivityMapProps {
  snapshot: Snapshot
  onSelect: (taskId: string) => void
  selectedTaskId: string | null
  locale: Locale
  originalAssets?: boolean
}
interface ActivityItem { task: Task; run: Run; preview?: ActivityPreview; pendingApproval: boolean }
interface Point { x: number; y: number }

const messages = {
  ja: { title: '活動マップ', noData: '実行中のタスクはありません', noPreview: '状態を確認中', starting: '起動中', waiting: '承認待ち', queued: '待機中', working: '作業中', responding: '応答中', tool: 'ツール出力', completed: '実行終了', failed: '失敗', interrupted: '中断', cancelled: 'キャンセル', expired: '期限切れ', other: '作業中', more: 'マップ外のタスク', selected: '選択中' },
  en: { title: 'Activity map', noData: 'No tasks are running', noPreview: 'Checking status', starting: 'Starting', waiting: 'Approval pending', queued: 'Queued', working: 'Working', responding: 'Responding', tool: 'Tool output', completed: 'Run finished', failed: 'Failed', interrupted: 'Interrupted', cancelled: 'Cancelled', expired: 'Expired', other: 'Working', more: 'More tasks', selected: 'Selected' },
} satisfies Record<Locale, Record<string, string>>

function hash(value: string): number {
  let result = 2166136261
  for (let index = 0; index < value.length; index++) result = Math.imul(result ^ value.charCodeAt(index), 16777619)
  return result >>> 0
}

export function spriteForTask(taskId: string, assignedSprite?: string): typeof SPRITES[number] {
  if (SPRITES.includes(assignedSprite as typeof SPRITES[number])) return assignedSprite as typeof SPRITES[number]
  return SPRITES[hash(taskId) % SPRITES.length]
}

interface ArtSize { width: number; height: number }
const spriteArtSizes = new Map<string, ArtSize>()
type Facing = 'left' | 'right'

function MapSprite({ facing, children }: { facing: Facing | null; children: ReactNode }) {
  const [lastFacing, setLastFacing] = useState<Facing>('left')
  useEffect(() => {
    if (facing) setLastFacing(facing)
  }, [facing])
  const direction = facing ?? lastFacing
  return <span className="activity-map__sprite" style={{ transform: direction === 'right' ? 'scaleX(-1)' : 'scaleX(1)' }}>{children}</span>
}

function artSize(img: HTMLImageElement, sprite: string): ArtSize {
  const cached = spriteArtSizes.get(sprite)
  if (cached) return cached
  const canvas = document.createElement('canvas')
  canvas.width = img.naturalWidth; canvas.height = img.naturalHeight
  const context = canvas.getContext('2d', { willReadFrequently: true })
  if (!context) return { width: img.naturalWidth, height: img.naturalHeight }
  context.drawImage(img, 0, 0)
  const pixels = context.getImageData(0, 0, canvas.width, canvas.height).data
  let minX = canvas.width, minY = canvas.height, maxX = -1, maxY = -1
  for (let y = 0; y < canvas.height; y++) for (let x = 0; x < canvas.width; x++) {
    if (pixels[(y * canvas.width + x) * 4 + 3] > 8) { minX = Math.min(minX, x); minY = Math.min(minY, y); maxX = Math.max(maxX, x); maxY = Math.max(maxY, y) }
  }
  const size = { width: Math.max(1, maxX - minX + 1), height: Math.max(1, maxY - minY + 1) }
  spriteArtSizes.set(sprite, size)
  return size
}

export function AssignedSprite({ taskId, sprite: assignedSprite, scale = 1, onError, onArtSize }: { taskId: string; sprite?: string; scale?: number; onError?: () => void; onArtSize?: (size: ArtSize) => void }) {
  const sprite = spriteForTask(taskId, assignedSprite)
  const { offset, onLoad } = useSpriteCenterOffset(sprite, scale)
  return <img className="activity-map__assigned-sprite" src={`/sprites/${sprite}.png`} alt="" draggable={false} onLoad={(event) => { onLoad(event.currentTarget); if (onArtSize) onArtSize(artSize(event.currentTarget, sprite)) }} onError={onError} style={{ position: 'absolute', left: '50%', top: '50%', width: 'auto', height: 'auto', maxWidth: 'none', transform: `translate(calc(-50% + ${offset.x}px), calc(-50% + ${offset.y}px)) scale(${scale})` }} />
}

function short(value: string, limit: number): string {
  const compact = value.replace(/\s+/g, ' ').trim()
  return compact.length > limit ? `${compact.slice(0, limit - 1)}…` : compact
}

type ProfileSnapshot = Snapshot & { settings?: Snapshot['settings'] & { agent_profiles?: Record<string, { name: string; sprite: string }> } }
function agentProfile(snapshot: Snapshot, taskId: string) {
  return (snapshot as ProfileSnapshot).settings?.agent_profiles?.[taskId]
}

function pendingForRun(approvals: Approval[], run: Run): boolean {
  return approvals.some((approval) => approval.run_id === run.id
    && approval.generation === run.generation
    && approval.state.toLowerCase() === 'pending')
}

export function activityLabel(run: Run, preview: ActivityPreview | undefined, pendingApproval: boolean, locale: Locale): string {
  const text = messages[locale]
  if (pendingApproval || ['waiting_approval', 'awaiting_approval'].includes(run.state.toLowerCase())) return text.waiting
  const state = run.state.toLowerCase().replace(/-/g, '_')
  if (state === 'starting') return text.starting
  if (state === 'queued' || state === 'created' || state === 'pending') return text.queued
  if (state === 'running' || state === 'in_progress') {
    if (preview?.role.toLowerCase() === 'tool') return `${text.tool}: ${short(preview.body, 38) || text.working}`
    // A saved assistant message does not prove the runtime's current tool/thinking stage.
    return text.working
  }
  if (['completed', 'succeeded', 'done', 'accepted'].includes(state)) return text.completed
  if (['failed', 'error'].includes(state)) return text.failed
  if (state === 'interrupted') return text.interrupted
  if (state === 'cancelled' || state === 'canceled') return text.cancelled
  if (state === 'expired') return text.expired
  return text.other
}

function isWalking(run: Run, pendingApproval: boolean): boolean {
  if (pendingApproval) return false
  const state = run.state.toLowerCase().replace(/-/g, '_')
  return state === 'starting' || state === 'running'
}

function pointFor(taskId: string, index: number, run: Run, tick: number, moving: boolean, originalMap = false): Point {
  const seed = hash(`${taskId}:${run.id}`)
  const lane = index % 3
  const laneY = [28, 53, 78][lane]
  const stack = Math.floor(index / 3)
  const y = Math.max(15, Math.min(87, laneY + ((stack % 3) - 1) * 5))
  const phase = (tick + ((seed >>> 4) % WALK_STEPS)) % (2 * WALK_STEPS - 2)
  const fraction = (phase < WALK_STEPS ? phase : 2 * WALK_STEPS - 2 - phase) / (WALK_STEPS - 1)
  // These corridors match the original town's walkable mask; do not cross buildings/water.
  if (originalMap) {
    const corridors = [{ x1: 6, x2: 69, y: 25 }, { x1: 7, x2: 74, y: 62 }]
    const corridor = corridors[index % corridors.length]
    const startX = clampActorX(corridor.x1 * 5.44)
    const endX = clampActorX(corridor.x2 * 5.44)
    const walkFraction = moving ? fraction : ((seed >>> 8) % 100) / 100
    return { x: startX + (endX - startX) * walkFraction, y: corridor.y * 4.8 }
  }
  if (!moving) return { x: ACTOR_X_MIN + ((seed >>> 8) % 100) / 100 * (ACTOR_X_MAX - ACTOR_X_MIN), y: y * 4.8 }
  const goesRight = ((seed >>> 2) & 1) === 0
  const walkFraction = goesRight ? fraction : 1 - fraction
  return { x: clampActorX(ACTOR_X_MIN + walkFraction * (ACTOR_X_MAX - ACTOR_X_MIN)), y: y * 4.8 }
}

function PixelGround() {
  return (
    <svg className="activity-map__ground" viewBox="0 0 544 480" preserveAspectRatio="none" aria-hidden="true">
      <defs>
        <pattern id="activity-map-pixels" width="48" height="48" patternUnits="userSpaceOnUse">
          <rect width="48" height="48" fill="#7eaa72" />
          <rect x="4" y="5" width="7" height="5" fill="#94bd81" />
          <rect x="31" y="18" width="10" height="6" fill="#6c9a67" />
          <rect x="17" y="34" width="6" height="5" fill="#a0c887" />
          <rect x="37" y="38" width="5" height="4" fill="#608b61" />
        </pattern>
      </defs>
      <rect width="544" height="480" fill="url(#activity-map-pixels)" />
      <path d="M0 120H544 M0 240H544 M0 360H544" stroke="#4c7957" strokeWidth="34" />
      <path d="M0 118H544 M0 238H544 M0 358H544" stroke="#d4c28f" strokeWidth="25" />
      <path d="M0 116H544 M0 236H544 M0 356H544" stroke="#e7d8ae" strokeWidth="4" strokeDasharray="18 22" />
      <g fill="#578661">
        <rect x="68" y="18" width="42" height="34" rx="3" /><rect x="75" y="12" width="30" height="40" rx="3" />
        <rect x="204" y="157" width="48" height="39" rx="3" /><rect x="213" y="151" width="31" height="47" rx="3" />
        <rect x="375" y="281" width="49" height="45" rx="3" /><rect x="384" y="274" width="32" height="50" rx="3" />
        <rect x="438" y="395" width="48" height="37" rx="3" /><rect x="448" y="388" width="28" height="44" rx="3" />
      </g>
      <g fill="#477653"><rect x="87" y="42" width="7" height="10" /><rect x="227" y="183" width="7" height="14" /><rect x="397" y="309" width="8" height="15" /><rect x="458" y="414" width="7" height="17" /></g>
      <g fill="#93bc7e"><rect x="35" y="67" width="17" height="8" /><rect x="190" y="431" width="22" height="9" /><rect x="308" y="70" width="20" height="8" /><rect x="503" y="440" width="24" height="8" /></g>
    </svg>
  )
}

const spriteColors: Record<typeof SPRITES[number], { body: string; dark: string; light: string }> = {
  slowking: { body: '#df8b7d', dark: '#784b60', light: '#f6d7c3' },
  hawlucha: { body: '#d34c3d', dark: '#38364d', light: '#e8d8bd' },
  bulbasaur: { body: '#68a876', dark: '#386a67', light: '#c8e39b' },
  pikachu: { body: '#e9c84b', dark: '#65543c', light: '#fff0a0' },
  squirtle: { body: '#6aa7c4', dark: '#385875', light: '#e8c98e' },
  charmander: { body: '#ed9850', dark: '#684b4a', light: '#f8d898' },
}

export function PixelCharacter({ sprite, selected }: { sprite: typeof SPRITES[number]; selected: boolean }) {
  const colors = spriteColors[sprite]
  return (
    <svg className={`activity-map__pixel-character ${selected ? 'is-selected' : ''}`} viewBox="0 0 24 28" shapeRendering="crispEdges" aria-hidden="true">
      {selected && <rect x="1" y="1" width="22" height="26" rx="2" fill="#fff2a9" opacity=".65" />}
      <rect x="7" y="2" width="10" height="3" fill={colors.dark} />
      <rect x="4" y="5" width="16" height="3" fill={colors.body} />
      <rect x="3" y="8" width="18" height="8" fill={colors.body} />
      <rect x="5" y="8" width="3" height="3" fill="#fff" /><rect x="16" y="8" width="3" height="3" fill="#fff" />
      <rect x="6" y="9" width="2" height="2" fill={colors.dark} /><rect x="16" y="9" width="2" height="2" fill={colors.dark} />
      <rect x="8" y="14" width="8" height="2" fill={colors.dark} />
      <rect x="6" y="16" width="12" height="6" fill={colors.light} />
      <rect x="4" y="18" width="3" height="4" fill={colors.body} /><rect x="17" y="18" width="3" height="4" fill={colors.body} />
      <rect x="7" y="22" width="4" height="4" fill={colors.dark} /><rect x="13" y="22" width="4" height="4" fill={colors.dark} />
    </svg>
  )
}

export function ActivityMap({ snapshot, onSelect, selectedTaskId, locale, originalAssets = false }: ActivityMapProps) {
  const text = messages[locale]
  const [tick, setTick] = useState(0)
  const [mapFailed, setMapFailed] = useState(false)
  const [artSizes, setArtSizes] = useState<Record<string, ArtSize>>({})
  const [failedSprites, setFailedSprites] = useState<Set<string>>(() => new Set())
  const viewportRef = useRef<HTMLDivElement>(null)
  const [sceneScale, setSceneScale] = useState(1)
  const [motionAllowed, setMotionAllowed] = useState(() => typeof document !== 'undefined'
    && !document.hidden
    && !window.matchMedia('(prefers-reduced-motion: reduce)').matches)
  const runsById = useMemo(() => new Map(snapshot.runs.map((run) => [run.id, run])), [snapshot.runs])
  const activityItems = useMemo<ActivityItem[]>(() => snapshot.tasks.flatMap((task) => {
    if (!task.current_run_id) return []
    const run = runsById.get(task.current_run_id)
    if (!run || run.task_id !== task.id) return []
    const pendingApproval = pendingForRun(snapshot.approvals, run)
    return [{ task, run, preview: snapshot.previews?.[task.id], pendingApproval }]
  }), [runsById, snapshot.approvals, snapshot.previews, snapshot.tasks])
  const mapItems = activityItems.slice(0, MAX_MAP_ACTORS)
  const overflowItems = activityItems.slice(MAX_MAP_ACTORS)
  const canWalk = mapItems.some(({ run, pendingApproval }) => isWalking(run, pendingApproval))

  useEffect(() => {
    const motion = window.matchMedia('(prefers-reduced-motion: reduce)')
    const update = () => setMotionAllowed(!document.hidden && !motion.matches)
    document.addEventListener('visibilitychange', update)
    motion.addEventListener('change', update)
    return () => {
      document.removeEventListener('visibilitychange', update)
      motion.removeEventListener('change', update)
    }
  }, [])

  useEffect(() => {
    if (!canWalk || !motionAllowed) return
    const timer = window.setInterval(() => setTick((current) => current + 1), 900)
    return () => window.clearInterval(timer)
  }, [canWalk, motionAllowed])

  useEffect(() => {
    const viewport = viewportRef.current
    if (!viewport) return
    const observer = new ResizeObserver((entries) => {
      const width = entries[0]?.contentRect.width ?? viewport.clientWidth
      if (width > 0) setSceneScale(width / 544)
    })
    observer.observe(viewport)
    return () => observer.disconnect()
  }, [])

  const assetMap = originalAssets && !mapFailed

  return (
    <section className="activity-map" aria-label={text.title}>
      <div className="activity-map__viewport" ref={viewportRef}>
        <div className="activity-map__scene" style={{ transform: `scale(${sceneScale})` }}>
          {assetMap ? <img className="activity-map__town-image" src="/town.png" alt="" onError={() => setMapFailed(true)} /> : <PixelGround />}
          {activityItems.length === 0 && <div className="activity-map__empty">{text.noData}</div>}
          <div className="activity-map__actors">
            {mapItems.map((item, index) => {
              const label = activityLabel(item.run, item.preview, item.pendingApproval, locale)
              const profile = agentProfile(snapshot, item.task.id)
              const sprite = spriteForTask(item.task.id, profile?.sprite)
              const selected = selectedTaskId === item.task.id
              const walking = isWalking(item.run, item.pendingApproval) && motionAllowed
              const moving = isWalking(item.run, item.pendingApproval)
              const point = pointFor(item.task.id, index, item.run, tick, moving, assetMap)
              const adjacentPoint = walking ? pointFor(item.task.id, index, item.run, tick > 0 ? tick - 1 : tick + 1, moving, assetMap) : null
              const directionDelta = adjacentPoint ? (tick > 0 ? point.x - adjacentPoint.x : adjacentPoint.x - point.x) : 0
              const facing = directionDelta === 0 ? null : directionDelta > 0 ? 'right' : 'left'
              const name = profile?.name?.trim() || (locale === 'ja' ? '名前未設定' : 'Unnamed')
              const useOriginal = originalAssets && !failedSprites.has(sprite)
              const rawSize = artSizes[sprite]
              const size = useOriginal ? { width: (rawSize?.width ?? 20) * 2, height: (rawSize?.height ?? 20) * 2 } : { width: 28, height: 33 }
              const bubble = bubbleForActor(point, size)
              return <div key={item.task.id} className={`activity-map__anchor ${walking ? 'is-walking' : ''}`} style={{ left: point.x, top: point.y }} data-task-id={item.task.id} data-art-width={size.width} data-art-height={size.height}>
                <button type="button" className={`activity-map__actor ${selected ? 'is-selected' : ''}`} aria-label={`${name} — ${label} — ${item.task.spec.objective}`} aria-pressed={selected} title={`${name} — ${label} — ${item.task.spec.objective}`} onClick={() => onSelect(item.task.id)}>
                  <MapSprite facing={walking ? facing : null}>
                    {useOriginal ? <AssignedSprite taskId={item.task.id} sprite={profile?.sprite} scale={2} onError={() => setFailedSprites((current) => new Set(current).add(sprite))} onArtSize={(size) => setArtSizes((current) => current[sprite]?.width === size.width && current[sprite]?.height === size.height ? current : { ...current, [sprite]: size })} /> : <PixelCharacter sprite={sprite} selected={selected} />}
                  </MapSprite>
                </button>
                <span className={`activity-map__bubble ${selected ? 'is-selected' : ''}`} style={{ left: bubble.left - point.x, top: bubble.top - point.y }} aria-hidden="true" title={`${name} — ${label}`}>
                  <svg className="activity-map__bubble-frame" viewBox="0 0 200 86" preserveAspectRatio="none" data-tail-x={bubble.tailX} data-tail-y="84" aria-hidden="true"><path d={bubbleFramePath(bubble.tailX, bubble.side)} fill="white" stroke="black" strokeWidth="3" strokeLinejoin="miter" /></svg>
                  <strong>{name}</strong><span>{short(label, 43)}</span>
                </span>
              </div>
            })}
          </div>
        </div>
      </div>
      {overflowItems.length > 0 && (
        <div className="activity-map__overflow">
          <h3>{text.more} · {overflowItems.length}</h3>
          <div className="activity-map__overflow-list">
            {overflowItems.map((item) => {
              const label = activityLabel(item.run, item.preview, item.pendingApproval, locale)
              const profile = agentProfile(snapshot, item.task.id)
              const name = profile?.name?.trim() || (locale === 'ja' ? '名前未設定' : 'Unnamed')
              const sprite = spriteForTask(item.task.id, profile?.sprite)
              const taskName = item.task.spec.objective || item.task.id
              return <button type="button" key={item.task.id} className={selectedTaskId === item.task.id ? 'is-selected' : ''} aria-label={`${name} — ${label} — ${taskName}`} title={`${name} — ${taskName}`} onClick={() => onSelect(item.task.id)}><span className="activity-map__overflow-sprite" aria-hidden="true"><PixelCharacter sprite={sprite} selected={selectedTaskId === item.task.id} /></span><strong>{name}</strong><span>{label}</span></button>
            })}
          </div>
        </div>
      )}
    </section>
  )
}
