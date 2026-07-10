package jobs

import "errors"

var (
	errNotFound       = errors.New("jobs: no such job")
	errAlreadyRunning = errors.New("jobs: job already running")
)

// ErrNotFound / ErrAlreadyRunning are the exported forms handlers map to statuses.
var (
	ErrNotFound       = errNotFound
	ErrAlreadyRunning = errAlreadyRunning
)
