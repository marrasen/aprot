import { describe, test, expect } from 'vitest';
import { ApiClient, type ClientTransport, type TransportCloseInfo } from '../api/client';
import { subscribeGet, takePeak } from '../api/burst-handlers';
import { numbers } from '../api/streaming-handlers';

// With getAuthToken set, the client must send nothing but the auth frame until
// auth_ok arrives. The server answers any other frame that arrives before auth
// with an auth_error that carries no id; a client that took that as the answer
// to its own auth frame rejected its own connection.
//
// AuthGateTransport mimics the server's gate (connection.go): before auth_ok,
// every non-auth frame gets an id-less auth_error. The test decides when the
// auth frame is answered. After auth_ok, requests and subscribes are answered
// with their first param, and stream requests with one item.
class AuthGateTransport implements ClientTransport {
    sent: Array<Record<string, unknown>> = [];
    private onMessage: ((d: string) => void) | null = null;
    private onClose: ((i?: TransportCloseInfo) => void) | null = null;
    private open = false;
    private authed = false;
    private authPending = false;

    connect(_url: string, onMessage: (d: string | Blob | ArrayBuffer) => void, onClose: (i?: TransportCloseInfo) => void): Promise<void> {
        this.onMessage = onMessage as (d: string) => void;
        this.onClose = onClose;
        this.open = true;
        this.authed = false;
        return Promise.resolve();
    }

    send(message: object): void {
        const m = message as Record<string, unknown>;
        this.sent.push(m);
        if (m.type === 'auth') {
            this.authPending = true;
            return;
        }
        if (!this.authed) {
            this.deliver({ type: 'auth_error', message: 'authentication required' });
            return;
        }
        const params = (m.params as unknown[] | undefined) ?? [];
        if (m.type === 'subscribe' || (m.type === 'request' && m.method !== 'StreamingHandlers.Numbers')) {
            this.deliver({ type: 'response', id: m.id, result: params[0] ?? 0 });
        } else if (m.type === 'request') {
            this.deliver({ type: 'stream_item', id: m.id, item: { n: 1 } });
            this.deliver({ type: 'stream_end', id: m.id });
        }
    }

    answerAuth(): void {
        if (!this.authPending) throw new Error('no auth frame sent yet');
        this.authPending = false;
        this.authed = true;
        this.deliver({ type: 'auth_ok' });
    }

    // serverSend delivers a frame as if the server sent it unprompted.
    serverSend(m: object): void {
        this.deliver(m);
    }

    serverClose(): void {
        this.open = false;
        const cb = this.onClose;
        queueMicrotask(() => cb?.({ code: 1000, wasClean: true }));
    }

    private deliver(m: object): void {
        const cb = this.onMessage;
        queueMicrotask(() => cb?.(JSON.stringify(m)));
    }

    disconnect(): void {
        if (!this.open) return;
        this.serverClose();
    }

    isConnected(): boolean { return this.open; }
}

const tick = () => new Promise((r) => setTimeout(r, 0));

// newClient returns a client whose getAuthToken waits until giveToken is
// called, with the connect started and the transport open.
async function connectWithSlowToken(): Promise<{
    t: AuthGateTransport; client: ApiClient; connected: Promise<void>; giveToken: () => void;
}> {
    const t = new AuthGateTransport();
    let resolveToken!: (tok: string) => void;
    const client = new ApiClient('ws://unused', {
        transport: t,
        reconnect: false,
        getAuthToken: () => new Promise<string>((r) => { resolveToken = r; }),
    });
    const connected = client.connect();
    await tick();
    return { t, client, connected, giveToken: () => resolveToken('tok') };
}

