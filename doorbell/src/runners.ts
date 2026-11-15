// runnersToStart is how many more runners to start for queued runs, with
// running already running (each may be busy) and at most slots in all: one
// per queued run, up to the free slots.
export function runnersToStart(queued: number, running: number, slots: number): number {
  return Math.max(0, Math.min(queued, slots - running));
}
