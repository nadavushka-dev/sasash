package tunnel

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Adopt migrates every forward recorded under one of variants — a host's
// generated <host>.<path> path-forcing aliases — into host's own state,
// then removes the variant state files. Tunnels belong to the machine, not
// the path-alias (SASASH.4.23): a forward added while a variant row was
// selected recorded a <host>.<path>.json, and once the variant rows are
// retired that record would be orphaned. Adopt surfaces it under host so
// nothing already added is lost.
//
// Dedup is by spec with host's own record winning (a spec host already
// tracks is not duplicated; the variant's copy is dropped with its file).
// A pid rides along unchanged — the process is the same machine's, alive by
// pid regardless of the alias it was spawned against, so it stays killable.
// A variant with no state file contributes nothing; when none has a file
// Adopt writes nothing at all, so a poll may call it freely. A variant
// alias that is not a plain state-file name is skipped. Errors wrap
// ErrStateWrite: the records the process side already made are real.
func (e Engine) Adopt(host string, variants []string) error {
	var moved []stateForward
	var files []string
	for _, v := range variants {
		if v == host {
			continue
		}
		path, err := e.hostFile(v, ".json")
		if err != nil {
			continue // not a plain file name: never ours to touch
		}
		if _, err := os.Stat(path); err != nil {
			continue // no record under this variant
		}
		st, _ := e.loadState(v)
		moved = append(moved, st.Forwards...)
		files = append(files, path)
	}
	if len(files) == 0 {
		return nil // nothing recorded under any variant: no write
	}
	if err := e.mutateState(host, "adopted variant tunnels", func(st *hostState) {
		have := make(map[string]bool, len(st.Forwards))
		for _, f := range st.Forwards {
			have[f.Spec] = true
		}
		for _, f := range moved {
			if !have[f.Spec] {
				have[f.Spec] = true
				st.Forwards = append(st.Forwards, f)
			}
		}
	}); err != nil {
		return err
	}
	var errs []error
	for _, path := range files {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("variant records adopted but %w: %v", ErrStateWrite, err))
		}
	}
	return errors.Join(errs...)
}