describe('Frames sent while authenticating', () => {
    test('a subscribe made while the token is being fetched waits for auth_ok', async () => {
        const { t, client, connected, giveToken } = await connectWithSlowToken();
        const values: number[] = [];
        const errors: Error[] = [];
        subscribeGet(client, 7, (v) => values.push(v), (e) => errors.push(e));
        expect(t.sent).toEqual([]);

        giveToken();
        await tick();
        expect(t.sent.map((m) => m.type)).toEqual(['auth']);
        t.answerAuth();
        await connected;
        await tick();

        expect(t.sent.map((m) => m.type)).toEqual(['auth', 'subscribe']);
        expect(values).toEqual([7]);
        expect(errors).toEqual([]);
        expect(client.getLastRejection()).toBeNull();
        expect(client.getState()).toBe('connected');
        client.disconnect();
    });

    test('a request made while the token is being fetched is sent after auth_ok', async () => {
        const { t, client, connected, giveToken } = await connectWithSlowToken();
        const req = takePeak(client);
        expect(client.getLoadingCount()).toBe(1);
        expect(t.sent).toEqual([]);

        giveToken();
        await tick();
        t.answerAuth();
        await connected;

        expect(await req).toBe(0);
        expect(t.sent.map((m) => m.type)).toEqual(['auth', 'request']);
        expect(client.getLastRejection()).toBeNull();
        expect(client.getState()).toBe('connected');
        client.disconnect();
    });

    test('a stream started while the token is being fetched starts after auth_ok', async () => {
        const { t, client, connected, giveToken } = await connectWithSlowToken();
        const items: unknown[] = [];
        const done = (async () => {
            for await (const item of numbers(client, 1, 0)) items.push(item);
        })();
        await tick();
        expect(t.sent).toEqual([]);
        expect(client.getLoadingCount()).toBe(1);

        giveToken();
        await tick();
        t.answerAuth();
        await connected;
        await done;

        expect(items).toEqual([{ n: 1 }]);
        expect(t.sent.map((m) => m.type)).toEqual(['auth', 'request']);
        expect(client.getLastRejection()).toBeNull();
        client.disconnect();
    });

    test('a stream waiting for auth ends with the connection error when the client gives up', async () => {
        const { t, client } = await connectWithSlowToken();
        const it = numbers(client, 1, 0)[Symbol.asyncIterator]();
        const next = it.next();
        await tick();
        client.disconnect();
        await expect(next).rejects.toThrow('Disconnected');
        expect(t.sent).toEqual([]);
        expect(client.getLoadingCount()).toBe(0);
    });

    test('a subscribe made between the auth frame and auth_ok is sent once', async () => {
        const { t, client, connected, giveToken } = await connectWithSlowToken();
        giveToken();
        await tick(); // auth frame sent, auth_ok not yet
        expect(t.sent.map((m) => m.type)).toEqual(['auth']);

        const values: number[] = [];
        subscribeGet(client, 3, (v) => values.push(v));
        t.answerAuth();
        await connected;
        await tick();

        expect(t.sent.filter((m) => m.type === 'subscribe').length).toBe(1);
        expect(values).toEqual([3]);
        client.disconnect();
    });

    test('an auth_error before the auth frame is sent is not taken as the auth reply', async () => {
        const { t, client, connected, giveToken } = await connectWithSlowToken();
        // An id-less auth_error that cannot answer an auth frame not yet sent.
        t.serverSend({ type: 'auth_error', message: 'authentication required' });
        await tick();
        expect(client.getState()).toBe('connecting');

        giveToken();
        await tick();
        t.answerAuth();
        await connected;
        expect(client.getLastRejection()).toBeNull();
        expect(client.getState()).toBe('connected');
        client.disconnect();
    });

    test('a close while the token is being fetched settles connect and is not a rejection', async () => {
        const { t, client, connected, giveToken } = await connectWithSlowToken();
        // The server's pending-auth timeout: an auth_error, then a close.
        t.serverSend({ type: 'auth_error', message: 'authentication timeout' });
        t.serverClose();
        await connected;

        expect(client.getState()).toBe('disconnected');
        expect(client.getLastRejection()).toBeNull();
        expect(client.getLastConnectionError()?.reason).toBe('server-closed');

        // The late token is not sent on the closed connection.
        giveToken();
        await tick();
        expect(t.sent).toEqual([]);
    });
});

describe('The pending-auth timeout', () => {
    // The server's timeout auth_error is marked timeout: true. It can cross
    // the client's auth frame on the wire, so it must not be taken as the
    // verdict on that frame: the close that follows is an ordinary close.
    test('a timeout auth_error crossing the auth frame is not a rejection', async () => {
        const { t, client, connected, giveToken } = await connectWithSlowToken();
        let rejected = false;
        const c2 = client as unknown as { options: { onConnectionRejected?: () => void } };
        c2.options.onConnectionRejected = () => { rejected = true; };

        giveToken();
        await tick();
        expect(t.sent.map((m) => m.type)).toEqual(['auth']);
        t.serverSend({ type: 'auth_error', message: 'authentication timeout', timeout: true });
        await tick();
        t.serverClose();
        await connected;
        await tick();

        expect(rejected).toBe(false);
        expect(client.getLastRejection()).toBeNull();
        expect(client.getLastConnectionError()?.reason).toBe('server-closed');
    });
});
