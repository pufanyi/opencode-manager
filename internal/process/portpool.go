package process

import (
	"fmt"
	"sync"
)

type PortPool struct {
	mu    sync.Mutex
	start int
	end   int
	used  map[int]bool
}

func NewPortPool(start, end int) *PortPool {
	return &PortPool{
		start: start,
		end:   end,
		used:  make(map[int]bool),
	}
}

func (p *PortPool) Allocate() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for port := p.start; port < p.end; port++ {
		if !p.used[port] {
			p.used[port] = true
			return port, nil
		}
	}
	return 0, fmt.Errorf("no ports available in range %d-%d", p.start, p.end)
}

func (p *PortPool) Release(port int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.used, port)
}

func (p *PortPool) Reserve(port int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.used[port] = true
}
