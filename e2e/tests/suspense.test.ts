import { describe, test, expect, beforeEach, afterEach } from 'vitest';
import { wsUrl } from './helpers';
import { ApiClient } from '../api/client';
import { createUser } from '../api/public-handlers';
import type { ListUsersResponse, GetUserResponse } from '../api/public-handlers';

// ---------------------------------------------------------------------------
// Replica of the Suspense promise cache that backs useQuerySuspense in the
// generated React client. The actual implementation lives in
// templates/_client-common.ts.tmpl and is consumed by useQuerySuspense via
// useSyncExternalStore + React 19's `use()`. We replicate it here so we can
// validate the contract -- initial promise, push-driven replacement,
// refcount cleanup, error propagation -- against a real aprot server
// without needing a React runtime in the e2e harness.
// ---------------------------------------------------------------------------

interface SuspensePromiseEntry<T = unknown> {
    promise: Promise<T>;
    listeners: Set<() => void>;
    refCount: number;
    unsubscribe: (() => void) | null;
    settled: boolean;
}

const suspensePromiseCaches = new WeakMap<ApiClient, Map<string, SuspensePromiseEntry>>();

function getCache(client: ApiClient): Map<string, SuspensePromiseEntry> {
    let cache = suspensePromiseCaches.get(client);
    if (!cache) {
        cache = new Map();
        suspensePromiseCaches.set(client, cache);
    }
    return cache;
}

function getOrCreateEntry<T>(
    client: ApiClient,
    key: string,
    method: string,
    params: unknown[],
): SuspensePromiseEntry<T> {
    const cache = getCache(client);
    const existing = cache.get(key) as SuspensePromiseEntry<T> | undefined;
    if (existing) return existing;

    let resolveInitial!: (v: T) => void;
    let rejectInitial!: (e: Error) => void;
    const initialPromise = new Promise<T>((res, rej) => {
        resolveInitial = res;
        rejectInitial = rej;
    });

    const entry: SuspensePromiseEntry<T> = {
        promise: initialPromise,
        listeners: new Set(),
        refCount: 0,
        unsubscribe: null,
        settled: false,
    };
    cache.set(key, entry);

    entry.unsubscribe = client.subscribe<T>(
        method, params,
        (data) => {
            if (!entry.settled) {
                entry.settled = true;
                resolveInitial(data);
                for (const l of entry.listeners) l();
            } else {
                entry.promise = Promise.resolve(data);
                for (const l of entry.listeners) l();
            }
        },
        (err) => {
            if (!entry.settled) {
                entry.settled = true;
                rejectInitial(err);
            } else {
                const rejected = Promise.reject(err);
                rejected.catch(() => {});
                entry.promise = rejected;
                for (const l of entry.listeners) l();
            }
        },
    );
    return entry;
}

