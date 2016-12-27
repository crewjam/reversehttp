// package reversehttp implements a simple scheme for reversing the
// request/response flow of HTTP in go.
//
// The listener accepts connections from a TCP client, which polls the
// server for requests. The request is passed to the handler on the
// client, and the response is passed back to the server.
//
// Although the scheme doesn't preclude pipelining, only one
// request/response pair can be in flight at a time for now.
//
package reversehttp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	uuid "github.com/satori/go.uuid"
)

type Response struct {
	Err  error
	HTTP *http.Response
}

type Request struct {
	HTTP     *http.Request
	Response chan Response
}

type Session struct {
	handlerMu sync.Mutex // one request at a time

	PendingRequest *Request
	Requests       chan Request
	closed         chan struct{}
	closer         *time.Timer
}

func (s *Session) Close() {
	close(s.closed)
	close(s.Requests)
}

var ErrSessionClosed = errors.New("session closed")

func (s *Session) RoundTrip(r *http.Request) (*http.Response, error) {
	select {
	case _, _ = <-s.closed:
		return nil, ErrSessionClosed
	default:
	}

	req := Request{
		HTTP:     r,
		Response: make(chan Response),
	}
	s.Requests <- req

	resp := <-req.Response
	return resp.HTTP, resp.Err
}

type Server struct {
	LongPollMinTimeout time.Duration
	LongPollMaxTimeout time.Duration
	SessionIdleTimeout time.Duration

	OnConnect func(*Session)

	doInit   sync.Once
	sessions map[string]*Session
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	s.doInit.Do(func() {
		if s.LongPollMaxTimeout == 0 {
			s.LongPollMaxTimeout = 2 * time.Minute
		}
		if s.SessionIdleTimeout == 0 {
			s.SessionIdleTimeout = 10 * time.Minute
		}
	})

	sessionID := r.Header.Get("X-Session")
	session := s.sessions[sessionID]
	if session == nil {
		sessionID = uuid.NewV4().String()
		session = &Session{
			closed:   make(chan struct{}),
			Requests: make(chan Request),
		}
		if s.sessions == nil {
			s.sessions = map[string]*Session{}
		}
		s.sessions[sessionID] = session
		if s.OnConnect != nil {
			go s.OnConnect(session)
		}
	}
	w.Header().Add("X-Session", sessionID)

	session.handlerMu.Lock()
	defer session.handlerMu.Unlock()

	// reset the timer that closes this session if idle.
	if session.closer != nil {
		session.closer.Stop()
	}
	session.closer = time.AfterFunc(s.SessionIdleTimeout, func() {

		session := s.sessions[sessionID]
		if session != nil {
			log.Printf("closing idle session")
			session.Close()
			delete(s.sessions, sessionID)
		}
	})

	if session.PendingRequest != nil {
		resp, err := http.ReadResponse(bufio.NewReader(r.Body), session.PendingRequest.HTTP)
		if err != nil {
			session.PendingRequest.Response <- Response{Err: err}
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		session.PendingRequest.Response <- Response{HTTP: resp}
		close(session.PendingRequest.Response)
		session.PendingRequest = nil
	}

	// figure out what the polling timeout should be
	timeout := time.Minute
	{
		if timeoutStr := r.Header.Get("X-Timeout"); timeoutStr != "" {
			var err error
			timeout, err = time.ParseDuration(timeoutStr)
			if err != nil {
				w.Header().Add("X-Error", "cannot parse duration in X-Timeout header")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		if timeout > s.LongPollMaxTimeout {
			w.Header().Add("X-Warning", fmt.Sprintf("timeout value too high, forcing to maximum %s",
				s.LongPollMaxTimeout.String()))
			timeout = s.LongPollMaxTimeout
		}
		if timeout < s.LongPollMinTimeout {
			w.Header().Add("X-Warning", fmt.Sprintf("timeout value too low, forcing to minimum %s",
				s.LongPollMinTimeout.String()))
			timeout = s.LongPollMinTimeout
		}
	}

	select {
	case _ = <-time.After(timeout):
		w.WriteHeader(http.StatusNoContent)
		return
	case req, ok := <-session.Requests:
		if !ok {
			log.Printf("410 Gone")
			w.WriteHeader(http.StatusGone)
			return
		}

		w.Header().Add("Content-type", "application/x-http-request")
		w.WriteHeader(http.StatusOK)
		if err := req.HTTP.Write(w); err != nil {
			req.Response <- Response{Err: err}
			return
		}
		session.PendingRequest = &req
	}
}

type ResponseWriter struct {
	w          io.WriteCloser
	header     http.Header
	headerSent bool
}

func (rw *ResponseWriter) Header() http.Header {
	if rw.header == nil {
		rw.header = http.Header{}
	}
	return rw.header
}

func (rw *ResponseWriter) Write(b []byte) (int, error) {
	if !rw.headerSent {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.w.Write(b)
}

func (rw *ResponseWriter) WriteHeader(statusCode int) {
	if !rw.headerSent {
		fmt.Fprintf(rw.w, "HTTP/1.1 %d %s\r\n", statusCode, http.StatusText(statusCode))
		rw.header.Write(rw.w)
		fmt.Fprintf(rw.w, "\r\n")
		rw.headerSent = true
	}
}

func ConnectAndServe(httpClient *http.Client, url string, handler http.Handler) error {
	var pollTimeout = time.Minute
	var sessionID string

	req, _ := http.NewRequest("POST", url, nil)
	req.Header.Add("X-Timeout", pollTimeout.String())
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}

	for {
		sessionID = resp.Header.Get("X-Session")
		if resp.StatusCode == http.StatusNoContent {
			req, _ := http.NewRequest("POST", url, nil)
			req.Header.Add("X-Timeout", pollTimeout.String())
			req.Header.Add("X-Session", sessionID)
			resp, err = httpClient.Do(req)
			if err != nil {
				return err
			}
			continue
		}
		if resp.StatusCode == http.StatusGone {
			return ErrSessionClosed
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s", resp.Status)
		}

		serverReq, err := http.ReadRequest(bufio.NewReader(resp.Body))
		if err != nil {
			return err
		}

		reqBodyReader, reqBodyWriter := io.Pipe()

		go func() {
			rw := &ResponseWriter{
				w: reqBodyWriter,
			}
			handler.ServeHTTP(rw, serverReq)
			reqBodyWriter.Close()
		}()

		req, _ = http.NewRequest("POST", url, reqBodyReader)
		req.Header.Add("X-Timeout", pollTimeout.String())
		req.Header.Add("X-Session", sessionID)
		req.Header.Add("Content-Type", "application/x-http-response")

		resp, err = httpClient.Do(req)
		if err != nil {
			return err
		}
	}
}
