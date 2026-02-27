import { useEffect, useState, useRef } from 'react'
import { ApiClient, ApiClientProvider, ApiError, useApiClient, useConnection } from './api/client'
import { TaskNodeStatus } from './api/tasks-handler'
import type { TaskNodeStatusType } from './api/tasks-handler'
import {
  useListUsers,
  useCreateUserMutation,
  useProcessBatchMutation,
  useUserCreatedEvent,
  useSystemNotificationEvent,
  useGetTaskMutation,
  useStartSharedWorkMutation,
  TaskStatus,
} from './api/handlers'
import type { TaskStatusType } from './api/handlers'
import type { SharedTaskState } from './api/tasks-handler'
import { useSharedTasks, cancelSharedTask } from './api/tasks'

// Initialize client
const client = new ApiClient(`ws://${window.location.host}/ws`)

function ConnectionStatus() {
  const { isConnected } = useConnection()
  return (
    <div className={`status ${isConnected ? 'connected' : 'disconnected'}`}>
      {isConnected ? 'Connected' : 'Disconnected'}
    </div>
  )
}

function CreateUserForm({ onLog }: { onLog: (msg: string, type?: string) => void }) {
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const { mutate, isLoading, error } = useCreateUserMutation()

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!name || !email) {
      onLog('Please enter name and email', 'error')
      return
    }
    try {
      const result = await mutate(name, email)
      onLog(`Created user: ${JSON.stringify(result)}`, 'response')
      setName('')
      setEmail('')
    } catch (err) {
      onLog(`Error: ${(err as Error).message}`, 'error')
    }
  }

  return (
    <div className="card">
      <h2>Create User</h2>
      <form onSubmit={handleSubmit}>
        <div className="form-group">
          <label>Name</label>
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="John Doe"
          />
        </div>
        <div className="form-group">
          <label>Email</label>
          <input
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="john@example.com"
          />
        </div>
        <button type="submit" disabled={isLoading}>
          {isLoading ? 'Creating...' : 'Create User'}
        </button>
        {error && <p style={{ color: 'red' }}>{error.message}</p>}
      </form>
    </div>
  )
}

function UsersList() {
  const { data, isLoading, refetch } = useListUsers()
  const { lastEvent } = useUserCreatedEvent()

  useEffect(() => {
    if (lastEvent) refetch()
  }, [lastEvent, refetch])

  return (
    <div className="card">
      <h2>Users</h2>
      <button onClick={refetch} disabled={isLoading}>
        {isLoading ? 'Loading...' : 'Refresh List'}
      </button>
      <ul className="users-list">
        {!data || data.users.length === 0 ? (
          <li>No users yet</li>
        ) : (
          data.users.map((user) => (
            <li key={user.id}>
              <strong>{user.name}</strong> - {user.email} ({user.id})
            </li>
          ))
        )}
      </ul>
    </div>
  )
}

// Helper to get status label using enum - demonstrates type-safe enum usage
function getStatusLabel(status: TaskStatusType): string {
  switch (status) {
    case TaskStatus.Created:
      return '⏳ Created'
    case TaskStatus.Running:
      return '🔄 Running'
    case TaskStatus.Completed:
      return '✅ Completed'
    case TaskStatus.Failed:
      return '❌ Failed'
    default:
      return status
  }
}

// Helper to get status badge class using enum
function getStatusClass(status: TaskStatusType): string {
  switch (status) {
    case TaskStatus.Created:
      return 'status-created'
    case TaskStatus.Running:
      return 'status-running'
    case TaskStatus.Completed:
      return 'status-completed'
    case TaskStatus.Failed:
      return 'status-failed'
    default:
      return ''
  }
}

