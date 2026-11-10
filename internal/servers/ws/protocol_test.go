package ws

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/stretchr/testify/require"

	"github.com/bluenviron/mediamtx/internal/test"
	"github.com/bluenviron/mediamtx/internal/unit"
)

func TestVideoEncoder(t *testing.T) {
	for _, h265 := range []bool{false, true} {
		t.Run(map[bool]string{false: "H264", true: "H265"}[h265], func(t *testing.T) {
			var f format.Format = test.FormatH264
			key, delta := []byte{0x65, 1}, []byte{0x41, 1}
			payload := func(n []byte) unit.Payload { return unit.PayloadH264{n} }
			if h265 {
				f = test.FormatH265
				key, delta = []byte{38, 1, 1}, []byte{2, 1, 1}
				payload = func(n []byte) unit.Payload { return unit.PayloadH265{n} }
			}
			e := newVideoEncoder(f)
			messages, err := e.encode(&unit.Unit{PTS: 100, Payload: payload(delta)})
			require.NoError(t, err)
			require.Empty(t, messages)
			messages, err = e.encode(&unit.Unit{PTS: 9000000000, Payload: payload(key)})
			require.NoError(t, err)
			require.Len(t, messages, 2)
			ci := messages[0]
			require.Equal(t, []byte{1, e.codec, e.sps[1], e.sps[3]}, ci[:4])
			offset := 4
			for _, p := range func() [][]byte {
				if h265 {
					return [][]byte{e.sps, e.pps, e.vps}
				}
				return [][]byte{e.sps, e.pps}
			}() {
				n := int(binary.BigEndian.Uint16(ci[offset:]))
				offset += 2
				require.Equal(t, p, ci[offset:offset+n])
				offset += n
			}
			require.Len(t, ci, offset)
			require.Equal(t, uint64(1<<32), binary.BigEndian.Uint64(messages[1][1:]))
			require.Equal(t, byte(1), messages[1][9])
			messages, err = e.encode(&unit.Unit{PTS: 9000000000 - 3600, Payload: payload(delta)})
			require.NoError(t, err)
			require.Len(t, messages, 1)
			require.Equal(t, uint64(1<<32)-3600, binary.BigEndian.Uint64(messages[0][1:]))
			_, err = e.encode(&unit.Unit{PTS: 0, Payload: payload(delta)})
			require.ErrorContains(t, err, "PTS reset")
		})
	}
}

func TestVideoEncoderParametersAndCRA(t *testing.T) {
	e := newVideoEncoder(&format.H264{})
	messages, err := e.encode(&unit.Unit{Payload: unit.PayloadH264{{0x65, 1}}})
	require.NoError(t, err)
	require.Empty(t, messages)
	messages, err = e.encode(&unit.Unit{Payload: unit.PayloadH264{test.FormatH264.SPS, test.FormatH264.PPS, {0x65, 1}}})
	require.NoError(t, err)
	require.Len(t, messages, 2)
	changed := bytes.Clone(test.FormatH264.SPS)
	changed[3]++
	_, err = e.encode(&unit.Unit{Payload: unit.PayloadH264{changed, {0x65, 1}}})
	require.ErrorContains(t, err, "parameters changed")

	e = newVideoEncoder(test.FormatH265)
	messages, err = e.encode(&unit.Unit{Payload: unit.PayloadH265{{42, 1, 1}}})
	require.NoError(t, err)
	require.Len(t, messages, 2)
	for _, typ := range []byte{16, 18} { // RASL_N / RASL_R
		messages, err = e.encode(&unit.Unit{Payload: unit.PayloadH265{{typ, 1, 1}}})
		require.NoError(t, err)
		require.Empty(t, messages)
	}
	messages, err = e.encode(&unit.Unit{PTS: 3600, Payload: unit.PayloadH265{{2, 1, 1}}})
	require.NoError(t, err)
	require.Len(t, messages, 1)
}

func TestProtocolBounds(t *testing.T) {
	for _, au := range [][][]byte{nil, {{}}, make([][]byte, math.MaxUint16+1), {make([]byte, maxFrameSize)}} {
		_, err := encodeFrame(0, false, au)
		require.Error(t, err)
	}
	e := newVideoEncoder(test.FormatH264)
	e.sps = make([]byte, math.MaxUint16+1)
	_, err := e.parameters()
	require.Error(t, err)
	_, err = e.encode(&unit.Unit{Payload: unit.PayloadH264{{}}})
	require.Error(t, err)
}
