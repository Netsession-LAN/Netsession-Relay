package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// healthAddr is where the health check finds a server listening on listen: a server on every address (":8080",
// "0.0.0.0:8080") is asked on the loopback.
func healthAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// runHealth exits 0 if the URL answers 200 (used as container health check; the image has no shell/curl).
func runHealth(args []string, _, stderr io.Writer) int {
	fs := newFlags("health", stderr)
	url := fs.String("url", "http://"+healthAddr(envOr("NSRELAY_HTTP_ADDR", "127.0.0.1:8080"))+"/healthz", "health URL")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(*url)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(stderr, "status", resp.Status)
		return 1
	}
	return 0
}
