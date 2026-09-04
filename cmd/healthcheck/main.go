// Command healthcheck probes a daemon's /healthz endpoint and reports the result
// as an exit status, which is the only thing a container healthcheck can read.
//
// It exists because the runtime image is distroless: there is no shell, no curl
// and no wget to write a healthcheck with, and adding a probe mode to each of the
// eleven daemons would be eleven changes to make and keep in step. One binary in
// the image serves all of them.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"
)

// defaultURL is the endpoint every daemon serves inside its own container when
// health_addr is set, so the compose healthchecks can stay identical.
const defaultURL = "http://127.0.0.1:8090/healthz"

// probeTimeout bounds the whole attempt. A daemon that cannot answer its own
// readiness endpoint within this is not healthy, whatever the reason.
const probeTimeout = 4 * time.Second

func main() {
	url := defaultURL
	if len(os.Args) > 1 {
		url = os.Args[1]
	}
	if err := probe(url); err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		os.Exit(1)
	}
}

// probe reports whether the endpoint answered 200. Any other status means the
// daemon is running but says it is not ready (a failed readiness check answers
// 503), which is exactly the state a container healthcheck exists to surface.
func probe(url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	// #nosec G704 -- the URL is the probe target the container healthcheck passes on the command line; fetching it is this binary's whole purpose
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	// #nosec G704 -- the URL is the probe target the container healthcheck passes on the command line; fetching it is this binary's whole purpose
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s reported %s", url, resp.Status)
	}
	return nil
}
