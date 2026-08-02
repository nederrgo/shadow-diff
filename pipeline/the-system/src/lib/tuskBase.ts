/** HTTP base for Tusk REST (`:8082`), same host as The System. */
export function tuskHttpBase(): string {
  return `http://${window.location.hostname}:8082`
}

/** WebSocket base for Tusk streams. */
export function tuskWsBase(): string {
  return `ws://${window.location.hostname}:8082`
}
