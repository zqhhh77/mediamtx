// Package ws implements MiBeeNvr-compatible encoded video output over WebSocket.
package ws

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/bluenviron/mediamtx/internal/auth"
	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/externalcmd"
	"github.com/bluenviron/mediamtx/internal/hooks"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/protocols/httpp"
	"github.com/bluenviron/mediamtx/internal/stream"
	"github.com/bluenviron/mediamtx/internal/unit"
)

type serverPathManager interface {
	AddReader(defs.PathAddReaderReq) (*defs.PathAddReaderRes, error)
}

type serverParent interface {
	logger.Writer
	OnFatalError(context.Context, error)
}

// Server is an encoded video WebSocket server. TLS is terminated by a reverse proxy.
type Server struct {
	Address         string
	AllowOrigins    []string
	ReadTimeout     conf.Duration
	WriteTimeout    conf.Duration
	ExternalCmdPool *externalcmd.Pool
	PathManager     serverPathManager
	Parent          serverParent

	ctx    context.Context
	cancel context.CancelFunc
	inner  *httpp.Server
}

// Initialize initializes the server.
func (s *Server) Initialize() error {
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.inner = &httpp.Server{
		Address:       s.Address,
		AllowOrigins:  s.AllowOrigins,
		ReadTimeout:   time.Duration(s.ReadTimeout),
		WriteTimeout:  time.Duration(s.WriteTimeout),
		Handler:       http.HandlerFunc(s.serveHTTP),
		Parent:        s,
		OnListenError: s.Parent.OnFatalError,
	}
	if err := s.inner.Initialize(); err != nil {
		s.cancel()
		return err
	}
	s.Log(logger.Info, "listener opened on %s (HTTP/WebSocket)", s.Address)
	return nil
}

// Close closes the listener and all readers, including upgraded connections.
func (s *Server) Close() {
	s.cancel()
	s.inner.Close()
	s.Log(logger.Info, "listener closed")
}

// Log implements logger.Writer.
func (s *Server) Log(level logger.Level, format string, args ...any) {
	s.Parent.Log(level, "[WS] "+format, args...)
}

func writeHTTPError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    any    `json:"data"`
	}{status, message, nil}) //nolint:errcheck
}

// Adapt the existing HTTP middleware's Unwrap chain to Gorilla's Hijacker interface.
type upgradeWriter struct{ http.ResponseWriter }

