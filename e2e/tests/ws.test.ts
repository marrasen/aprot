import { describe, test, expect, beforeEach, afterEach } from 'vitest';
import { wsUrl } from './helpers';
import { ApiClient, ApiError } from '../api/client';
import { TaskStatus } from '../api/public-handlers';
import '../api/protected-handlers';

describe('WebSocket Transport', () => {
    let client: ApiClient;

    beforeEach(async () => {
        client = new ApiClient(wsUrl(), { reconnect: false, heartbeatInterval: 0 });
        await client.connect();
    });

    afterEach(() => {
        client.disconnect();
    });

    test('createUser returns id, name, email', async () => {
        const res = await client.createUser({ name: 'Alice', email: 'alice@test.com' });
        expect(res.id).toBeDefined();
        expect(res.name).toBe('Alice');
        expect(res.email).toBe('alice@test.com');
    });

    test('getUser returns created user', async () => {
        const created = await client.createUser({ name: 'Bob', email: 'bob@test.com' });
        const user = await client.getUser({ id: created.id });
        expect(user.id).toBe(created.id);
        expect(user.name).toBe('Bob');
        expect(user.email).toBe('bob@test.com');
    });

    test('listUsers returns array', async () => {
        await client.createUser({ name: 'Carol', email: 'carol@test.com' });
        const res = await client.listUsers();
        expect(Array.isArray(res.users)).toBe(true);
        expect(res.users.length).toBeGreaterThanOrEqual(1);
    });

    test('getTask returns enum status field', async () => {
        const task = await client.getTask({ id: 'task-1' });
        expect(task.id).toBe('task-1');
        expect(task.name).toBe('Example Task');
        expect(task.status).toBe(TaskStatus.Running);
    });

    test('processBatch reports progress callbacks', async () => {
        const progressUpdates: { current: number; total: number; message: string }[] = [];
        const res = await client.processBatch(
            { items: ['a', 'b', 'c'], delay: 50 },
            {
                onProgress: (current, total, message) => {
                    progressUpdates.push({ current, total, message });
                },
            },
        );

        expect(res.processed).toBe(3);
        expect(res.results).toEqual(['processed_a', 'processed_b', 'processed_c']);
        expect(progressUpdates.length).toBe(3);
        expect(progressUpdates[0]).toEqual({ current: 1, total: 3, message: 'Processing: a' });
    });

    test('processBatch abort cancels request', async () => {
        const controller = new AbortController();
        const promise = client.processBatch(
            { items: ['a', 'b', 'c', 'd', 'e'], delay: 200 },
            { signal: controller.signal },
        );

        setTimeout(() => controller.abort(), 100);

        await expect(promise).rejects.toThrow();
    });

    test('sendNotification triggers push event to same client', async () => {
        const received = new Promise<{ message: string; level: string }>((resolve) => {
            client.onSystemNotificationEvent((data) => {
                resolve(data);
            });
        });

        await client.sendNotification({ message: 'hello', level: 'info' });

        const event = await received;
        expect(event.message).toBe('hello');
        expect(event.level).toBe('info');
    });

    test('createUser broadcasts UserCreated to second client', async () => {
        const client2 = new ApiClient(wsUrl(), { reconnect: false, heartbeatInterval: 0 });
        await client2.connect();

        try {
            const received = new Promise<{ id: string; name: string; email: string }>((resolve) => {
                client2.onUserCreatedEvent((data) => {
                    resolve(data);
                });
            });

            const created = await client.createUser({ name: 'Dave', email: 'dave@test.com' });

            const event = await received;
            expect(event.id).toBe(created.id);
            expect(event.name).toBe('Dave');
            expect(event.email).toBe('dave@test.com');
        } finally {
            client2.disconnect();
        }
    });

    test('createUser with empty name throws ApiError with isInvalidParams', async () => {
        try {
            await client.createUser({ name: '', email: 'bad@test.com' });
            expect.fail('Should have thrown');
        } catch (err) {
            expect(err).toBeInstanceOf(ApiError);
            expect((err as ApiError).isInvalidParams()).toBe(true);
        }
    });

    test('unknown method throws ApiError with isNotFound', async () => {
        try {
            await client.request('NonExistent', {});
            expect.fail('Should have thrown');
        } catch (err) {
            expect(err).toBeInstanceOf(ApiError);
            expect((err as ApiError).isNotFound()).toBe(true);
        }
    });
});
