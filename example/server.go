package main

import (
	"bufio"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/crewjam/reversehttp"
)

func main() {
	addr := flag.String("listen", ":8100", "The address to listen on")
	flag.Parse()

	var sessions []*reversehttp.Session

	handler := reversehttp.Server{
		OnConnect: func(s *reversehttp.Session) {
			req, _ := http.NewRequest("GET", "/uptime", nil)
			c := http.Client{Transport: s}
			resp, err := c.Do(req)
			if err != nil {
				log.Printf("ERROR: %s", err)
			}
			log.Printf("new connection")
			io.Copy(os.Stdout, resp.Body)
		},
	}

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		for _, s := range sessions {
			req, _ := http.NewRequest("GET", "/echo", strings.NewReader(scanner.Text))
			_, err := http.Client{Transport: s}.Do(req)
			if err == reversehttp.ErrSessionClosed {
				continue
			}
			if err != nil {
				log.Printf("ERROR: %s", err)
			}
		}
	}

	go http.ListenAndServe(*addr, &handler)
}
