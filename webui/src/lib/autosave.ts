export type SerialAutosaveQueue<T> = {
  enqueue: (value: T) => void
  whenIdle: () => Promise<void>
}

/** Debounce text edits while still allowing blur to flush immediately. */
export function createDebouncedSave(save: () => void | Promise<void>, delay = 500) {
  let timer: ReturnType<typeof setTimeout> | undefined
  let pending = false

  async function flush() {
    if (timer !== undefined) clearTimeout(timer)
    timer = undefined
    if (!pending) return
    pending = false
    await save()
  }

  function schedule() {
    pending = true
    if (timer !== undefined) clearTimeout(timer)
    timer = setTimeout(() => { void flush() }, delay)
  }

  function cancel() {
    if (timer !== undefined) clearTimeout(timer)
    timer = undefined
    pending = false
  }

  return { schedule, flush, cancel }
}

/**
 * Serialize writes, coalesce queued updates, and continue draining after a failed
 * request. The caller owns validation and error presentation in the save callback.
 */
export function createSerialAutosaveQueue<T>(
  save: (value: T) => void | Promise<void>,
  merge: (current: T, next: T) => T,
  onError: (error: unknown, value: T) => void = () => {},
): SerialAutosaveQueue<T> {
  let pending: T | undefined
  let running = false
  let idleResolvers: Array<() => void> = []

  async function drain() {
    if (running) return
    running = true
    while (pending !== undefined) {
      const current = pending
      pending = undefined
      try {
        await save(current)
      } catch (error) {
        onError(error, current)
      }
    }
    running = false
    const resolvers = idleResolvers
    idleResolvers = []
    for (const resolve of resolvers) resolve()
    // Protect against a new item being enqueued at the end of a drain.
    if (pending !== undefined) void drain()
  }

  function enqueue(value: T) {
    pending = pending === undefined ? value : merge(pending, value)
    if (!running) void drain()
  }

  function whenIdle() {
    if (!running && pending === undefined) return Promise.resolve()
    return new Promise<void>(resolve => idleResolvers.push(resolve))
  }

  return { enqueue, whenIdle }
}

/** Retry one settings write after reloading the latest version and rebasing once. */
export async function retryOnceOnConflict<T, P>(
  request: (patch: P) => Promise<T>,
  patch: P,
  isConflict: (error: unknown) => boolean,
  refresh: () => Promise<void>,
  rebase: (patch: P) => P,
): Promise<T> {
  try {
    return await request(patch)
  } catch (error) {
    if (!isConflict(error)) throw error
    await refresh()
    return request(rebase(patch))
  }
}
