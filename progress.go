package aprot

// ProgressReporter allows handlers to report progress during long operations.
type ProgressReporter interface {
	// Update sends a progress update to the client.
	Update(current, total int, message string)
}

// noopProgress is a no-op implementation of ProgressReporter.
type noopProgress struct{}

func (p *noopProgress) Update(current, total int, message string) {}

// progressReporter sends progress updates for a specific request.
type progressReporter struct {
	conn      *Conn
	requestID string
}

func newProgressReporter(conn *Conn, requestID string) *progressReporter {
	return &progressReporter{
		conn:      conn,
		requestID: requestID,
	}
}

func (p *progressReporter) Update(current, total int, message string) {
	p.conn.sendProgress(p.requestID, current, total, message)
}

func (p *progressReporter) updateTasks(tasks []*TaskNode) {
	msg := ProgressMessage{Type: TypeProgress, ID: p.requestID, Tasks: tasks}
	p.conn.sendJSON(msg)
}

// sendNodeUpdate sends a targeted per-node update (output and/or progress)
// for a specific task node within this request.
func (p *progressReporter) sendNodeUpdate(taskID string, output *string, current, total *int) {
	msg := ProgressMessage{
		Type:    TypeProgress,
		ID:      p.requestID,
		TaskID:  taskID,
		Output:  output,
		Current: current,
		Total:   total,
	}
	p.conn.sendJSON(msg)
}
