package slack

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeSlackJSON writes body as a Slack-style JSON response, adding
// "ok": true unless the body already carries an "ok" field. The input map is
// not mutated so page fixtures can be served repeatedly.
func writeSlackJSON(t *testing.T, w http.ResponseWriter, body map[string]any) {
	t.Helper()

	resp := map[string]any{"ok": true}
	maps.Copy(resp, body)

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		t.Errorf("fake slack: encode response: %v", err)
	}
}

// fakeSlackList serves a paginated Slack list API from an httptest server.
// Pages are keyed by the cursor the client sends ("" for the first call).
// Every request payload is recorded for later assertions in the test
// goroutine (require must not be called from handler goroutines).
type fakeSlackList struct {
	t     *testing.T
	path  string
	pages map[string]map[string]any

	mu       sync.Mutex
	payloads []map[string]string
}

// newFakeSlackList starts an httptest fake Slack serving the given pages for
// the given API method and returns it with a Client pointed at it.
func newFakeSlackList(t *testing.T, path string, pages map[string]map[string]any) (*fakeSlackList, *Client) {
	t.Helper()

	fake := &fakeSlackList{t: t, path: path, pages: pages}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	return fake, NewClientWithBaseURL("xoxb-test-token", server.URL)
}

func (f *fakeSlackList) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Slack's cursor-paginated read methods (conversations.list, users.list)
	// only read form-encoded params — a JSON body is silently ignored. Enforce
	// form encoding here so a regression back to a JSON body fails loudly
	// instead of silently serving Slack's defaults (limit 100, first page,
	// public channels only).
	if ct := req.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		f.t.Errorf("fake slack: expected form-encoded request, got Content-Type %q", ct)
		writeSlackJSON(f.t, w, map[string]any{"ok": false, "error": "not_form_encoded"})

		return
	}

	if err := req.ParseForm(); err != nil {
		f.t.Errorf("fake slack: parse form: %v", err)
		writeSlackJSON(f.t, w, map[string]any{"ok": false, "error": "invalid_payload"})

		return
	}

	payload := make(map[string]string, len(req.PostForm))
	for key := range req.PostForm {
		payload[key] = req.PostForm.Get(key)
	}

	f.mu.Lock()
	f.payloads = append(f.payloads, payload)
	f.mu.Unlock()

	if req.URL.Path != "/"+f.path {
		writeSlackJSON(f.t, w, map[string]any{"ok": false, "error": "unknown_method"})

		return
	}

	cursor := payload["cursor"]

	page, ok := f.pages[cursor]
	if !ok {
		writeSlackJSON(f.t, w, map[string]any{"ok": false, "error": "invalid_cursor"})

		return
	}

	writeSlackJSON(f.t, w, page)
}

// recordedPayloads returns a copy of the request payloads seen so far.
func (f *fakeSlackList) recordedPayloads() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.payloads)
}

// newCappedFakeSlack starts an httptest fake Slack that answers every request
// with a single unique item and a distinct, *always-advancing* next_cursor,
// i.e. a server that never stops advertising genuinely new pages. Because each
// cursor and item ID is unique per call, neither the non-advancing-cursor guard
// nor the ID de-dup fires — only the page cap stops the loop. Returns the
// request counter and a Client.
func newCappedFakeSlack(t *testing.T, itemsField string) (*atomic.Int64, *Client) {
	t.Helper()

	calls := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		writeSlackJSON(t, w, map[string]any{
			itemsField:          []map[string]any{{"id": fmt.Sprintf("ID%d", n), "name": "endless"}},
			"response_metadata": map[string]any{"next_cursor": fmt.Sprintf("cursor-%d", n)},
		})
	}))
	t.Cleanup(server.Close)

	return calls, NewClientWithBaseURL("xoxb-test-token", server.URL)
}

// requirePageSizeLimit asserts that a recorded request payload carries the
// standard page-size limit.
func requirePageSizeLimit(r *require.Assertions, payload map[string]string) {
	limit, ok := payload["limit"]
	r.True(ok, "limit must be sent on every page")
	r.Equal(strconv.Itoa(listPageSize), limit)
}

func TestNewClientTargetsRealSlackAPI(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal(SlackAPIBaseURL, NewClient("xoxb-test-token").baseURL)
}

