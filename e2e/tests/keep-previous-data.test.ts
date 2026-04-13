import { describe, test, expect } from 'vitest';

// ---------------------------------------------------------------------------
// The `selectWithPreviousData` helper lives inside the generated React client
// (templates/_client-common.ts.tmpl) and is called from `useQuery`. It's the
// core of the `keepPreviousData` option: given a ref holding the previous
// snapshot and the current cached snapshot, it decides whether to surface the
// kept data or the fresh one.
//
// We replicate it here so we can test its behaviour with plain vitest, the
// same way query-cache.test.ts replicates subscribeCached. No React runtime,
// no running server, just the pure selector logic.
// ---------------------------------------------------------------------------

interface Snapshot<T = unknown> {
    data: T | null;
    error: Error | null;
    isLoading: boolean;
}

function selectWithPreviousData<T>(
    previousRef: { current: Snapshot<T> | null },
    snapshot: Snapshot<T>,
): Snapshot<T> {
    if (snapshot.data !== null) {
        previousRef.current = snapshot;
        return snapshot;
    }
    if (previousRef.current && previousRef.current.data !== null) {
        return {
            data: previousRef.current.data,
            error: snapshot.error,
            isLoading: snapshot.isLoading,
        };
    }
    return snapshot;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('selectWithPreviousData', () => {
    test('first mount with no previous data returns the snapshot unchanged', () => {
        const ref: { current: Snapshot<string> | null } = { current: null };
        const snap: Snapshot<string> = { data: null, error: null, isLoading: true };

        const effective = selectWithPreviousData(ref, snap);

        expect(effective).toBe(snap);
        expect(ref.current).toBeNull();
    });

    test('fresh data updates the ref and returns the snapshot as-is', () => {
        const ref: { current: Snapshot<string> | null } = { current: null };
        const snap: Snapshot<string> = { data: 'alpha', error: null, isLoading: false };

        const effective = selectWithPreviousData(ref, snap);

        expect(effective).toBe(snap);
        expect(ref.current).toBe(snap);
    });

    test('param change with cache miss surfaces the kept data while loading', () => {
        // Simulates the zeit "Logs by day" arrow click: the hook has been
        // showing data for week N, the user clicks forward to week N+1, and
        // the new cache entry starts empty. The previous week's data should
        // remain on screen until the new week's data arrives.
        const ref: { current: Snapshot<string> | null } = { current: null };

        // Week N data arrives.
        const weekN: Snapshot<string> = { data: 'week-N', error: null, isLoading: false };
        selectWithPreviousData(ref, weekN);

        // Param change — the new cache entry is empty.
        const weekNPlus1Loading: Snapshot<string> = { data: null, error: null, isLoading: true };
        const duringTransition = selectWithPreviousData(ref, weekNPlus1Loading);

        expect(duringTransition.data).toBe('week-N');
        expect(duringTransition.isLoading).toBe(true);
        expect(duringTransition.error).toBeNull();
        // Ref is not overwritten until real data lands.
        expect(ref.current).toBe(weekN);
    });

    test('kept data is replaced as soon as new data lands', () => {
        const ref: { current: Snapshot<string> | null } = { current: null };

        const weekN: Snapshot<string> = { data: 'week-N', error: null, isLoading: false };
        selectWithPreviousData(ref, weekN);

        const weekNPlus1Loading: Snapshot<string> = { data: null, error: null, isLoading: true };
        selectWithPreviousData(ref, weekNPlus1Loading);

        const weekNPlus1Loaded: Snapshot<string> = { data: 'week-N+1', error: null, isLoading: false };
        const effective = selectWithPreviousData(ref, weekNPlus1Loaded);

        expect(effective.data).toBe('week-N+1');
        expect(effective.isLoading).toBe(false);
        expect(ref.current).toBe(weekNPlus1Loaded);
    });

    test('error on the new cache entry surfaces alongside the kept data', () => {
        // The user switches params and the new fetch fails. We want to keep
        // the previous data on screen AND surface the error so QueryErrors
        // can render an Alert. Losing either half is a regression.
        const ref: { current: Snapshot<string> | null } = { current: null };

        const weekN: Snapshot<string> = { data: 'week-N', error: null, isLoading: false };
        selectWithPreviousData(ref, weekN);

        const weekNPlus1Failed: Snapshot<string> = {
            data: null,
            error: new Error('upstream 500'),
            isLoading: false,
        };
        const effective = selectWithPreviousData(ref, weekNPlus1Failed);

        expect(effective.data).toBe('week-N');
        expect(effective.error).toEqual(new Error('upstream 500'));
        expect(effective.isLoading).toBe(false);
    });

    test('transition back to a still-cached param returns its data immediately', () => {
        // User clicks forward then back. Week N is still in the shared cache,
        // so useSyncExternalStore hands us its snapshot directly — we want to
        // return that, not the ref which currently holds week N+1.
        const ref: { current: Snapshot<string> | null } = { current: null };

        const weekN: Snapshot<string> = { data: 'week-N', error: null, isLoading: false };
        selectWithPreviousData(ref, weekN);

        const weekNPlus1: Snapshot<string> = { data: 'week-N+1', error: null, isLoading: false };
        selectWithPreviousData(ref, weekNPlus1);

        // Click back — cached snapshot for week N returns instantly.
        const backToWeekN: Snapshot<string> = { data: 'week-N', error: null, isLoading: false };
        const effective = selectWithPreviousData(ref, backToWeekN);

        expect(effective.data).toBe('week-N');
        expect(ref.current?.data).toBe('week-N');
    });

    test('does not flash null on a subsequent empty-snapshot render', () => {
        // Regression guard: after an initial data load, any subsequent render
        // that for whatever reason sees data: null (cache miss, unmount race,
        // strict-mode double-render) should still surface the kept data.
        const ref: { current: Snapshot<string> | null } = { current: null };

        const initial: Snapshot<string> = { data: 'alpha', error: null, isLoading: false };
        selectWithPreviousData(ref, initial);

        for (let i = 0; i < 5; i++) {
            const empty: Snapshot<string> = { data: null, error: null, isLoading: true };
            const effective = selectWithPreviousData(ref, empty);
            expect(effective.data).toBe('alpha');
        }
    });
});
