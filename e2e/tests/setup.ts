import { type ChildProcess, spawn, execSync } from 'child_process';
import { writeFileSync, unlinkSync } from 'fs';
import { dirname, join } from 'path';
import { fileURLToPath } from 'url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const ADDR_FILE = join(__dirname, '..', '.test-server-addr');

let serverProcess: ChildProcess;

export async function setup() {
    serverProcess = spawn('go', ['run', './server'], {
        cwd: join(__dirname, '..'),
        stdio: ['ignore', 'pipe', 'pipe'],
    });

    const addr = await new Promise<string>((resolve, reject) => {
        let data = '';
        serverProcess.stdout!.on('data', (chunk: Buffer) => {
            data += chunk.toString();
            const newline = data.indexOf('\n');
            if (newline !== -1) {
                resolve(data.substring(0, newline).trim());
            }
        });
        serverProcess.stderr!.on('data', (chunk: Buffer) => {
            process.stderr.write(chunk);
        });
        serverProcess.on('error', reject);
        setTimeout(() => reject(new Error('Server startup timeout')), 60000);
    });

    writeFileSync(ADDR_FILE, addr);
}

export async function teardown() {
    if (serverProcess?.pid) {
        // go run spawns a child process; kill the entire tree
        if (process.platform === 'win32') {
            try {
                execSync(`taskkill /f /t /pid ${serverProcess.pid}`, { stdio: 'ignore' });
            } catch {
                // Process may already be gone
            }
        } else {
            // Negative PID kills the process group on Unix
            try {
                process.kill(-serverProcess.pid, 'SIGKILL');
            } catch {
                serverProcess.kill('SIGKILL');
            }
        }
    }
    try {
        unlinkSync(ADDR_FILE);
    } catch {
        // Ignore if already cleaned up
    }
}