function TaskViewer({ onLog }: { onLog: (msg: string, type?: string) => void }) {
  const [taskId, setTaskId] = useState('task_1')
  const { mutate, data: task, isLoading, error } = useGetTaskMutation()

  const handleGetTask = async () => {
    if (!taskId) {
      onLog('Please enter a task ID', 'error')
      return
    }
    try {
      const result = await mutate(taskId)
      onLog(`Got task: ${JSON.stringify(result)}`, 'response')

      // Demonstrate type-safe enum comparison
      if (result.status === TaskStatus.Running) {
        onLog(`Task ${result.id} is currently running`, 'progress')
      } else if (result.status === TaskStatus.Completed) {
        onLog(`Task ${result.id} has completed`, 'response')
      } else if (result.status === TaskStatus.Failed) {
        onLog(`Task ${result.id} has failed`, 'error')
      }
    } catch (err) {
      onLog(`Error: ${(err as Error).message}`, 'error')
    }
  }

  return (
    <div className="card">
      <h2>Get Task (Enum Demo)</h2>
      <p style={{ color: '#666', fontSize: 14, marginBottom: 15 }}>
        Demonstrates using TypeScript enums for type-safe status handling.
      </p>
      <div className="form-group">
        <label>Task ID</label>
        <input
          type="text"
          value={taskId}
          onChange={(e) => setTaskId(e.target.value)}
          placeholder="task_123"
        />
      </div>
      <button onClick={handleGetTask} disabled={isLoading}>
        {isLoading ? 'Loading...' : 'Get Task'}
      </button>
      {error && <p style={{ color: 'red' }}>{error.message}</p>}
      {task && (
        <div style={{ marginTop: 15, padding: 10, background: '#f8f9fa', borderRadius: 4 }}>
          <div><strong>ID:</strong> {task.id}</div>
          <div><strong>Name:</strong> {task.name}</div>
          <div>
            <strong>Status:</strong>{' '}
            <span className={`status-badge ${getStatusClass(task.status)}`}>
              {getStatusLabel(task.status)}
            </span>
          </div>
        </div>
      )}
    </div>
  )
}

function BatchProcessor({ onLog }: { onLog: (msg: string, type?: string) => void }) {
  const [items, setItems] = useState('apple, banana, cherry, date, elderberry')
  const [delay, setDelay] = useState(500)
  const [progress, setProgress] = useState({ current: 0, total: 0, message: 'Ready' })

  const { mutate, isLoading, cancel, reset } = useProcessBatchMutation({
    onProgress: (current, total, message) => {
      setProgress({ current, total, message })
      onLog(`Progress: ${current}/${total} - ${message}`, 'progress')
    },
  })

  const handleProcess = async () => {
    const itemList = items.split(',').map((s) => s.trim()).filter(Boolean)
    if (itemList.length === 0) {
      onLog('Please enter items', 'error')
      return
    }

    setProgress({ current: 0, total: itemList.length, message: 'Starting...' })

    try {
      const result = await mutate(itemList, delay)
      setProgress({ current: result.processed, total: result.processed, message: 'Done!' })
      onLog(`Batch complete: ${JSON.stringify(result)}`, 'response')
    } catch (err) {
      onLog(`Batch error: ${(err as Error).message}`, 'error')
      setProgress((p) => ({ ...p, message: `Error: ${(err as Error).message}` }))
    }
  }

  const handleCancel = () => {
    cancel()
    onLog('Cancellation requested', 'error')
    reset()
  }

  const pct = progress.total > 0 ? (progress.current / progress.total) * 100 : 0

  return (
    <div className="card">
      <h2>Batch Processing (with Progress & Cancellation)</h2>
      <div className="form-group">
        <label>Items (comma-separated)</label>
        <input type="text" value={items} onChange={(e) => setItems(e.target.value)} />
      </div>
      <div className="form-group">
        <label>Delay per item (ms)</label>
        <input type="number" value={delay} onChange={(e) => setDelay(Number(e.target.value))} />
      </div>
      <button onClick={handleProcess} disabled={isLoading}>Process Batch</button>
      <button className="danger" onClick={handleCancel} disabled={!isLoading}>Cancel</button>
      <div className="progress-bar">
        <div className="progress-bar-fill" style={{ width: `${pct}%` }} />
      </div>
      <div className="progress-text">
        {progress.total > 0 ? `${progress.current}/${progress.total}: ${progress.message}` : progress.message}
      </div>
    </div>
  )
}

function getNodeStatusLabel(status: TaskNodeStatusType): string {
  switch (status) {
    case TaskNodeStatus.Created:
      return 'Created'
    case TaskNodeStatus.Running:
      return 'Running'
    case TaskNodeStatus.Completed:
      return 'Completed'
    case TaskNodeStatus.Failed:
      return 'Failed'
    default:
      return status
  }
}

function getNodeStatusClass(status: TaskNodeStatusType): string {
  switch (status) {
    case TaskNodeStatus.Created:
      return 'status-created'
    case TaskNodeStatus.Running:
      return 'status-running'
    case TaskNodeStatus.Completed:
      return 'status-completed'
    case TaskNodeStatus.Failed:
      return 'status-failed'
    default:
      return ''
  }
}

