package tasks

import (
	"sync/atomic"

	"github.com/marrasen/aprot"
)

// taskDelivery abstracts how task state updates are delivered to clients.
// requestDelivery sends to a single client; sharedDelivery broadcasts to all.
type taskDelivery interface {
	sendSnapshot(root *taskNode)
	sendOutput(taskID, msg string)
	sendProgress(taskID string, current, total int)
	onComplete(node *taskNode)
	onFail(node *taskNode)
	allocID() string
	isShared() bool
}

// requestDelivery sends task updates to a single requesting client.
type requestDelivery struct {
	sender aprot.RequestSender
	nextID atomic.Int64
	root   *taskNode // lazily created implicit root
}

func newRequestDelivery(sender aprot.RequestSender) *requestDelivery {
	return &requestDelivery{sender: sender}
}

func (d *requestDelivery) sendSnapshot(_ *taskNode) {
	if d.root == nil {
		return
	}
	nodes := d.root.snapshotChildren()
	if nodes != nil {
		d.sender.SendJSON(taskTreeMessage{
			Type:  aprot.TypeProgress,
			ID:    d.sender.RequestID(),
			Tasks: nodes,
		})
	}
}

func (d *requestDelivery) sendOutput(taskID, msg string) {
	d.sender.SendJSON(taskNodeOutputMessage{
		Type:   aprot.TypeProgress,
		ID:     d.sender.RequestID(),
		TaskID: taskID,
		Output: msg,
	})
}

func (d *requestDelivery) sendProgress(taskID string, current, total int) {
	d.sender.SendJSON(taskNodeProgressMessage{
		Type:    aprot.TypeProgress,
		ID:      d.sender.RequestID(),
		TaskID:  taskID,
		Current: current,
		Total:   total,
	})
}

func (d *requestDelivery) onComplete(node *taskNode) {
	node.setStatus(TaskNodeStatusCompleted)
}

func (d *requestDelivery) onFail(node *taskNode) {
	// status and error already set by caller
}

func (d *requestDelivery) allocID() string {
	n := d.nextID.Add(1)
	return "t" + itoa(n)
}

func (d *requestDelivery) isShared() bool { return false }

// sharedDelivery broadcasts task updates to all connected clients.
type sharedDelivery struct {
	manager *taskManager
}

func newSharedDelivery(manager *taskManager) *sharedDelivery {
	return &sharedDelivery{manager: manager}
}

func (d *sharedDelivery) sendSnapshot(_ *taskNode) {
	d.manager.broadcastNow()
}

func (d *sharedDelivery) sendOutput(taskID, msg string) {
	d.manager.sendUpdate(taskID, &msg, nil, nil)
}

func (d *sharedDelivery) sendProgress(taskID string, current, total int) {
	d.manager.sendUpdate(taskID, nil, &current, &total)
}

func (d *sharedDelivery) onComplete(node *taskNode) {
	node.setStatus(TaskNodeStatusCompleted)
}

func (d *sharedDelivery) onFail(node *taskNode) {
	// status and error already set by caller
}

func (d *sharedDelivery) allocID() string {
	return d.manager.allocID()
}

func (d *sharedDelivery) isShared() bool { return true }
