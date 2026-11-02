package estate

import "sync"

// MaxWorkers caps the goroutines one parallel pass uses (probes, reads, pushes). A pass is sized to its work: a site
// of 1,000 devices is not held to a small fixed pool, and a slow device (not yet up, short timeout) delays only itself.
const MaxWorkers = 256

// PoolSize is the worker count for n items: n, capped at MaxWorkers (at least 1).
func PoolSize(n int) int {
	if n < 1 {
		return 1
	}
	if n > MaxWorkers {
		return MaxWorkers
	}
	return n
}

// Parallel runs f(i) for i in [0, n) on PoolSize(n) workers and waits for all of them.
func Parallel(n int, f func(i int)) {
	if n == 0 {
		return
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < PoolSize(n); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				f(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}
