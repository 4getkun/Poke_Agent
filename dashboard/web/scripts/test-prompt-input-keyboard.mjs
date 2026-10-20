import assert from 'node:assert/strict'
import { getPromptInputKeyAction, isPromptInputComposing } from '../src/components/promptInputKeyboard.js'

const key = (overrides = {}) => ({
  key: 'Enter',
  ctrlKey: false,
  shiftKey: false,
  hasCompletions: false,
  compositionActive: false,
  ...overrides,
})

assert.equal(isPromptInputComposing(true, {}), true)
assert.equal(isPromptInputComposing(false, { isComposing: true }), true)
assert.equal(isPromptInputComposing(false, { keyCode: 229 }), true)
assert.equal(getPromptInputKeyAction(key({ compositionActive: true, ctrlKey: true })), 'ignore')
assert.equal(getPromptInputKeyAction(key({ nativeEvent: { isComposing: true }, hasCompletions: true })), 'ignore')
assert.equal(getPromptInputKeyAction(key({ nativeEvent: { keyCode: 229 }, hasCompletions: true })), 'ignore')
assert.equal(getPromptInputKeyAction(key({ ctrlKey: true })), 'send')
assert.equal(getPromptInputKeyAction(key({ ctrlKey: true, hasCompletions: true })), 'send')
assert.equal(getPromptInputKeyAction(key({ hasCompletions: true })), 'complete')
assert.equal(getPromptInputKeyAction(key()), 'newline')
assert.equal(getPromptInputKeyAction(key({ shiftKey: true, ctrlKey: true })), 'newline')
assert.equal(getPromptInputKeyAction(key({ key: 'Tab', hasCompletions: true })), 'ignore')

process.stdout.write('promptInputKeyboard: 12 assertions passed\n')
