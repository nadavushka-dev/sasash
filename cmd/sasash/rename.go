package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/nadavushka-dev/sasash/internal/pathswitch"
	"github.com/nadavushka-dev/sasash/internal/sshconf"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// renameHost wires the TUI's R key (SASASH.4.16): the one writer on
// sasash.conf, then the state-cache files that carry the host's observed
// history — a missing one is skipped, a failed move is reported under
// ui.ErrCacheKept (the host IS renamed by then). A generated pinned
// variant is refused up front: it is the switcher's block, and renaming
// it alone would orphan it from its parent — the parent's rename is
// what carries the variants along.
func renameHost(confPath, stateDir string) func(old, new string) (pathswitch.RenameResult, error) {
	return func(old, new string) (pathswitch.RenameResult, error) {
		f, err := sshconf.ParseFile(confPath)
		if err != nil {
			return pathswitch.RenameResult{}, err
		}
		if v := pinnedVariant(f, old); v != nil {
			return pathswitch.RenameResult{}, v
		}
		res, err := pathswitch.Rename(confPath, old, new)
		if err != nil {
			return res, err
		}
		// Every cache is tried: one failing must not leave the next
		// orphaned under the old name; the report names what stayed.
		var kept []error
		for _, cr := range pathswitch.CacheRenames(stateDir, old, new) {
			if err := os.Rename(cr.From, cr.To); err != nil && !errors.Is(err, fs.ErrNotExist) {
				kept = append(kept, err)
			}
		}
		if len(kept) > 0 {
			return res, fmt.Errorf("%w: %v", ui.ErrCacheKept, errors.Join(kept...))
		}
		return res, nil
	}
}