func TestListChannelsPaginatesAcrossAllPages(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	pages := map[string]map[string]any{
		"": {
			"channels": []map[string]any{
				{"id": "C1", "name": "alpha"},
				{"id": "C2", "name": "beta", "is_private": true, "is_member": true},
			},
			"response_metadata": map[string]any{"next_cursor": "cursor-2"},
		},
		"cursor-2": {
			"channels":          []map[string]any{{"id": "C3", "name": "gamma"}},
			"response_metadata": map[string]any{"next_cursor": "cursor-3"},
		},
		// Last page: empty cursor ends the chain. Contains the regression
		// case — a channel that only exists past the first page.
		"cursor-3": {
			"channels":          []map[string]any{{"id": "C4", "name": "solidping-dev", "is_member": true}},
			"response_metadata": map[string]any{"next_cursor": ""},
		},
	}

	fake, client := newFakeSlackList(t, "conversations.list", pages)

	channels, err := client.ListChannels(t.Context())
	r.NoError(err)

	names := make([]string, 0, len(channels))
	for _, ch := range channels {
		names = append(names, ch.Name)
	}

	// All pages are aggregated — in particular the channel that only exists
	// on the last page must be present.
	r.Equal([]string{"alpha", "beta", "gamma", "solidping-dev"}, names)

	// Attributes survive the round-trip.
	r.True(channels[1].IsPrivate)
	r.True(channels[1].IsMember)

	// Every page was requested exactly once, following the cursor chain, and
	// each request carried the page-size limit and channel type filters.
	payloads := fake.recordedPayloads()
	r.Len(payloads, 3)

	cursors := make([]string, 0, len(payloads))

	for _, payload := range payloads {
		cursor := payload["cursor"]
		cursors = append(cursors, cursor)

		requirePageSizeLimit(r, payload)
		r.Equal("public_channel,private_channel", payload["types"])
		r.Equal("true", payload["exclude_archived"])
	}

	r.Equal([]string{"", "cursor-2", "cursor-3"}, cursors)

	// The first request must not send a cursor at all.
	_, hasCursor := payloads[0]["cursor"]
	r.False(hasCursor)
}

func TestListChannelsStopsAtPageCap(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	calls, client := newCappedFakeSlack(t, "channels")

	channels, err := client.ListChannels(t.Context())
	r.NoError(err)

	// A server that always returns a cursor must not loop forever: the cap
	// stops the fetch and the pages collected so far are returned.
	r.Equal(int64(maxListPages), calls.Load())
	r.Len(channels, maxListPages)
}

func TestListChannelsPropagatesAPIError(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSlackJSON(t, w, map[string]any{"ok": false, "error": "invalid_auth"})
	}))
	t.Cleanup(server.Close)

	client := NewClientWithBaseURL("xoxb-bad-token", server.URL)

	_, err := client.ListChannels(t.Context())
	r.ErrorIs(err, ErrSlackAPI)
	r.ErrorContains(err, "invalid_auth")
}

func TestListUsersPaginatesAndFiltersAcrossPages(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	pages := map[string]map[string]any{
		"": {
			"members": []map[string]any{
				{"id": "U1", "name": "alice", "real_name": "Alice Anderson"},
				{"id": "UBOT", "name": "robot", "is_bot": true},
			},
			"response_metadata": map[string]any{"next_cursor": "cursor-2"},
		},
		"cursor-2": {
			"members": []map[string]any{
				{"id": "UDEL", "name": "dave", "deleted": true},
				{"id": "U2", "name": "bob", "real_name": "Bob Brown"},
			},
			"response_metadata": map[string]any{"next_cursor": "cursor-3"},
		},
		"cursor-3": {
			"members":           []map[string]any{{"id": "U3", "name": "carol", "real_name": "Carol Clark"}},
			"response_metadata": map[string]any{"next_cursor": ""},
		},
	}

	fake, client := newFakeSlackList(t, "users.list", pages)

	users, err := client.ListUsers(t.Context())
	r.NoError(err)

	ids := make([]string, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}

	// Bots and deleted users are filtered out on every page; regular users
	// from all pages (including the last) are kept.
	r.Equal([]string{"U1", "U2", "U3"}, ids)

	payloads := fake.recordedPayloads()
	r.Len(payloads, 3)

	cursors := make([]string, 0, len(payloads))

	for _, payload := range payloads {
		cursor := payload["cursor"]
		cursors = append(cursors, cursor)

		requirePageSizeLimit(r, payload)
	}

	r.Equal([]string{"", "cursor-2", "cursor-3"}, cursors)
}

