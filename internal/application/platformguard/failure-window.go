package platformguard

import "time"

const failureBucketCount = 32

type failureBucket struct {
	calls    int64
	failures int64
}

type failureWindow struct {
	buckets     [failureBucketCount]failureBucket
	tick        int64
	calls       int64
	failures    int64
	initialized bool
}

func (w *failureWindow) record(now time.Time, interval time.Duration, failed bool) {
	tick := now.UnixNano() / int64(interval/failureBucketCount)
	if w.initialized && tick < w.tick {
		tick = w.tick
	}
	w.advance(tick)
	bucket := &w.buckets[failureBucketIndex(tick)]
	bucket.calls++
	w.calls++
	if failed {
		bucket.failures++
		w.failures++
	}
}

func (w *failureWindow) advance(tick int64) {
	if !w.initialized || tick-w.tick >= failureBucketCount {
		*w = failureWindow{tick: tick, initialized: true}
		return
	}
	// Не более 31 корзины, независимо от числа вызовов; отдельная история запросов не хранится.
	for next := w.tick + 1; next <= tick; next++ {
		bucket := &w.buckets[failureBucketIndex(next)]
		w.calls -= bucket.calls
		w.failures -= bucket.failures
		*bucket = failureBucket{}
	}
	w.tick = tick
}

func (w *failureWindow) exceeded(minCalls, percent int) bool {
	return w.calls >= int64(minCalls) && w.failures*100 >= w.calls*int64(percent)
}

func failureBucketIndex(tick int64) int64 {
	index := tick % failureBucketCount
	if index < 0 {
		index += failureBucketCount
	}
	return index
}
