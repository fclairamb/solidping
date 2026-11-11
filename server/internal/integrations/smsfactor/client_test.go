package smsfactor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// capturedRequest is what the fake API saw.
type capturedRequest struct {
	method string
	path   string
	header http.Header
	body   []byte
}

// newFakeAPI answers every request with status / body and records it.
func newFakeAPI(t *testing.T, status int, body string) (*httptest.Server, func() []capturedRequest) {
	t.Helper()

	var (
		mu   sync.Mutex
		seen []capturedRequest
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)

		mu.Lock()
		seen = append(seen, capturedRequest{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: raw})
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return srv, func() []capturedRequest {
		mu.Lock()
		defer mu.Unlock()

		return append([]capturedRequest(nil), seen...)
	}
}

const okResponse = `{"status":1,"message":"OK","ticket":"14672468","cost":2,"credits":1234,` +
	`"total":2,"sent":2,"blacklisted":0,"duplicated":0,"npai":0,"invalid":0,"not_allowed":0,` +
	`"flood":0,"country_limit":0}`

func TestSend_RequestShape(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv, seen := newFakeAPI(t, http.StatusOK, okResponse)
	client := NewClient("tok-123", WithBaseURL(srv.URL+"/"))

	res, err := client.Send(context.Background(), &SendParams{
		Recipients: []string{"+33612345678", "+15551234567"},
		Text:       "check DOWN",
		Sender:     " SolidPing ",
	})
	r.NoError(err)
	r.Equal("14672468", res.Ticket)
	r.Equal(StatusOK, res.Status)
	r.Equal(2, res.Sent)
	r.InDelta(1234, res.Credits, 0.001)

	reqs := seen()
	r.Len(reqs, 1)
	r.Equal(http.MethodPost, reqs[0].method)
	r.Equal("/", reqs[0].path)
	r.Equal("Bearer tok-123", reqs[0].header.Get("Authorization"))
	r.Equal("application/json", reqs[0].header.Get("Accept"))
	r.Equal("application/json", reqs[0].header.Get("Content-Type"))

	var body struct {
		SMS struct {
			Message struct {
				Text     string  `json:"text"`
				PushType string  `json:"pushtype"`
				Sender   *string `json:"sender"`
			} `json:"message"`
			Recipients struct {
				GSM []struct {
					GSMSMSID string `json:"gsmsmsid"`
					Value    string `json:"value"`
				} `json:"gsm"`
			} `json:"recipients"`
		} `json:"sms"`
	}
	r.NoError(json.Unmarshal(reqs[0].body, &body))
	r.Equal("check DOWN", body.SMS.Message.Text)
	r.Equal("alert", body.SMS.Message.PushType)
	r.NotNil(body.SMS.Message.Sender)
	r.Equal("SolidPing", *body.SMS.Message.Sender)
	r.Len(body.SMS.Recipients.GSM, 2)
	r.Equal("33612345678", body.SMS.Recipients.GSM[0].Value, "the leading + is stripped")
	r.Equal("15551234567", body.SMS.Recipients.GSM[1].Value)
	r.NotEmpty(body.SMS.Recipients.GSM[0].GSMSMSID)
	r.NotEqual(body.SMS.Recipients.GSM[0].GSMSMSID, body.SMS.Recipients.GSM[1].GSMSMSID)
}

func TestSend_EmptySenderIsOmitted(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv, seen := newFakeAPI(t, http.StatusOK, okResponse)
	client := NewClient("tok", WithBaseURL(srv.URL))

	_, err := client.Send(context.Background(), &SendParams{Recipients: []string{"+33612345678"}, Text: "hi"})
	r.NoError(err)

	reqs := seen()
	r.Len(reqs, 1)
	r.NotContains(string(reqs[0].body), `"sender"`)
}

