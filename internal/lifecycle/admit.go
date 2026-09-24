package lifecycle

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/alex2481kobe/orca/internal/config"
	"github.com/alex2481kobe/orca/internal/run"
)

// ReadyTimeout bounds the launcher's wait for the supervisor's readiness
// byte.
const ReadyTimeout = 10 * time.Second

// readyTimeout is ReadyTimeout; tests shorten it.
var readyTimeout = ReadyTimeout

// admission is one normalized request, keyed for dedupe.
type admission struct {
	key, hash string
	// prepare runs under the index lock once dedupe found no run for key,
	// with every stored record. It checks what may have changed since the
	// request was made and returns the record to reserve: its request, and
	// for a continuation its previous_id and native session.
	prepare func(runs []*run.Run) (*run.Run, error)
}

// admit is the launcher, shared by Start and Send. Under the index lock it
// looks the key up first, so a retry recovers its run whatever else has
// changed; then it prepares the record, resolves the worker binary from the
// config, reserves the run lock and the starting record, and starts the
// supervisor. Outside the lock it waits for readiness and returns the run.
func admit(d Deps, c Caller, ad admission) (*run.Run, bool, error) {
	var (
		out      *run.Run
		reused   bool
		readyR   *os.File
		detached *exec.Cmd
	)
	err := d.Store.WithIndexLock(func() error {
		runs, bad, err := d.Store.List()
		if err != nil {
			return err
		}
		if len(bad) > 0 {
			// An unreadable record may hold this key or a native session in
			// use, so admitting anything could duplicate a paid run.
			ids := slices.Sorted(maps.Keys(bad))
			return run.Errorf(run.CodeCorruptState, "unreadable run records %s; repair or move them out of the runs directory", strings.Join(ids, ", "))
		}
		for _, r := range runs {
			if r.Key != ad.key {
				continue
			}
			if r.RequestSHA256 != ad.hash {
				return run.Errorf(run.CodeKeyConflict, "key %q was used for a different request (run %s); use a new key", ad.key, r.ID)
			}
			out, reused = view(d, r), true
			return nil
		}
		r, err := ad.prepare(runs)
		if err != nil {
			return err
		}
		if r.Runtime.Binary, err = resolveBinary(d, r.Request.Worker); err != nil {
			return err
		}
		lock, err := reserve(d, c, ad, r)
		if err != nil {
			return err
		}
		out = r
		readyR, detached, err = handOff(d, r, lock)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	if reused {
		return out, true, nil
	}
	defer readyR.Close()
	run.Reap(detached)
	if _, err := run.WaitReady(readyR, readyTimeout); err != nil {
		return nil, false, run.Errorf(run.CodeStoreIO, "read readiness of run %s: %v", out.ID, err)
	}
	// Ready: the running record. EOF or timeout: whatever the record says.
	r, err := d.Store.Read(out.ID)
	if err != nil {
		return nil, false, err
	}
	return view(d, r), false, nil
}

// reserve takes the run lock before the record exists, then writes the
// starting record r, filled in from the admission and the caller.
func reserve(d Deps, c Caller, ad admission, r *run.Run) (*os.File, error) {
	id, err := run.NewID()
	if err != nil {
		return nil, err
	}
	lock, err := d.Store.CreateRunLock(id)
	if err != nil {
		return nil, err
	}
	r.SchemaVersion, r.ID, r.Key, r.RequestSHA256 = run.SchemaVersion, id, ad.key, ad.hash
	r.CallerID, r.CallerSource = c.ID, c.Source
	if parent := d.Env["ORCA_RUN_ID"]; parent != "" {
		if _, err := d.Store.Read(parent); err == nil {
			r.ParentID = &parent
		}
	}
	r.State, r.Execution, r.CreatedAt = run.StateStarting, run.ExecNotStarted, d.now()
	if err := d.Store.Put(r); err != nil {
		lock.Close()
		if run.Visible(err) {
			// Readers already see the record; with its lock free it reads
			// interrupted/not_started. The lock file stays: never recreated.
			return nil, run.Errorf(run.CodeStoreIO, "run %s: starting record not durable: %v; retry with a new key", id, err)
		}
		_ = d.Store.Remove(id) // nobody saw the id; drop the lock and temps
		return nil, run.Errorf(run.CodeStoreIO, "run %s: write starting record: %v", id, err)
	}
	return lock, nil
}

// handOff starts the supervisor with the run lock and the readiness pipe.
// The lock is only closed, never unlocked: the supervisor shares its open
// file description. If the supervisor cannot start, the lock is free and
// readers see the run interrupted/not_started.
func handOff(d Deps, r *run.Run, lock *os.File) (*os.File, *exec.Cmd, error) {
	readyR, readyW, err := run.NewReadiness()
	if err != nil {
		lock.Close()
		return nil, nil, run.Errorf(run.CodeStoreIO, "run %s: readiness pipe: %v", r.ID, err)
	}
	cmd, err := run.StartDetached(d.Self, []string{"supervise", r.ID}, []*os.File{lock, readyW})
	lock.Close()
	readyW.Close()
	if err != nil {
		readyR.Close()
		return nil, nil, fmt.Errorf("run %s: start supervisor: %w", r.ID, err)
	}
	return readyR, cmd, nil
}

// resolveBinary reads the config and finds the worker's binary. A broken
// config or a missing binary refuses the run.
func resolveBinary(d Deps, name string) (string, error) {
	var cfg config.Config
	if d.ConfigPath != "" {
		var err error
		if cfg, err = config.Load(d.ConfigPath); err != nil {
			return "", run.Errorf(run.CodeInvalidInput, "%v", err)
		}
	}
	var configured string
	switch name {
	case "codex":
		configured = cfg.Workers.Codex.Binary
	case "claude":
		configured = cfg.Workers.Claude.Binary
	}
	binary, err := config.Resolve(name, configured, d.Env)
	if err != nil {
		return "", run.Errorf(run.CodeInvalidInput, "worker %s is not available: %v", name, err)
	}
	return binary, nil
}
