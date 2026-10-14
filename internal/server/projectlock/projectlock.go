// Package projectlock serializes service cutover and retirement within the
// single Cobalt daemon. Deployment dispatch and scheduled reconciliation run
// independently; an active-deployment snapshot alone cannot exclude a cutover.
package projectlock

import "sync"

var projects sync.Map

func mutex(projectID int64) *sync.Mutex {
	value, _ := projects.LoadOrStore(projectID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

// Lock owns service mutations for a project's cutover, including rollback.
func Lock(projectID int64) func() {
	m := mutex(projectID)
	m.Lock()
	return m.Unlock
}

// TryLock lets a reconcile tick skip a project being changed by a deploy,
// without delaying reconciliation of the rest of the fleet.
func TryLock(projectID int64) (func(), bool) {
	m := mutex(projectID)
	if !m.TryLock() {
		return nil, false
	}
	return m.Unlock, true
}
