package main

import "errors"

// errNewSessionRequested is returned by runWith when the human used /new: the
// current session was closed and preserved, and run starts a fresh one in this
// same process.
var errNewSessionRequested = errors.New("duo new session requested")

// freshSession is the configuration of the session /new starts. It is the
// launch configuration with everything that pointed at an existing session
// cleared — a `duo resume <id>` launch must not reopen the session /new just
// preserved — and a new session id; every other setting carries over.
func (c config) freshSession() config {
	c.resume = false
	c.resumeSession = ""
	c.session = defaultSession()
	return c
}
