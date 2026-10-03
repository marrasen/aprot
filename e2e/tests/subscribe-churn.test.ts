import { describe, test, expect, afterEach } from 'vitest';
import { wsUrl } from './helpers';
import { ApiClient } from '../api/client';
import { subscribeGet, takePeak } from '../api/burst-handlers';

// A page swaps one large set of subscriptions for another, as on a route
// change: 300 rows unmount and 300 new rows mount in the same tick.
//
// Two things must hold. First, unsubscribing a waiting subscription must not
// send queued subscriptions that the same loop is about to unsubscribe. Second,
// the server keeps a slot for each unsubscribed subscription until its handler
// returns (BurstHandlers.Get sleeps 20ms and ignores the cancel), so repeated
// swaps must not pile those up past the server's limit of 256 and get new
// subscriptions refused. The client allows 64 live subscribe frames waiting for
// an answer, and 128 counting unsubscribed ones the server has not answered.
describe('Subscribe churn', () => {
    const N = 300;
    let client: ApiClient;
    afterEach(() => client.disconnect());

    // recordFrames records every frame the client sends from now on.
    function recordFrames(c: ApiClient): Array<Record<string, unknown>> {
        const sent: Array<Record<string, unknown>> = [];
        const t = (c as unknown as { transport: { send: (m: object) => void } }).transport;
        const orig = t.send.bind(t);
        t.send = (m: object) => { sent.push(m as Record<string, unknown>); orig(m); };
        return sent;
    }

    const subscribeIds = (frames: Array<Record<string, unknown>>) =>
        frames.filter((m) => m.type === 'subscribe').map((m) => Number(m.id));

    // subscribeAll subscribes N queries and resolves once each has its first
    // result or an error.
    function subscribeAll(errors: Error[]): { unsubs: Array<() => void>; done: Promise<void> } {
        const unsubs: Array<() => void> = [];
        let settled = 0;
        const done = new Promise<void>((resolve) => {
            for (let i = 0; i < N; i++) {
                let first = true;
                const settle = () => {
                    if (!first) return;
                    first = false;
                    if (++settled === N) resolve();
                };
                unsubs.push(subscribeGet(client, i, (v) => {
                    expect(v).toBe(i);
                    settle();
                }, (e) => { errors.push(e); settle(); }));
            }
        });
        return { unsubs, done };
    }

    test('swapping 300 subscriptions for 300 others sends nothing for the removed ones', async () => {
        client = new ApiClient(wsUrl(), { reconnect: false });
        await client.connect();
        await takePeak(client);
        const sent = recordFrames(client);
        const errors: Error[] = [];

        const old = subscribeAll(errors);
        const oldIds = subscribeIds(sent);
        expect(oldIds.length).toBe(64);
        const lastOldId = oldIds[0] + N - 1;

        // Route change, in one tick.
        const swapAt = sent.length;
        for (const u of old.unsubs) u();
        const next = subscribeAll(errors);
        await next.done;

        // Only the 64 first frames ever went out for the old set.
        const afterSwap = subscribeIds(sent.slice(swapAt));
        expect(afterSwap.filter((id) => id <= lastOldId)).toEqual([]);
        expect(afterSwap.length).toBe(N);
        expect(errors).toEqual([]);
        // 64 old first runs still finishing plus 64 new ones.
        expect(await takePeak(client)).toBeLessThanOrEqual(128);
        for (const u of next.unsubs) u();
    }, 20000);

    test('rapid repeated swaps stay under the server limit and none is refused', async () => {
        client = new ApiClient(wsUrl(), { reconnect: false });
        await client.connect();
        await takePeak(client);
        const errors: Error[] = [];

        // Five swaps, each in its own tick but well inside one 20ms handler
        // run, so the server is still running the first runs of every set.
        let set = subscribeAll(errors);
        for (let swap = 0; swap < 5; swap++) {
            await new Promise((r) => setTimeout(r, 0));
            for (const u of set.unsubs) u();
            set = subscribeAll(errors);
        }
        await set.done;

        expect(errors).toEqual([]);
        expect(await takePeak(client)).toBeLessThanOrEqual(128);
        for (const u of set.unsubs) u();
    }, 20000);
});
