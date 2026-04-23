//go:build loadtest

// Package main is a tiny throughput-biased load generator for
// /v1/ingest/samples. Build and run with:
//
//	go run -tags=loadtest ./testdata/load \
//	  --url=https://localhost/v1/ingest/samples \
//	  --token=tt_... \
//	  --nodes=100 --rps=1000 --duration=60s
//
// It does not simulate realistic metric distributions — the goal is to
// stress the ingest path (gzip decode, NDJSON split, batcher, CH
// insert) not the alerter. Use a dedicated test server; this script
// will happily push 10k rps until the server folds.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

type sample struct {
	V    int    `json:"v"`
	Ts   string `json:"ts"`
	Host struct {
		Name    string `json:"name"`
		OS      string `json:"os"`
		Arch    string `json:"arch"`
		Kernel  string `json:"kernel"`
		Uptime  int    `json:"uptime_s"`
		Sampler string `json:"sampler"`
	} `json:"host"`
	CPU struct {
		Pct    float64 `json:"pct"`
		Load1  float64 `json:"load_1"`
		Load5  float64 `json:"load_5"`
		Load15 float64 `json:"load_15"`
		Cores  int     `json:"cores"`
	} `json:"cpu"`
	Mem struct {
		Used  uint64 `json:"used"`
		Total uint64 `json:"total"`
	} `json:"mem"`
	Swap struct {
		Used  uint64 `json:"used"`
		Total uint64 `json:"total"`
	} `json:"swap"`
	Errors []string `json:"errors"`
}

func main() {
	url := flag.String("url", "http://localhost:8080/v1/ingest/samples", "")
	token := flag.String("token", "", "Bearer token (sampler kind)")
	nodes := flag.Int("nodes", 10, "distinct node IDs to simulate")
	rps := flag.Int("rps", 100, "target requests per second")
	duration := flag.Duration("duration", 30*time.Second, "")
	flag.Parse()

	if *token == "" {
		fmt.Println("--token required")
		return
	}

	nodeIDs := make([]string, *nodes)
	for i := range nodeIDs {
		nodeIDs[i] = uuid.New().String()
	}

	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	var sent, errs atomic.Int64
	tick := time.NewTicker(time.Second / time.Duration(*rps))
	defer tick.Stop()

	var wg sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			fmt.Printf("sent=%d errs=%d elapsed=%s\n", sent.Load(), errs.Load(), duration.String())
			return
		case <-tick.C:
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				if err := push(*url, *token, nodeIDs[int(sent.Load())%len(nodeIDs)]); err != nil {
					errs.Add(1)
				}
				sent.Add(1)
			}(int(sent.Load()))
		}
	}
}

func push(url, token, nodeID string) error {
	s := sample{V: 1, Ts: time.Now().UTC().Format(time.RFC3339Nano)}
	s.Host.Name = "load-" + nodeID[:8]
	s.Host.OS = "linux"
	s.Host.Arch = "amd64"
	s.Host.Kernel = "6.6"
	s.Host.Uptime = 1
	s.Host.Sampler = "0.1.0"
	s.CPU.Pct = 50
	s.CPU.Cores = 4
	s.Mem.Used, s.Mem.Total = 512*1024*1024, 2048*1024*1024
	s.Errors = []string{}
	b, _ := json.Marshal(s)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write(append(b, '\n'))
	_ = gz.Close()

	req, _ := http.NewRequest("POST", url+"?node_id="+nodeID, &buf)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
