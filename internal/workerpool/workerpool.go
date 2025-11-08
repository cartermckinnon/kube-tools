package workerpool

import "sync"

type WorkerPool interface {
	Submit(task func() error) WorkerPool
	Start() WorkerPool
	Stop() []error
}

func New(concurrency int) WorkerPool {
	return &workerPool{
		tasks:       make(chan func() error),
		errors:      make([]error, 0),
		concurrency: concurrency,
		workers:     sync.WaitGroup{},
	}
}

type workerPool struct {
	tasks       chan func() error
	errors      []error
	errorsLock  sync.Mutex
	concurrency int
	workers     sync.WaitGroup
}

func (wp *workerPool) Submit(task func() error) WorkerPool {
	wp.tasks <- task
	return wp
}

func (wp *workerPool) Start() WorkerPool {
	for range wp.concurrency {
		wp.workers.Add(1)
		go func() {
			defer wp.workers.Done()
			for task := range wp.tasks {
				if err := task(); err != nil {
					wp.errorsLock.Lock()
					wp.errors = append(wp.errors, err)
					wp.errorsLock.Unlock()
				}
			}
		}()
	}
	return wp
}

func (wp *workerPool) Stop() []error {
	close(wp.tasks)
	wp.workers.Wait()
	return wp.errors
}
