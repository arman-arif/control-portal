import { useCallback, useEffect, useRef, useState } from 'react'

// usePolling calls load() now and then every `interval` ms while mounted.
export function usePolling(load, interval) {
  const [data, setData] = useState(null)
  const [error, setError] = useState(null)
  const loadRef = useRef(load)
  loadRef.current = load

  const refresh = useCallback(async () => {
    try {
      setData(await loadRef.current())
      setError(null)
    } catch (err) {
      setError(err)
    }
  }, [])

  useEffect(() => {
    refresh()
    const t = setInterval(refresh, interval)
    return () => clearInterval(t)
  }, [refresh, interval])

  return { data, error, refresh }
}
