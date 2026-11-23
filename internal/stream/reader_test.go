package stream

import (
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/require"
)

func TestReaderDropsNewFrameOnQueueFull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &Reader{Parent: nilLogger{}, queueSize: 1}
		r.start()
		defer r.stop()

		resume := make(chan struct{})
		r.push(func() error { <-resume; return nil })
		synctest.Wait()

		var delivered []int
		pushFrame := func(id int) {
			r.push(func() error {
				delivered = append(delivered, id)
				return nil
			})
		}
		pushFrame(1)
		pushFrame(2)
		close(resume)
		synctest.Wait()
		pushFrame(3)
		synctest.Wait()

		require.Equal(t, []int{1, 3}, delivered)
		require.Equal(t, uint64(1), r.OutboundFramesDiscarded())
	})
}
