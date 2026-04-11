import { describe, test, expect, beforeEach, afterEach } from 'vitest';
import { wsUrl } from './helpers';
import { ApiClient } from '../api/client';
import { createUser, subscribeListUsers, subscribeGetUser } from '../api/public-handlers';
import type { ListUsersResponse, GetUserResponse } from '../api/public-handlers';

describe('Subscription Refresh', () => {
    let client: ApiClient;

    beforeEach(async () => {
        client = new ApiClient(wsUrl(), { reconnect: false });
        await client.connect();
    });

    afterEach(() => {
        client.disconnect();
    });

    test('subscribeListUsers receives initial data', async () => {
        const data = await new Promise<ListUsersResponse>((resolve) => {
            subscribeListUsers(client, (result) => {
                resolve(result);
            });
        });

        expect(Array.isArray(data.users)).toBe(true);
    });

    test('subscribeListUsers refreshes when a user is created', async () => {
        // Subscribe and wait for initial data to land.
        const initial = await new Promise<ListUsersResponse>((resolve) => {
            subscribeListUsers(client, (result) => {
                resolve(result);
            });
        });

        // Now create a user — this triggers TriggerRefresh("users") server-side.
        const created = await createUser(client, 'SubRefresh', 'subrefresh@test.com');

        // The subscription must eventually deliver a list that contains the new user.
        // Other tests may also create users concurrently, so we wait for any
        // callback that includes our specific user.
        const refreshed = await new Promise<ListUsersResponse>((resolve) => {
            subscribeListUsers(client, (result) => {
                if (result.users.some((u) => u.id === created.id)) {
                    resolve(result);
                }
            });
        });

        expect(refreshed.users.some((u) => u.id === created.id)).toBe(true);
        expect(refreshed.users.length).toBeGreaterThan(initial.users.length);
    });

    test('subscribeGetUser receives initial data', async () => {
        const created = await createUser(client, 'GetSubUser', 'getsub@test.com');

        const data = await new Promise<GetUserResponse>((resolve) => {
            subscribeGetUser(client, created.id, (result) => {
                resolve(result);
            });
        });

        expect(data.id).toBe(created.id);
        expect(data.name).toBe('GetSubUser');
    });

    test('unsubscribe stops receiving updates', async () => {
        // Subscribe and collect callbacks after we unsubscribe.
        const callsAfterUnsub: ListUsersResponse[] = [];
        let unsubscribed = false;

        const gotInitial = new Promise<void>((resolve) => {
            const unsubscribe = subscribeListUsers(client, (result) => {
                if (unsubscribed) {
                    callsAfterUnsub.push(result);
                    return;
                }
                // Got initial data — unsubscribe immediately.
                unsubscribe();
                unsubscribed = true;
                resolve();
            });
        });

        await gotInitial;

        // Create a user — this fires TriggerRefresh, but the unsubscribed
        // callback should NOT fire.
        await createUser(client, 'AfterUnsub', 'afterunsub@test.com');
        await new Promise((r) => setTimeout(r, 500));

        expect(callsAfterUnsub.length).toBe(0);
    });

    test('two subscribers to same method both receive refresh', async () => {
        const created = await createUser(client, 'DualSub', 'dualsub@test.com');

        // Two independent subscriptions — both should receive a list containing
        // the newly-created user.
        const resultA = new Promise<ListUsersResponse>((resolve) => {
            subscribeListUsers(client, (result) => {
                if (result.users.some((u) => u.id === created.id)) resolve(result);
            });
        });
        const resultB = new Promise<ListUsersResponse>((resolve) => {
            subscribeListUsers(client, (result) => {
                if (result.users.some((u) => u.id === created.id)) resolve(result);
            });
        });

        const [dataA, dataB] = await Promise.all([resultA, resultB]);
        expect(dataA.users.some((u) => u.id === created.id)).toBe(true);
        expect(dataB.users.some((u) => u.id === created.id)).toBe(true);
    });

    test('subscriber on second client receives refresh triggered by first', async () => {
        const client2 = new ApiClient(wsUrl(), { reconnect: false });
        await client2.connect();

        try {
            // Subscribe on client2, then trigger refresh from client1.
            const created = await createUser(client, 'CrossClient', 'crossclient@test.com');

            const refreshed = await new Promise<ListUsersResponse>((resolve) => {
                subscribeListUsers(client2, (result) => {
                    if (result.users.some((u) => u.id === created.id)) resolve(result);
                });
            });

            expect(refreshed.users.some((u) => u.id === created.id)).toBe(true);
        } finally {
            client2.disconnect();
        }
    });
});
