import { useEffect } from 'react'

/**
 * Run an async data loader on mount and whenever the loader's identity (i.e.
 * its useCallback deps) changes, or when `enabled` becomes true.
 *
 * Pages keep their loader as a memoized function because the same loader is
 * also re-run after mutations ("refresh"). Calling such a loader directly in
 * a useEffect trips react-hooks/set-state-in-effect: the React Compiler
 * analysis doesn't model `await`, so state set once the request resolves is
 * reported as a synchronous setState in the effect. The loaders only commit
 * state as the request settles, which is the pattern effects exist for.
 */
export function useAsyncLoad(load: () => Promise<unknown>, enabled = true): void {
  useEffect(() => {
    if (!enabled) return
    load().catch((error: unknown) => {
      // Loaders handle their own errors; this only guards against an
      // unhandled rejection if one throws anyway.
      console.error('Load failed:', error)
    })
  }, [load, enabled])
}
