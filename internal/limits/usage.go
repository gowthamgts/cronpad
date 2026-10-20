package limits

import (
	"context"
	"fmt"
)

func (r *Registry) seedLocked(ctx context.Context) error {
	month := r.now().UTC().Format("2006-01")
	if r.usageMonth == month && r.usage != nil {
		return nil
	}
	usage := map[string]int64{}
	if r.Store != nil {
		rows, e := r.Store.HistoryReader().MonthlyUsage(ctx, month)
		if e != nil {
			return fmt.Errorf("seed monthly usage: %w", e)
		}
		for _, row := range rows {
			usage[row.WorkspaceID] = row.Runs
		}
	}
	r.usageMonth = month
	r.usage = usage
	r.reservations = map[string]string{}
	return nil
}

// Seed loads the current month once at startup. Month rollover reloads lazily.
func (r *Registry) Seed(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seedLocked(ctx)
}

// Usage is O(1) after seeding, and includes pending first admissions.
func (r *Registry) Usage(ctx context.Context, ws string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.seedLocked(ctx); e != nil {
		return 0, e
	}
	return r.usage[ws], nil
}

// Reserve atomically admits a first occurrence, including pending batch writes.
// Existing durable runs must bypass this method: retries never consume a slot.
func (r *Registry) Reserve(ctx context.Context, ws, key string) error {
	_, err := r.ReserveNew(ctx, ws, key)
	return err
}

// ReserveNew also reports ownership, so a deferred claim releases only its own reservation.
func (r *Registry) ReserveNew(ctx context.Context, ws, key string) (bool, error) {
	s, e := r.For(ctx, ws)
	if e != nil {
		return false, e
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e = r.seedLocked(ctx); e != nil {
		return false, e
	}
	if _, ok := r.reservations[key]; ok {
		return false, nil
	}
	if s.MaxRunsPerMonth > 0 && s.OverQuota != Allow && r.usage[ws] >= int64(s.MaxRunsPerMonth) {
		return false, &Error{"max_runs_per_month_per_workspace", int64(s.MaxRunsPerMonth)}
	}
	r.reservations[key] = ws
	r.usage[ws]++
	return true, nil
}

// Release returns a reservation for an occurrence which never fired.
func (r *Registry) Release(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ws, ok := r.reservations[key]; ok {
		r.usage[ws]--
		delete(r.reservations, key)
	}
}

// Forget drops a committed reservation identity without changing monthly usage.
func (r *Registry) Forget(key string) { r.mu.Lock(); delete(r.reservations, key); r.mu.Unlock() }
