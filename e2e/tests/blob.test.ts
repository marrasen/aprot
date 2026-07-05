import { describe, test, expect, beforeEach, afterEach } from 'vitest';
import { wsUrl, sseUrl } from './helpers';
import { ApiClient } from '../api/client';
import { getBlob, setBlob, subscribeGetBlob } from '../api/blob-handlers';

// The server seeds the payload with non-UTF-8 bytes (see e2eapi.NewBlobHandlers)
// so both the binary frame and the base64 fallback paths must round-trip them.
const SEED = new Uint8Array([0x00, 0x01, 0xfe, 0xff, 0x76, 0x31]); // ...'v1'

async function bytesOf(blob: Blob): Promise<Uint8Array> {
    return new Uint8Array(await blob.arrayBuffer());
}

describe('Blob responses over WebSocket (binary frames)', () => {
    let client: ApiClient;

    beforeEach(async () => {
        client = new ApiClient(wsUrl(), { reconnect: false });
        await client.connect();
    });

    afterEach(() => {
        client.disconnect();
    });

    test('getBlob resolves a DOM Blob with contentType and exact bytes', async () => {
        const blob = await getBlob(client);
        expect(blob).toBeInstanceOf(Blob);
        expect(blob.type).toBe('application/x-e2e');
        expect(await bytesOf(blob)).toEqual(SEED);
    });

    test('subscription refresh delivers a Blob to the callback', async () => {
        // Initial subscription result must be a Blob.
        const initial = await new Promise<Blob>((resolve) => {
            subscribeGetBlob(client, (data) => resolve(data));
        });
        expect(initial).toBeInstanceOf(Blob);

        // Mutate — the server fires TriggerRefresh("blob") and the refreshed
        // payload must arrive through the same subscription, still as a Blob.
        const marker = `ws-refresh-${Date.now()}`;
        const refreshed = await new Promise<Blob>((resolve) => {
            subscribeGetBlob(client, (data) => {
                void (async () => {
                    const text = new TextDecoder().decode(await bytesOf(data));
                    if (text === marker) resolve(data);
                })();
            });
            void setBlob(client, marker);
        });

        expect(refreshed).toBeInstanceOf(Blob);
        expect(refreshed.type).toBe('application/x-e2e');
    });
});

describe('Blob responses over SSE ($blob JSON fallback)', () => {
    let client: ApiClient;

    beforeEach(async () => {
        client = new ApiClient(sseUrl(), { transport: 'sse', reconnect: false });
        await client.connect();
    });

    afterEach(() => {
        client.disconnect();
    });

    test('getBlob resolves a DOM Blob with contentType and exact bytes', async () => {
        const marker = `sse-bytes-${Date.now()}`;
        await setBlob(client, marker);
        const blob = await getBlob(client);
        expect(blob).toBeInstanceOf(Blob);
        expect(blob.type).toBe('application/x-e2e');
        expect(new TextDecoder().decode(await bytesOf(blob))).toBe(marker);
    });

    test('subscription refresh delivers a Blob to the callback', async () => {
        const initial = await new Promise<Blob>((resolve) => {
            subscribeGetBlob(client, (data) => resolve(data));
        });
        expect(initial).toBeInstanceOf(Blob);

        const marker = `sse-refresh-${Date.now()}`;
        const refreshed = await new Promise<Blob>((resolve) => {
            subscribeGetBlob(client, (data) => {
                void (async () => {
                    const text = new TextDecoder().decode(await bytesOf(data));
                    if (text === marker) resolve(data);
                })();
            });
            void setBlob(client, marker);
        });

        expect(refreshed).toBeInstanceOf(Blob);
        expect(refreshed.type).toBe('application/x-e2e');
    });
});
