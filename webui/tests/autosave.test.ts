import { afterEach, describe, expect, it, vi } from 'vitest'
import { createDebouncedSave, createSerialAutosaveQueue, retryOnceOnConflict } from '../src/lib/autosave'
import { dismissSavedToast, savedToastVisible, showSavedToast } from '../src/lib/save-toast'

afterEach(() => {
  vi.useRealTimers()
  dismissSavedToast()
})

describe('autosave helpers', () => {
  it('debounces repeated text edits and flushes immediately on demand', async () => {
    vi.useFakeTimers()
    const save = vi.fn()
    const debounced = createDebouncedSave(save, 500)

    debounced.schedule()
    vi.advanceTimersByTime(300)
    debounced.schedule()
    vi.advanceTimersByTime(499)
    expect(save).not.toHaveBeenCalled()
    await debounced.flush()
    expect(save).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(1000)
    expect(save).toHaveBeenCalledTimes(1)
  })

  it('serializes requests, merges pending values, and continues after a failure', async () => {
    let releaseFirst!: () => void
    const firstGate = new Promise<void>(resolve => { releaseFirst = resolve })
    const calls: number[] = []
    const failures: unknown[] = []
    let active = 0
    let maxActive = 0
    let queue!: ReturnType<typeof createSerialAutosaveQueue<number>>
    queue = createSerialAutosaveQueue<number>(async value => {
      active++
      maxActive = Math.max(maxActive, active)
      calls.push(value)
      try {
        if (value === 1) await firstGate
        if (value === 2) throw new Error('write failed')
      } finally {
        active--
      }
    }, (_current, next) => next, error => {
      failures.push(error)
      queue.enqueue(3)
    })

    queue.enqueue(1)
    queue.enqueue(2)
    await Promise.resolve()
    expect(calls).toEqual([1])
    releaseFirst()
    await queue.whenIdle()

    expect(calls).toEqual([1, 2, 3])
    expect(maxActive).toBe(1)
    expect(failures).toHaveLength(1)
  })

  it('reloads and rebases one settings conflict retry, but does not retry twice', async () => {
    let serverVersion = 1
    const requests: Array<{ version: number; patch: { routing: { preference: string } } }> = []
    let conflict = true
    const request = async (patch: { routing: { preference: string } }) => {
      requests.push({ version: serverVersion, patch })
      if (conflict) {
        conflict = false
        throw { code: 'settings_conflict' }
      }
      return 'saved'
    }
    const result = await retryOnceOnConflict(
      request,
      { routing: { preference: 'cost' } },
      error => typeof error === 'object' && error !== null && 'code' in error && error.code === 'settings_conflict',
      async () => { serverVersion = 2 },
      patch => ({ routing: { preference: patch.routing.preference } }),
    )

    expect(result).toBe('saved')
    expect(requests).toEqual([
      { version: 1, patch: { routing: { preference: 'cost' } } },
      { version: 2, patch: { routing: { preference: 'cost' } } },
    ])
  })

  it('shows a shared accessible-toast state for three seconds and refreshes it on repeated saves', () => {
    vi.useFakeTimers()
    showSavedToast()
    expect(savedToastVisible.value).toBe(true)
    vi.advanceTimersByTime(2500)
    showSavedToast()
    vi.advanceTimersByTime(2999)
    expect(savedToastVisible.value).toBe(true)
    vi.advanceTimersByTime(1)
    expect(savedToastVisible.value).toBe(false)
  })
})
