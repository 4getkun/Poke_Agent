/**
 * @param {boolean} compositionActive
 * @param {unknown} nativeEvent
 */
export function isPromptInputComposing(compositionActive, nativeEvent) {
  if (compositionActive) return true
  if (!nativeEvent || typeof nativeEvent !== 'object') return false

  const event = /** @type {{ isComposing?: boolean; keyCode?: number }} */ (nativeEvent)
  return event.isComposing === true || event.keyCode === 229
}

/**
 * @param {{ key: string; ctrlKey: boolean; shiftKey: boolean; hasCompletions: boolean; compositionActive: boolean; nativeEvent?: unknown }} input
 * @returns {'ignore' | 'newline' | 'send' | 'complete'}
 */
export function getPromptInputKeyAction(input) {
  if (isPromptInputComposing(input.compositionActive, input.nativeEvent)) return 'ignore'
  if (input.key !== 'Enter') return 'ignore'
  if (input.shiftKey) return 'newline'
  if (input.ctrlKey) return 'send'
  if (input.hasCompletions) return 'complete'
  return 'newline'
}
