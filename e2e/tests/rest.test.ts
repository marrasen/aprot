import { describe, test, expect } from 'vitest';
import { getServerAddr } from './helpers';

// REST adapter end-to-end coverage. The REST surface previously had zero e2e
// tests, which is how the typed-path-param bug (#207 P1) — int/bool path params
// returning 400 — survived. These tests hit the adapter over plain HTTP.

function apiUrl(path: string): string {
    return `http://${getServerAddr()}/api${path}`;
}

describe('REST adapter', () => {
    test('typed int and bool path params decode (regression #207 P1)', async () => {
        const res = await fetch(apiUrl('/echo-handlers/get-echo/42/true/bob'));
        expect(res.status).toBe(200);

        const body = await res.json();
        // count/flag arrive as a real number/boolean, not the raw path strings.
        expect(body.count).toBe(42);
        expect(body.flag).toBe(true);
        expect(body.label).toBe('bob');
    });

    test('a false bool path param decodes', async () => {
        const res = await fetch(apiUrl('/echo-handlers/get-echo/7/false/x'));
        expect(res.status).toBe(200);
        const body = await res.json();
        expect(body.count).toBe(7);
        expect(body.flag).toBe(false);
    });

    test('sql.NullString is unwrapped to a bare string over REST', async () => {
        const res = await fetch(apiUrl('/echo-handlers/get-echo/1/true/zed'));
        const body = await res.json();
        // Unwrapped string, not the {"String":"hi-zed","Valid":true} object —
        // the REST transport uses the same sql.Null-aware marshaler as WS/SSE.
        expect(body.note).toBe('hi-zed');
        expect(typeof body.note).toBe('string');
    });

    test('a non-numeric int path param returns 400', async () => {
        const res = await fetch(apiUrl('/echo-handlers/get-echo/notanint/true/x'));
        expect(res.status).toBe(400);
        const body = await res.json();
        expect(body.code).toBe(-32602); // CodeInvalidParams
    });
});
