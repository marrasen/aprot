import { type ChildProcess, spawn, execFileSync } from 'child_process';
import { dirname, join } from 'path';
import { fileURLToPath } from 'url';
import { beforeAll, afterAll } from 'vitest';

// Per-worker server lifecycle. Vitest runs this file once per test file
// under pool: 'forks', so the beforeAll/afterAll hooks below apply to the
// whole test file and each file gets its own Go server process — no
// cross-file TriggerRefresh pollution (issue #180).
//
// The server binary itself is built once in setup.ts (globalSetup) so each
// worker pays process-spawn cost only, not go-build cost.

const __dirname = dirname(fileURLToPath(import.meta.url));
const binaryPath = join(
    __dirname,
    '..',
    'server',
    process.platform === 'win32' ? 'server.exe' : 'server',
);

declare global {
    // Worker-local because each vitest fork has its own globalThis. Reading
    // from helpers.ts instead of the filesystem keeps the address scoped to
    // the current test file's server.
    // eslint-disable-next-line no-var
    var __APROT_E2E_SERVER_ADDR__: string | undefined;
}

let serverProcess: ChildProcess | undefined;

beforeAll(async () => {
    serverProcess = spawn(binaryPath, [], {
        cwd: join(__dirname, '..'),
        stdio: ['ignore', 'pipe', 'pipe'],
        // Detached on Unix so we can kill the whole process group on
        // teardown; the pre-built binary is a single process today, but
        // staying consistent with the prior setup is safer against future
        // wrapper scripts.
        detached: process.platform !== 'win32',
    });

    const addr = await new Promise<string>((resolve, reject) => {
        let data = '';
        serverProcess!.stdout!.on('data', (chunk: Buffer) => {
            data += chunk.toString();
            const newline = data.indexOf('\n');
            if (newline !== -1) {
                resolve(data.substring(0, newline).trim());
            }
        });
        serverProcess!.stderr!.on('data', (chunk: Buffer) => {
            process.stderr.write(chunk);
        });
        serverProcess!.on('error', reject);
        setTimeout(() => reject(new Error('Server startup timeout')), 30000);
    });

    globalThis.__APROT_E2E_SERVER_ADDR__ = addr;
}, 30000);

afterAll(() => {
    if (serverProcess?.pid) {
        if (process.platform === 'win32') {
            try {
                execFileSync('taskkill', ['/f', '/t', '/pid', String(serverProcess.pid)], {
                    stdio: 'ignore',
                });
            } catch {
                // Process may already be gone
            }
        } else {
            try {
                process.kill(-serverProcess.pid, 'SIGKILL');
            } catch {
                serverProcess.kill('SIGKILL');
            }
        }
    }
    globalThis.__APROT_E2E_SERVER_ADDR__ = undefined;
});
