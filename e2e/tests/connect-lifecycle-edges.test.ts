import { describe, test, expect, vi, afterEach } from 'vitest';
import { ApiClient, type ClientTransport, type TransportCloseInfo } from '../api/client';
import { subscribeGet } from '../api/burst-handlers';

// Scratch tests for the PR #402 review of the TypeScript client.

// EchoTransport answers frames itself. With sync=true it delivers replies
// synchronously from inside send(), as an in-process transport could.
class EchoTransport implements ClientTransport {
    sent: Array<Record<string, unknown>> = [];
    answerSubscribes = true;
    private onMessage: ((d: string) => void) | null = null;
    private onClose: ((i?: TransportCloseInfo) => void) | null = null;
    private open = false;
    constructor(private sync: boolean) {}

    connect(_url: string, onMessage: (d: string | Blob | ArrayBuffer) => void, onClose: (i?: TransportCloseInfo) => void): Promise<void> {
        this.onMessage = onMessage as (d: string) => void;
        this.onClose = onClose;
        this.open = true;
        return Promise.resolve();
    }
    send(message: object): void {
        // Like WebSocketTransport: a send on a closed channel is a no-op.
        if (!this.open) return;
        const m = message as Record<string, unknown>;
        this.sent.push(m);
        if (m.type === 'auth') this.deliver({ type: 'auth_ok' });
        else if (m.type === 'subscribe' && this.answerSubscribes) this.deliver({ type: 'response', id: m.id, result: 1 });
    }
    private deliver(m: object): void {
        const cb = this.onMessage;
        if (this.sync) cb?.(JSON.stringify(m));
        else queueMicrotask(() => cb?.(JSON.stringify(m)));
    }
    disconnect(): void {
        if (!this.open) return;
        this.open = false;
        const cb = this.onClose;
        queueMicrotask(() => cb?.({ code: 1000, wasClean: true }));
    }
    isConnected(): boolean { return this.open; }
}

const tick = () => new Promise((r) => setTimeout(r, 0));

describe('Connect lifecycle edge cases', () => {
    afterEach(() => { vi.useRealTimers(); });

    // Finding: authenticate() sets waiter.sent = true only AFTER
    // transport.send(). A transport that delivers auth_ok synchronously from
    // send() has it dropped by `if (!waiter?.sent) break;`, and connect()
    // never settles. Passed before 60ea293.
    test('auth_ok delivered synchronously from send() completes the handshake', async () => {
        const t = new EchoTransport(true);
        const client = new ApiClient('ws://unused', { transport: t, reconnect: false, getAuthToken: () => 'tok' });
        const result = await Promise.race([
            client.connect().then(() => 'settled'),
            new Promise((r) => setTimeout(() => r('timeout'), 500)),
        ]);
        expect(result).toBe('settled');
        expect(client.getState()).toBe('connected');
        client.disconnect();
    });

    // Control: the same transport with async delivery works.
    test('auth_ok delivered asynchronously completes the handshake', async () => {
        const t = new EchoTransport(false);
        const client = new ApiClient('ws://unused', { transport: t, reconnect: false, getAuthToken: () => 'tok' });
        await client.connect();
        expect(client.getState()).toBe('connected');
        client.disconnect();
    });

    // Residual (pre-existing in a worse form): disconnect() then connect() in
    // the same tick while the token loads (React StrictMode effect pattern).
    // connect() joins the dying attempt via connectInFlight and the client
    // ends 'disconnected'.
    test('disconnect() then connect() during the token fetch ends connected', async () => {
        const t = new EchoTransport(false);
        let resolveToken!: (s: string) => void;
        const client = new ApiClient('ws://unused', {
            transport: t, reconnect: false,
            getAuthToken: () => new Promise<string>((r) => { resolveToken = r; }),
        });
        void client.connect();
        await tick();
        client.disconnect();
        const second = client.connect();
        await tick();
        resolveToken?.('tok');
        await second;
        await tick();
        expect(client.getState()).toBe('connected');
        client.disconnect();
    });

    // Drain timer is cleared by disconnect() (no timer outlives the client).
    test('no drain timer outlives disconnect()', async () => {
        vi.useFakeTimers();
        const t = new EchoTransport(false);
        t.answerSubscribes = false;
        const client = new ApiClient('ws://unused', { transport: t, reconnect: false });
        const p = client.connect();
        await vi.advanceTimersByTimeAsync(0);
        await p;
        const unsub = subscribeGet(client, 1, () => {});
        unsub();
        expect(vi.getTimerCount()).toBe(1);
        client.disconnect();
        expect(vi.getTimerCount()).toBe(0);
    });

    // A draining slot the server never answers frees after 10 s and the
    // queued subscribe is then sent.
    test('a draining slot the server never answers frees after the timeout', async () => {
        vi.useFakeTimers();
        const t = new EchoTransport(false);
        t.answerSubscribes = false;
        const client = new ApiClient('ws://unused', { transport: t, reconnect: false });
        const p = client.connect();
        await vi.advanceTimersByTimeAsync(0);
        await p;
        // 64 live, then drop them all (64 draining), then 64 more live.
        let unsubs = Array.from({ length: 64 }, (_, i) => subscribeGet(client, i, () => {}));
        for (const u of unsubs) u();
        unsubs = Array.from({ length: 64 }, (_, i) => subscribeGet(client, i, () => {}));
        await vi.advanceTimersByTimeAsync(0);
        for (const u of unsubs) u(); // 128 draining
        subscribeGet(client, 999, () => {});
        await vi.advanceTimersByTimeAsync(0);
        const subs = () => t.sent.filter((m) => m.type === 'subscribe').length;
        expect(subs()).toBe(128);
        await vi.advanceTimersByTimeAsync(10000);
        expect(subs()).toBe(129);
        client.disconnect();
    });
});
