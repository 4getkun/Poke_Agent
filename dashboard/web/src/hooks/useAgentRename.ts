import { useState, useCallback, useRef } from 'react'
import { renameAgent } from '../api'

// Encapsulates the rename editing state + API call shared by AgentCard
// and ChatPanel. Both had ~20-line duplicates of this logic.
export function useAgentRename(sessionId: string, displayName: string) {
  const [isRenaming, setIsRenaming] = useState(false)
  const [newName, setNewName] = useState(displayName)
  const pending = useRef(false)

  const startRename = useCallback(() => {
    setNewName(displayName)
    setIsRenaming(true)
  }, [displayName])

  const cancelRename = useCallback(() => {
    setIsRenaming(false)
  }, [])

  const submitRename = useCallback(async () => {
    if (pending.current) return
    const trimmed = newName.trim()
    setIsRenaming(false)
    if (!trimmed || trimmed === displayName) return
    pending.current = true
    try {
      await renameAgent(sessionId, trimmed)
    } catch (e) {
      console.error('rename failed', e)
    }
    pending.current = false
  }, [sessionId, newName, displayName])

  return { isRenaming, newName, setNewName, startRename, cancelRename, submitRename }
}
