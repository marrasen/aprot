// @vitest-environment jsdom
import { describe, test, expect, afterEach } from 'vitest';
import { render, screen, waitFor, cleanup } from '@testing-library/react';
import { wsUrl, wsRejectUrl } from './helpers';
import { ApiClient, ApiClientProvider, useConnection } from '../react-api/client';

// useConnection surfaces the rejection alongside the state so a UI can tell
// "session expired, sign in again" from "server unreachable" (#283). This is
// also the StrictMode-safe wiring the auth recipe documents: the client is
// created and connected outside React and provided synchronously, so no render
// gates on the connect promise.

function ConnectionProbe() {
    const { state, rejection } = useConnection();
    return (
        <div>
            <span>state: {state}</span>
            <span>rejection: {rejection ? rejection.message : 'none'}</span>
        </div>
    );
}

describe('useConnection (React)', () => {
    let client: ApiClient | null = null;

    afterEach(() => {
        cleanup();
        client?.disconnect();
        client = null;
    });

    test('exposes the rejection ApiError after the server rejects', async () => {
        client = new ApiClient(wsRejectUrl(), { reconnect: false });
        client.connect();

        render(
            <ApiClientProvider value={client}>
                <ConnectionProbe />
            </ApiClientProvider>,
        );

        await waitFor(
            () => expect(screen.getByText('rejection: invalid session')).toBeTruthy(),
            { timeout: 10000 },
        );
        expect(screen.getByText('state: disconnected')).toBeTruthy();
    });

    test('reports no rejection on a healthy connection', async () => {
        client = new ApiClient(wsUrl(), { reconnect: false });
        await client.connect();

        render(
            <ApiClientProvider value={client}>
                <ConnectionProbe />
            </ApiClientProvider>,
        );

        await waitFor(
            () => expect(screen.getByText('state: connected')).toBeTruthy(),
            { timeout: 10000 },
        );
        expect(screen.getByText('rejection: none')).toBeTruthy();
    });
});
