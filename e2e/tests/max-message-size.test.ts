import { getEventListeners } from 'node:events';
import { describe, test, expect, afterEach, vi } from 'vitest';
import { wsUrl, wsSmallLimitUrl, wsNoLimitUrl } from './helpers';
import { ApiClient, ConnectionError, MessageTooLargeError, type ClientTransport, type ConnectionState, type TransportCloseInfo } from '../api/client';
import { setBlob, subscribeSetBlob } from '../api/blob-handlers';
import { getConnURL } from '../api/conn-handlers';
import { numbers } from '../api/streaming-handlers';

// The /ws-small server advertises maxMessageSize 4096 in its config frame. A
// frame larger than that would make the server close the socket, failing
// every call on it, so the client must refuse it locally and fail only the
// call that built it.
const LIMIT = 4096;
const BIG = 'x'.repeat(LIMIT * 2);

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

function newClient(url: string | (() => string)): { client: ApiClient; states: ConnectionState[] } {
    const client = new ApiClient(url, { reconnectInterval: 50, reconnectMaxInterval: 50 });
    const states: ConnectionState[] = [];
    client.onStateChange((s) => states.push(s));
    return { client, states };
}

// connected opens a client and makes one round trip, so the config frame
// has been applied whatever connect() waits for. 'a call made before the
// first config frame is checked' covers what connect() itself waits for.
async function connected(url: string | (() => string)): Promise<{ client: ApiClient; states: ConnectionState[] }> {
    const c = newClient(url);
    await c.client.connect();
    await getConnURL(c.client);
    return c;
}

async function waitForState(client: ApiClient, state: ConnectionState): Promise<void> {
    const deadline = Date.now() + 2000;
    while (client.getState() !== state) {
        if (Date.now() > deadline) throw new Error(`state stayed ${client.getState()}, want ${state}`);
        await sleep(5);
    }
}

// dropSocket closes the socket without disconnect(), so the client
// reconnects.
async function dropSocket(client: ApiClient): Promise<void> {
    (client as unknown as { transport: { disconnect: () => void } }).transport.disconnect();
    await waitForState(client, 'reconnecting');
}

// recordFrames records every frame the client hands its transport, including
// ones the transport then refuses.
function recordFrames(client: ApiClient): Array<Record<string, unknown>> {
    const frames: Array<Record<string, unknown>> = [];
    const t = (client as unknown as { transport: ClientTransport }).transport;
    const send = t.send.bind(t);
    t.send = (m: object) => {
        frames.push(m as Record<string, unknown>);
        send(m);
    };
    return frames;
}

