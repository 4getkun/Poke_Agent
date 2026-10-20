export const BUBBLE_WIDTH: number
export const BUBBLE_HEIGHT: number
export const FRAME_HEIGHT: number
export const FRAME_TIP_Y: number
export const ACTOR_X_MIN: number
export const ACTOR_X_MAX: number
export function clampActorX(x: number): number
export function bubbleForActor(point: { x: number; y: number }, art: { width: number; height: number }): { left: number; top: number; tailX: number; anchorX: number; anchorY: number; side: string }
export function bubbleFramePath(tailX: number, side: string): string
