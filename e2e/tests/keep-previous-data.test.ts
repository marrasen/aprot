import { describe, test, expect } from 'vitest';
import { selectWithPreviousData, type SubscriptionSnapshot } from '../react-api/client';

// ---------------------------------------------------------------------------
// The `selectWithPreviousData` helper is exported from the generated React
// client (templates/_client-common.ts.tmpl) and is called from `useQuery`.
// It's the core of the `keepPreviousData` option: given a ref holding the
// previous snapshot and the current cached snapshot, it decides whether to
// surface the kept data or the fresh one.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('selectWithPreviousData', () => {
    test('first mount with no previous data returns the snapshot unchanged', () => {
        const ref: { current: SubscriptionSnapshot<string> | null } = { current: null };
        const snap: SubscriptionSnapshot<string> = { data: null, error: null, isLoading: true };

        const effective = selectWithPreviousData(ref, snap);

        expect(effective).toBe(snap);
        expect(ref.current).toBeNull();
    });

    test('fresh data updates the ref and returns the snapshot as-is', () => {
        const ref: { current: SubscriptionSnapshot<string> | null } = { current: null };
        const snap: SubscriptionSnapshot<string> = { data: 'alpha', error: null, isLoading: false };

        const effective = selectWithPreviousData(ref, snap);

        expect(effective).toBe(snap);
        expect(ref.current).toBe(snap);
    });

    test('param change with cache miss surfaces the kept data while loading', () => {
        // Simulates the zeit "Logs by day" arrow click: the hook has been
        // showing data for week N, the user clicks forward to week N+1, and
        // the new cache entry starts empty. The previous week's data should
        // remain on screen until the new week's data arrives.
        const ref: { current: SubscriptionSnapshot<string> | null } = { current: null };

        // Week N data arrives.
        const weekN: SubscriptionSnapshot<string> = { data: 'week-N', error: null, isLoading: false };
        selectWithPreviousData(ref, weekN);

        // Param change — the new cache entry is empty.
        const weekNPlus1Loading: SubscriptionSnapshot<string> = { data: null, error: null, isLoading: true };
        const duringTransition = selectWithPreviousData(ref, weekNPlus1Loading);

        expect(duringTransition.data).toBe('week-N');
        expect(duringTransition.isLoading).toBe(true);
        expect(duringTransition.error).toBeNull();
        // Ref is not overwritten until real data lands.
        expect(ref.current).toBe(weekN);
    });

    test('kept data is replaced as soon as new data lands', () => {
        const ref: { current: SubscriptionSnapshot<string> | null } = { current: null };

        const weekN: SubscriptionSnapshot<string> = { data: 'week-N', error: null, isLoading: false };
        selectWithPreviousData(ref, weekN);

        const weekNPlus1Loading: SubscriptionSnapshot<string> = { data: null, error: null, isLoading: true };
        selectWithPreviousData(ref, weekNPlus1Loading);

        const weekNPlus1Loaded: SubscriptionSnapshot<string> = { data: 'week-N+1', error: null, isLoading: false };
        const effective = selectWithPreviousData(ref, weekNPlus1Loaded);

        expect(effective.data).toBe('week-N+1');
        expect(effective.isLoading).toBe(false);
        expect(ref.current).toBe(weekNPlus1Loaded);
    });

    test('error on the new cache entry surfaces alongside the kept data', () => {
        // The user switches params and the new fetch fails. We want to keep
        // the previous data on screen AND surface the error so QueryErrors
        // can render an Alert. Losing either half is a regression.
        const ref: { current: SubscriptionSnapshot<string> | null } = { current: null };

        const weekN: SubscriptionSnapshot<string> = { data: 'week-N', error: null, isLoading: false };
        selectWithPreviousData(ref, weekN);

        const weekNPlus1Failed: SubscriptionSnapshot<string> = {
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
        const ref: { current: SubscriptionSnapshot<string> | null } = { current: null };

        const weekN: SubscriptionSnapshot<string> = { data: 'week-N', error: null, isLoading: false };
        selectWithPreviousData(ref, weekN);

        const weekNPlus1: SubscriptionSnapshot<string> = { data: 'week-N+1', error: null, isLoading: false };
        selectWithPreviousData(ref, weekNPlus1);

        // Click back — cached snapshot for week N returns instantly.
        const backToWeekN: SubscriptionSnapshot<string> = { data: 'week-N', error: null, isLoading: false };
        const effective = selectWithPreviousData(ref, backToWeekN);

        expect(effective.data).toBe('week-N');
        expect(ref.current?.data).toBe('week-N');
    });

    test('does not flash null on a subsequent empty-snapshot render', () => {
        // Regression guard: after an initial data load, any subsequent render
        // that for whatever reason sees data: null (cache miss, unmount race,
        // strict-mode double-render) should still surface the kept data.
        const ref: { current: SubscriptionSnapshot<string> | null } = { current: null };

        const initial: SubscriptionSnapshot<string> = { data: 'alpha', error: null, isLoading: false };
        selectWithPreviousData(ref, initial);

        for (let i = 0; i < 5; i++) {
            const empty: SubscriptionSnapshot<string> = { data: null, error: null, isLoading: true };
            const effective = selectWithPreviousData(ref, empty);
            expect(effective.data).toBe('alpha');
        }
    });
});
