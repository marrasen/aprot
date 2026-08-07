import { describe, test, expect } from 'vitest';
import { wsTokenUrl, wsUrl } from './helpers';
import { ApiClient } from '../api/client';
import { getConnURL } from '../api/conn-handlers';

// getConnectParams resolves per connection attempt and merges into the URL
// query, so a short-lived credential stays fresh across reconnects while the
// base URL remains static (#283).
describe('getConnectParams (WebSocket)', () => {
    test('merges into an existing query string, overriding same-named params', async () => {
        const client = new ApiClient(`${wsUrl()}?x=1&keep=1`, {
            reconnect: false,
            getConnectParams: () => ({ x: '9', token: 'a b&c' }),
        });
        await client.connect();

        const { url } = await getConnURL(client);
        const query = new URLSearchParams(url.slice(url.indexOf('?') + 1));

        expect(query.get('x')).toBe('9');       // overridden
        expect(query.get('keep')).toBe('1');    // preserved
        expect(query.get('token')).toBe('a b&c'); // encoded and decoded intact

        client.disconnect();
    });

    test('composes with a URL function, and both are re-resolved per attempt', async () => {
        let urlCalls = 0;
        let paramCalls = 0;

        const client = new ApiClient(
            () => {
                urlCalls++;
                return `${wsUrl()}?from=fn`;
            },
            {
                reconnect: true,
                reconnectInterval: 50,
                getConnectParams: () => {
                    paramCalls++;
                    return { token: 'fresh' };
                },
            },
        );
        await client.connect();

        const first = await getConnURL(client);
        const firstQuery = new URLSearchParams(first.url.slice(first.url.indexOf('?') + 1));
        expect(firstQuery.get('from')).toBe('fn');
        expect(firstQuery.get('token')).toBe('fresh');
        expect(urlCalls).toBe(1);
        expect(paramCalls).toBe(1);

        // Force a drop; the reconnect must re-run both callbacks.
        const reconnected = new Promise<void>((resolve) => {
            const off = client.onStateChange((state) => {
                if (state === 'connected') {
                    off();
                    resolve();
                }
            });
        });
        (client as unknown as { transport: { disconnect(): void } }).transport.disconnect();
        await reconnected;
        expect(client.getState()).toBe('connected');

        expect(urlCalls).toBeGreaterThan(1);
        expect(paramCalls).toBeGreaterThan(1);
        expect(paramCalls).toBe(urlCalls);

        client.disconnect();
    });

    test('a throwing getConnectParams fails the attempt like a transport error', async () => {
        const errors: string[] = [];
        let rejections = 0;
        let attempt = 0;

        const client = new ApiClient(wsTokenUrl(), {
            reconnect: true,
            reconnectInterval: 50,
            getConnectParams: () => {
                if (attempt++ === 0) throw new Error('token mint failed');
                return { token: 'good' };
            },
            onConnectionRejected: () => {
                rejections++;
            },
        });
        client.onConnectionError((err) => errors.push(err.reason));

        await client.connect();
        await new Promise((resolve) => setTimeout(resolve, 800));

        // The failure is a local one, not a server rejection.
        expect(errors[0]).toBe('network-error');
        expect(rejections).toBe(0);
        // And normal reconnect recovers once the callback succeeds.
        expect(client.getState()).toBe('connected');

        client.disconnect();
    });
});
