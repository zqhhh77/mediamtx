package core

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"

	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/test"
)

func TestWebSocketRTSPSharedReaders(t *testing.T) {
	for _, codec := range []string{"h264", "h265"} {
		t.Run(codec, func(t *testing.T) {
			config := "rtspTransports: [tcp]\nrtmp: no\nhls: no\nwebrtc: no\nsrt: no\nmoq: no\nws: yes\npaths:\n  camera/001:\n"
			hookFile := filepath.ToSlash(filepath.Join(t.TempDir(), "hooks"))
			if runtime.GOOS != "windows" {
				config += fmt.Sprintf("    runOnRead: sh -c 'echo read:$MTX_READER_TYPE >> %s'\n"+
					"    runOnUnread: sh -c 'echo unread:$MTX_READER_TYPE >> %s'\n", hookFile, hookFile)
			}
			p, ok := newInstance(t, config)
			require.True(t, ok)
			defer p.Close()
			medi := test.UniqueMediaH264()
			key := []byte{0x65, 1}
			codecByte := byte(4)
			if codec == "h265" {
				medi.Formats = []format.Format{test.FormatH265}
				key = []byte{38, 1, 1}
				codecByte = 5
			}
			publisher := &gortsplib.Client{}
			require.NoError(t, publisher.StartRecording("rtsp://127.0.0.1:8554/camera/001", &description.Session{Medias: []*description.Media{medi}}))
			defer publisher.Close()
			stop, done := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(done)
				ticker := time.NewTicker(20 * time.Millisecond)
				defer ticker.Stop()
				var seq uint16
				for {
					select {
					case <-stop:
						return
					case <-ticker.C:
						if err := publisher.WritePacketRTP(medi, &rtp.Packet{
							Version: 2, Marker: true, PayloadType: 96, SequenceNumber: seq,
							Timestamp: uint32(seq) * 1800, SSRC: 123, Payload: key,
						}); err != nil {
							return
						}
						seq++
					}
				}
			}()
			defer func() { close(stop); <-done }()
			connect := func() *websocket.Conn {
				conn, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:8894/camera/001", nil)
				require.NoError(t, err)
				require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
				typ, ci, err := conn.ReadMessage()
				require.NoError(t, err)
				require.Equal(t, websocket.BinaryMessage, typ)
				require.Equal(t, []byte{1, codecByte}, ci[:2])
				_, frame, err := conn.ReadMessage()
				require.NoError(t, err)
				require.Equal(t, byte(2), frame[0])
				require.Equal(t, byte(1), frame[9])
				require.Greater(t, binary.BigEndian.Uint16(frame[10:]), uint16(0))
				return conn
			}
			conns := []*websocket.Conn{connect(), connect(), connect()}
			for _, c := range conns {
				defer c.Close()
			}
			item, err := p.pathManager.APIPathsGet("camera/001")
			require.NoError(t, err)
			require.Len(t, item.Readers, 3)
			for _, reader := range item.Readers {
				require.Equal(t, defs.APIPathReaderTypeWSSession, reader.Type)
			}
			require.NoError(t, conns[0].WriteMessage(websocket.BinaryMessage, []byte{4}))
			for _, c := range conns {
				require.NoError(t, c.Close())
			}
			if codec == "h264" {
				for range 100 {
					c := connect()
					require.NoError(t, c.Close())
				}
			}
			require.Eventually(t, func() bool {
				item, err := p.pathManager.APIPathsGet("camera/001")
				return err == nil && len(item.Readers) == 0
			}, 3*time.Second, 10*time.Millisecond)
			if runtime.GOOS != "windows" {
				require.Eventually(t, func() bool {
					b, _ := os.ReadFile(hookFile)
					return strings.Contains(string(b), "read:wsSession") && strings.Contains(string(b), "unread:wsSession")
				}, 3*time.Second, 10*time.Millisecond)
			}
			c := connect()
			publisher.Close()
			for {
				_, b, err := c.ReadMessage()
				if err != nil {
					t.Fatal("source closed without EOS:", err)
				}
				if b[0] == 255 {
					break
				}
			}
			c.Close()
		})
	}
}

func TestWebSocketHTTPAuthentication(t *testing.T) {
	requests := make(chan map[string]any, 2)
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(400)
			return
		}
		requests <- body
		w.WriteHeader(200)
	}))
	defer authServer.Close()
	p, ok := newInstance(t, "rtsp: no\nrtmp: no\nhls: no\nwebrtc: no\nsrt: no\nmoq: no\nws: yes\nauthMethod: http\nauthHTTPAddress: "+authServer.URL+"\npaths:\n  camera/001:\n")
	require.True(t, ok)
	defer p.Close()
	_, res, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:8894/camera/001?user=viewer&pass=secret", nil)
	require.Error(t, err)
	require.NotNil(t, res)
	require.Equal(t, http.StatusNotFound, res.StatusCode)
	res.Body.Close()
	select {
	case body := <-requests:
		require.Equal(t, "ws", body["protocol"])
		require.Equal(t, "read", body["action"])
		require.Equal(t, "camera/001", body["path"])
		require.Equal(t, "viewer", body["user"])
		require.Equal(t, "secret", body["password"])
	case <-time.After(time.Second):
		t.Fatal("external authentication was not invoked")
	}
}

func TestWebSocketAccess(t *testing.T) {
	p, ok := newInstance(t, "rtsp: no\nrtmp: no\nhls: no\nwebrtc: no\nsrt: no\nmoq: no\nws: yes\npaths:\n  camera/001:\n")
	require.True(t, ok)
	defer p.Close()
	_, res, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:8894/camera/missing", nil)
	require.Error(t, err)
	require.NotNil(t, res)
	require.Equal(t, http.StatusNotFound, res.StatusCode)
	res.Body.Close()
	_, res, err = websocket.DefaultDialer.Dial("ws://127.0.0.1:8894/camera/001", http.Header{"Origin": []string{"https://evil.example"}})
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, res.StatusCode)
	res.Body.Close()
}
