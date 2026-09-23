import { WebSocket } from 'ws';

// Polyfill browser APIs for Node.js test environment
(globalThis as unknown as Record<string, unknown>).WebSocket = WebSocket;

// Per-worker server address, populated by setup-per-file.ts's beforeAll
// hook (issue #180). Reading from a worker-local global keeps every test
// file pointed at its own server, so TriggerRefresh calls in one file
// never leak into active subscriptions in another file.
export function getServerAddr(): string {
    const addr = globalThis.__APROT_E2E_SERVER_ADDR__;
    if (!addr) {
        throw new Error(
            'Test server address not set — ensure ./tests/setup-per-file.ts is listed in vitest.setupFiles',
        );
    }
    return addr;
}

export function wsUrl(): string {
    return `ws://${getServerAddr()}/ws`;
}

export function wsRejectUrl(): string {
    return `ws://${getServerAddr()}/ws-reject`;
}

export function wsTokenUrl(): string {
    return `ws://${getServerAddr()}/ws-token`;
}

// wsTextOnlyUrl declines binary frames, so Blob results arrive as the JSON
// $blob envelope (#279).
export function wsTextOnlyUrl(): string {
    return `ws://${getServerAddr()}/ws?binary=0`;
}