func TestSend_Outcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		httpStatus int
		body       string
		wantErr    error
		wantTicket string
		wantInErr  string
	}{
		{name: "ok", httpStatus: http.StatusOK, body: okResponse, wantTicket: "14672468"},
		{
			name: "numeric ticket", httpStatus: http.StatusOK,
			body:       `{"status":"1","message":"OK","ticket":99,"sent":"1"}`,
			wantTicket: "99",
		},
		{
			name: "moderation is accepted even with sent 0", httpStatus: http.StatusOK,
			body:       `{"status":-8,"message":"Campaign under moderation","ticket":"55","sent":0}`,
			wantTicket: "55",
		},
		{
			name: "auth error in a 200", httpStatus: http.StatusOK,
			body: `{"status":-1,"message":"Authentication error"}`, wantErr: ErrRejected,
			wantInErr: "status -1",
		},
		{
			name: "not enough credits", httpStatus: http.StatusOK,
			body: `{"status":-3,"message":"Not enough credits"}`, wantErr: ErrRejected,
			wantInErr: "Not enough credits",
		},
		{
			name: "data error with details", httpStatus: http.StatusBadRequest,
			body: `{"status":-7,"message":"Data error","details":"bad gsm"}`, wantErr: ErrRejected,
			wantInErr: "bad gsm",
		},
		{
			name: "everything filtered", httpStatus: http.StatusOK,
			body:    `{"status":1,"message":"OK","ticket":"1","total":2,"sent":0,"invalid":1,"blacklisted":1}`,
			wantErr: ErrNoRecipientReached, wantInErr: "invalid=1 blacklisted=1",
		},
		{
			name: "http error without json", httpStatus: http.StatusBadGateway,
			body: `<html>bad gateway</html>`, wantErr: ErrRequestFailed, wantInErr: "502",
		},
		{
			name: "200 without json", httpStatus: http.StatusOK,
			body: `<response/>`, wantErr: ErrRequestFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			srv, _ := newFakeAPI(t, tt.httpStatus, tt.body)
			client := NewClient("tok", WithBaseURL(srv.URL))

			res, err := client.Send(context.Background(), &SendParams{
				Recipients: []string{"+33612345678", "+33612345679"}, Text: "hi",
			})
			if tt.wantErr != nil {
				r.ErrorIs(err, tt.wantErr)
				r.Nil(res)

				if tt.wantInErr != "" {
					r.Contains(err.Error(), tt.wantInErr)
				}

				return
			}

			r.NoError(err)
			r.Equal(tt.wantTicket, res.Ticket)
		})
	}
}

func TestSend_InputErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv, seen := newFakeAPI(t, http.StatusOK, okResponse)

	_, err := NewClient("", WithBaseURL(srv.URL)).Send(context.Background(),
		&SendParams{Recipients: []string{"+33612345678"}, Text: "hi"})
	r.ErrorIs(err, ErrMissingToken)

	_, err = NewClient("tok", WithBaseURL(srv.URL)).Send(context.Background(), &SendParams{Text: "hi"})
	r.ErrorIs(err, ErrNoRecipients)

	r.Empty(seen(), "no request is made for invalid input")
}

func TestSend_Unreachable(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	_, err := NewClient("tok", WithBaseURL(url)).Send(context.Background(),
		&SendParams{Recipients: []string{"+33612345678"}, Text: "hi"})
	r.ErrorIs(err, ErrRequestFailed)
}

func TestVerifyToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		httpStatus int
		body       string
		wantErr    error
	}{
		{name: "ok", httpStatus: http.StatusOK, body: `{"status":1,"credits":"1234"}`},
		{
			name: "auth error in body", httpStatus: http.StatusOK,
			body: `{"status":-1,"message":"Auth"}`, wantErr: ErrTokenRejected,
		},
		{name: "401 without body", httpStatus: http.StatusUnauthorized, body: ``, wantErr: ErrTokenRejected},
		{name: "server error", httpStatus: http.StatusInternalServerError, body: `oops`, wantErr: ErrTokenUnverifiable},
		{
			name: "other negative status", httpStatus: http.StatusOK,
			body: `{"status":-99,"message":"?"}`, wantErr: ErrTokenUnverifiable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			srv, seen := newFakeAPI(t, tt.httpStatus, tt.body)

			err := NewClient("tok", WithBaseURL(srv.URL)).VerifyToken(context.Background())
			if tt.wantErr != nil {
				r.ErrorIs(err, tt.wantErr)
			} else {
				r.NoError(err)
			}

			reqs := seen()
			r.Len(reqs, 1)
			r.Equal(http.MethodGet, reqs[0].method)
			r.Equal("/credits", reqs[0].path)
			r.Equal("Bearer tok", reqs[0].header.Get("Authorization"))
			r.Equal("application/json", reqs[0].header.Get("Accept"))
		})
	}
}

