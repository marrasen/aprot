import { readFileSync } from 'fs';
import { dirname, join } from 'path';
import { fileURLToPath } from 'url';
import { WebSocket } from 'ws';
import EventSource from 'eventsource';

const __dirname = dirname(fileURLToPath(import.meta.url));

// Polyfill browser APIs for Node.js test environment
(globalThis as any).WebSocket = WebSocket;
(globalThis as any).EventSource = EventSource;

export function getServerAddr(): string {
    return readFileSync(join(__dirname, '..', '.test-server-addr'), 'utf-8').trim();
}

export function wsUrl(): string {
    return `ws://${getServerAddr()}/ws`;
}

export function wsRejectUrl(): string {
    return `ws://${getServerAddr()}/ws-reject`;
}

export function sseUrl(): string {
    return `http://${getServerAddr()}/sse`;
}