describe('maxMessageSize from the config frame', () => {
    let client: ApiClient | undefined;

    afterEach(() => {
        client?.disconnect();
        client = undefined;
        vi.restoreAllMocks();
    });

    test('an oversized request fails locally and the connection stays up', async () => {
        const c = await connected(wsSmallLimitUrl());
        client = c.client;

        const err = await setBlob(client, BIG).catch((e: unknown) => e);
        expect(err).toBeInstanceOf(MessageTooLargeError);
        const tooLarge = err as MessageTooLargeError;
        expect(tooLarge.limit).toBe(LIMIT);
        expect(tooLarge.size).toBeGreaterThan(LIMIT);
        expect(tooLarge.message).toContain(`${tooLarge.size} bytes`);
        expect(tooLarge.message).toContain(`${LIMIT} bytes`);

        // The socket was never touched: no state change, and calls that fit
        // keep working on the same connection.
        expect(client.getState()).toBe('connected');
        await expect(setBlob(client, 'fits')).resolves.toBeNull();
        await expect(getConnURL(client)).resolves.toBeDefined();
        expect(c.states).toEqual(['connecting', 'connected']);
    });

    test('the limit counts UTF-8 bytes, not string length', async () => {
        client = (await connected(wsSmallLimitUrl())).client;

        // 3000 characters but 6000 bytes of UTF-8.
        const err = await setBlob(client, 'é'.repeat(3000)).catch((e: unknown) => e);
        expect(err).toBeInstanceOf(MessageTooLargeError);
        expect((err as MessageTooLargeError).size).toBeGreaterThan(6000);

        // 1500 four-byte characters (surrogate pairs) are 6000 bytes too.
        const emoji = await setBlob(client, '😀'.repeat(1500)).catch((e: unknown) => e);
        expect(emoji).toBeInstanceOf(MessageTooLargeError);
        expect((emoji as MessageTooLargeError).size).toBeGreaterThan(6000);

        // 3000 single-byte characters fit.
        await expect(setBlob(client, 'e'.repeat(3000))).resolves.toBeNull();
    });

    test('an oversized subscription fails through onError and is dropped', async () => {
        const c = await connected(wsSmallLimitUrl());
        client = c.client;

        const errors: Error[] = [];
        const results: unknown[] = [];
        client.subscribe('BlobHandlers.GetBlob', [BIG], (r) => results.push(r), (e) => errors.push(e));
        await sleep(50);

        expect(errors).toHaveLength(1);
        expect(errors[0]).toBeInstanceOf(MessageTooLargeError);
        expect(results).toHaveLength(0);
        expect(client.getState()).toBe('connected');
        expect(client.getLoadingCount()).toBe(0);
        await expect(getConnURL(client)).resolves.toBeDefined();
        expect(c.states).toEqual(['connecting', 'connected']);
    });

    test('an oversized stream fails locally', async () => {
        client = (await connected(wsSmallLimitUrl())).client;

        const iterate = async () => {
            for await (const _ of client!.requestStream('StreamingHandlers.Numbers', [BIG])) {
                // Never reached: the stream fails before it is sent.
            }
        };
        await expect(iterate()).rejects.toBeInstanceOf(MessageTooLargeError);
        expect(client.getState()).toBe('connected');
        expect(client.getLoadingCount()).toBe(0);
        await expect(getConnURL(client)).resolves.toBeDefined();
    });

    test('a call made before the first config frame is checked', async () => {
        const c = newClient(wsSmallLimitUrl());
        client = c.client;

        // Both are buffered while connecting and sent only once the config
        // frame has set the limit.
        const connecting = client.connect();
        const oversized = setBlob(client, BIG).catch((e: unknown) => e);
        const fits = setBlob(client, 'fits');
        await connecting;

        expect(await oversized).toBeInstanceOf(MessageTooLargeError);
        await expect(fits).resolves.toBeNull();
        expect(c.states).toEqual(['connecting', 'connected']);
    });

    test('after a reconnect, the new server\'s higher limit applies', async () => {
        let url = wsSmallLimitUrl();
        const c = await connected(() => url);
        client = c.client;

        // The server comes back with no limit.
        url = wsNoLimitUrl();
        await dropSocket(client);
        const errors: Error[] = [];
        const results: unknown[] = [];
        subscribeSetBlob(client, BIG, (r) => results.push(r), (e) => errors.push(e));
        const request = setBlob(client, BIG);

        await expect(request).resolves.toBeNull();
        await sleep(50);
        expect(errors).toEqual([]);
        expect(results).toHaveLength(1);
        expect(client.getState()).toBe('connected');
    });

    test('after a reconnect, the new server\'s lower limit applies', async () => {
        let url = wsNoLimitUrl();
        const c = await connected(() => url);
        client = c.client;

        // The server comes back with a 4096-byte limit. Sending the queued
        // oversized request would make it close the connection.
        url = wsSmallLimitUrl();
        await dropSocket(client);
        const oversized = setBlob(client, BIG).catch((e: unknown) => e);
        const fits = setBlob(client, 'fits');

        expect(await oversized).toBeInstanceOf(MessageTooLargeError);
        await expect(fits).resolves.toBeNull();
        await sleep(50);
        expect(c.states).toEqual(['connecting', 'connected', 'reconnecting', 'connected']);
    });

    test('aborting a refused request sends no cancel', async () => {
        client = (await connected(wsSmallLimitUrl())).client;
        const frames = recordFrames(client);

        const ac = new AbortController();
        const err = await setBlob(client, BIG, { signal: ac.signal }).catch((e: unknown) => e);
        expect(err).toBeInstanceOf(MessageTooLargeError);
        ac.abort();
        await sleep(10);

        expect(frames.filter((f) => f.type === 'cancel')).toEqual([]);
        expect(client.getLoadingCount()).toBe(0);
    });

    test('params that cannot be serialized fail only that call', async () => {
        client = (await connected(wsSmallLimitUrl())).client;

        const err = await setBlob(client, 1n as unknown as string).catch((e: unknown) => e);
        expect(err).toBeInstanceOf(TypeError);
        expect(client.getLoadingCount()).toBe(0);

        // In a flush, the calls after it are still sent.
        await dropSocket(client);
        const bad = setBlob(client, 1n as unknown as string).catch((e: unknown) => e);
        const fits = setBlob(client, 'fits');
        expect(await bad).toBeInstanceOf(TypeError);
        await expect(fits).resolves.toBeNull();
        expect(client.getLoadingCount()).toBe(0);
    });

    test('an oversized subscription unsubscribed at once reports nothing', async () => {
        client = (await connected(wsSmallLimitUrl())).client;
        const frames = recordFrames(client);
        const reported: Error[] = [];
        client.onRequestError((e) => reported.push(e));

        // A React effect that subscribes and is cleaned up in the same tick.
        const errors: Error[] = [];
        const unsubscribe = client.subscribe('BlobHandlers.GetBlob', [BIG], () => {}, (e) => errors.push(e));
        unsubscribe();
        await sleep(20);

        expect(errors).toEqual([]);
        expect(reported).toEqual([]);
        // The server never saw the subscription, so there is nothing to end.
        expect(frames.filter((f) => f.type === 'unsubscribe')).toEqual([]);
    });

    test('each frame is serialized once', async () => {
        client = (await connected(wsSmallLimitUrl())).client;
        const stringify = vi.spyOn(JSON, 'stringify');

        await setBlob(client, 'fits');
        const requestFrames = stringify.mock.calls.filter(([v]) => (v as { type?: string })?.type === 'request');
        expect(requestFrames).toHaveLength(1);
    });

    test('with no advertised limit nothing is rejected locally', async () => {
        client = (await connected(wsNoLimitUrl())).client;

        await expect(setBlob(client, 'x'.repeat(256 * 1024))).resolves.toBeNull();
        expect(client.getState()).toBe('connected');
    });
});

