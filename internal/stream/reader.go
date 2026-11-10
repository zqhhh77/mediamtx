package stream

import (
	"fmt"

	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/ringbuffer"

	"github.com/bluenviron/mediamtx/internal/counterdumper"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/unit"
)

// OnDataFunc is the callback passed to OnData().
type OnDataFunc func(*unit.Unit) error

// Reader is a stream reader.
type Reader struct {
	SkipOutboundBytes bool
	// CloseOnQueueFull preserves inter-frame dependencies by failing instead of dropping frames.
	CloseOnQueueFull bool
	Parent           logger.Writer

	onDatas                 map[*description.Media]map[format.Format]OnDataFunc
	queueSize               int
	buffer                  *ringbuffer.RingBuffer
	outboundFramesDiscarded *counterdumper.Dumper
	strictQueue             chan func() error
	strictStop              chan struct{}
	strictOverflow          chan struct{}

	// out
	err chan error
}

// OnData registers a callback that is called when data from given format is available.
func (r *Reader) OnData(origMedia *description.Media, origFormat format.Format, cb OnDataFunc) {
	if r.onDatas == nil {
		r.onDatas = make(map[*description.Media]map[format.Format]OnDataFunc)
	}
	if r.onDatas[origMedia] == nil {
		r.onDatas[origMedia] = make(map[format.Format]OnDataFunc)
	}
	r.onDatas[origMedia][origFormat] = cb
}

// Formats returns all formats for which the reader has registered a OnData callback.
func (r *Reader) Formats() []format.Format {
	n := 0
	for _, formats := range r.onDatas {
		for range formats {
			n++
		}
	}

	if n == 0 {
		return nil
	}

	out := make([]format.Format, n)
	n = 0

	for _, formats := range r.onDatas {
		for forma := range formats {
			out[n] = forma
			n++
		}
	}

	return out
}

// OutboundFramesDiscarded returns the number of frames discarded because the reader is too slow.
func (r *Reader) OutboundFramesDiscarded() uint64 {
	if r.CloseOnQueueFull {
		return 0
	}
	return r.outboundFramesDiscarded.Get()
}

// error returns whenever there's an error.
// It can be called only after stream.AddReader().
func (r *Reader) Error() chan error {
	return r.err
}

func (r *Reader) start() {
	if r.CloseOnQueueFull {
		r.strictQueue = make(chan func() error, r.queueSize)
		r.strictStop = make(chan struct{})
		r.strictOverflow = make(chan struct{})
		r.err = make(chan error)
		go r.run()
		return
	}
	buffer, _ := ringbuffer.New(uint64(r.queueSize))
	r.buffer = buffer
	r.err = make(chan error)

	r.outboundFramesDiscarded = &counterdumper.Dumper{
		OnReport: func(val uint64) {
			r.Parent.Log(logger.Warn, "reader is too slow, discarding %d %s",
				val,
				func() string {
					if val == 1 {
						return "frame"
					}
					return "frames"
				}())
		},
	}
	r.outboundFramesDiscarded.Start()

	go r.run()
}

func (r *Reader) stop() {
	if r.CloseOnQueueFull {
		close(r.strictStop)
		<-r.err //nolint:errcheck
		return
	}
	r.buffer.Close()
	r.outboundFramesDiscarded.Stop()
	<-r.err //nolint:errcheck
}

func (r *Reader) run() {
	r.err <- r.runInner()
	close(r.err)
}

func (r *Reader) runInner() error {
	if r.CloseOnQueueFull {
		for {
			select {
			case <-r.strictStop:
				return fmt.Errorf("terminated")
			case <-r.strictOverflow:
				return fmt.Errorf("reader queue is full")
			case cb := <-r.strictQueue:
				select {
				case <-r.strictOverflow:
					return fmt.Errorf("reader queue is full")
				default:
				}
				if err := cb(); err != nil {
					return err
				}
			}
		}
	}
	for {
		cb, ok := r.buffer.Pull()
		if !ok {
			return fmt.Errorf("terminated")
		}

		err := cb.(func() error)()
		if err != nil {
			return err
		}
	}
}

func (r *Reader) push(cb func() error) {
	if r.CloseOnQueueFull {
		select {
		case <-r.strictOverflow:
			return
		default:
		}
		select {
		case r.strictQueue <- cb:
		default:
			close(r.strictOverflow)
		}
		return
	}
	ok := r.buffer.Push(cb)
	if !ok {
		r.outboundFramesDiscarded.Increase()
	}
}
