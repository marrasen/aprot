// @vitest-environment jsdom
import { describe, test, expect, beforeAll, afterAll } from 'vitest';
import { render, screen, waitFor, cleanup } from '@testing-library/react';
import { wsUrl } from './helpers';
import { ApiClient, ApiClientProvider } from '../react-api/client';
import { useGetTask } from '../react-api/public-handlers';

// Executes a generated React query hook against the live server, so the
// loading -> data state machine actually runs instead of being only
// type-checked (#207 e2e gap). Uses the public getTask handler, which returns a
// fixed task with no auth or setup required.

function TaskProbe() {
    const { data, error, isLoading } = useGetTask('task-1');
    if (error) return <div>error: {error.message}</div>;
    if (isLoading || !data) return <div>loading</div>;
    return <div>task: {data.name}</div>;
}

describe('React hooks (WebSocket)', () => {
    let client: ApiClient;

    beforeAll(async () => {
        client = new ApiClient(wsUrl(), { reconnect: false });
        await client.connect();
    });

    afterAll(() => {
        cleanup();
        client.disconnect();
    });

    test('useGetTask transitions from loading to data', async () => {
        render(
            <ApiClientProvider value={client}>
                <TaskProbe />
            </ApiClientProvider>,
        );

        // The first synchronous render shows the loading state (the request is
        // dispatched from an effect and resolves over the network).
        expect(screen.getByText('loading')).toBeTruthy();

        // Then the hook resolves with the server's response.
        await waitFor(
            () => expect(screen.getByText('task: Example Task')).toBeTruthy(),
            { timeout: 10000 },
        );
    });
});
