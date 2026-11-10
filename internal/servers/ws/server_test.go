package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/bluenviron/mediamtx/internal/auth"
	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/externalcmd"
	"github.com/bluenviron/mediamtx/internal/stream"
	"github.com/bluenviron/mediamtx/internal/test"
)

type testPath struct{ removed atomic.Int32 }

func (*testPath) Name() string                                { return "camera/001" }
func (*testPath) SafeConf() *conf.Path                        { return &conf.Path{} }
func (*testPath) ExternalCmdEnv() externalcmd.Environment     { return externalcmd.Environment{} }
func (*testPath) RemovePublisher(defs.PathRemovePublisherReq) {}
func (p *testPath) RemoveReader(defs.PathRemoveReaderReq)     { p.removed.Add(1) }

func TestHTTPFailures(t *testing.T) {
	for _, ca := range []struct {
		name        string
		status      int
		authError   bool
		unsupported bool
	}{
		{"auth", 401, true, false}, {"unsupported", 400, false, true},
	} {
		t.Run(ca.name, func(t *testing.T) {
			path := &testPath{}
			desc := &description.Session{Medias: []*description.Media{{Type: description.MediaTypeVideo, Formats: []format.Format{&format.VP8{}}}}}
			strm := &stream.Stream{OrigDesc: desc, WriteQueueSize: 4, RTPMaxPayloadSize: 1450, Parent: &test.ServerParent{}}
			require.NoError(t, strm.Initialize())
			defer strm.Close()
			pm := &test.PathManager{AddReaderImpl: func(req defs.PathAddReaderReq) (*defs.PathAddReaderRes, error) {
				require.Equal(t, auth.ProtocolWS, req.AccessRequest.Proto)
				require.Equal(t, "viewer", req.AccessRequest.Credentials.User)
				require.Equal(t, "secret", req.AccessRequest.Credentials.Pass)
				if ca.authError {
					return nil, &auth.Error{AskCredentials: true}
				}
				return &defs.PathAddReaderRes{Path: path, Stream: strm, AuthRes: &auth.Result{}}, nil
			}}
			s := &Server{ctx: context.Background(), PathManager: pm, Parent: &test.ServerParent{}}
			r := httptest.NewRequest(http.MethodGet, "http://localhost/camera/001?user=viewer&pass=secret", nil)
			w := httptest.NewRecorder()
			s.serveHTTP(w, r)
			require.Equal(t, ca.status, w.Code)
			var body struct {
				Code    int
				Message string
				Data    any
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			require.Equal(t, w.Code, body.Code)
			require.NotEmpty(t, body.Message)
			require.Nil(t, body.Data)
			if ca.unsupported {
				require.Equal(t, int32(1), path.removed.Load())
			}
		})
	}
}

func TestStartupTimeoutAndControlCleanup(t *testing.T) {
	for _, invalidControl := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "control"}[invalidControl], func(t *testing.T) {
			path := &testPath{}
			strm := &stream.Stream{OrigDesc: &description.Session{Medias: []*description.Media{test.UniqueMediaH264()}}, WriteQueueSize: 4, RTPMaxPayloadSize: 1450, Parent: &test.ServerParent{}}
			require.NoError(t, strm.Initialize())
			defer strm.Close()
			s := &Server{ctx: context.Background(), ReadTimeout: conf.Duration(100 * time.Millisecond), WriteTimeout: conf.Duration(time.Second), Parent: &test.ServerParent{},
				PathManager: &test.PathManager{AddReaderImpl: func(defs.PathAddReaderReq) (*defs.PathAddReaderRes, error) {
					return &defs.PathAddReaderRes{Path: path, Stream: strm, AuthRes: &auth.Result{}}, nil
				}},
			}
			h := httptest.NewServer(http.HandlerFunc(s.serveHTTP))
			defer h.Close()
			c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(h.URL, "http")+"/camera/001", nil)
			require.NoError(t, err)
			defer c.Close()
			require.NoError(t, c.SetReadDeadline(time.Now().Add(time.Second)))
			if invalidControl {
				require.NoError(t, c.WriteMessage(websocket.BinaryMessage, []byte{1, 2, 3}))
			}
			_, b, err := c.ReadMessage()
			require.NoError(t, err)
			require.Equal(t, []byte{255}, b)
			require.Eventually(t, func() bool { return path.removed.Load() == 1 }, time.Second, time.Millisecond)
		})
	}
}
