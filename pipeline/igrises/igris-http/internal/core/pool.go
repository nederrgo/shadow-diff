package core

import "sync"

// Pool runs multicast jobs on a fixed number of workers.
type Pool struct {
	jobs    chan func()
	wg      sync.WaitGroup
	stopOnce sync.Once
}

// NewPool starts n worker goroutines.
func NewPool(n int) *Pool {
	if n < 1 {
		n = 1
	}
	p := &Pool{jobs: make(chan func(), n*4)}
	p.wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer p.wg.Done()
			for job := range p.jobs {
				job()
			}
		}()
	}
	return p
}

// Submit queues a job for execution.
func (p *Pool) Submit(job func()) {
	p.jobs <- job
}

// Stop closes the job channel and waits for workers to finish.
func (p *Pool) Stop() {
	p.stopOnce.Do(func() {
		close(p.jobs)
		p.wg.Wait()
	})
}
