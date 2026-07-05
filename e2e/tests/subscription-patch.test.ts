import { describe, test, expect, beforeEach, afterEach } from 'vitest';
import { wsUrl } from './helpers';
import { ApiClient, mergeByKey } from '../api/client';
import { subscribeListPatchItems, setRating, getListExecutions } from '../api/patch-handlers';
import type { PatchItemList, PatchItem } from '../api/patch-handlers';

// Subscription patches (#237): a mutation pushes O(patch) to subscribers that
// declared patch support, instead of re-running the query and re-sending the
// full result. The server-side execution counter proves which path ran.

describe('Subscription patches (WebSocket)', () => {
    let client: ApiClient;

    beforeEach(async () => {
        client = new ApiClient(wsUrl(), { reconnect: false });
        await client.connect();
    });

    afterEach(() => {
        client.disconnect();
    });

    test('patch reaches an opted-in subscriber without re-running the query', async () => {
        const patches: unknown[] = [];
        let initial: PatchItemList | null = null;

        const unsubscribe = subscribeListPatchItems(
            client,
            (data) => { initial ??= data; },
            undefined,
            { onPatch: (patch) => patches.push(patch) },
        );

        // Wait for the initial subscription result.
        await expect.poll(() => initial !== null).toBe(true);
        const executionsBefore = await getListExecutions(client);

        const marker = Math.floor(Math.random() * 1_000_000);
        await setRating(client, 'p1', marker);

        await expect.poll(() => patches.length).toBeGreaterThan(0);
        expect(patches[0]).toEqual({ id: 'p1', rating: marker });

        // The patch replaced the refresh: the query must not have re-executed.
        const executionsAfter = await getListExecutions(client);
        expect(executionsAfter).toBe(executionsBefore);

        unsubscribe();
    });

    test('subscriber without patch support falls back to a full refresh', async () => {
        const results: PatchItemList[] = [];
        const unsubscribe = subscribeListPatchItems(client, (data) => { results.push(data); });

        await expect.poll(() => results.length).toBeGreaterThan(0);
        const executionsBefore = await getListExecutions(client);

        const marker = Math.floor(Math.random() * 1_000_000);
        await setRating(client, 'p2', marker);

        // The full result arrives through the normal refresh path.
        await expect.poll(() =>
            results.some((r) => r.items.some((i) => i.id === 'p2' && i.rating === marker)),
        ).toBe(true);

        // ...which means the query re-executed exactly once for this subscriber.
        const executionsAfter = await getListExecutions(client);
        expect(executionsAfter).toBe(executionsBefore + 1);

        unsubscribe();
    });

    test('mergeByKey folds patches into a keyed array', () => {
        const merge = mergeByKey<PatchItem>('id');
        const data: PatchItem[] = [
            { id: 'p1', rating: 0 },
            { id: 'p2', rating: 1 },
        ];

        const merged = merge(data, { id: 'p2', rating: 9 });
        expect(merged).toEqual([
            { id: 'p1', rating: 0 },
            { id: 'p2', rating: 9 },
        ]);
        // Pure: the input array is untouched.
        expect(data[1].rating).toBe(1);

        // Arrays of patches and unknown keys are handled.
        const merged2 = merge(data, [{ id: 'p1', rating: 7 }, { id: 'missing', rating: 3 }]);
        expect(merged2).toEqual([
            { id: 'p1', rating: 7 },
            { id: 'p2', rating: 1 },
        ]);
    });
});
