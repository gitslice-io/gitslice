// Pool sizing and WebSocket bounds for the CI doorbell.

export const MAX_BELL_SOCKETS = 16;

// runnersToStart is how many more runners to start for queued runs, with
// running already running (each may be busy) and at most slots in all: one
// per queued run, up to the free slots.
export function runnersToStart(queued: number, running: number, slots: number): number {
  return Math.max(0, Math.min(queued, slots - running));
}

export interface RunnerSlot {
  isRunning(): Promise<boolean>;
  begin(): Promise<boolean>;
}

// startRunnerPool checks all slot states in parallel, then starts up to
// runnersToStart(queued, running, slots) idle slots in parallel, returning
// the total number of running or newly started runners.
export async function startRunnerPool(slots: readonly RunnerSlot[], queued: number): Promise<number> {
  if (slots.length === 0) return 0;
  const states = await Promise.all(slots.map((slot) => slot.isRunning()));
  let running = 0;
  const idle: RunnerSlot[] = [];
  for (let i = 0; i < slots.length; i++) {
    if (states[i]) running++;
    else idle.push(slots[i]);
  }
  const want = runnersToStart(queued, running, slots.length);
  if (want <= 0 || idle.length === 0) return running;
  const results = await Promise.all(idle.slice(0, want).map((slot) => slot.begin()));
  const started = results.filter(Boolean).length;
  return running + started;
}

export interface ClosableSocket {
  send(data: string): void;
  close(code?: number, reason?: string): void;
}

// evictOldestSockets closes the oldest sockets so accepting one new socket
// keeps at most maxSockets open on a single Bell Durable Object.
export function evictOldestSockets(existing: readonly Pick<ClosableSocket, "close">[], maxSockets = MAX_BELL_SOCKETS): void {
  const keep = Math.max(0, maxSockets - 1);
  for (let i = 0; i < existing.length - keep; i++) {
    try {
      existing[i].close(1000, "too many connections");
    } catch {
      // closing already
    }
  }
}

// handleBellFrame answers literal "ping" frames (if any bypass hibernation
// auto-response) and closes sockets that send unexpected frames.
export function handleBellFrame(ws: ClosableSocket, message: string | ArrayBuffer): void {
  if (message === "ping") {
    try {
      ws.send("pong");
    } catch {
      // closing already
    }
    return;
  }
  try {
    ws.close(1008, "unexpected message");
  } catch {
    // closed already
  }
}
