import { describe, test, expect } from 'vitest';
import { wsRejectUrl } from './helpers';
import { ApiClient, ApiError, ErrorCode } from '../api/client';

describe('Connection Rejected (WebSocket)', () => {
    test('onConnectionRejected fires and client stops reconnecting', async () => {
        let rejectedError: ApiError | null = null;

        const client = new ApiClient(wsRejectUrl(), {
            reconnect: true,
            reconnectInterval: 100,
            heartbeatInterval: 0,
            onConnectionRejected: (error) => {
                rejectedError = error;
            },
        });

        await client.connect();

        // Wait for the rejection to be processed.
        await new Promise((resolve) => setTimeout(resolve, 500));

        expect(rejectedError).toBeInstanceOf(ApiError);
        expect(rejectedError!.code).toBe(ErrorCode.ConnectionRejected);
        expect(rejectedError!.isConnectionRejected()).toBe(true);
        expect(rejectedError!.message).toBe('invalid session');
        expect(client.getState()).toBe('disconnected');

        client.disconnect();
    });

    test('client does not reconnect after rejection', async () => {
        let rejectionCount = 0;

        const client = new ApiClient(wsRejectUrl(), {
            reconnect: true,
            reconnectInterval: 100,
            heartbeatInterval: 0,
            onConnectionRejected: () => {
                rejectionCount++;
            },
        });

        await client.connect();

        // Wait long enough that reconnection would have happened if not blocked.
        await new Promise((resolve) => setTimeout(resolve, 1000));

        expect(rejectionCount).toBe(1);
        expect(client.getState()).toBe('disconnected');

        client.disconnect();
    });
});
