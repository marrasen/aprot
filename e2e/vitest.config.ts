import { defineConfig } from 'vitest/config';

export default defineConfig({
    test: {
        // globalSetup builds the Go server binary once before any test file.
        // setupFiles then runs per-worker (once per test file under
        // pool: 'forks') and spawns an isolated server process for that
        // file — issue #180 eliminates cross-file TriggerRefresh pollution
        // that caused flakes in query-cache.test.ts.
        globalSetup: './tests/setup.ts',
        setupFiles: ['./tests/setup-per-file.ts'],
        testTimeout: 30000,
        hookTimeout: 30000,
        teardownTimeout: 5000,
        pool: 'forks',
    },
});
