export const BUBBLE_WIDTH = 200
export const BUBBLE_HEIGHT = 72
export const FRAME_HEIGHT = 86
export const FRAME_TIP_Y = 84
export const ACTOR_X_MIN = 48
export const ACTOR_X_MAX = 368

export function clampActorX(x) {
  return Math.max(ACTOR_X_MIN, Math.min(ACTOR_X_MAX, x))
}

// The actor button is 42px high, translated upward by 62% of its height.
const ACTOR_CENTER_Y = 42 * (0.5 - 0.62)

export function bubbleForActor(point, art) {
  const anchorX = point.x + Math.min(12, art.width / 4)
  const anchorY = point.y + ACTOR_CENTER_Y - art.height / 2 - 2
  const side = 'right'
  const left = Math.max(4, Math.min(340, anchorX - 44))
  return { left, top: anchorY - FRAME_TIP_Y * BUBBLE_HEIGHT / FRAME_HEIGHT, tailX: anchorX - left, anchorX, anchorY, side }
}

export function bubbleFramePath(tailX, side) {
  const p = (delta) => +(tailX + delta).toFixed(3)
  const tail = side === 'left'
    ? `H${p(0)}V78H${p(6)}V84H${p(-6)}V78H${p(-13)}V72H${p(-20)}V66`
    : `H${p(20)}V72H${p(13)}V78H${p(6)}V84H${p(-6)}V78H${p(0)}V66`
  return `M9 2H191V7H196V12H198V61H194V66${tail}H9V61H4V56H2V12H6V7H9Z`
}
