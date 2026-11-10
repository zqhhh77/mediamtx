package ws

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"

	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"

	"github.com/bluenviron/mediamtx/internal/unit"
)

const maxFrameSize = 32 * 1024 * 1024

// videoEncoder owns parameter copies; units queued by stream.Reader remain immutable.
type videoEncoder struct {
	codec         byte
	vps, sps, pps []byte
	codecInfo     []byte
	started       bool
	skipLeading   bool
	firstPTS      int64
	maxPTS        int64
}

func newVideoEncoder(forma format.Format) *videoEncoder {
	e := &videoEncoder{}
	switch f := forma.(type) {
	case *format.H264:
		e.codec = 4
		e.sps, e.pps = f.SafeParams()
	case *format.H265:
		e.codec = 5
		e.vps, e.sps, e.pps = f.SafeParams()
	}
	e.vps = bytes.Clone(e.vps)
	e.sps = bytes.Clone(e.sps)
	e.pps = bytes.Clone(e.pps)
	return e
}

func (e *videoEncoder) parameters() ([]byte, error) {
	if len(e.sps) < 4 || len(e.pps) == 0 || (e.codec == 5 && len(e.vps) == 0) {
		return nil, nil
	}
	// These bytes deliberately match MiBeeNvr, including its H.265 profile/level semantics.
	b := []byte{1, e.codec, e.sps[1], e.sps[3]}
	params := [][]byte{e.sps, e.pps}
	if e.codec == 5 {
		params = append(params, e.vps)
	}
	for _, p := range params {
		if len(p) > math.MaxUint16 {
			return nil, fmt.Errorf("parameter set exceeds protocol limit")
		}
		b = binary.BigEndian.AppendUint16(b, uint16(len(p)))
		b = append(b, p...)
	}
	return b, nil
}

func encodeFrame(pts uint64, key bool, au [][]byte) ([]byte, error) {
	if len(au) == 0 || len(au) > math.MaxUint16 {
		return nil, fmt.Errorf("invalid NALU count")
	}
	size := 12
	for _, nalu := range au {
		if len(nalu) == 0 || len(nalu) > maxFrameSize-4 || size > maxFrameSize-4-len(nalu) {
			return nil, fmt.Errorf("invalid or oversized access unit")
		}
		size += 4 + len(nalu)
	}
	b := make([]byte, 0, size)
	b = append(b, 2)
	b = binary.BigEndian.AppendUint64(b, pts)
	if key {
		b = append(b, 1)
	} else {
		b = append(b, 0)
	}
	b = binary.BigEndian.AppendUint16(b, uint16(len(au)))
	for _, nalu := range au {
		b = binary.BigEndian.AppendUint32(b, uint32(len(nalu)))
		b = append(b, nalu...)
	}
	return b, nil
}

func (e *videoEncoder) encode(u *unit.Unit) ([][]byte, error) {
	var au [][]byte
	if e.codec == 4 {
		au, _ = u.Payload.(unit.PayloadH264)
	} else {
		au, _ = u.Payload.(unit.PayloadH265)
	}
	if len(au) == 0 {
		return nil, nil
	}
	leading, cra, vcl := false, false, false
	for _, nalu := range au {
		if len(nalu) == 0 || (e.codec == 5 && len(nalu) < 2) {
			return nil, fmt.Errorf("invalid NALU")
		}
		if e.codec == 4 {
			switch nalu[0] & 31 {
			case 7:
				e.sps = bytes.Clone(nalu)
			case 8:
				e.pps = bytes.Clone(nalu)
			case 1, 2, 3, 4, 5:
				vcl = true
			}
		} else {
			typ := (nalu[0] >> 1) & 63
			switch typ {
			case 32:
				e.vps = bytes.Clone(nalu)
			case 33:
				e.sps = bytes.Clone(nalu)
			case 34:
				e.pps = bytes.Clone(nalu)
			case 21:
				cra = true
			}
			vcl = vcl || typ <= 31
			leading = leading || (typ >= 6 && typ <= 9)
		}
	}
	ci, err := e.parameters()
	if err != nil {
		return nil, err
	}
	if e.started && !bytes.Equal(ci, e.codecInfo) {
		return nil, fmt.Errorf("video parameters changed; reconnect required")
	}
	if !vcl || ci == nil {
		return nil, nil
	}
	key := (e.codec == 4 && h264.IsRandomAccess(au)) || (e.codec == 5 && h265.IsRandomAccess(au))
	var messages [][]byte
	if !e.started {
		if !key {
			return nil, nil
		}
		e.started = true
		e.firstPTS, e.maxPTS = u.PTS, u.PTS
		e.codecInfo = ci
		e.skipLeading = cra
		messages = append(messages, ci)
	} else if e.skipLeading && leading {
		return nil, nil
	} else if !leading {
		e.skipLeading = false
	}
	// Reserve time before the first keyframe for reordered B pictures without altering PTS differences.
	const origin = uint64(1 << 32)
	var pts uint64
	if u.PTS >= e.firstPTS {
		delta := uint64(u.PTS) - uint64(e.firstPTS)
		if delta > math.MaxInt64-origin {
			return nil, fmt.Errorf("PTS overflow")
		}
		pts = origin + delta
	} else {
		delta := uint64(e.firstPTS) - uint64(u.PTS)
		if delta > origin {
			return nil, fmt.Errorf("PTS reset")
		}
		pts = origin - delta
	}
	if u.PTS < e.maxPTS && uint64(e.maxPTS)-uint64(u.PTS) > 900000 {
		return nil, fmt.Errorf("PTS reset")
	}
	if u.PTS > e.maxPTS {
		e.maxPTS = u.PTS
	}
	frame, err := encodeFrame(pts, key, au)
	if err != nil {
		return nil, err
	}
	return append(messages, frame), nil
}