func TestListUsersStopsAtPageCap(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	calls, client := newCappedFakeSlack(t, "members")

	users, err := client.ListUsers(t.Context())
	r.NoError(err)

	r.Equal(int64(maxListPages), calls.Load())
	r.Len(users, maxListPages)
}

// TestListChannelsStopsOnNonAdvancingCursor is the core regression test: when
// Slack hands back a non-empty next_cursor that yields the same page again
// (the conversations.list quirk that surfaced 25 copies of every channel), the
// loop must stop instead of re-fetching maxListPages times, and each channel
// must appear exactly once.
func TestListChannelsStopsOnNonAdvancingCursor(t *testing.T) {
	t.Parallel()

	// channelsPage serves the same two channels on every call, advertising the
	// given next_cursor. If the loop followed a repeating cursor it would append
	// these channels once per page.
	channelsPage := func(nextCursor string) map[string]any {
		return map[string]any{
			"channels": []map[string]any{
				{"id": "C1", "name": "alpha"},
				{"id": "C2", "name": "beta"},
			},
			"response_metadata": map[string]any{"next_cursor": nextCursor},
		}
	}

	tests := []struct {
		name      string
		pages     map[string]map[string]any
		wantCalls int
	}{
		{
			// First page ("") advertises "stuck"; the "stuck" page advertises
			// "stuck" again — a cursor that never advances.
			name: "cursor repeats immediately",
			pages: map[string]map[string]any{
				"":      channelsPage("stuck"),
				"stuck": channelsPage("stuck"),
			},
			wantCalls: 2,
		},
		{
			// Cursor advances for a couple of pages, then page "b" points back
			// to the already-followed cursor "a" — a cycle.
			name: "cursor repeats after N pages",
			pages: map[string]map[string]any{
				"":  channelsPage("a"),
				"a": channelsPage("b"),
				"b": channelsPage("a"),
			},
			wantCalls: 3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			fake, client := newFakeSlackList(t, "conversations.list", tc.pages)

			channels, err := client.ListChannels(t.Context())
			r.NoError(err)

			names := make([]string, 0, len(channels))
			for _, ch := range channels {
				names = append(names, ch.Name)
			}

			// Each channel appears exactly once — not wantCalls copies, and
			// certainly not maxListPages copies.
			r.Equal([]string{"alpha", "beta"}, names)

			// The loop terminated as soon as the cursor stopped advancing.
			r.Len(fake.recordedPayloads(), tc.wantCalls)
			r.Less(tc.wantCalls, maxListPages)
		})
	}
}

// TestListUsersStopsOnNonAdvancingCursor is the symmetric regression test for
// users.list — a repeating cursor must not surface duplicate users.
func TestListUsersStopsOnNonAdvancingCursor(t *testing.T) {
	t.Parallel()

	membersPage := func(nextCursor string) map[string]any {
		return map[string]any{
			"members": []map[string]any{
				{"id": "U1", "name": "alice", "real_name": "Alice Anderson"},
				{"id": "U2", "name": "bob", "real_name": "Bob Brown"},
			},
			"response_metadata": map[string]any{"next_cursor": nextCursor},
		}
	}

	tests := []struct {
		name      string
		pages     map[string]map[string]any
		wantCalls int
	}{
		{
			name: "cursor repeats immediately",
			pages: map[string]map[string]any{
				"":      membersPage("stuck"),
				"stuck": membersPage("stuck"),
			},
			wantCalls: 2,
		},
		{
			name: "cursor repeats after N pages",
			pages: map[string]map[string]any{
				"":  membersPage("a"),
				"a": membersPage("b"),
				"b": membersPage("a"),
			},
			wantCalls: 3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			fake, client := newFakeSlackList(t, "users.list", tc.pages)

			users, err := client.ListUsers(t.Context())
			r.NoError(err)

			ids := make([]string, 0, len(users))
			for _, u := range users {
				ids = append(ids, u.ID)
			}

			r.Equal([]string{"U1", "U2"}, ids)

			r.Len(fake.recordedPayloads(), tc.wantCalls)
			r.Less(tc.wantCalls, maxListPages)
		})
	}
}
