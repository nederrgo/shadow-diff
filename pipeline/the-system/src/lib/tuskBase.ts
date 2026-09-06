/** HTTP base for Tusk REST — same origin (nginx proxies `/api/` → Tusk). */
export function tuskHttpBase(): string {
  return ''
}

/** WebSocket base for Tusk streams — same host; nginx proxies `/ws/`. */
export function tuskWsBase(): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${proto}//${window.location.host}`
}
