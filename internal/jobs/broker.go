package jobs

import "sync"

// Broker wakes subscribers when a job's log grows.
//
// It carries no data: a subscriber is only told that something changed and
// reads the new events from the store. That keeps the store the single source
// of truth, so a late subscriber and a live one follow the same path.
type Broker struct {
	mu          sync.Mutex
	subscribers map[string]map[chan struct{}]struct{}
}

// NewBroker builds an empty broker.
func NewBroker() *Broker {
	return &Broker{subscribers: make(map[string]map[chan struct{}]struct{})}
}

// Subscribe returns a channel that receives a signal whenever the job
// changes, and a function that releases the subscription.
func (b *Broker) Subscribe(jobID string) (<-chan struct{}, func()) {
	signal := make(chan struct{}, 1)

	b.mu.Lock()
	if b.subscribers[jobID] == nil {
		b.subscribers[jobID] = make(map[chan struct{}]struct{})
	}
	b.subscribers[jobID][signal] = struct{}{}
	b.mu.Unlock()

	return signal, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if group, found := b.subscribers[jobID]; found {
			delete(group, signal)
			if len(group) == 0 {
				delete(b.subscribers, jobID)
			}
		}
	}
}

// Notify signals every subscriber of a job. Sends are non-blocking: a
// subscriber that has not drained its previous signal already knows there is
// work waiting, so a slow reader cannot stall the pipeline.
func (b *Broker) Notify(jobID string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for signal := range b.subscribers[jobID] {
		select {
		case signal <- struct{}{}:
		default:
		}
	}
}
