import { describe, test, expect, beforeEach, afterEach } from 'vitest';
import { wsUrl } from './helpers';
import { ApiClient, ApiError } from '../api/client';
import {
    streamNumbers,
    streamFailing,
    streamPairs,
    streamPanics,
} from '../api/streaming-handlers';
import type { StreamNumberItem } from '../api/streaming-handlers';

describe('Streaming handlers (iter.Seq)', () => {
    let client: ApiClient;

    beforeEach(async () => {
        client = new ApiClient(wsUrl(), { reconnect: false });
        await client.connect();
    });

    afterEach(() => {
        client.disconnect();
    });

    test('iter.Seq handler yields items in order via for-await', async () => {
        const items: StreamNumberItem[] = [];
        for await (const item of streamNumbers(client, 5)) {
            items.push(item);
        }
        expect(items).toHaveLength(5);
        expect(items.map((i) => i.index)).toEqual([1, 2, 3, 4, 5]);
        expect(items.map((i) => i.label)).toEqual([
            'item-1',
            'item-2',
            'item-3',
            'item-4',
            'item-5',
        ]);
    });

    test('breaking out of for-await cancels the server-side stream', async () => {
        const items: StreamNumberItem[] = [];
        for await (const item of streamNumbers(client, 1000)) {
            items.push(item);
            if (items.length === 3) break;
        }
        expect(items).toHaveLength(3);
        // The server stops yielding when it observes context cancellation.
        // We don't have a direct way to observe the cancelation count from
        // the client side, but reaching here without hanging confirms the
        // stream was properly torn down.
    });

    test('preflight error is thrown on first iteration', async () => {
        await expect(async () => {
            // eslint-disable-next-line @typescript-eslint/no-unused-vars
            for await (const _ of streamFailing(client)) {
                // should throw before the first item
            }
        }).rejects.toBeInstanceOf(ApiError);
    });

    test('mid-stream panic surfaces as ApiError after partial items', async () => {
        const items: number[] = [];
        let caught: unknown = null;
        try {
            for await (const n of streamPanics(client)) {
                items.push(n);
            }
        } catch (err) {
            caught = err;
        }
        expect(items).toEqual([1, 2]);
        expect(caught).toBeInstanceOf(ApiError);
    });

    test('iter.Seq2 handler yields [key, value] tuples', async () => {
        const pairs: [string, number][] = [];
        for await (const pair of streamPairs(client)) {
            pairs.push(pair);
        }
        expect(pairs).toEqual([
            ['alpha', 1],
            ['beta', 2],
            ['gamma', 3],
        ]);
    });

    test('AbortSignal aborts an in-flight stream', async () => {
        const controller = new AbortController();
        const items: StreamNumberItem[] = [];
        const iter = streamNumbers(client, 1000, { signal: controller.signal });
        setTimeout(() => controller.abort(), 20);
        try {
            for await (const item of iter) {
                items.push(item);
                if (items.length > 500) break; // safety
            }
        } catch (err) {
            // Aborted iteration throws; the server-side handler should have
            // stopped yielding once it observed cancellation.
            expect((err as Error).message).toMatch(/abort/i);
        }
        expect(items.length).toBeLessThan(200);
    });

    test('empty count yields zero items and terminates cleanly', async () => {
        const items: StreamNumberItem[] = [];
        for await (const item of streamNumbers(client, 0)) {
            items.push(item);
        }
        expect(items).toHaveLength(0);
    });

    test('multiple iterations of the same iterable start independent streams', async () => {
        const iterable = streamNumbers(client, 3);
        const first: number[] = [];
        const second: number[] = [];
        for await (const item of iterable) first.push(item.index);
        for await (const item of iterable) second.push(item.index);
        expect(first).toEqual([1, 2, 3]);
        expect(second).toEqual([1, 2, 3]);
    });
});