// LimitTransport is a custom transport that enforces the limit itself, the
// way the ClientTransport contract describes: it refuses a frame over the
// limit it was given, answers auth with auth_ok, and answers requests with
// null unless answerRequests is off. Frame types in refuse throw from send(),
// as a transport whose channel fails would.
class LimitTransport implements ClientTransport {
    limits: Array<number | undefined> = [];
    sent: Array<Record<string, unknown>> = [];
    answerRequests = true;
    refuse = new Set<string>();
    connects = 0;
    private onMessage: ((d: string) => void) | null = null;
    private onClose: ((i?: TransportCloseInfo) => void) | null = null;
    private open = false;
    constructor(private config: object | null) {}

    connect(_url: string, onMessage: (d: string | Blob | ArrayBuffer) => void, onClose: (i?: TransportCloseInfo) => void): Promise<void> {
        this.onMessage = onMessage as (d: string) => void;
        this.onClose = onClose;
        this.open = true;
        this.connects++;
        if (this.config) this.deliver(this.config);
        return Promise.resolve();
    }
    setMaxMessageSize(limit: number | undefined): void {
        this.limits.push(limit);
    }
    send(message: object): void {
        const m = message as Record<string, unknown>;
        if (this.refuse.has(m.type as string)) throw new Error(`channel refused ${m.type as string}`);
        const data = JSON.stringify(message);
        const limit = this.limits[this.limits.length - 1];
        if (limit !== undefined && data.length > limit) throw new MessageTooLargeError(data.length, limit);
        this.sent.push(m);
        if (m.type === 'auth') this.deliver({ type: 'auth_ok' });
        else if (m.type === 'request' && this.answerRequests) this.deliver({ type: 'response', id: m.id, result: null });
    }
    private deliver(m: object): void {
        const cb = this.onMessage;
        queueMicrotask(() => cb?.(JSON.stringify(m)));
    }
    disconnect(): void {
        if (!this.open) return;
        this.open = false;
        const cb = this.onClose;
        queueMicrotask(() => cb?.({ code: 1000, wasClean: true }));
    }
    isConnected(): boolean { return this.open; }
}

// RejectAfterConfigTransport plays a byte-stream server whose connect hook
// rejects the connection: that server sends config first, then the
// rejection. Both arrive in one task, and disconnect() reports the close
// synchronously.
class RejectAfterConfigTransport implements ClientTransport {
    private open = false;
    private onClose: ((i?: TransportCloseInfo) => void) | null = null;

    connect(_url: string, onMessage: (d: string | Blob | ArrayBuffer) => void, onClose: (i?: TransportCloseInfo) => void): Promise<void> {
        this.onClose = onClose;
        this.open = true;
        queueMicrotask(() => {
            onMessage(JSON.stringify({ type: 'config' }));
            onMessage(JSON.stringify({ type: 'error', code: -32002, message: 'rejected by hook' }));
        });
        return Promise.resolve();
    }
    send(): void {}
    disconnect(): void {
        if (!this.open) return;
        this.open = false;
        this.onClose?.({ code: 1000, wasClean: true });
    }
    isConnected(): boolean { return this.open; }
}