func TestVerifyToken_EmptyTokenIsRejectedWithoutACall(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv, seen := newFakeAPI(t, http.StatusOK, `{"status":1}`)
	r.ErrorIs(NewClient(" ", WithBaseURL(srv.URL)).VerifyToken(context.Background()), ErrTokenRejected)
	r.Empty(seen())
}

func TestValidSender(t *testing.T) {
	t.Parallel()

	tests := []struct {
		sender string
		want   bool
	}{
		{"SolidPing", true},
		{"ACME", true},
		{"Acme Alerts", true},
		{"12345", true},
		{"A", true},
		{"", false},
		{"TwelveCharsX", false},
		{"acme-alerts", false},
		{"acmé", false},
		{"+33612345678", false},
	}

	for _, tt := range tests {
		t.Run(tt.sender, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, ValidSender(tt.sender))
		})
	}
}

func TestNewClient_Defaults(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	client := NewClient("tok", WithBaseURL(""), WithHTTPClient(nil))
	r.Equal(DefaultBaseURL, client.BaseURL)
	r.NotNil(client.HTTPClient)
	r.Equal("tok", client.Token)
}

func TestDoRequestBuildErrors(t *testing.T) {
	t.Parallel()

	client := NewClient("tok")

	_, err := client.do(context.Background(), http.MethodGet, "/x", make(chan int))
	require.Error(t, err)
	require.Contains(t, err.Error(), "encoding smsfactor request")

	_, err = client.do(context.Background(), "BAD METHOD", "/x", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "creating smsfactor request")
}

func TestDoUsesDefaultHTTPClientWhenNil(t *testing.T) {
	t.Parallel()

	srv, seen := newFakeAPI(t, http.StatusOK, `{"status":1,"credits":3}`)
	client := &Client{BaseURL: srv.URL, Token: "tok"}

	resp, err := client.do(context.Background(), http.MethodGet, "/credits", nil)
	require.NoError(t, err)
	require.Equal(t, 1, resp.Status.Int())
	require.Len(t, seen(), 1)
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (failingBody) Close() error             { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDoTransportAndBodyReadErrors(t *testing.T) {
	t.Parallel()

	failing := &Client{
		BaseURL: "http://example.invalid",
		Token:   "tok",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, io.ErrClosedPipe
		})},
	}

	_, err := failing.do(context.Background(), http.MethodGet, "/credits", nil)
	require.ErrorIs(t, err, ErrRequestFailed)
	require.ErrorIs(t, err, io.ErrClosedPipe)

	unreadable := &Client{
		BaseURL: "http://example.invalid",
		Token:   "tok",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: failingBody{}}, nil
		})},
	}

	_, err = unreadable.do(context.Background(), http.MethodGet, "/credits", nil)
	require.ErrorIs(t, err, ErrRequestFailed)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestWithHTTPClientOption(t *testing.T) {
	t.Parallel()

	custom := &http.Client{}
	require.Same(t, custom, NewClient("tok", WithHTTPClient(custom)).HTTPClient)

	// A nil client leaves the default in place.
	require.NotNil(t, NewClient("tok", WithHTTPClient(nil)).HTTPClient)
}

func TestFlexNumberDecoding(t *testing.T) {
	t.Parallel()

	var resp apiResponse

	require.NoError(t, json.Unmarshal([]byte(`{"status":"7","cost":null,"credits":""}`), &resp))
	require.Equal(t, 7, resp.Status.Int())
	require.Equal(t, 0, resp.Cost.Int())
	require.Equal(t, 0, resp.Credits.Int())

	err := json.Unmarshal([]byte(`{"status":"abc"}`), &resp)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid number")
}

func TestFlexStringDecoding(t *testing.T) {
	t.Parallel()

	var resp apiResponse

	require.NoError(t, json.Unmarshal([]byte(`{"ticket":null}`), &resp))
	require.Empty(t, string(resp.Ticket))

	require.NoError(t, json.Unmarshal([]byte(`{"ticket":"abc"}`), &resp))
	require.Equal(t, "abc", string(resp.Ticket))

	require.NoError(t, json.Unmarshal([]byte(`{"ticket":14672468}`), &resp))
	require.Equal(t, "14672468", string(resp.Ticket))

	var direct flexString

	err := direct.UnmarshalJSON([]byte(`"bad\q"`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid string")
}
