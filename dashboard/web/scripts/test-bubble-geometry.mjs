import assert from 'node:assert/strict'
import { ACTOR_X_MAX, ACTOR_X_MIN, bubbleForActor, BUBBLE_HEIGHT, FRAME_HEIGHT, FRAME_TIP_Y, bubbleFramePath, clampActorX } from '../src/windows/bubbleGeometry.js'

let checks = 0
for (const point of [{ x: ACTOR_X_MIN, y: 120 }, { x: 200, y: 120 }, { x: ACTOR_X_MAX, y: 297.6 }]) {
  for (const art of [{ width: 40, height: 40 }, { width: 54, height: 80 }, { width: 28, height: 33 }]) {
    const bubble = bubbleForActor(point, art)
    assert.equal(bubble.side, 'right', 'bubble stays on the right side of the actor')
    assert.ok(bubble.left >= 4 && bubble.left + 200 <= 540, 'frame stays inside the town')
    assert.ok(bubble.top >= 0, 'the tallest supported sprite keeps its frame visible')
    assert.ok(bubble.tailX >= 20 && bubble.tailX <= 180, 'tail stays within the frame bounds')
    for (const scale of [0.5, 0.77, 1.2]) {
      assert.ok(Math.abs((bubble.left + bubble.tailX) * scale - bubble.anchorX * scale) < 0.001, 'tail follows the visible head horizontally, including at edges')
      assert.ok(Math.abs((bubble.top + FRAME_TIP_Y * BUBBLE_HEIGHT / FRAME_HEIGHT) * scale - bubble.anchorY * scale) < 0.001, 'tail remains attached when the map scales')
      checks += 2
    }
    assert.match(bubbleFramePath(bubble.tailX, bubble.side), /^M9 2.*Z$/)
  }
}
assert.equal(clampActorX(ACTOR_X_MIN - 1), ACTOR_X_MIN)
assert.equal(clampActorX(ACTOR_X_MIN), ACTOR_X_MIN)
assert.equal(clampActorX(200), 200)
assert.equal(clampActorX(ACTOR_X_MAX), ACTOR_X_MAX)
assert.equal(clampActorX(ACTOR_X_MAX + 1), ACTOR_X_MAX)
assert.equal(bubbleForActor({ x: 120, y: 120 }, { width: 40, height: 40 }).anchorY, 92.96)
console.log(`bubble geometry: ${checks} scaled head/tail attachment checks passed`)
