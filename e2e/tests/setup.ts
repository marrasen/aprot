import { execFileSync } from 'child_process';
import { dirname, join } from 'path';
import { fileURLToPath } from 'url';
import { existsSync, unlinkSync } from 'fs';

const __dirname = dirname(fileURLToPath(import.meta.url));

// Pre-build the Go e2e server once before any test file runs. Each test
// file then spawns its own copy of this binary via setup-per-file.ts so
// every vitest worker gets an isolated server — see issue #180 for the
// cross-file TriggerRefresh pollution that broke query-cache.test.ts when
// all workers shared a single server via globalSetup.
const binaryPath = join(
    __dirname,
    '..',
    'server',
    process.platform === 'win32' ? 'server.exe' : 'server',
);

export async function setup() {
    execFileSync(
        'go',
        ['build', '-o', binaryPath, './server'],
        {
            cwd: join(__dirname, '..'),
            stdio: 'inherit',
        },
    );
}

export async function teardown() {
    if (existsSync(binaryPath)) {
        try {
            unlinkSync(binaryPath);
        } catch {
            // Ignore if already cleaned up
        }
    }
}
