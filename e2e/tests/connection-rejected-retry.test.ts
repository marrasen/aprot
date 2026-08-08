import { describe, test, expect } from 'vitest';
import { wsRejectUrl, wsTokenUrl, wsUrl } from './helpers';
import { ApiClient, ApiError, ErrorCode } from '../api/client';

// reconnectOnRejected turns a server rejection from terminal into retryable —
// the opt-in behavior consumers previously hand-rolled with
// onConnectionRejected + setTimeout(connect) (#283). The default (terminal)
// behavior is pinned by connection-rejected.test.ts.
describe('Rejection retry (WebSocket)', () => {
    test('retries a rejection and connects once the params carry a fresh token', async () => {
        let rejections = 0;
        let attempt = 0;

        const client = new ApiClient(wsTokenUrl(), {
            reconnectOnRejected: { delayMs: 100 },
            // First attempt presents a stale token, the retry a fresh one.
            getConnectParams: () => ({ token: attempt++ === 0 ? 'stale' : 'good' }),
            onConnectionRejected: () => {
                rejections++;
            },
        });

        await client.connect();

        await new Promise((resolve) => setTimeout(resolve, 1000));

        expect(client.getState()).toBe('connected');
        expect(rejections).toBe(1);
        // A successful connect clears the rejection history.
        expect(client.getLastRejection()).toBeNull();

        client.disconnect();
    });

    test('maxAttempts bounds the retries and then parks the client', async () => {
        let rejections = 0;

        const client = new ApiClient(wsRejectUrl(), {
            reconnectOnRejected: { delayMs: 50, maxAttempts: 2 },
            onConnectionRejected: () => {
                rejections++;
            },
        });

        await client.connect();

        await new Promise((resolve) => setTimeout(resolve, 800));

        // Initial attempt plus exactly two retries.
        expect(rejections).toBe(3);
        expect(client.getState()).toBe('disconnected');

        // Parked for good: no further attempts.
        await new Promise((resolve) => setTimeout(resolve, 400));
        expect(rejections).toBe(3);

        client.disconnect();
    });

    test('disconnect() during the retry wait cancels the pending retry', async () => {
        let rejections = 0;

        const client = new ApiClient(wsRejectUrl(), {
            reconnectOnRejected: { delayMs: 500 },
            onConnectionRejected: () => {
                rejections++;
            },
        });

        await client.connect();

        // Wait for the first rejection but not for the retry to fire.
        await new Promise((resolve) => setTimeout(resolve, 200));
        expect(rejections).toBe(1);
        expect(client.getState()).toBe('reconnecting');

        client.disconnect();

        await new Promise((resolve) => setTimeout(resolve, 800));
        expect(rejections).toBe(1);
        expect(client.getState()).toBe('disconnected');
    });

    test('the retry streak resets after a successful connect', async () => {
        let rejections = 0;
        let attempt = 0;

        const client = new ApiClient(wsTokenUrl(), {
            // One retry per streak — a non-resetting counter would park the
            // client on the second streak.
            reconnectOnRejected: { delayMs: 100, maxAttempts: 1 },
            getConnectParams: () => ({ token: attempt++ % 2 === 0 ? 'stale' : 'good' }),
            onConnectionRejected: () => {
                rejections++;
            },
        });

        await client.connect();
        await new Promise((resolve) => setTimeout(resolve, 800));
        expect(client.getState()).toBe('connected');
        expect(rejections).toBe(1);

        client.disconnect();
        await client.connect();
        await new Promise((resolve) => setTimeout(resolve, 800));
        expect(client.getState()).toBe('connected');
        expect(rejections).toBe(2);

        client.disconnect();
    });

    test('getLastRejection reports why the session ended, even after later failures', async () => {
        const client = new ApiClient(wsRejectUrl(), { reconnect: false });
        await client.connect();
        await new Promise((resolve) => setTimeout(resolve, 300));

        const rejection = client.getLastRejection();
        expect(rejection).toBeInstanceOf(ApiError);
        expect(rejection!.code).toBe(ErrorCode.ConnectionRejected);
        expect(rejection!.message).toBe('invalid session');
        expect(client.getLastConnectionError()!.reason).toBe('server-rejected');

        // A later transport failure overwrites lastConnectionError but must not
        // erase why the session ended — that is the distinction a UI needs to
        // render "session expired" rather than "server unreachable".
        const unreachable = new ApiClient('ws://127.0.0.1:1/ws', { reconnect: false });
        await unreachable.connect();
        await new Promise((resolve) => setTimeout(resolve, 300));
        expect(unreachable.getLastConnectionError()!.reason).toBe('network-error');
        expect(unreachable.getLastRejection()).toBeNull();

        expect(client.getLastRejection()).toBe(rejection);

        client.disconnect();
        unreachable.disconnect();
    });

    test('a clean connect leaves no rejection recorded', async () => {
        const client = new ApiClient(wsUrl(), { reconnect: false });
        await client.connect();

        expect(client.getState()).toBe('connected');
        expect(client.getLastRejection()).toBeNull();

        client.disconnect();
    });
});
