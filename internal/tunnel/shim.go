package tunnel

import (
	"context"
	"errors"
	"fmt"
)

// TEMPORARY bridge (SASASH.7, CONTRACT-tunnel amendment M1): the Model A
// engine surface the TUI and the CLI still call — Start, Forward, Cancel,
// Up, DownSet — kept on the RUNNER with the Model A argv so those callers
// and their tests (which build a real Engine over a fake runner and no
// spawner) keep behaving exactly as on main and can never reach a real
// process. The Model B engine lives beside it under Spawn / Kill / KillAll
// / SpawnSet / KillSet. This file is deleted, and its callers renamed, in
// the consumer rename pass once 2.11 and 4.16 are merged. Do not add
// callers. Not part of the contract.

// Start launches the Model A master: `ssh -fN <host>`, recorded as
// sasash-started in the stats store.
func (e Engine) Start(ctx context.Context, host string) error {
	if err := e.exec(ctx, "ssh", "-fN", host); err != nil {
		return err
	}
	if e.Stats != nil {
		if err := e.Stats.RecordMasterStart(host); err != nil {
			return fmt.Errorf("master started but %w: %v", ErrStateWrite, err)
		}
	}
	return nil
}

// Forward is the Model A live inject: `ssh -O forward -L <spec> <host>`,
// recorded without a pid.
func (e Engine) Forward(ctx context.Context, host, set, spec string) error {
	if err := e.exec(ctx, "ssh", "-O", "forward", "-L", spec, host); err != nil {
		return err
	}
	return e.record(host, set, spec, 0)
}

// Cancel is the Model A live cancel: `ssh -O cancel -L <spec> <host>`,
// then Kill's record rule.
func (e Engine) Cancel(ctx context.Context, host, spec string) error {
	err := e.exec(ctx, "ssh", "-O", "cancel", "-L", spec, host)
	if serr := e.mutateState(host, "cancel attempted", func(st *hostState) {
		st.retire(spec)
	}); serr != nil && err == nil {
		err = serr
	}
	return err
}

// Up is the Model A composition: ensure the master, then Forward each
// spec; a state-write failure never aborts (B1).
func (e Engine) Up(ctx context.Context, host, set string, specs []string) error {
	alive, err := e.CheckMaster(ctx, host)
	if err != nil {
		return err
	}
	var stateErrs []error
	if !alive {
		if err := e.Start(ctx, host); err != nil {
			if !errors.Is(err, ErrStateWrite) {
				return err
			}
			stateErrs = append(stateErrs, err)
		}
	}
	for _, spec := range specs {
		if err := e.Forward(ctx, host, set, spec); err != nil {
			if errors.Is(err, ErrStateWrite) {
				stateErrs = append(stateErrs, err)
				continue
			}
			return err
		}
	}
	return errors.Join(stateErrs...)
}

// DownSet is the Model A composition: cancel the set's specs (given ∪
// recorded), joining errors; a named set's records are cleared, the
// ad-hoc set's are kept (C1).
func (e Engine) DownSet(ctx context.Context, host, set string, specs []string) error {
	st, _ := e.loadState(host)
	seen := make(map[string]bool)
	all := make([]string, 0, len(specs))
	for _, s := range specs {
		if !seen[s] {
			seen[s] = true
			all = append(all, s)
		}
	}
	for _, f := range st.Forwards {
		if f.Set == set && !seen[f.Spec] {
			seen[f.Spec] = true
			all = append(all, f.Spec)
		}
	}
	var errs []error
	for _, spec := range all {
		if err := e.exec(ctx, "ssh", "-O", "cancel", "-L", spec, host); err != nil {
			errs = append(errs, err)
		}
	}
	if set == "" {
		return errors.Join(errs...)
	}
	if serr := e.mutateState(host, "set cancelled", func(st *hostState) {
		st.remove(func(f stateForward) bool { return f.Set == set })
	}); serr != nil {
		errs = append(errs, serr)
	}
	return errors.Join(errs...)
}
