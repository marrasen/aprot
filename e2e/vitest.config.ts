import { defineConfig } from 'vitest/config';

export default defineConfig({
    test: {
        globalSetup: './tests/setup.ts',
        testTimeout: 30000,
        hookTimeout: 15000,
        teardownTimeout: 1000,
        pool: 'forks',
    },
});
