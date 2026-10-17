// Copyright 2025 Christophe Eymard
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package harness runs the cross-server benchmark described in
// benchmarks/README.md : it starts one server at a time, drives a closed-loop
// load against it, checks that servers answer equivalently, and writes the
// results. Scenario definitions live in benchmarks/scenarios.
package harness

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/samber/oops"
)

// Server names one system under test. Its string value is also its
// docker compose service name.
type Server string

const (
	Rel           Server = "rel"
	PostgREST     Server = "postgrest"
	PostGraphile4 Server = "postgraphile4"
	PostGraphile5 Server = "postgraphile5"
)

// AllServers is the order servers are benchmarked and reported in.
var AllServers = []Server{Rel, PostgREST, PostGraphile4, PostGraphile5}

// Support says how a server handles a scenario.
type Support int

const (
	// Unsupported : the server cannot express the scenario ; it is skipped and reported as such.
	Unsupported Support = iota
	// Native : the server does it in one request, with its own means.
	Native
	// Emulated : the client chains several requests to get the same result.
	Emulated
)

func (s Support) String() string {
	switch s {
	case Native:
		return "native"
	case Emulated:
		return "emulated"
	}
	return "unsupported"
}

// Request is one HTTP call. Path includes the query string.
type Request struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// Env is what scenarios need to know about the loaded dataset to build
// requests that hit real rows.
type Env struct {
	Size       string
	Properties int64
	Guests     int64
	Rooms      int64
	// BookingIDs is a sample of booking uuids, for primary-key lookups.
	BookingIDs []string
	// RoomIDs and GuestIDs are all room / guest ids, for write scenarios.
	RoomIDs  []int64
	GuestIDs []int64
	// ScratchIDs are the "scratch-*" amenities a write scenario's Prepare
	// inserted, for delete scenarios to remove.
	ScratchIDs []int64
}

// Impl is how one server runs one scenario.
type Impl struct {
	Support Support
	// Note is shown in the report next to the support level (e.g. "3 round trips").
	Note string
	// Run performs operation number n using c, and returns the body of the
	// final response. n differs for every call and picks the rows to touch ;
	// the equivalence check calls Run with n = 0 on every server.
	Run func(c *Client, env *Env, n uint64) ([]byte, error)
	// Normalize turns the body Run returned into a value comparable across
	// servers (same rows, same fields, plain JSON types). Reads only.
	Normalize func(body []byte) (any, error)
}

// Scenario is one benchmarked operation, with one Impl per server.
type Scenario struct {
	Name  string
	Group string
	Write bool
	// Impls is keyed by server ; a missing key means Unsupported.
	Impls map[Server]Impl
	// Prepare, if set, runs on the freshly reset database before the server
	// starts ; ops is how many operations the run will perform.
	Prepare func(ctx context.Context, db *pgxpool.Pool, ops int) error
	// Check runs after a write scenario's fixed number of operations and
	// returns an error if the database is not in the state those ops must
	// have produced. ops is the number of operations that succeeded.
	Check func(ctx context.Context, db *pgxpool.Pool, ops int) error
	// AllowMismatch lists servers whose normalized output may differ from the
	// first supported server's, with the reason (reported, not failing).
	AllowMismatch map[Server]string
}

// Client is the HTTP client a scenario's Run uses. It counts round trips and
// bytes so the report can show them. Safe for concurrent use.
type Client struct {
	Base string
	hc   *http.Client

	trips atomic.Int64
	bytes atomic.Int64
}

// NewClient returns a client for the server at base (e.g. "http://localhost:18080").
func NewClient(base string) *Client {
	tr := &http.Transport{
		MaxIdleConns:        512,
		MaxIdleConnsPerHost: 512,
		IdleConnTimeout:     90 * time.Second,
		// every server gets the same uncompressed bytes
		DisableCompression: true,
	}
	return &Client{Base: base, hc: &http.Client{Transport: tr, Timeout: 60 * time.Second}}
}

// Do sends r and returns the body. Any status outside 2xx is an error that
// includes the (truncated) body.
func (c *Client) Do(r Request) ([]byte, error) {
	var body io.Reader
	if r.Body != nil {
		body = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequest(r.Method, c.Base+r.Path, body)
	if err != nil {
		return nil, oops.Wrapf(err, "building request %s %s", r.Method, r.Path)
	}
	for k, v := range r.Header {
		req.Header[k] = v
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, oops.Wrapf(err, "%s %s", r.Method, r.Path)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, oops.Wrapf(err, "reading %s %s", r.Method, r.Path)
	}
	c.trips.Add(1)
	c.bytes.Add(int64(len(out)))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return out, oops.With("status", resp.StatusCode).Errorf("%s %s returned %d: %s", r.Method, r.Path, resp.StatusCode, truncate(out, 300))
	}
	return out, nil
}

// Close drops idle connections.
func (c *Client) Close() { c.hc.CloseIdleConnections() }

// Trips returns the number of HTTP round trips made so far.
func (c *Client) Trips() int64 { return c.trips.Load() }

// Bytes returns the total response bytes received so far.
func (c *Client) Bytes() int64 { return c.bytes.Load() }

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
