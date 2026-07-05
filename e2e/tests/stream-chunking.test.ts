import { describe, test, expect, beforeEach, afterEach } from 'vitest';
import { WebSocket } from 'ws';
import { wsUrl } from './helpers';
import { ApiClient } from '../api/client';
import { numbers } from '../api/streaming-handlers';

// The e2e server enables ServerOptions.StreamChunking with MaxItems=3, so
// streamed items cross the wire batched in stream_chunk frames (#239). The
// generated-client transparency is covered by stream.test.ts staying green;
// this file asserts the wire format itself over a raw WebSocket, and that the
// AsyncIterable still yields every item in order.

interface RawFrame {
    type: string;
    id?: string;
    items?: { index: number; label: string }[];
}

describe('Stream chunking (#239)', () => {
    test('items arrive batched in stream_chunk frames, never stream_item', async () => {
        const ws = new WebSocket(wsUrl());
        await new Promise<void>((resolve, reject) => {
            ws.on('open', () => resolve());
            ws.on('error', reject);
        });

        const frames: RawFrame[] = [];
        const done = new Promise<void>((resolve, reject) => {
            const timer = setTimeout(() => reject(new Error('timed out waiting for stream_end')), 5000);
            ws.on('message', (data) => {
                const msg = JSON.parse(data.toString()) as RawFrame;
                if (msg.id !== 'chunk-test') return;
                frames.push(msg);
                if (msg.type === 'stream_end') {
                    clearTimeout(timer);
                    resolve();
                }
            });
            ws.on('error', reject);
        });

        ws.send(
            JSON.stringify({
                type: 'request',
                id: 'chunk-test',
                method: 'StreamingHandlers.Numbers',
                params: [10, 0],
            }),
        );
        await done;
        ws.close();

        expect(frames.filter((f) => f.type === 'stream_item')).toHaveLength(0);

        const chunks = frames.filter((f) => f.type === 'stream_chunk');
        // MaxItems=3 → at most 3 items per frame, and 10 items must arrive in
        // strictly fewer frames than items (the point of chunking). The exact
        // split can vary if the 20ms MaxDelay timer fires mid-stream, so
        // assert bounds rather than an exact [3,3,3,1] layout.
        expect(chunks.length).toBeGreaterThanOrEqual(2);
        expect(chunks.length).toBeLessThan(10);
        for (const c of chunks) {
            expect(c.items!.length).toBeGreaterThanOrEqual(1);
            expect(c.items!.length).toBeLessThanOrEqual(3);
        }

        const received = chunks.flatMap((c) => c.items!);
        expect(received.map((it) => it.index)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);
        expect(received[0].label).toBe('item-1');

        expect(frames[frames.length - 1].type).toBe('stream_end');
    });
});

describe('Stream chunking via generated client', () => {
    let client: ApiClient;

    beforeEach(async () => {
        client = new ApiClient(wsUrl(), { reconnect: false });
        await client.connect();
    });

    afterEach(() => {
        client.disconnect();
    });

    test('a slow producer still delivers items promptly (MaxDelay flush)', async () => {
        // 3 items at 100ms apart with MaxItems=3: without the MaxDelay timer
        // the first item would only ship when the third fills the chunk
        // (~200ms later). The 20ms MaxDelay flush must deliver it long before
        // the producer finishes.
        const firstItemAt: number[] = [];
        const start = Date.now();
        const items: number[] = [];
        for await (const item of numbers(client, 3, 100)) {
            firstItemAt.push(Date.now() - start);
            items.push(item.index);
        }
        expect(items).toEqual([1, 2, 3]);
        // Total stream time is ~200ms+; the first item must arrive well
        // before the end. Generous bound to avoid CI flakiness.
        expect(firstItemAt[0]).toBeLessThan(150);
    });
});
