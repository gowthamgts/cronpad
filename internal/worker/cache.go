package worker

import (
	"container/list"
	"context"
	"fmt"
	"sync"

	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
)

// JobCache holds immutable configuration snapshots keyed by job and version.
// API edits invalidate it after committing; schedule advancement changes no configuration.
type JobCache struct {
	store    store.Store
	capacity int
	mu       sync.Mutex
	items    map[string]*list.Element
	order    *list.List
}
type cacheEntry struct {
	key string
	job sqlitegen.Job
}

// NewJobCache creates a bounded cache shared by the dispatcher and workers.
func NewJobCache(s store.Store, capacity int) *JobCache {
	return &JobCache{s, capacity, sync.Mutex{}, map[string]*list.Element{}, list.New()}
}
func cacheKey(id string, v int64) string { return fmt.Sprintf("%s:%d", id, v) }

// Get returns a cached version or loads the current row for stale-version rejection.
func (c *JobCache) Get(ctx context.Context, id string, version int64) (sqlitegen.Job, error) {
	key := cacheKey(id, version)
	c.mu.Lock()
	if item := c.items[key]; item != nil {
		c.order.MoveToFront(item)
		j := item.Value.(cacheEntry).job
		c.mu.Unlock()
		return j, nil
	}
	c.mu.Unlock()
	j, err := c.store.ControlReader().DispatchJob(ctx, id)
	if err != nil {
		return j, err
	}
	c.Put(j)
	return j, nil
}

// Put warms the cache from a reconciliation page without another SQLite lookup.
func (c *JobCache) Put(j sqlitegen.Job) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := cacheKey(j.ID, j.Version)
	if old := c.items[key]; old != nil {
		old.Value = cacheEntry{key, j}
		c.order.MoveToFront(old)
		return
	}
	c.items[key] = c.order.PushFront(cacheEntry{key, j})
	if c.order.Len() > c.capacity {
		last := c.order.Back()
		delete(c.items, last.Value.(cacheEntry).key)
		c.order.Remove(last)
	}
}

// Invalidate removes all snapshots for a committed job edit or state change.
func (c *JobCache) Invalidate(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, item := range c.items {
		if item.Value.(cacheEntry).job.ID == id {
			delete(c.items, key)
			c.order.Remove(item)
		}
	}
}
