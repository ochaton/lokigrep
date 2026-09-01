package loki

import (
	"context"
	"time"
)

// Follower polls query_range for new lines. Polling (instead of the websocket
// tail endpoint) keeps the pagination path shared with the historical query and
// still picks up lines that reach Loki late, thanks to the overlap window.
type Follower struct {
	Client   *Client
	Queries  []string
	Interval time.Duration
	Overlap  time.Duration
	Delay    time.Duration // hold lines back this long so output stays ordered
	Emit     EmitFunc
	OnCycle  func() // called after each poll, used to flush output
}

// Run tails from since until the context is cancelled. seen is pre-seeded with
// the keys of the entries the historical query already printed.
func (f *Follower) Run(ctx context.Context, since time.Time, seen map[string]int64) error {
	if seen == nil {
		seen = map[string]int64{}
	}
	interval := f.Interval
	if interval <= 0 {
		interval = time.Second
	}
	overlap := f.Overlap
	if overlap <= 0 {
		overlap = 5 * time.Second
	}

	last := since
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var buffer []Entry
	for {
		select {
		case <-ctx.Done():
			return f.flush(buffer)
		case <-ticker.C:
		}

		now := time.Now()
		start := last.Add(-overlap)
		if !now.After(start) {
			continue
		}

		err := f.Client.Stream(ctx, f.Queries, start, now, 0, func(e Entry) error {
			k := e.Key()
			if _, dup := seen[k]; dup {
				return nil
			}
			seen[k] = e.NS
			buffer = append(buffer, e)
			return nil
		})
		if err != nil {
			if ctx.Err() != nil {
				return f.flush(buffer)
			}
			return err
		}

		// A line can reach Loki after lines that are newer than it, so hold
		// everything back by Delay and emit in timestamp order.
		watermark := now.Add(-f.Delay).UnixNano()
		sortEntries(buffer)
		emitUpTo := 0
		for emitUpTo < len(buffer) && buffer[emitUpTo].NS <= watermark {
			if err := f.Emit(buffer[emitUpTo]); err != nil {
				return err
			}
			emitUpTo++
		}
		buffer = append(buffer[:0], buffer[emitUpTo:]...)

		last = now
		cutoff := now.Add(-2*overlap - f.Delay).UnixNano()
		for k, ns := range seen {
			if ns < cutoff {
				delete(seen, k)
			}
		}
		if f.OnCycle != nil {
			f.OnCycle()
		}
	}
}

// flush emits whatever is still held back, used when the user interrupts.
func (f *Follower) flush(buffer []Entry) error {
	sortEntries(buffer)
	for i := range buffer {
		if err := f.Emit(buffer[i]); err != nil {
			return err
		}
	}
	return nil
}
