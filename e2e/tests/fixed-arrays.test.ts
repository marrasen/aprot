import { describe, test, expect, beforeEach, afterEach } from 'vitest';
import { wsUrl } from './helpers';
import { ApiClient } from '../api/client';
import { echoArrays } from '../api/fixed-array-handlers';

// Fixed-size array codegen round-trip (#240). [N]T fields are typed as TS
// tuples (plain arrays above the tuple cap) and cross the wire as JSON
// arrays; [N]byte is base64-encoded as a string by go-json-experiment/json.
// The tuple parameter types below are themselves part of the test: if
// codegen regressed to `any`, the literal payloads would still compile, but
// the server-side [N]T unmarshal would reject wrong-length arrays — so the
// round-trip asserts both shape and values.

describe('Fixed-size array round-trip (WebSocket)', () => {
    let client: ApiClient;

    beforeEach(async () => {
        client = new ApiClient(wsUrl(), { reconnect: false });
        await client.connect();
    });

    afterEach(() => {
        client.disconnect();
    });

    test('tuples, above-cap arrays, and base64 byte arrays round-trip', async () => {
        const big = Array.from({ length: 20 }, (_, i) => i);
        const res = await echoArrays(client, {
            wbMul: [1.5, 1, 1, 2.25],
            grid: [
                [1, 2],
                [3, 4],
            ],
            big,
            // [8]byte on the server — base64 of bytes 1..8 on the wire.
            hash: 'AQIDBAUGBwg=',
        });

        expect(res.wbMul).toEqual([1.5, 1, 1, 2.25]);
        expect(res.grid).toEqual([
            [1, 2],
            [3, 4],
        ]);
        expect(res.big).toEqual(big);
        expect(res.hash).toBe('AQIDBAUGBwg=');
    });
});
