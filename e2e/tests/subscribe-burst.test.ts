import { describe, test, expect, afterEach } from 'vitest';
import { wsUrl } from './helpers';
import { ApiClient } from '../api/client';
import { subscribeGet, takePeak, dropMe } from '../api/burst-handlers';

// The server runs each subscription's first query in one of the
// connection's request slots and refuses frames beyond MaxConcurrentRequests
// (256). Get takes 20ms, so 300 subscriptions sent at once overlap. The
// client keeps at most 64 waiting for their first answer, so none is refused,
// neither on the first burst nor on the resubscribe burst after a reconnect.
describe('Subscribe burst', () => {
    const N = 300;
    let client: ApiClient;
    const unsubs: Array<() => void> = [];

    afterEach(() => {
        for (const u of unsubs.splice(0)) u();
        client.disconnect();
    });

    test('300 subscriptions all succeed, before and after a reconnect', async () => {
        client = new ApiClient(wsUrl(), { reconnect: true });
        await client.connect();
        await takePeak(client); // reset

        const errors: Error[] = [];
        const counts: number[] = Array.from({ length: N }, () => 0);
        let rounds: { target: number; resolve: () => void } | null = null;
        const check = () => {
            if (rounds && counts.every((c) => c >= rounds!.target)) rounds.resolve();
        };
        const waitRound = (target: number) => new Promise<void>((resolve) => {
            rounds = { target, resolve };
            check();
        });

        for (let i = 0; i < N; i++) {
            unsubs.push(subscribeGet(client, i, (v) => {
                expect(v).toBe(i);
                counts[i]++;
                check();
            }, (err) => { errors.push(err); }));
        }
        await waitRound(1);
        expect(errors).toEqual([]);
        expect(await takePeak(client)).toBeLessThanOrEqual(64);

        await dropMe(client);
        await waitRound(2);
        expect(errors).toEqual([]);
        expect(await takePeak(client)).toBeLessThanOrEqual(64);
    }, 20000);
});
