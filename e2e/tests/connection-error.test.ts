import { describe, test, expect } from 'vitest';
import { wsUrl, wsRejectUrl } from './helpers';
import {
    ApiClient,
    ApiError,
    ConnectionError,
    ErrorCode,
    type ConnectionErrorReason,
} from '../api/client';

// These tests exercise the structured ConnectionError API end-to-end. The
// reason classification is the user-visible payoff — apps need to switch on
// 'offline' / 'server-rejected' / 'server-closed' / 'network-error' /
// 'manual' to render appropriate messaging.

describe('ConnectionError', () => {
    test('extends Error so legacy `instanceof Error` catches still match', () => {
        const err = new ConnectionError('manual', 'test');
        expect(err).toBeInstanceOf(Error);
        expect(err).toBeInstanceOf(ConnectionError);
        expect(err.name).toBe('ConnectionError');
    });

    test('exposes reason + helper predicates for every reason', () => {
        const reasons: ConnectionErrorReason[] = [
            'offline',
            'server-rejected',
            'server-closed',
            'network-error',
            'manual',
        ];
        for (const r of reasons) {
            const err = new ConnectionError(r, 'm');
            expect(err.reason).toBe(r);
            expect(err.isOffline()).toBe(r === 'offline');
            expect(err.isServerRejected()).toBe(r === 'server-rejected');
            expect(err.isServerClosed()).toBe(r === 'server-closed');
            expect(err.isNetworkError()).toBe(r === 'network-error');
            expect(err.isManual()).toBe(r === 'manual');
        }
    });

    test('server-rejected: pending request rejects with ConnectionError carrying ApiError as cause', async () => {
        const client = new ApiClient(wsRejectUrl(), {
            reconnect: false,
        });

        const errors: ConnectionError[] = [];
        client.onConnectionError((err) => errors.push(err));

        await client.connect();

        // Wait for the server to send the ConnectionRejected ApiError and tear
        // the socket down. 500ms matches the existing connection-rejected.test.ts
        // pacing.
        await new Promise((r) => setTimeout(r, 500));

        expect(client.getState()).toBe('disconnected');
        expect(errors.length).toBeGreaterThanOrEqual(1);

        const last = client.getLastConnectionError();
        expect(last).toBeInstanceOf(ConnectionError);
        expect(last!.reason).toBe('server-rejected');
        expect(last!.isServerRejected()).toBe(true);
        // cause is the underlying ApiError so apps can inspect err.cause.code
        // without re-running the rejection callback.
        expect(last!.cause).toBeInstanceOf(ApiError);
        expect((last!.cause as ApiError).code).toBe(ErrorCode.ConnectionRejected);
        expect((last!.cause as ApiError).message).toBe('invalid session');

        client.disconnect();
    });

    test('manual: request after client.disconnect() rejects with reason "manual"', async () => {
        const client = new ApiClient(wsUrl(), { reconnect: false });
        await client.connect();
        client.disconnect();

        await expect(client.request('PublicHandlers.GetUser', ['x']))
            .rejects
            .toMatchObject({
                name: 'ConnectionError',
                reason: 'manual',
            });
    });

    test('manual: in-flight request rejects with ConnectionError when caller calls disconnect()', async () => {
        const client = new ApiClient(wsUrl(), { reconnect: false });
        await client.connect();

        // Fire a request, then immediately disconnect. The server has no
        // chance to reply before the close lands, so the in-flight call
        // surfaces handleClose's classified error.
        const promise = client.request('PublicHandlers.GetUser', ['x']);
        client.disconnect();

        const err: unknown = await promise.catch((e: unknown) => e);
        expect(err).toBeInstanceOf(ConnectionError);
        expect((err as ConnectionError).reason).toBe('manual');
    });

    test('onConnectionError fires with classified error on close', async () => {
        const client = new ApiClient(wsUrl(), { reconnect: false });
        await client.connect();

        const captured: ConnectionError[] = [];
        const off = client.onConnectionError((err) => captured.push(err));

        // Force a close via transport.disconnect() so manualDisconnect stays
        // false and we exercise the post-upgrade clean-close path. The
        // server-side ws close lands as either code 1000-1015 ('server-closed')
        // or code 1006 ('network-error') depending on the exact close
        // sequence the runtime produces — both are valid, non-manual reasons.
        const transport = (client as unknown as {
            transport: { disconnect: () => void };
        }).transport;
        transport.disconnect();

        // Wait for the close event to propagate.
        await new Promise((r) => setTimeout(r, 200));

        expect(captured.length).toBeGreaterThanOrEqual(1);
        const err = captured[captured.length - 1];
        expect(err).toBeInstanceOf(ConnectionError);
        expect(err.reason).not.toBe('manual');

        off();
        client.disconnect();
    });

    test('offline: navigator.onLine === false drives the offline classification', async () => {
        // Stub navigator.onLine for this test. The Node test environment
        // includes a polyfilled navigator via undici; if it isn't writable
        // we skip the assertion that depends on the stub rather than fail
        // spuriously.
        const originalDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'navigator');
        const writable = (() => {
            try {
                Object.defineProperty(globalThis, 'navigator', {
                    value: { onLine: false },
                    configurable: true,
                });
                return true;
            } catch {
                return false;
            }
        })();

        try {
            const client = new ApiClient(wsUrl(), { reconnect: false });
            // Don't even try to connect — request() consults navigator.onLine
            // when building the immediate-reject error.
            const err: unknown = await client.request('PublicHandlers.GetUser', ['x']).catch((e: unknown) => e);
            expect(err).toBeInstanceOf(ConnectionError);
            if (writable) {
                const ce = err as ConnectionError;
                expect(ce.reason).toBe('offline');
                expect(ce.isOffline()).toBe(true);
            }
            client.disconnect();
        } finally {
            // Restore navigator to whatever the runtime had originally.
            if (originalDescriptor) {
                Object.defineProperty(globalThis, 'navigator', originalDescriptor);
            } else {
                delete (globalThis as unknown as { navigator?: unknown }).navigator;
            }
        }
    });
});
