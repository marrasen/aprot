import { describe, test, expect, beforeEach, afterEach } from 'vitest';
import { wsUrl, wsTextOnlyUrl } from './helpers';
import { ApiClient } from '../api/client';
import { emitPreview, emitTick, onPreviewFrame, onTickEvent } from '../api/push-handlers';

async function bytesOf(blob: Blob): Promise<Uint8Array> {
    return new Uint8Array(await blob.arrayBuffer());
}

// Waits for the next push of one event, so each test asserts on a frame it
// caused rather than on whatever happened to be in flight.
function nextPush<T>(register: (handler: (data: T) => void) => () => void): Promise<T> {
    return new Promise<T>((resolve) => {
        const off = register((data) => {
            off();
            resolve(data);
        });
    });
}

// A binary push (event data typed as a type defined from aprot.Blob) must
// resolve a DOM Blob with the exact bytes, on both encodings (#387).
describe('Binary push events', () => {
    let client: ApiClient;

    beforeEach(async () => {
        client = new ApiClient(wsUrl(), { reconnect: false });
        await client.connect();
    });

    afterEach(() => {
        client.disconnect();
    });

    test('a blob push arrives as a DOM Blob with contentType and exact bytes', async () => {
        const marker = `preview-${Date.now()}`;
        const received = nextPush<Blob>((h) => onPreviewFrame(client, h));
        await emitPreview(client, marker);

        const blob = await received;
        expect(blob).toBeInstanceOf(Blob);
        expect(blob.type).toBe('application/x-e2e');
        expect(new TextDecoder().decode(await bytesOf(blob))).toBe(marker);
    });

    test('a droppable JSON push still arrives when the client keeps up', async () => {
        const received = nextPush<{ seq: number }>((h) => onTickEvent(client, h));
        await emitTick(client, 7);

        expect((await received).seq).toBe(7);
    });
});

// The same blob push over a connection that declined binary frames takes the
// $blob JSON envelope, and the client must rebuild the identical Blob — the
// client-visible type never depends on what was negotiated.
describe('Binary push events with binary frames declined', () => {
    let client: ApiClient;

    beforeEach(async () => {
        client = new ApiClient(wsTextOnlyUrl(), { reconnect: false });
        await client.connect();
    });

    afterEach(() => {
        client.disconnect();
    });

    test('a blob push falls back to $blob and still resolves a DOM Blob', async () => {
        const marker = `preview-text-only-${Date.now()}`;
        const received = nextPush<Blob>((h) => onPreviewFrame(client, h));
        await emitPreview(client, marker);

        const blob = await received;
        expect(blob).toBeInstanceOf(Blob);
        expect(blob.type).toBe('application/x-e2e');
        expect(new TextDecoder().decode(await bytesOf(blob))).toBe(marker);
    });
});
