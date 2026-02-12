import { describe, test, expect, beforeEach, afterEach } from 'vitest';
import { wsUrl } from './helpers';
import { ApiClient, cancelSharedTask } from '../api/client';
import type { TaskNode, SharedTaskState } from '../api/client';
import { processWithSubTasks, startSharedWork } from '../api/public-handlers';
import { onTaskStateEvent, onTaskOutputEvent } from '../api/task-cancel-handler';

describe('SubTask (WebSocket)', () => {
    let client: ApiClient;

    beforeEach(async () => {
        client = new ApiClient(wsUrl(), { reconnect: false, heartbeatInterval: 0 });
        await client.connect();
    });

    afterEach(() => {
        client.disconnect();
    });

    test('processWithSubTasks receives task tree via onTaskProgress', async () => {
        const taskUpdates: TaskNode[][] = [];

        const res = await processWithSubTasks(
            client,
            { steps: ['Build', 'Test', 'Deploy'], delay: 50 },
            {
                onTaskProgress: (tasks) => {
                    taskUpdates.push(tasks);
                },
            },
        );

        expect(res.completed).toBe(3);

        // Should have received at least one task tree update
        expect(taskUpdates.length).toBeGreaterThanOrEqual(1);

        // Last update should have 3 children (all completed)
        const lastUpdate = taskUpdates[taskUpdates.length - 1];
        expect(lastUpdate.length).toBe(3);

        // All tasks should be completed
        for (const task of lastUpdate) {
            expect(task.status).toBe('completed');
        }

        // Verify task titles match steps
        const titles = lastUpdate.map(t => t.title);
        expect(titles).toContain('Build');
        expect(titles).toContain('Test');
        expect(titles).toContain('Deploy');
    });

    test('processWithSubTasks receives output via onOutput', async () => {
        const outputs: string[] = [];

        const res = await processWithSubTasks(
            client,
            { steps: ['Alpha', 'Beta'], delay: 50 },
            {
                onOutput: (output) => {
                    outputs.push(output);
                },
            },
        );

        expect(res.completed).toBe(2);

        // Should have received output messages (2 steps * 2 outputs each = 4)
        expect(outputs.length).toBeGreaterThanOrEqual(2);
        expect(outputs.some(o => o.includes('Alpha'))).toBe(true);
        expect(outputs.some(o => o.includes('Beta'))).toBe(true);
    });
});

