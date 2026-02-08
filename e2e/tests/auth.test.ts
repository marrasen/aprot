import { describe, test, expect, beforeEach, afterEach } from 'vitest';
import { wsUrl } from './helpers';
import { ApiClient, ApiError } from '../api/client';
import '../api/public-handlers';
import '../api/protected-handlers';

describe('Auth Flow (WebSocket)', () => {
    let client: ApiClient;

    beforeEach(async () => {
        client = new ApiClient(wsUrl(), { reconnect: false, heartbeatInterval: 0 });
        await client.connect();
    });

    afterEach(() => {
        client.disconnect();
    });

    test('login returns token and username', async () => {
        const res = await client.login({ username: 'testuser', password: 'pass' });
        expect(res.token).toBeDefined();
        expect(res.token.length).toBeGreaterThan(0);
        expect(res.username).toBe('testuser');
        expect(res.user_id).toBeDefined();
    });

    test('getProfile after login returns profile', async () => {
        const loginRes = await client.login({ username: 'profileuser', password: 'pass' });

        // After login, the connection is authenticated — no token needed in params
        const profile = await client.getProfile();
        expect(profile.username).toBe('profileuser');
        expect(profile.user_id).toBe(loginRes.user_id);
    });

    test('getProfile without login throws Unauthorized', async () => {
        try {
            await client.getProfile();
            expect.fail('Should have thrown');
        } catch (err) {
            expect(err).toBeInstanceOf(ApiError);
            expect((err as ApiError).isUnauthorized()).toBe(true);
        }
    });

    test('sendMessage delivers DirectMessage push to recipient', async () => {
        // Client 1: sender
        const senderLogin = await client.login({ username: 'sender', password: 'pass' });

        // Client 2: recipient
        const client2 = new ApiClient(wsUrl(), { reconnect: false, heartbeatInterval: 0 });
        await client2.connect();

        try {
            const recipientLogin = await client2.login({ username: 'recipient', password: 'pass' });

            const received = new Promise<{ from_user_id: string; from_user: string; message: string }>((resolve) => {
                client2.onDirectMessageEvent((data) => {
                    resolve(data);
                });
            });

            // After login, connection is authenticated — send message directly
            await client.sendMessage({
                to_user_id: recipientLogin.user_id,
                message: 'Hello from sender',
            });

            const event = await received;
            expect(event.from_user).toBe('sender');
            expect(event.message).toBe('Hello from sender');
            expect(event.from_user_id).toBe(senderLogin.user_id);
        } finally {
            client2.disconnect();
        }
    });
});
