package core

import (
	"context"
	"sync"
)

type Controller struct {
	mutex  sync.Mutex
	paused bool
	resume chan struct{}
}

func NewController() *Controller { return &Controller{} }

func (controller *Controller) Pause() bool {
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if controller.paused {
		return false
	}
	controller.paused = true
	controller.resume = make(chan struct{})
	return true
}

func (controller *Controller) Resume() bool {
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	if !controller.paused {
		return false
	}
	controller.paused = false
	close(controller.resume)
	controller.resume = nil
	return true
}

func (controller *Controller) IsPaused() bool {
	controller.mutex.Lock()
	defer controller.mutex.Unlock()
	return controller.paused
}

func (controller *Controller) Wait(ctx context.Context) error {
	for {
		controller.mutex.Lock()
		paused := controller.paused
		resume := controller.resume
		controller.mutex.Unlock()
		if !paused || resume == nil {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-resume:
		}
	}
}