describe('SharedTask (WebSocket)', () => {
    let client: ApiClient;

    beforeEach(async () => {
        client = new ApiClient(wsUrl(), { reconnect: false, heartbeatInterval: 0 });
        await client.connect();
    });

    afterEach(() => {
        client.disconnect();
    });

    test('startSharedWork returns taskRef and broadcasts TaskStateEvent', async () => {
        // Set up listener for TaskStateEvent BEFORE starting work
        const stateEvents: SharedTaskState[][] = [];
        const unsubscribe = onTaskStateEvent(client, (event) => {
            stateEvents.push(event.tasks);
        });

        try {
            const ref = await startSharedWork(client, {
                title: 'E2E Shared Work',
                steps: ['Step1', 'Step2'],
                delay: 50,
            });

            expect(ref.taskId).toBeDefined();
            expect(ref.taskId).not.toBe('');

            // Wait for the task to complete and broadcast
            await new Promise(resolve => setTimeout(resolve, 500));

            // Should have received at least one TaskStateEvent
            expect(stateEvents.length).toBeGreaterThanOrEqual(1);

            // At least one event should contain our task
            const hasOurTask = stateEvents.some(tasks =>
                tasks.some(t => t.title === 'E2E Shared Work')
            );
            expect(hasOurTask).toBe(true);
        } finally {
            unsubscribe();
        }
    });

    test('shared task broadcasts to second client', async () => {
        const client2 = new ApiClient(wsUrl(), { reconnect: false, heartbeatInterval: 0 });
        await client2.connect();

        try {
            // Listen on the SECOND client for the shared task
            const received = new Promise<SharedTaskState[]>((resolve) => {
                onTaskStateEvent(client2, (event) => {
                    // Look for our specific task
                    if (event.tasks.some(t => t.title === 'Broadcast Test')) {
                        resolve(event.tasks);
                    }
                });
            });

            // Start shared work from the FIRST client
            const ref = await startSharedWork(client, {
                title: 'Broadcast Test',
                steps: ['A', 'B'],
                delay: 50,
            });

            expect(ref.taskId).toBeDefined();

            // Second client should receive the task state
            const tasks = await received;
            const task = tasks.find(t => t.title === 'Broadcast Test');
            expect(task).toBeDefined();
            expect(task!.id).toBe(ref.taskId);
        } finally {
            client2.disconnect();
        }
    });

    test('shared task sends TaskOutputEvent', async () => {
        const outputs: { taskId: string; output: string }[] = [];
        const unsubscribe = onTaskOutputEvent(client, (event) => {
            outputs.push(event);
        });

        try {
            const ref = await startSharedWork(client, {
                title: 'Output Test',
                steps: ['X', 'Y'],
                delay: 50,
            });

            // Wait for work to complete
            await new Promise(resolve => setTimeout(resolve, 500));

            // Should have received output events for our task
            const taskOutputs = outputs.filter(o => o.taskId === ref.taskId);
            expect(taskOutputs.length).toBeGreaterThanOrEqual(1);
            expect(taskOutputs.some(o => o.output.includes('X'))).toBe(true);
        } finally {
            unsubscribe();
        }
    });

    test('cancelSharedTask cancels a running task', async () => {
        // Start a long-running shared task
        const ref = await startSharedWork(client, {
            title: 'Cancel Test',
            steps: ['Slow1', 'Slow2', 'Slow3', 'Slow4', 'Slow5'],
            delay: 500,
        });

        // Give it a moment to start
        await new Promise(resolve => setTimeout(resolve, 100));

        // Cancel it
        await cancelSharedTask(client, ref.taskId);

        // Wait for the cancelled state to broadcast
        await new Promise(resolve => setTimeout(resolve, 400));

        // The task should eventually be removed (completed/failed tasks are removed after 200ms)
        // Verify by checking that a fresh TaskStateEvent doesn't contain the task
        const finalState = await new Promise<SharedTaskState[]>((resolve) => {
            // The task should be gone by now, but we might get an empty array
            // or no event at all. Let's use a timeout approach.
            let lastState: SharedTaskState[] = [];
            const unsub = onTaskStateEvent(client, (event) => {
                lastState = event.tasks;
            });

            setTimeout(() => {
                unsub();
                resolve(lastState);
            }, 500);
        });

        // The cancelled task should not be in the active tasks anymore
        const cancelledTask = finalState.find(t => t.id === ref.taskId);
        expect(cancelledTask).toBeUndefined();
    });

    test('late joiner receives active shared tasks', async () => {
        // Start a long-running shared task from client 1
        const ref = await startSharedWork(client, {
            title: 'Late Join Test',
            steps: ['Long1', 'Long2', 'Long3'],
            delay: 300,
        });

        // Give the task a moment to start and broadcast
        await new Promise(resolve => setTimeout(resolve, 200));

        // Connect a NEW client (late joiner)
        const lateClient = new ApiClient(wsUrl(), { reconnect: false, heartbeatInterval: 0 });
        await lateClient.connect();

        try {
            // The late joiner should receive a TaskStateEvent with active tasks
            const received = await new Promise<SharedTaskState[]>((resolve, reject) => {
                const timeout = setTimeout(() => reject(new Error('Timeout waiting for task state')), 3000);
                onTaskStateEvent(lateClient, (event) => {
                    if (event.tasks.some(t => t.id === ref.taskId)) {
                        clearTimeout(timeout);
                        resolve(event.tasks);
                    }
                });
            });

            const task = received.find(t => t.id === ref.taskId);
            expect(task).toBeDefined();
            expect(task!.title).toBe('Late Join Test');
        } finally {
            lateClient.disconnect();
        }
    });
});