function subscribeToEntry<T>(
    client: ApiClient,
    key: string,
    method: string,
    params: unknown[],
    listener: () => void,
): () => void {
    const entry = getOrCreateEntry<T>(client, key, method, params);
    entry.listeners.add(listener);
    entry.refCount++;
    return () => {
        entry.listeners.delete(listener);
        entry.refCount--;
        if (entry.refCount === 0) {
            queueMicrotask(() => {
                if (entry.refCount === 0) {
                    entry.unsubscribe?.();
                    entry.unsubscribe = null;
                    getCache(client).delete(key);
                }
            });
        }
    };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('Suspense Promise Cache', () => {
    let client: ApiClient;

    beforeEach(async () => {
        client = new ApiClient(wsUrl(), { reconnect: false });
        await client.connect();
    });

    afterEach(() => {
        client.disconnect();
    });

    test('initial promise resolves with first server response', async () => {
        // This is what React's use() reads on the initial Suspense throw.
        const entry = getOrCreateEntry<ListUsersResponse>(
            client, 'init:[]', 'PublicHandlers.ListUsers', [],
        );
        const data = await entry.promise;
        expect(Array.isArray(data.users)).toBe(true);
        expect(entry.settled).toBe(true);
    });

    test('promise is stable across reads while value is unchanged', async () => {
        // use() requires a stable Promise reference -- each render must read
        // the same promise object until new data arrives, otherwise the
        // component would suspend forever.
        const entry = getOrCreateEntry<ListUsersResponse>(
            client, 'stable:[]', 'PublicHandlers.ListUsers', [],
        );
        const promiseAtCreate = entry.promise;
        await entry.promise;
        const promiseAfterSettle = entry.promise;
        const promiseAfterAnotherRead = entry.promise;

        // The same Promise object, before and after settlement, until a push.
        expect(promiseAfterSettle).toBe(promiseAtCreate);
        expect(promiseAfterAnotherRead).toBe(promiseAtCreate);
    });

    test('second subscriber shares cached entry (refcounting)', async () => {
        const cache = getCache(client);
        const KEY = 'shared:[]';

        const unsub1 = subscribeToEntry(
            client, KEY, 'PublicHandlers.ListUsers', [], () => {},
        );
        expect(cache.get(KEY)!.refCount).toBe(1);

        const unsub2 = subscribeToEntry(
            client, KEY, 'PublicHandlers.ListUsers', [], () => {},
        );
        expect(cache.get(KEY)!.refCount).toBe(2);

        // Both share the same entry — and therefore the same promise.
        const entryA = cache.get(KEY)!;
        const entryB = cache.get(KEY)!;
        expect(entryA).toBe(entryB);
        expect(entryA.promise).toBe(entryB.promise);

        unsub1();
        expect(cache.get(KEY)!.refCount).toBe(1);

        // Drain microtask queue so deferred cleanup would have run.
        await Promise.resolve();
        await Promise.resolve();
        expect(cache.has(KEY)).toBe(true);

        unsub2();
        // Two microtasks for the deferred cleanup + safety margin.
        await Promise.resolve();
        await Promise.resolve();
        expect(cache.has(KEY)).toBe(false);
    });

    test('server push replaces the cached promise with new identity', async () => {
        // This is the live-update path: after the initial resolved promise,
        // each TriggerRefresh push replaces entry.promise with a freshly-
        // resolved one. React's useSyncExternalStore detects the identity
        // change and re-runs use() with the new value.
        const KEY = 'push:[]';
        const entry = getOrCreateEntry<ListUsersResponse>(
            client, KEY, 'PublicHandlers.ListUsers', [],
        );

        let notifyCount = 0;
        subscribeToEntry(
            client, KEY, 'PublicHandlers.ListUsers', [], () => { notifyCount++; },
        );

        // Wait for the initial promise to settle.
        await entry.promise;
        const promiseBeforePush = entry.promise;

        // Triggering a server-side TriggerRefresh by creating a user.
        const uniqueName = 'SuspensePush_' + Date.now();
        const created = await createUser(client, uniqueName, `${uniqueName}@test.com`);

        // Wait for the push to land and replace the promise reference.
        const start = Date.now();
        while (entry.promise === promiseBeforePush && Date.now() - start < 5000) {
            await new Promise((r) => setTimeout(r, 25));
        }

        expect(entry.promise).not.toBe(promiseBeforePush);
        expect(notifyCount).toBeGreaterThanOrEqual(1);

        const newData = await entry.promise;
        expect(newData.users.some((u) => u.id === created.id)).toBe(true);
    });

    test('refcount drop schedules deferred cleanup; remount cancels it', async () => {
        // This protects against Strict Mode double-mount and Suspense
        // fallback teardowns that would otherwise tear down an in-flight
        // subscription between unmount and remount.
        const cache = getCache(client);
        const KEY = 'remount:[]';

        const unsub1 = subscribeToEntry(
            client, KEY, 'PublicHandlers.ListUsers', [], () => {},
        );
        await cache.get(KEY)!.promise;

        // Drop refcount to 0 -- schedules deferred cleanup.
        unsub1();
        expect(cache.get(KEY)!.refCount).toBe(0);

        // Re-subscribe BEFORE the microtask fires.
        const unsub2 = subscribeToEntry(
            client, KEY, 'PublicHandlers.ListUsers', [], () => {},
        );
        expect(cache.get(KEY)!.refCount).toBe(1);

        // Drain microtasks.
        await Promise.resolve();
        await Promise.resolve();

        // Entry survived the unmount/remount round trip — same promise,
        // same subscription, no extra round trip to the server.
        expect(cache.has(KEY)).toBe(true);
        expect(cache.get(KEY)!.settled).toBe(true);

        unsub2();
    });

    test('error response rejects the initial promise', async () => {
        // use() will throw the rejection into the nearest error boundary.
        // We verify the promise rejects with a clear ApiError.
        const entry = getOrCreateEntry<GetUserResponse>(
            client, 'err:[non-existent]', 'PublicHandlers.GetUser', ['non-existent-id'],
        );
        await expect(entry.promise).rejects.toThrow();
    });

    test('different cache keys create independent subscriptions', async () => {
        // Confirms params-based cache keying. Two entries with different
        // params do not share a promise.
        const cache = getCache(client);
        const created = await createUser(client, 'IndKey', 'indkey-suspense@test.com');

        const unsubA = subscribeToEntry(
            client, 'PublicHandlers.ListUsers:[]', 'PublicHandlers.ListUsers', [], () => {},
        );
        const unsubB = subscribeToEntry(
            client, `PublicHandlers.GetUser:["${created.id}"]`, 'PublicHandlers.GetUser', [created.id], () => {},
        );

        expect(cache.size).toBe(2);
        expect(cache.has('PublicHandlers.ListUsers:[]')).toBe(true);
        expect(cache.has(`PublicHandlers.GetUser:["${created.id}"]`)).toBe(true);

        // Different entries hold different promises.
        const promA = cache.get('PublicHandlers.ListUsers:[]')!.promise;
        const promB = cache.get(`PublicHandlers.GetUser:["${created.id}"]`)!.promise;
        expect(promA).not.toBe(promB);

        unsubA();
        unsubB();
    });
});