function TaskNodeTree({ task }: { task: SharedTaskState }) {
  const client = useApiClient()

  const handleCancel = () => {
    cancelSharedTask(client, task.id).catch(() => {})
  }

  const isActive = task.status === TaskNodeStatus.Running || task.status === TaskNodeStatus.Created

  return (
    <div style={{ padding: 10, marginBottom: 8, background: '#f8f9fa', borderRadius: 4, border: '1px solid #dee2e6' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div>
          <strong>{task.title}</strong>
          {task.meta?.userName && <span style={{ color: '#888', marginLeft: 8 }}>by {task.meta.userName}</span>}
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <span className={`status-badge ${getNodeStatusClass(task.status)}`}>
            {getNodeStatusLabel(task.status)}
          </span>
          {isActive && task.isOwner && (
            <button className="danger" style={{ padding: '2px 8px', fontSize: 12 }} onClick={handleCancel}>
              Cancel
            </button>
          )}
        </div>
      </div>
      {task.current != null && task.total != null && task.total > 0 && (
        <div style={{ marginTop: 6 }}>
          <div className="progress-bar" style={{ height: 6 }}>
            <div className="progress-bar-fill" style={{ width: `${(task.current / task.total) * 100}%` }} />
          </div>
          <div style={{ fontSize: 12, color: '#666', marginTop: 2 }}>{task.current}/{task.total}</div>
        </div>
      )}
      {task.error && <div style={{ color: 'red', fontSize: 13, marginTop: 4 }}>{task.error}</div>}
      {task.children && task.children.length > 0 && (
        <div style={{ marginTop: 8, marginLeft: 16, borderLeft: '2px solid #dee2e6', paddingLeft: 10 }}>
          {task.children.map((child) => (
            <div key={child.id} style={{ marginBottom: 4, fontSize: 13 }}>
              <span className={`status-badge ${getNodeStatusClass(child.status)}`} style={{ fontSize: 11, marginRight: 6 }}>
                {getNodeStatusLabel(child.status)}
              </span>
              {child.title}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function SharedWorkPanel({ onLog }: { onLog: (msg: string, type?: string) => void }) {
  const [title, setTitle] = useState('Deploy Pipeline')
  const [steps, setSteps] = useState('Build, Test, Lint, Package, Deploy')
  const [delay, setDelay] = useState(800)
  const sharedTasks = useSharedTasks()

  const { mutate, data: result, error, isLoading, cancel, reset } = useStartSharedWorkMutation()

  const handleStart = async () => {
    const stepList = steps.split(',').map((s) => s.trim()).filter(Boolean)
    if (!title || stepList.length === 0) {
      onLog('Please enter a title and steps', 'error')
      return
    }
    reset()
    try {
      const res = await mutate(title, stepList, delay)
      onLog(`Shared task "${title}" completed: ${res.completed}/${res.totalSteps} steps in ${res.totalDuration}ms`, 'response')
    } catch (err) {
      if ((err as Error).message !== 'Request aborted') {
        onLog(`Shared task error: ${(err as Error).message}`, 'error')
      }
    }
  }

  const handleCancel = () => {
    cancel()
    onLog('Shared task cancelled', 'error')
  }

  const isCanceled = error instanceof ApiError && error.isCanceled()

  return (
    <div className="card">
      <h2>Shared Tasks (with SubTasks & Cancellation)</h2>
      <p style={{ color: '#666', fontSize: 14, marginBottom: 15 }}>
        Shared tasks are visible to all connected clients. Each step computes a SHA-256 hash.
      </p>
      <div className="form-group">
        <label>Title</label>
        <input type="text" value={title} onChange={(e) => setTitle(e.target.value)} placeholder="My Task" />
      </div>
      <div className="form-group">
        <label>Steps (comma-separated)</label>
        <input type="text" value={steps} onChange={(e) => setSteps(e.target.value)} />
      </div>
      <div className="form-group">
        <label>Delay per step (ms)</label>
        <input type="number" value={delay} onChange={(e) => setDelay(Number(e.target.value))} />
      </div>
      <button onClick={handleStart} disabled={isLoading}>
        {isLoading ? 'Running...' : 'Start Shared Task'}
      </button>
      <button className="danger" onClick={handleCancel} disabled={!isLoading} style={{ marginLeft: 8 }}>
        Cancel
      </button>

      {isCanceled && (
        <div style={{ marginTop: 12, padding: 10, background: '#fff3cd', borderRadius: 4, border: '1px solid #ffc107' }}>
          Task was canceled.
        </div>
      )}
      {error && !isCanceled && (
        <p style={{ color: 'red', marginTop: 8 }}>{error.message}</p>
      )}

      {result && (
        <div style={{ marginTop: 12, padding: 10, background: '#d4edda', borderRadius: 4, border: '1px solid #28a745' }}>
          <div>
            <strong>Completed:</strong> {result.completed}/{result.totalSteps} steps in {result.totalDuration}ms
            {result.results.some((r) => r.error) && (
              <span style={{ color: '#856404', marginLeft: 8 }}>({result.results.filter((r) => r.error).length} failed)</span>
            )}
          </div>
          <table style={{ width: '100%', marginTop: 8, fontSize: 13, borderCollapse: 'collapse' }}>
            <thead>
              <tr style={{ textAlign: 'left', borderBottom: '1px solid #c3e6cb' }}>
                <th style={{ padding: '4px 8px' }}>Step</th>
                <th style={{ padding: '4px 8px' }}>Duration</th>
                <th style={{ padding: '4px 8px' }}>Result</th>
              </tr>
            </thead>
            <tbody>
              {result.results.map((r, i) => (
                <tr key={i} style={{ borderBottom: '1px solid #e8f5e9', background: r.error ? '#fff3cd' : undefined }}>
                  <td style={{ padding: '4px 8px' }}>{r.step}</td>
                  <td style={{ padding: '4px 8px' }}>{r.duration ? `${r.duration}ms` : '-'}</td>
                  <td style={{ padding: '4px 8px' }}>
                    {r.error
                      ? <span style={{ color: '#dc3545' }}>{r.error}</span>
                      : <span style={{ fontFamily: 'monospace', fontSize: 12 }}>{r.hash}</span>
                    }
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {sharedTasks.length > 0 && (
        <div style={{ marginTop: 16 }}>
          <h3 style={{ marginBottom: 8 }}>Active Shared Tasks</h3>
          {sharedTasks.map((task) => (
            <TaskNodeTree key={task.id} task={task} />
          ))}
        </div>
      )}
    </div>
  )
}

function EventLog({ logs, onClear }: { logs: { message: string; type: string; time: string }[]; onClear: () => void }) {
  const { lastEvent: notification } = useSystemNotificationEvent()
  const logRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight
  }, [logs])

  return (
    <div className="card">
      <h2>Event Log</h2>
      <button onClick={onClear}>Clear Log</button>
      {notification && (
        <div style={{ marginTop: 10, padding: 10, background: '#fff3cd', borderRadius: 4 }}>
          Last notification: [{notification.level}] {notification.message}
        </div>
      )}
      <div className="log" ref={logRef}>
        {logs.map((log, i) => (
          <div key={i} className={`log-entry ${log.type}`}>
            [{log.time}] {log.message}
          </div>
        ))}
      </div>
    </div>
  )
}

function AppContent() {
  const [logs, setLogs] = useState<{ message: string; type: string; time: string }[]>([])
  const { isConnected } = useConnection()
  const { lastEvent: userCreated } = useUserCreatedEvent()

  useEffect(() => {
    if (isConnected) addLog('Connected to server', 'response')
  }, [isConnected])

  useEffect(() => {
    if (userCreated) addLog(`User created: ${userCreated.name} (${userCreated.id})`, 'push')
  }, [userCreated])

  const addLog = (message: string, type = '') => {
    setLogs((prev) => [...prev, { message, type, time: new Date().toLocaleTimeString() }])
  }

  return (
    <>
      <h1>aprot React Example</h1>
      <ConnectionStatus />
      <div className="grid">
        <CreateUserForm onLog={addLog} />
        <UsersList />
      </div>
      <TaskViewer onLog={addLog} />
      <BatchProcessor onLog={addLog} />
      <SharedWorkPanel onLog={addLog} />
      <EventLog logs={logs} onClear={() => setLogs([])} />
    </>
  )
}

export default function App() {
  const [connected, setConnected] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    client.connect().then(() => setConnected(true)).catch((err: Error) => setError(err.message))
  }, [])

  if (error) {
    return (
      <div className="card">
        <h2>Connection Error</h2>
        <p style={{ color: 'red' }}>{error}</p>
        <button onClick={() => window.location.reload()}>Retry</button>
      </div>
    )
  }

  if (!connected) return <div className="card"><h2>Connecting...</h2></div>

  return (
    <ApiClientProvider value={client}>
      <AppContent />
    </ApiClientProvider>
  )
}
