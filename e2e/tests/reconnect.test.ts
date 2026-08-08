import { describe, test, expect } from 'vitest';
import { wsUrl } from './helpers';
import { ApiClient, type ConnectionState } from '../api/client';
import { listUsers, processBatch } from '../api/public-handlers';

// A connection attempt started while a reconnect backoff is pending must
// cancel that backoff. Left armed, the timer fires after the connection is
// already up and replaces the live socket — and because the transport detaches
// the replaced socket's handlers, its close is never reported, so anything in
// flight at that moment never settles (issue #287).

const DEAD_URL = 'ws://127.0.0.1:1/ws'; // connection refused, immediately

function sleep(ms: number): Promise<void> {
    return new Promise((r) => setTimeout(r, ms));
}

// enterBackoff builds a client whose first attempt fails, leaving it in
// 'reconnecting' with a timer armed, then points it at the real server.
// Returns the client plus the per-attempt URL-resolution counter, which is the
// observable "did we open another connection" signal.
async function enterBackoff(reconnectInterval: number): Promise<{
    client: ApiClient;
    attempts: () => number;
}> {
    let url = DEAD_URL;
    let attempts = 0;
    const client = new ApiClient(
        () => {
            attempts++;
            return url;
        },
        {
            reconnect: true,
            reconnectInterval,
            reconnectMaxInterval: reconnectInterval,
        },
    );

    await client.connect();
    expect(client.getState()).toBe('reconnecting');
    expect(attempts).toBe(1);

    url = wsUrl();
    return { client, attempts: () => attempts };
}

describe('reconnect backoff', () => {
    test('connect() during backoff connects now and disarms the pending timer', async () => {
        const interval = 300;
        const { client, attempts } = await enterBackoff(interval);

        await client.connect();
        expect(client.getState()).toBe('connected');
        expect(attempts()).toBe(2);

        // Watch for the stale timer: pre-fix it fires here, taking the client
        // through 'connecting' again as it replaces the working socket.
        const states: ConnectionState[] = [];
        const off = client.onStateChange((s) => states.push(s));
        await sleep(interval * 3);
        off();

        expect(states).toEqual([]);
        expect(attempts()).toBe(2);
        expect(client.getState()).toBe('connected');

        // The socket that survived is the one still in use.
        await expect(listUsers(client)).resolves.toMatchObject({ users: expect.any(Array) });

        client.disconnect();
    });

    test('a request in flight when the cancelled timer would have fired still settles', async () => {
        const interval = 300;
        const { client } = await enterBackoff(interval);
        await client.connect();

        // A ~600ms request issued at ~interval/2 is still in flight when the
        // stale timer fires. Pre-fix that timer replaced the socket underneath
        // it, and since the replaced socket's close is never reported the
        // request neither resolved nor rejected — it just hung.
        await sleep(interval / 2);
        const result = await Promise.race([
            processBatch(client, ['a', 'b', 'c', 'd', 'e', 'f'], 100).then(() => 'settled'),
            sleep(5000).then(() => 'hung'),
        ]);
        expect(result).toBe('settled');

        client.disconnect();
    });

    test('reconnectNow() abandons a long backoff instead of waiting it out', async () => {
        // Long enough that only cancelling the timer — not waiting for it —
        // can get this test connected.
        const { client, attempts } = await enterBackoff(60_000);

        await client.reconnectNow();
        expect(client.getState()).toBe('connected');
        expect(attempts()).toBe(2);

        client.disconnect();
    });

    test('reconnectNow() is a no-op on a live connection', async () => {
        let attempts = 0;
        const client = new ApiClient(() => {
            attempts++;
            return wsUrl();
        });
        await client.connect();
        expect(attempts).toBe(1);

        await client.reconnectNow();
        expect(attempts).toBe(1);
        expect(client.getState()).toBe('connected');
        await expect(listUsers(client)).resolves.toMatchObject({ users: expect.any(Array) });

        client.disconnect();
    });

    test('concurrent connection attempts share one socket', async () => {
        const { client, attempts } = await enterBackoff(60_000);

        // Both calls are made before either resolves, so the second must join
        // the attempt in flight rather than opening a competing socket.
        await Promise.all([client.reconnectNow(), client.reconnectNow()]);
        expect(attempts()).toBe(2);
        expect(client.getState()).toBe('connected');

        client.disconnect();
    });
});
