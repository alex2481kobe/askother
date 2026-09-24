// Package worker builds the command line for each supported worker CLI and
// decodes the worker's JSONL stdout into a verdict. Adapters are narrow: they
// never create run records, take locks, choose roles or send signals.
//
// This package does not import internal/run, which imports it. Exit here is
// the observed process exit; the run package owns the persisted form.
package worker

// Invocation is everything an adapter needs to build one worker command.
type Invocation struct {
	Binary, Prompt, CWD string
	Model, Effort, Mode string
	ResumeID            string // the native session to continue; empty for a new one
	TempAnswer          string // same-directory temp path for the answer
}

// Command is the process to start. cmd.Dir and Env are set by the run
// package, not the adapter.
type Command struct {
	Path  string
	Args  []string
	Stdin []byte // written exactly, then closed
}

// Exit is the observed exit of the worker process. Code is nil when the
// process was killed by a signal or its status is unknown.
type Exit struct {
	Code   *int
	Signal string
}

// Update is what one stdout line contributed. An empty SessionID means no news.
type Update struct {
	SessionID string
}

// Final is the decoder's verdict after exit and bounded drain. Success
// implies a non-empty SessionID and an AnswerPath.
type Final struct {
	SessionID  string
	Success    bool
	AnswerPath string // temp file holding the answer, or "" if none
	Failure    *Failure
}

// Facts describe a worker: its name, default and accepted modes, and the
// settings Orca always passes, each with its reason inside the string.
type Facts struct {
	Name, DefaultMode string
	Modes             []string
	Pinned            []string
}

// Adapter builds commands for one worker CLI and decodes its output. An
// adapter value holds no per-run state: it is safe to reuse across runs and
// goroutines, and NewDecoder takes the run's Invocation (the one passed to
// Build) for the answer path and the session it must validate.
type Adapter interface {
	Facts() Facts
	Build(Invocation) (Command, error)
	NewDecoder(Invocation) Decoder
}

// Decoder turns a worker's JSONL stdout into updates and a final verdict.
// Unknown event types are ignored; malformed required data is a PROTOCOL
// failure.
type Decoder interface {
	Consume(line []byte) (Update, error) // one complete JSONL line
	Finish(Exit) (Final, error)          // after exit and bounded drain
}
