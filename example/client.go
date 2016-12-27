package main

import (
	"encoding/json"
	"flag"
	"net/http"
	"time"

	"github.com/crewjam/reversehttp"
)

func main() {
	startTime := time.Now()

	addr := flag.String("url", "http://localhost:8100", "The address to connect to")
	flag.Parse()

	http.Handle("/uptime", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(struct {
			Uptime time.Duration
		}{Uptime: time.Now().Sub(startTime)})
	}))

	reversehttp.ConnectAndServe(http.DefaultClient, *addr, http.DefaultServeMux)
}
