import { ApiClient } from './client';

let client: ApiClient | null = null;
let abortController: AbortController | null = null;

function updateStatus(connected: boolean): void {
    const el = document.getElementById('status')!;
    if (connected) {
        el.textContent = 'Connected';
        el.className = 'status connected';
    } else {
        el.textContent = 'Disconnected';
        el.className = 'status disconnected';
    }
}

function log(message: string, type: string = ''): void {
    const logEl = document.getElementById('log')!;
    const entry = document.createElement('div');
    entry.className = `log-entry ${type}`;
    entry.textContent = `[${new Date().toLocaleTimeString()}] ${message}`;
    logEl.appendChild(entry);
    logEl.scrollTop = logEl.scrollHeight;
}

function clearLog(): void {
    document.getElementById('log')!.innerHTML = '';
}

async function createUser(): Promise<void> {
    if (!client) return;

    const name = (document.getElementById('userName') as HTMLInputElement).value.trim();
    const email = (document.getElementById('userEmail') as HTMLInputElement).value.trim();

    if (!name || !email) {
        log('Please enter name and email', 'error');
        return;
    }

    try {
        const result = await client.createUser({ name, email });
        log(`Created user: ${JSON.stringify(result)}`, 'response');
        (document.getElementById('userName') as HTMLInputElement).value = '';
        (document.getElementById('userEmail') as HTMLInputElement).value = '';
    } catch (err) {
        log(`Error: ${(err as Error).message}`, 'error');
    }
}

async function listUsers(): Promise<void> {
    if (!client) return;

    try {
        const result = await client.listUsers({});
        const listEl = document.getElementById('usersList')!;

        if (result.users.length === 0) {
            listEl.innerHTML = '<li>No users yet</li>';
        } else {
            listEl.innerHTML = result.users.map(u =>
                `<li><strong>${u.name}</strong> - ${u.email} (${u.id})</li>`
            ).join('');
        }
    } catch (err) {
        log(`Error listing users: ${(err as Error).message}`, 'error');
    }
}

async function processBatch(): Promise<void> {
    if (!client) return;

    const itemsStr = (document.getElementById('batchItems') as HTMLInputElement).value.trim();
    const delay = parseInt((document.getElementById('batchDelay') as HTMLInputElement).value) || 500;

    if (!itemsStr) {
        log('Please enter items', 'error');
        return;
    }

    const items = itemsStr.split(',').map(s => s.trim()).filter(s => s);

    (document.getElementById('batchBtn') as HTMLButtonElement).disabled = true;
    (document.getElementById('cancelBtn') as HTMLButtonElement).disabled = false;
    (document.getElementById('progressFill') as HTMLElement).style.width = '0%';
    document.getElementById('progressText')!.textContent = 'Starting...';

    abortController = new AbortController();

    try {
        const result = await client.processBatch(
            { items, delay },
            {
                signal: abortController.signal,
                onProgress: (current, total, message) => {
                    const pct = (current / total) * 100;
                    (document.getElementById('progressFill') as HTMLElement).style.width = `${pct}%`;
                    document.getElementById('progressText')!.textContent = `${current}/${total}: ${message}`;
                    log(`Progress: ${current}/${total} - ${message}`, 'progress');
                }
            }
        );
        (document.getElementById('progressFill') as HTMLElement).style.width = '100%';
        document.getElementById('progressText')!.textContent = `Done! Processed ${result.processed} items`;
        log(`Batch complete: ${JSON.stringify(result)}`, 'response');
    } catch (err) {
        log(`Batch error: ${(err as Error).message}`, 'error');
        document.getElementById('progressText')!.textContent = `Error: ${(err as Error).message}`;
    } finally {
        (document.getElementById('batchBtn') as HTMLButtonElement).disabled = false;
        (document.getElementById('cancelBtn') as HTMLButtonElement).disabled = true;
        abortController = null;
    }
}

function cancelBatch(): void {
    if (abortController) {
        abortController.abort();
        log('Cancellation requested', 'error');
    }
}

async function init(): Promise<void> {
    client = new ApiClient(`ws://${window.location.host}/ws`);

    // Set up push event handlers using generated typed methods
    client.onUserCreated((data) => {
        log(`User created: ${data.name} (${data.id})`, 'push');
        listUsers();
    });

    client.onUserUpdated((data) => {
        log(`User updated: ${data.name} (${data.id})`, 'push');
        listUsers();
    });

    client.onSystemNotification((data) => {
        log(`Notification [${data.level}]: ${data.message}`, 'push');
    });

    // Handle connection close
    const origConnect = client.connect.bind(client);
    client.connect = async () => {
        await origConnect();
        // Re-setup close handler to update status
    };

    try {
        await client.connect();
        updateStatus(true);
        log('Connected to server', 'response');
        listUsers();
    } catch (err) {
        log(`Connection failed: ${(err as Error).message}`, 'error');
    }
}

// Expose functions to window for HTML onclick handlers
declare global {
    interface Window {
        createUser: typeof createUser;
        listUsers: typeof listUsers;
        processBatch: typeof processBatch;
        cancelBatch: typeof cancelBatch;
        clearLog: typeof clearLog;
    }
}

window.createUser = createUser;
window.listUsers = listUsers;
window.processBatch = processBatch;
window.cancelBatch = cancelBatch;
window.clearLog = clearLog;

// Start the app
init();
