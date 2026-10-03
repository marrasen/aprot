import { describe, test, expect } from 'vitest';
import { wsUrl } from './helpers';
import { ApiClient, ConnectionError, type ConnectionState } from '../api/client';
import { login } from '../api/public-handlers';

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const withTimeout = <T>(p: Promise<T>, ms: number) =>
    Promise.race([p.then((v) => ({ ok: true as const, v }), (e) => ({ ok: false as const, e })), sleep(ms).then(() => 'timeout' as const)]);

describe('disconnect() then connect() over a real WebSocket', () => {
    // StrictMode effect pattern while the token loads: the abandoned socket is
    // OPEN; its close event lands after the chained attempt opened a new one.
    test('same-tick disconnect/connect during token fetch: requests work afterwards', async () => {
        let calls = 0;
        let resolveFirst!: (s: string) => void;
        const states: ConnectionState[] = [];
        const errors: string[] = [];
        const client = new ApiClient(wsUrl(), {
            reconnect: false,
            getAuthToken: () => {
                calls++;
                if (calls === 1) return new Promise<string>((r) => { resolveFirst = r; });
                return 'tok';
            },
        });
        client.onStateChange((s) => states.push(s));
        client.onConnectionError((e) => errors.push(`${e.reason}:${e.message}`));
        void client.connect();
        await sleep(100); // socket open, token pending
        client.disconnect();
        const second = client.connect();
        await second;
        await sleep(300); // let the old socket's close event land
        void resolveFirst;
        const r = await withTimeout(login(client, 'r3user', 'pass'), 1000);
        console.log('states', states, 'errors', errors, 'state', client.getState(), 'login', r === 'timeout' ? r : r.ok ? 'ok' : String((r as { e: unknown }).e));
        expect(client.getState()).toBe('connected');
        expect(r).not.toBe('timeout');
        expect((r as { ok: boolean }).ok).toBe(true);
        client.disconnect();
    });

    // Same, with reconnect enabled (default).
    test('same-tick disconnect/connect during token fetch, reconnect on', async () => {
        let calls = 0;
        const states: ConnectionState[] = [];
        const errors: string[] = [];
        const client = new ApiClient(wsUrl(), {
            reconnectInterval: 50,
            getAuthToken: () => {
                calls++;
                if (calls === 1) return new Promise<string>(() => {});
                return 'tok';
            },
        });
        client.onStateChange((s) => states.push(s));
        client.onConnectionError((e) => errors.push(`${e.reason}:${e.message}`));
        void client.connect();
        await sleep(100);
        client.disconnect();
        await client.connect();
        await sleep(500);
        const r = await withTimeout(login(client, 'r3user2', 'pass'), 1000);
        console.log('states', states, 'errors', errors, 'state', client.getState(), 'login', r === 'timeout' ? r : r.ok ? 'ok' : String((r as { e: unknown }).e));
        expect(errors).toEqual(['manual:Disconnected']);
        expect(r).not.toBe('timeout');
        expect((r as { ok: boolean }).ok).toBe(true);
        client.disconnect();
    });

    // Same-tick disconnect/connect while the socket is still opening.
    test('same-tick disconnect/connect while the socket is opening', async () => {
        const states: ConnectionState[] = [];
        const errors: string[] = [];
        const client = new ApiClient(wsUrl(), { reconnect: false });
        client.onStateChange((s) => states.push(s));
        client.onConnectionError((e) => errors.push(`${e.reason}:${e.message}`));
        void client.connect();
        client.disconnect();
        await client.connect();
        await sleep(300);
        const r = await withTimeout(login(client, 'r3user3', 'pass'), 1000);
        console.log('states', states, 'errors', errors, 'state', client.getState(), 'login', r === 'timeout' ? r : r.ok ? 'ok' : String((r as { e: unknown }).e));
        expect(errors).toEqual(['manual:Disconnected']);
        expect(client.getState()).toBe('connected');
        client.disconnect();
    });

    // Documented: connect() synchronously moves the client into 'connecting',
    // so a request issued right after a non-awaited connect() is buffered.
    test('request right after connect() following disconnect() is buffered, not rejected', async () => {
        let resolveTok!: (s: string) => void;
        let calls = 0;
        const client = new ApiClient(wsUrl(), {
            reconnect: false,
            getAuthToken: () => (++calls === 1 ? new Promise<string>((r) => { resolveTok = r; }) : 'tok'),
        });
        void client.connect();
        await sleep(100);
        client.disconnect();
        void client.connect();
        const stateAfter = client.getState();
        const r = await withTimeout(login(client, 'r3user4', 'pass'), 1000);
        void resolveTok;
        console.log('state right after connect()', stateAfter, 'login', r === 'timeout' ? r : r.ok ? 'ok' : String((r as { e: unknown }).e));
        expect(stateAfter).toBe('connecting');
        expect((r as { ok: boolean }).ok).toBe(true);
        client.disconnect();
    });

    // Pre-existing? disconnect() while url() is pending, no connect(): the
    // abandoned attempt should not open a socket.
    test('disconnect() while the url function is pending stays disconnected', async () => {
        let resolveUrl!: (s: string) => void;
        const client = new ApiClient(() => new Promise<string>((r) => { resolveUrl = r; }), { reconnect: false });
        void client.connect();
        await sleep(10);
        client.disconnect();
        resolveUrl(wsUrl());
        await sleep(300);
        console.log('state after url resolved', client.getState());
        expect(client.getState()).toBe('disconnected');
        client.disconnect();
    });

    test('send() throwing synchronously from inside auth', async () => {
        const client = new ApiClient(wsUrl(), { reconnect: false, getAuthToken: () => 'tok' });
        const rejected: unknown[] = [];
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        const tr = (client as any).transport;
        const origSend = tr.send.bind(tr);
        let thrown = false;
        tr.send = (m: { type: string }) => {
            if (m.type === 'auth' && !thrown) { thrown = true; throw new Error('boom'); }
            origSend(m);
        };
        const c2 = new ApiClient(wsUrl(), { reconnect: false, getAuthToken: () => 'tok', onConnectionRejected: (e) => rejected.push(e) });
        void c2;
        const client3 = new ApiClient(wsUrl(), { reconnect: false, getAuthToken: () => 'tok', onConnectionRejected: (e) => rejected.push(e.message) });
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        const tr3 = (client3 as any).transport;
        const o3 = tr3.send.bind(tr3);
        tr3.send = (m: { type: string }) => { if (m.type === 'auth') throw new Error('boom'); o3(m); };
        const r = await withTimeout(client3.connect(), 1000);
        console.log('send-throw', r, client3.getState(), rejected, (client3 as unknown as { authWaiter: unknown }).authWaiter);
        expect(r).not.toBe('timeout');
        expect((client3 as unknown as { authWaiter: unknown }).authWaiter).toBeNull();
        client3.disconnect();
        client.disconnect();
        void ConnectionError;
    });
});
