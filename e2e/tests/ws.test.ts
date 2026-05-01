import { describe, test, expect, beforeEach, afterEach } from 'vitest';
import { wsUrl } from './helpers';
import { ApiClient, ApiError } from '../api/client';
import { TaskStatus, createUser, getUser, listUsers, getTask, processBatch, sendNotification, onUserCreatedEvent, onSystemNotificationEvent, subscribeListUsers } from '../api/public-handlers';
import type { ListUsersResponse } from '../api/public-handlers';

describe('WebSocket Transport', () => {
    let client: ApiClient;

    beforeEach(async () => {
        client = new ApiClient(wsUrl(), { reconnect: false });
        await client.connect();
    });

    afterEach(() => {
        client.disconnect();
    });

    test('createUser returns id, name, email', async () => {
        const res = await createUser(client, 'Alice', 'alice@test.com');
        expect(res.id).toBeDefined();
        expect(res.name).toBe('Alice');
        expect(res.email).toBe('alice@test.com');
    });

    test('getUser returns created user', async () => {
        const created = await createUser(client, 'Bob', 'bob@test.com');
        const user = await getUser(client, created.id);
        expect(user.id).toBe(created.id);
        expect(user.name).toBe('Bob');
        expect(user.email).toBe('bob@test.com');
    });

    test('listUsers returns array', async () => {
        await createUser(client, 'Carol', 'carol@test.com');
        const res = await listUsers(client);
        expect(Array.isArray(res.users)).toBe(true);
        expect(res.users.length).toBeGreaterThanOrEqual(1);
    });

    test('getTask returns enum status field', async () => {
        const task = await getTask(client, 'task-1');
        expect(task.id).toBe('task-1');
        expect(task.name).toBe('Example Task');
        expect(task.status).toBe(TaskStatus.Running);
    });

    test('processBatch reports progress callbacks', async () => {
        const progressUpdates: { current: number; total: number; message: string }[] = [];
        const res = await processBatch(
            client,
            ['a', 'b', 'c'],
            50,
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
        const promise = processBatch(
            client,
            ['a', 'b', 'c', 'd', 'e'],
            200,
            { signal: controller.signal },
        );

        setTimeout(() => controller.abort(), 100);

        await expect(promise).rejects.toThrow();
    });

    test('sendNotification triggers push event to same client', async () => {
        const received = new Promise<{ message: string; level: string }>((resolve) => {
            onSystemNotificationEvent(client, (data) => {
                resolve(data);
            });
        });

        await sendNotification(client, 'hello', 'info');

        const event = await received;
        expect(event.message).toBe('hello');
        expect(event.level).toBe('info');
    });

    test('createUser broadcasts UserCreated to second client', async () => {
        const client2 = new ApiClient(wsUrl(), { reconnect: false });
        await client2.connect();

        try {
            const received = new Promise<{ id: string; name: string; email: string }>((resolve) => {
                onUserCreatedEvent(client2, (data) => {
                    resolve(data);
                });
            });

            const created = await createUser(client, 'Dave', 'dave@test.com');

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
            await createUser(client, '', 'bad@test.com');
            expect.fail('Should have thrown');
        } catch (err) {
            expect(err).toBeInstanceOf(ApiError);
            expect((err as ApiError).isInvalidParams()).toBe(true);
        }
    });

    test('getLoadingCount excludes subscription pending entries', async () => {
        // Before subscribing, loading count should be 0
        expect(client.getLoadingCount()).toBe(0);

        // Subscribe and wait for initial data
        await new Promise<void>((resolve) => {
            subscribeListUsers(client, (_result: ListUsersResponse) => {
                resolve();
            });
        });

        // After subscription data arrives, loading count should still be 0.
        // Subscription pending entries must not count toward getLoadingCount().
        expect(client.getLoadingCount()).toBe(0);
    });

    test('onLoadingChange does not flag subscriptions as loading after reconnect', async () => {
        // Use a dedicated client with auto-reconnect enabled. The default
        // afterEach disconnects the suite-wide client; this one is cleaned
        // up locally.
        const reconnectClient = new ApiClient(wsUrl(), {
            reconnect: true,
            reconnectInterval: 50,
        });
        await reconnectClient.connect();

        try {
            // Subscribe and wait for initial data to land. The active
            // subscription must survive the reconnect so resubscribeAll()
            // re-registers it server-side.
            await new Promise<void>((resolve) => {
                subscribeListUsers(reconnectClient, (_result: ListUsersResponse) => {
                    resolve();
                });
            });
            expect(reconnectClient.getLoadingCount()).toBe(0);

            // Capture every onLoadingChange notification fired from this
            // point onward. With only an active subscription pending (no
            // user-initiated requests), no notification should report
            // count > 0 — including the one fired by resubscribeAll() after
            // the reconnect lands.
            const counts: number[] = [];
            const offLoading = reconnectClient.onLoadingChange((count) => counts.push(count));

            // Force a transport-level disconnect to simulate a network drop
            // (Spotify-foreground pause, auth-token rotation, etc.). Going
            // through transport.disconnect() — not client.disconnect() —
            // keeps `manualDisconnect` false, so the auto-reconnect path
            // engages and resubscribeAll() runs.
            const transport = (reconnectClient as unknown as {
                transport: { disconnect: () => void };
            }).transport;
            transport.disconnect();

            // Wait for the client to fully re-enter the connected state.
            await new Promise<void>((resolve) => {
                const offState = reconnectClient.onStateChange((s) => {
                    if (s === 'connected') {
                        offState();
                        resolve();
                    }
                });
            });

            // Give resubscribe responses time to land.
            await new Promise((r) => setTimeout(r, 200));

            offLoading();

            // After reconnect, getLoadingCount() must still report 0 (it
            // already excludes subscription pending entries).
            expect(reconnectClient.getLoadingCount()).toBe(0);

            // The notification stream must agree. With the regression in
            // notifyLoadingChange(), resubscribeAll() puts subscription IDs
            // back in `pending` and notifies listeners with `pending.size`,
            // so useIsLoading() flips to true and stays stuck until each
            // initial response arrives — exactly the symptom this guards.
            expect(counts.every((c) => c === 0)).toBe(true);
        } finally {
            reconnectClient.disconnect();
        }
    });

    test('unknown method throws ApiError with isNotFound', async () => {
        try {
            await client.request('NonExistent', []);
            expect.fail('Should have thrown');
        } catch (err) {
            expect(err).toBeInstanceOf(ApiError);
            expect((err as ApiError).isNotFound()).toBe(true);
        }
    });
});
