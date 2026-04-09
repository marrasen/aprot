import { ApiClient, getWebSocketUrl, getSSEUrl } from './api/client';
import { TaskStatus, TaskStatusType, ListUsersResponse, createUser, getTask, processBatch, subscribeListUsers, onUserCreatedEvent, onUserUpdatedEvent, onSystemNotificationEvent } from './api/public-handlers';

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

async function doCreateUser(): Promise<void> {
    if (!client) return;
    const name = (document.getElementById('userName') as HTMLInputElement).value.trim();
    const email = (document.getElementById('userEmail') as HTMLInputElement).value.trim();
    if (!name || !email) {
        log('Please enter name and email', 'error');
        return;
    }
    try {
        const result = await createUser(client, name, email);
        log(`Created user: ${JSON.stringify(result)}`, 'response');
        (document.getElementById('userName') as HTMLInputElement).value = '';
        (document.getElementById('userEmail') as HTMLInputElement).value = '';
    } catch (err) {
        log(`Error: ${(err as Error).message}`, 'error');
    }
}

// Render the users list. Called by the subscribeListUsers subscription
// whenever the server pushes an updated result (initial load + every
// TriggerRefresh(ctx, "users") on the server).
function renderUsers(data: ListUsersResponse): void {
    const listEl = document.getElementById('usersList')!;
    listEl.textContent = '';
    if (data.users.length === 0) {
        const empty = document.createElement('li');
        empty.textContent = 'No users yet';
        listEl.appendChild(empty);
        return;
    }
    for (const u of data.users) {
        const li = document.createElement('li');
        const strong = document.createElement('strong');
        strong.textContent = u.name;
        li.appendChild(strong);
        li.appendChild(document.createTextNode(` - ${u.email} (${u.id})`));
        listEl.appendChild(li);
    }
}

// Helper to get a human-readable label for task status using the enum
function getStatusLabel(status: TaskStatusType): string {
    switch (status) {
        case TaskStatus.Created:
            return '⏳ Created';
        case TaskStatus.Running:
            return '🔄 Running';
        case TaskStatus.Completed:
            return '✅ Completed';
        case TaskStatus.Failed:
            return '❌ Failed';
        default:
            return status;
    }
}

// Helper to get CSS class for status badge
function getStatusClass(status: TaskStatusType): string {
    switch (status) {
        case TaskStatus.Created:
            return 'status-created';
        case TaskStatus.Running:
            return 'status-running';
        case TaskStatus.Completed:
            return 'status-completed';
        case TaskStatus.Failed:
            return 'status-failed';
        default:
            return '';
    }
}

async function doGetTask(): Promise<void> {
    if (!client) return;
    const taskId = (document.getElementById('taskId') as HTMLInputElement).value.trim();
    if (!taskId) {
        log('Please enter a task ID', 'error');
        return;
    }
    try {
        const task = await getTask(client, taskId);

        // Display task info with enum-based status
        const taskInfoEl = document.getElementById('taskInfo')!;
        const statusLabel = getStatusLabel(task.status);
        const statusClass = getStatusClass(task.status);

        taskInfoEl.innerHTML = `
            <div><strong>ID:</strong> ${task.id}</div>
            <div><strong>Name:</strong> ${task.name}</div>
            <div><strong>Status:</strong> <span class="status-badge ${statusClass}">${statusLabel}</span></div>
        `;

        // Demonstrate enum comparison
        if (task.status === TaskStatus.Running) {
            log(`Task ${task.id} is currently running`, 'progress');
        } else if (task.status === TaskStatus.Completed) {
            log(`Task ${task.id} has completed`, 'response');
        } else if (task.status === TaskStatus.Failed) {
            log(`Task ${task.id} has failed`, 'error');
        } else {
            log(`Task ${task.id} status: ${task.status}`, 'response');
        }

        log(`Got task: ${JSON.stringify(task)}`, 'response');
    } catch (err) {
        log(`Error: ${(err as Error).message}`, 'error');
    }
}

async function doProcessBatch(): Promise<void> {
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
        const result = await processBatch(
            client,
            items,
            delay,
            {
                signal: abortController.signal,
                onProgress: (current: number, total: number, message: string) => {
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

function createClient(transport: 'websocket' | 'sse'): ApiClient {
    const url = transport === 'sse' ? getSSEUrl() : getWebSocketUrl();
    const opts = transport === 'sse' ? { transport: 'sse' as const } : undefined;
    const c = new ApiClient(url, opts);

    c.onStateChange((state) => {
        updateStatus(state === 'connected');
    });

    // Update the global loading indicator dot whenever any request is in flight.
    c.onLoadingChange((count) => {
        const dot = document.getElementById('loadingDot');
        if (!dot) return;
        const busy = count > 0;
        dot.style.background = busy ? '#ffc107' : '#28a745';
        dot.title = busy ? `${count} request(s) in flight` : 'Idle';
    });

    // Push events still demonstrate one-shot notifications. The user list
    // itself is kept fresh by the subscribeListUsers subscription below, so
    // these handlers only log — no manual refetch needed.
    onUserCreatedEvent(c, (data) => {
        log(`User created: ${data.name} (${data.id})`, 'push');
    });

    onUserUpdatedEvent(c, (data) => {
        log(`User updated: ${data.name} (${data.id})`, 'push');
    });

    onSystemNotificationEvent(c, (data) => {
        log(`Notification [${data.level}]: ${data.message}`, 'push');
    });

    return c;
}

async function connectAndSubscribe(): Promise<void> {
    if (!client) return;
    const transport = (document.getElementById('transportSelect') as HTMLSelectElement).value as 'websocket' | 'sse';
    try {
        await client.connect();
        log(`Connected via ${transport}`, 'response');
        // Server-driven subscription: the callback fires on initial load and
        // again whenever any handler calls aprot.TriggerRefresh(ctx, "users").
        subscribeListUsers(client, renderUsers, (err) => {
            log(`Users subscription error: ${err.message}`, 'error');
        });
    } catch (err) {
        log(`Connection failed: ${(err as Error).message}`, 'error');
    }
}

async function reconnect(): Promise<void> {
    if (client) {
        client.disconnect();
    }
    const transport = (document.getElementById('transportSelect') as HTMLSelectElement).value as 'websocket' | 'sse';
    client = createClient(transport);
    await connectAndSubscribe();
}

async function init(): Promise<void> {
    const transport = (document.getElementById('transportSelect') as HTMLSelectElement).value as 'websocket' | 'sse';
    client = createClient(transport);
    await connectAndSubscribe();
}

// Expose functions to window for HTML onclick handlers
declare global {
    interface Window {
        createUser: typeof doCreateUser;
        getTask: typeof doGetTask;
        processBatch: typeof doProcessBatch;
        cancelBatch: typeof cancelBatch;
        clearLog: typeof clearLog;
        reconnect: typeof reconnect;
    }
}

window.createUser = doCreateUser;
window.getTask = doGetTask;
window.processBatch = doProcessBatch;
window.cancelBatch = cancelBatch;
window.clearLog = clearLog;
window.reconnect = reconnect;

init();
