// @vitest-environment jsdom
import { describe, test, expect, beforeAll, afterAll } from 'vitest';
import { render, screen, waitFor, cleanup } from '@testing-library/react';
import { wsUrl } from './helpers';
import { ApiClient, ApiClientProvider, mergeByKey } from '../react-api/client';
import { useListPatchItems, setRating, getListExecutions } from '../react-api/patch-handlers';
import type { PatchItemList } from '../react-api/patch-handlers';

// Subscription patches through the React query cache (#237): two components
// share one cached subscription with an applyPatch reducer; a mutation's
// patch must update both — applied to the shared snapshot — without the
// subscribed query re-executing on the server.

const applyPatch = (data: PatchItemList, patch: unknown): PatchItemList => ({
    ...data,
    items: mergeByKey<PatchItemList['items'][number]>('id')(data.items, patch),
});

function RatingProbe({ label }: { label: string }) {
    const { data } = useListPatchItems({ applyPatch });
    const p3 = data?.items.find((i) => i.id === 'p3');
    if (!p3) return <div>{label}: loading</div>;
    return <div>{label}: p3={p3.rating}</div>;
}

describe('React hooks apply subscription patches to the shared cache', () => {
    let client: ApiClient;

    beforeAll(async () => {
        client = new ApiClient(wsUrl(), { reconnect: false });
        await client.connect();
    });

    afterAll(() => {
        cleanup();
        client.disconnect();
    });

    test('a patch updates every component on the shared cache without a refetch', async () => {
        render(
            <ApiClientProvider value={client}>
                <RatingProbe label="a" />
                <RatingProbe label="b" />
            </ApiClientProvider>,
        );

        await waitFor(() => {
            expect(screen.getByText('a: p3=0')).toBeTruthy();
            expect(screen.getByText('b: p3=0')).toBeTruthy();
        }, { timeout: 10000 });

        const executionsBefore = await getListExecutions(client);

        await setRating(client, 'p3', 42);

        await waitFor(() => {
            expect(screen.getByText('a: p3=42')).toBeTruthy();
            expect(screen.getByText('b: p3=42')).toBeTruthy();
        }, { timeout: 10000 });

        // Both components updated from the patched shared snapshot; the
        // subscribed query never re-executed on the server.
        const executionsAfter = await getListExecutions(client);
        expect(executionsAfter).toBe(executionsBefore);
    });
});
