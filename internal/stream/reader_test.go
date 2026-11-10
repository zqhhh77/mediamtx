package stream

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReaderCloseOnQueueFull(t *testing.T) {
	r := &Reader{CloseOnQueueFull: true, queueSize: 1}
	r.start()
	defer r.stop()
	entered, release := make(chan struct{}), make(chan struct{})
	r.push(func() error { close(entered); <-release; return nil })
	<-entered
	r.push(func() error { t.Error("queued frame must not be sent after overflow"); return nil })
	var writers sync.WaitGroup
	for range 8 {
		writers.Go(func() { r.push(func() error { t.Error("overflow frame must not be sent"); return nil }) })
	}
	writers.Wait()
	close(release)
	select {
	case err := <-r.Error():
		require.EqualError(t, err, "reader queue is full")
	case <-time.After(time.Second):
		t.Fatal("overflow did not terminate reader")
	}
	require.Zero(t, r.OutboundFramesDiscarded())
}