func (w upgradeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

type session struct {
	server *Server
	id     uuid.UUID
	ctx    context.Context
	cancel context.CancelFunc
}

// Close implements defs.Reader.
func (s *session) Close() { s.cancel() }

// Log implements logger.Writer.
func (s *session) Log(level logger.Level, format string, args ...any) {
	s.server.Log(level, "[session %s] "+format, append([]any{s.id.String()[:8]}, args...)...)
}

// APIReaderDescribe implements defs.Reader.
func (s *session) APIReaderDescribe() *defs.APIPathReader {
	return &defs.APIPathReader{Type: defs.APIPathReaderTypeWSSession, ID: s.id.String()}
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeHTTPError(w, http.StatusMethodNotAllowed, "仅支持 GET 请求")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if err := conf.IsValidPathName(name); err != nil {
		writeHTTPError(w, http.StatusBadRequest, "流路径不合法")
		return
	}
	upgrader := websocket.Upgrader{
		HandshakeTimeout: time.Duration(s.WriteTimeout),
		Error: func(w http.ResponseWriter, _ *http.Request, status int, _ error) {
			writeHTTPError(w, status, "WebSocket 握手失败或来源未获允许")
		},
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true
			}
			u, err := url.Parse(origin)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return false
			}
			return strings.EqualFold(u.Host, r.Host) || httpp.IsOriginAllowed(origin, s.AllowOrigins)
		},
	}
	if !upgrader.CheckOrigin(r) {
		writeHTTPError(w, http.StatusForbidden, "请求来源未获允许")
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	sx := &session{server: s, id: uuid.New(), ctx: ctx, cancel: cancel}
	creds := &auth.Credentials{}
	creds.FromHTTP(r)
	q := r.URL.Query()
	if creds.User == "" && creds.Pass == "" && creds.Token == "" {
		creds.User, creds.Pass, creds.Token = q.Get("user"), q.Get("pass"), q.Get("token")
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	res, err := s.PathManager.AddReader(defs.PathAddReaderReq{
		Author: sx,
		AccessRequest: defs.PathAccessRequest{
			Name: name, Query: r.URL.RawQuery, Proto: auth.ProtocolWS,
			ID: &sx.id, Credentials: creds, IP: net.ParseIP(ip),
			UserAgent: r.UserAgent(), EnableAskCredentials: true,
		},
	})
	if err != nil {
		var authErr *auth.Error
		var noStream *defs.PathNoStreamAvailableError
		var notConfigured *conf.PathNotConfiguredError
		switch {
		case errors.As(err, &authErr):
			if authErr.AskCredentials {
				w.Header().Set("WWW-Authenticate", `Basic realm="mediamtx"`)
			}
			writeHTTPError(w, http.StatusUnauthorized, "读取鉴权失败")
		case errors.As(err, &noStream), errors.As(err, &notConfigured), errors.Is(err, conf.ErrPathNotFound):
			writeHTTPError(w, http.StatusNotFound, "流不存在或尚未就绪")
		default:
			writeHTTPError(w, http.StatusServiceUnavailable, "流暂不可用")
		}
		return
	}
	defer res.Path.RemoveReader(defs.PathRemoveReaderReq{Author: sx})
	// The cloned output description contains current parameters; original formats select the subscription.
	desc := res.Stream.OutDescCopy()
	reader := &stream.Reader{Parent: sx, CloseOnQueueFull: true}
	var encoder *videoEncoder
	var mediaIndex, formatIndex int
	for mi, medi := range res.Stream.OrigDesc.Medias {
		for fi, forma := range medi.Formats {
			switch forma.(type) {
			case *format.H264, *format.H265:
				if encoder == nil {
					mediaIndex, formatIndex = mi, fi
					encoder = newVideoEncoder(desc.Medias[mi].Formats[fi])
				}
			}
		}
	}
	if encoder == nil {
		writeHTTPError(w, http.StatusBadRequest, "该流没有可输出的 H.264 或 H.265 视频轨")
		return
	}
	if ctx.Err() != nil {
		writeHTTPError(w, http.StatusServiceUnavailable, "流已关闭")
		return
	}
	conn, err := upgrader.Upgrade(upgradeWriter{w}, r, nil)
	if err != nil {
		return
	}
	defer conn.Close() //nolint:errcheck
	sx.Log(logger.Info, "is reading from path '%s'", name)
	err = sx.run(conn, r, res, reader, encoder, mediaIndex, formatIndex)
	sx.Log(logger.Info, "closed: %v", err)
}

func (s *session) run(conn *websocket.Conn, r *http.Request, res *defs.PathAddReaderRes,
	reader *stream.Reader, encoder *videoEncoder, mi, fi int,
) error {
	write := func(data []byte) error {
		if err := conn.SetWriteDeadline(time.Now().Add(time.Duration(s.server.WriteTimeout))); err != nil {
			return err
		}
		return conn.WriteMessage(websocket.BinaryMessage, data)
	}
	firstFrame := make(chan struct{})
	medi := res.Stream.OrigDesc.Medias[mi]
	reader.OnData(medi, medi.Formats[fi], func(u *unit.Unit) error {
		if s.ctx.Err() != nil {
			return fmt.Errorf("terminated")
		}
		started := encoder.started
		messages, err := encoder.encode(u)
		if err != nil {
			return err
		}
		for _, message := range messages {
			if err = write(message); err != nil {
				return err
			}
		}
		if !started && encoder.started {
			close(firstFrame)
		}
		return nil
	})
	res.Stream.AddReader(reader)
	onUnread := hooks.OnRead(hooks.OnReadParams{
		Logger: s, ExternalCmdPool: s.server.ExternalCmdPool,
		Conf: res.Path.SafeConf(), ExternalCmdEnv: res.Path.ExternalCmdEnv(),
		Reader: *s.APIReaderDescribe(), Query: r.URL.RawQuery,
	})
	defer onUnread()
	readDone := make(chan error, 1)
	readExited := make(chan struct{})
	conn.SetReadLimit(64)
	conn.SetReadDeadline(time.Now().Add(40 * time.Second)) //nolint:errcheck
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(40 * time.Second)) })
	go func() {
		defer close(readExited)
		for {
			typ, data, err := conn.ReadMessage()
			if err != nil {
				readDone <- err
				return
			}
			if typ != websocket.BinaryMessage || len(data) != 1 || data[0] != 4 {
				readDone <- fmt.Errorf("invalid control message")
				return
			}
			// No upstream keyframe feedback exists in the shared path/stream interface.
		}
	}()
	defer func() {
		s.cancel()
		// Removal waits at most one media write timeout; EOS then has exclusive writer access.
		res.Stream.RemoveReader(reader)
		conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond)) //nolint:errcheck
		conn.WriteMessage(websocket.BinaryMessage, []byte{255})       //nolint:errcheck
		conn.Close()                                                  //nolint:errcheck
		<-readExited
	}()
	startup := time.NewTimer(time.Duration(s.server.ReadTimeout))
	defer startup.Stop()
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	var expires <-chan time.Time
	if res.AuthRes.ExpiresAt != nil {
		timer := time.NewTimer(time.Until(*res.AuthRes.ExpiresAt))
		defer timer.Stop()
		expires = timer.C
	}
	for {
		select {
		case <-s.ctx.Done():
			return fmt.Errorf("terminated")
		case <-r.Context().Done():
			return fmt.Errorf("client disconnected")
		case err := <-readDone:
			return err
		case err := <-reader.Error():
			return err
		case <-firstFrame:
			firstFrame = nil
			startup.Stop()
		case <-startup.C:
			return fmt.Errorf("timed out waiting for video parameters and random access frame")
		case <-expires:
			return fmt.Errorf("session expired")
		case <-ping.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(time.Duration(s.server.WriteTimeout))); err != nil {
				return err
			}
		}
	}
}
