export type PromptInputKeyAction = 'ignore' | 'newline' | 'send' | 'complete'

export interface PromptInputKeyInput {
  key: string
  ctrlKey: boolean
  shiftKey: boolean
  hasCompletions: boolean
  compositionActive: boolean
  nativeEvent?: unknown
}

export declare function isPromptInputComposing(compositionActive: boolean, nativeEvent?: unknown): boolean
export declare function getPromptInputKeyAction(input: PromptInputKeyInput): PromptInputKeyAction