describe('maxMessageSize with a custom transport', () => {
    // A test that times out never reaches its own cleanup, so fake timers
    // must not outlive it and stall the next test's sleeps.
    afterEach(() => { vi.useRealTimers(); });

    test('a rejection right after the config frame leaves the client disconnected', async () => {
        const client = new ApiClient('ws://unused', { transport: new RejectAfterConfigTransport(), reconnect: false });
        await client.connect();
        await sleep(10);

        expect(client.getState()).toBe('disconnected');
        expect(client.getLastConnectionError()?.isServerRejected()).toBe(true);
    });

    test('disconnect() during the config wait settles connect() at once', async () => {
        const client = new ApiClient('ws://unused', { transport: new LimitTransport(null), reconnect: false, connectTimeout: 10000 });
        const connecting = client.connect();
        await sleep(10);
        expect(client.getState()).toBe('connecting');

        const started = Date.now();
        client.disconnect();
        await connecting;
        expect(Date.now() - started).toBeLessThan(1000);
        expect(client.getState()).toBe('disconnected');
    });

    test('the transport gets the limit and its refusal fails only that call', async () => {
        const t = new LimitTransport({ type: 'config', maxMessageSize: 100 });
        const client = new ApiClient('ws://unused', { transport: t, reconnect: false });
        await client.connect();

        // Cleared for the new connection, then set from its config frame.
        expect(t.limits).toEqual([undefined, 100]);
        const err = await client.request('BlobHandlers.SetBlob', ['x'.repeat(200)]).catch((e: unknown) => e);
        expect(err).toBeInstanceOf(MessageTooLargeError);
        await expect(client.request('BlobHandlers.SetBlob', ['fits'])).resolves.toBeNull();
        expect(client.getState()).toBe('connected');
        client.disconnect();
    });

    test('an attempt with no config frame fails with an error that says so', async () => {
        const t = new LimitTransport(null);
        const client = new ApiClient('ws://unused', { transport: t, reconnect: false, connectTimeout: 50 });
        const errors: Error[] = [];
        client.onConnectionError((e) => errors.push(e));

        const connecting = client.connect();
        const request = client.request('BlobHandlers.SetBlob', ['fits']).catch((e: unknown) => e);
        await connecting;
        await sleep(10);

        expect(client.getState()).toBe('disconnected');
        expect(t.sent).toEqual([]);
        // Reported once: the transport's own close report is not handled again.
        expect(errors).toHaveLength(1);
        expect(errors[0].message).toContain('no config frame arrived within 50 ms');
        expect(client.getLastConnectionError()?.isNetworkError()).toBe(true);
        expect(await request).toBe(errors[0]);
    });

    test('attempts with no config frame count toward reconnectMaxAttempts', async () => {
        const t = new LimitTransport(null);
        const client = new ApiClient('ws://unused', {
            transport: t, connectTimeout: 20, reconnectInterval: 5, reconnectMaxInterval: 5, reconnectMaxAttempts: 2,
        });
        await client.connect();
        await sleep(400);

        // The first attempt and two retries, then the client gives up.
        expect(t.connects).toBe(3);
        expect(client.getState()).toBe('disconnected');
    });

    test('with connectTimeout 0 the config wait is still bounded', async () => {
        vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
        const client = new ApiClient('ws://unused', { transport: new LimitTransport(null), reconnect: false, connectTimeout: 0 });
        const connecting = client.connect();
        await vi.advanceTimersByTimeAsync(10_000);
        await connecting;

        expect(client.getState()).toBe('disconnected');
        expect(client.getLastConnectionError()?.message).toContain('no config frame arrived within 10000 ms');
    });

    test('an auth frame over the limit is reported with its cause', async () => {
        const t = new LimitTransport({ type: 'config', maxMessageSize: 100 });
        const rejections: Error[] = [];
        const client = new ApiClient('ws://unused', {
            transport: t,
            reconnect: false,
            getAuthToken: () => 'x'.repeat(200),
            onConnectionRejected: (e) => rejections.push(e),
        });
        await client.connect();
        await sleep(10);

        expect(rejections).toHaveLength(1);
        expect(rejections[0].message).toMatch(/^authentication failed: message too large: the message is \d+ bytes/);
        expect(client.getState()).toBe('disconnected');
    });

    test('a refused refreshAuth frame leaves no auth waiter behind', async () => {
        const t = new LimitTransport({ type: 'config', maxMessageSize: 100 });
        const client = new ApiClient('ws://unused', { transport: t, reconnect: false, getAuthToken: () => 'tok' });
        await client.connect();

        await expect(client.refreshAuth('x'.repeat(200))).rejects.toBeInstanceOf(MessageTooLargeError);
        // No answer will come for a frame that never went out, so nothing may
        // wait for one: a later id-less auth_error would be taken as its answer.
        expect((client as unknown as { authWaiter: unknown }).authWaiter).toBeNull();
        await expect(client.refreshAuth('tok2')).resolves.toBeUndefined();
        client.disconnect();
    });

    test('a cancel or unsubscribe the transport refuses neither throws nor hangs the call', async () => {
        const t = new LimitTransport({ type: 'config' });
        t.answerRequests = false;
        t.refuse = new Set(['cancel', 'unsubscribe']);
        const client = new ApiClient('ws://unused', { transport: t, reconnect: false });
        await client.connect();

        const ac = new AbortController();
        const request = client.request('BlobHandlers.SetBlob', ['fits'], { signal: ac.signal });
        ac.abort();
        const outcome = await Promise.race([
            request.then(() => 'resolved', (e: Error) => e.message),
            sleep(200).then(() => 'still pending'),
        ]);
        expect(outcome).toBe('Request aborted');

        const unsubscribe = client.subscribe('BlobHandlers.GetBlob', [], () => {});
        expect(() => unsubscribe()).not.toThrow();
        expect(client.getLoadingCount()).toBe(0);
        client.disconnect();
    });
});

