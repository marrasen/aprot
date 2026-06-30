import { describe, test, expect, beforeEach, afterEach } from 'vitest';
import { wsUrl } from './helpers';
import { ApiClient, ApiError, getValidationErrors } from '../api/client';
import { signup } from '../api/signup-handlers';

// Validation -> getValidationErrors() round-trip. Previously the structured
// FieldError payload and the getValidationErrors() client helper were never
// exercised end-to-end against a live server.

describe('Validation round-trip (WebSocket)', () => {
    let client: ApiClient;

    beforeEach(async () => {
        client = new ApiClient(wsUrl(), { reconnect: false });
        await client.connect();
    });

    afterEach(() => {
        client.disconnect();
    });

    test('valid input passes validation', async () => {
        const res = await signup(client, { name: 'Alice', email: 'alice@example.com', age: 30 });
        expect(res.ok).toBe(true);
    });

    test('invalid input throws a structured validation error', async () => {
        try {
            await signup(client, { name: 'A', email: 'not-an-email', age: 5 });
            expect.fail('Should have thrown');
        } catch (err) {
            expect(err).toBeInstanceOf(ApiError);
            expect((err as ApiError).isValidationFailed()).toBe(true);

            const fieldErrors = getValidationErrors(err);
            expect(fieldErrors).not.toBeNull();

            const byField = new Map(fieldErrors!.map((fe) => [fe.field, fe]));
            // All three fields fail their rules: name(min=2), email(email), age(gte=13).
            expect(byField.has('name')).toBe(true);
            expect(byField.has('email')).toBe(true);
            expect(byField.has('age')).toBe(true);

            // Each entry carries the failing tag and a message.
            expect(byField.get('name')!.tag).toBe('min');
            expect(byField.get('email')!.tag).toBe('email');
            expect(byField.get('age')!.tag).toBe('gte');
            for (const fe of fieldErrors!) {
                expect(typeof fe.message).toBe('string');
                expect(fe.message.length).toBeGreaterThan(0);
            }
        }
    });

    test('getValidationErrors returns null for a non-validation error', () => {
        expect(getValidationErrors(new Error('nope'))).toBeNull();
        expect(getValidationErrors(new ApiError(-32601, 'method not found'))).toBeNull();
    });
});
