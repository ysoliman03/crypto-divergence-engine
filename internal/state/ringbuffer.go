package state

import "time"

// Entry is a single timestamped value stored in a RingBuffer.
type Entry struct {
	At    time.Time
	Value float64
}

// RingBuffer is a fixed-capacity circular buffer of timestamped float64 values.
// When full, the oldest entry is overwritten. Not goroutine-safe — callers must
// synchronize (e.g. via ShardedMap.Use).
type RingBuffer struct {
	entries []Entry
	head    int // index of the next write slot
	count   int // number of valid entries (≤ cap)
}

func NewRingBuffer(capacity int) *RingBuffer {
	return &RingBuffer{entries: make([]Entry, capacity)}
}

func (r *RingBuffer) Push(at time.Time, value float64) {
	r.entries[r.head] = Entry{At: at, Value: value}
	r.head = (r.head + 1) % len(r.entries)
	if r.count < len(r.entries) {
		r.count++
	}
}

// SumSince returns the sum of all entries with At > now-window.
// Entries are stored oldest-to-newest, so iteration stops being useful
// once we pass the cutoff — but linear scan is fine at current volumes.
func (r *RingBuffer) SumSince(now time.Time, window time.Duration) float64 {
	cutoff := now.Add(-window)
	start := r.head - r.count
	if start < 0 {
		start += len(r.entries)
	}
	var sum float64
	for i := 0; i < r.count; i++ {
		e := r.entries[(start+i)%len(r.entries)]
		if e.At.After(cutoff) {
			sum += e.Value
		}
	}
	return sum
}

func (r *RingBuffer) Len() int { return r.count }

// Slice returns the values of the last n entries, oldest first.
// If n exceeds the number of valid entries, all entries are returned.
func (r *RingBuffer) Slice(n int) []float64 {
	if n > r.count {
		n = r.count
	}
	if n == 0 {
		return nil
	}
	start := r.head - n
	if start < 0 {
		start += len(r.entries)
	}
	out := make([]float64, n)
	for i := range n {
		out[i] = r.entries[(start+i)%len(r.entries)].Value
	}
	return out
}