describe('abort listeners', () => {
    let client: ApiClient | undefined;

    afterEach(() => {
        client?.disconnect();
        client = undefined;
    });

    test('a finished request or stream leaves no listener on its signal', async () => {
        // The main endpoint has the streaming handlers and the default 4 MiB
        // limit, so the refused calls need a payload over 4 MiB.
        client = (await connected(wsUrl())).client;
        const huge = 'x'.repeat(5 * 1024 * 1024);
        const ac = new AbortController();

        await setBlob(client, 'fits', { signal: ac.signal });
        await expect(setBlob(client, huge, { signal: ac.signal })).rejects.toBeInstanceOf(MessageTooLargeError);
        for await (const _ of numbers(client, 2, 0, { signal: ac.signal })) {
            // Drain the stream to its end.
        }
        const refused = async () => {
            for await (const _ of client!.requestStream('StreamingHandlers.Numbers', [huge], { signal: ac.signal })) {
                // Never reached: the stream fails before it is sent.
            }
        };
        await expect(refused()).rejects.toBeInstanceOf(MessageTooLargeError);

        expect(getEventListeners(ac.signal, 'abort')).toHaveLength(0);
        // So aborting the shared signal later cancels nothing.
        const frames = recordFrames(client);
        ac.abort();
        await sleep(10);
        expect(frames.filter((f) => f.type === 'cancel')).toEqual([]);
    });

    test('a request buffered while connecting leaves no listener once it settles', async () => {
        const c = newClient(wsSmallLimitUrl());
        client = c.client;
        const ac = new AbortController();

        const connecting = client.connect();
        const request = setBlob(client, 'fits', { signal: ac.signal });
        await connecting;
        await expect(request).resolves.toBeNull();

        expect(getEventListeners(ac.signal, 'abort')).toHaveLength(0);
    });

    // _onSettle is how tasks.ts removes its push listeners, so a buffered
    // request that leaves the buffer unsent must still call it.
    test('a buffered request aborted before it goes out settles its options', async () => {
        // No config frame arrives, so the request stays buffered.
        client = new ApiClient('ws://unused', { transport: new LimitTransport(null), reconnect: false });
        const settled: string[] = [];
        void client.connect();
        const ac = new AbortController();
        const request = client.request('BlobHandlers.SetBlob', ['a'], { signal: ac.signal, _onSettle: () => settled.push('settled') });

        ac.abort();
        await expect(request).rejects.toThrow('Request aborted');
        expect(settled).toEqual(['settled']);
    });

    test('a buffered request rejected with the buffer settles its options and listener', async () => {
        // The attempt gets no config frame and, with reconnect off, the
        // client gives up and rejects the buffer.
        client = new ApiClient('ws://unused', { transport: new LimitTransport(null), reconnect: false, connectTimeout: 20 });
        const settled: string[] = [];
        const ac = new AbortController();
        const connecting = client.connect();
        const request = client.request('BlobHandlers.SetBlob', ['b'], { signal: ac.signal, _onSettle: () => settled.push('settled') })
            .catch((e: unknown) => e);
        await connecting;

        expect(await request).toBeInstanceOf(ConnectionError);
        expect(settled).toEqual(['settled']);
        expect(getEventListeners(ac.signal, 'abort')).toHaveLength(0);
    });
});
