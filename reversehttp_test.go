package reversehttp

import (
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestFunctionality(t *testing.T) {
	done := make(chan struct{})

	server := Server{}

	server.OnConnect = func(session *Session) {
		revClient := http.Client{
			Transport: session,
		}
		for i := 0; i < 2; i++ {
			req, _ := http.NewRequest("FROB", "/grob", strings.NewReader("frob the grob!"))
			resp, err := revClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			log.Printf("got response: %s", resp.Status)
		}
		close(done)
	}

	serverSock, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(serverSock, &server)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("got request: %s %s", r.Method, r.URL)
		body, _ := ioutil.ReadAll(r.Body)
		log.Printf("got request body: %s", string(body))
		w.Header().Add("X-Foo", "Bar")
		w.Write([]byte("asdf asdf asdf"))
	})

	serverURL := fmt.Sprintf("http://%s/", serverSock.Addr())
	go ConnectAndServe(http.DefaultClient, serverURL, &handler)

	<-done
}

func TestPollTimeout(t *testing.T) {
	done := make(chan struct{})
	server := Server{
		LongPollMaxTimeout: 50 * time.Millisecond,
		OnConnect: func(session *Session) {

			// sleep a while to we're sure that the client must
			// reconnect at least once
			time.Sleep(150 * time.Millisecond)

			revClient := http.Client{
				Transport: session,
			}
			req, _ := http.NewRequest("FROB", "/grob", nil)
			resp, err := revClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Errorf("expected StatusOK, got %s", resp.Status)
				t.Fail()
			}
			close(done)
		},
	}

	serverSock, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(serverSock, &server)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Hello, World!")
	})

	serverURL := fmt.Sprintf("http://%s/", serverSock.Addr())
	go ConnectAndServe(http.DefaultClient, serverURL, &handler)

	<-done
}

func TestHandlerError(t *testing.T) {
	done := make(chan struct{})
	server := Server{
		OnConnect: func(session *Session) {
			revClient := http.Client{
				Transport: session,
			}
			req, _ := http.NewRequest("FAIL", "/grob", nil)
			resp, err := revClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusTeapot {
				t.Errorf("expected StatusTeapot, got %s", resp.Status)
				t.Fail()
			}
			close(done)
		},
	}

	serverSock, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(serverSock, &server)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	serverURL := fmt.Sprintf("http://%s/", serverSock.Addr())
	go ConnectAndServe(http.DefaultClient, serverURL, &handler)

	<-done
}

func TestServerClosesConnection(t *testing.T) {
	done := make(chan struct{})
	server := Server{
		OnConnect: func(session *Session) {
			session.Close()

			// this fails because the session is closed
			revClient := http.Client{
				Transport: session,
			}
			req, _ := http.NewRequest("FAIL", "/grob", nil)
			_, err := revClient.Do(req)
			if err != ErrSessionClosed {
				t.Errorf("expected ErrSessionClosed, got %s", err)
				t.Fail()
			}
			close(done)
		},
	}

	serverSock, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(serverSock, &server)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	serverURL := fmt.Sprintf("http://%s/", serverSock.Addr())
	err = ConnectAndServe(http.DefaultClient, serverURL, &handler)
	if err != ErrSessionClosed {
		t.Errorf("expected ErrSessionClosed, got %s", err)
		t.Fail()
	}

	<-done
}

func TestConnectionTimeout(t *testing.T) {
	server := Server{
		SessionIdleTimeout: 50 * time.Millisecond,
		OnConnect: func(session *Session) {
		},
	}

	serverSock, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(serverSock, &server)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// make the session last longer than the timeout
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusTeapot)
	})

	serverURL := fmt.Sprintf("http://%s/", serverSock.Addr())
	err = ConnectAndServe(http.DefaultClient, serverURL, &handler)
	if err != ErrSessionClosed {
		t.Errorf("expected ErrSessionClosed, got %s", err)
		t.Fail()
	}
}

func TestBadTimeoutValues(t *testing.T) {
	server := Server{
		LongPollMinTimeout: 50 * time.Millisecond,
		LongPollMaxTimeout: 150 * time.Millisecond,
		OnConnect: func(s *Session) {
		},
	}

	serverSock, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(serverSock, &server)
	serverURL := fmt.Sprintf("http://%s/", serverSock.Addr())

	req, _ := http.NewRequest("POST", serverURL, nil)
	req.Header.Add("X-Timeout", "not-a-valid-time")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected StatusBadRequest, got %s", resp.Status)
	}

	{
		req, _ := http.NewRequest("POST", serverURL, nil)
		req.Header.Add("X-Timeout", "200ms")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("expected StatusNoContent, got %s", resp.Status)
		}
		if resp.Header.Get("X-Warning") != "timeout value too high, forcing to maximum 150ms" {
			t.Errorf("expected warning, got %s", resp.Header.Get("X-Warning"))
		}
	}

	{
		req, _ := http.NewRequest("POST", serverURL, nil)
		req.Header.Add("X-Timeout", "10ms")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("expected StatusNoContent, got %s", resp.Status)
		}
		if resp.Header.Get("X-Warning") != "timeout value too low, forcing to minimum 50ms" {
			t.Errorf("expected warning, got %s", resp.Header.Get("X-Warning"))
		}
	}
}
