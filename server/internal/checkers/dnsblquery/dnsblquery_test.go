package dnsblquery_test

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/dnsblquery"
)

var errServfail = errors.New("servfail")

type fakeLookuper map[string]struct {
	addrs []string
	err   error
}

func (f fakeLookuper) LookupHost(_ context.Context, host string) ([]string, error) {
	if v, ok := f[host]; ok {
		return v.addrs, v.err
	}

	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func TestReverseIPv4(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("4.3.2.1", dnsblquery.ReverseIPv4("1.2.3.4"))
	r.Equal("4.3.2.1", dnsblquery.ReverseIPv4("::ffff:1.2.3.4"))
	r.Empty(dnsblquery.ReverseIPv4("2001:db8::1"))
	r.Empty(dnsblquery.ReverseIPv4("not an ip"))
}

func TestReverseIPv6(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(
		"1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2",
		dnsblquery.ReverseIPv6("2001:db8::1"),
	)
	r.Empty(dnsblquery.ReverseIPv6("1.2.3.4"))
	r.Empty(dnsblquery.ReverseIPv6("garbage"))
}

func TestReverseIP(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("4.3.2.1", dnsblquery.ReverseIP("1.2.3.4"))
	r.Equal(
		"1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2",
		dnsblquery.ReverseIP("2001:db8::1"),
	)
	r.Empty(dnsblquery.ReverseIP("garbage"))
}

func TestIsErrorCode(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(dnsblquery.IsErrorCode("127.255.255.254"))
	r.False(dnsblquery.IsErrorCode("127.0.0.2"))
	r.False(dnsblquery.IsErrorCode("not an ip"))
	r.False(dnsblquery.IsErrorCode("::1"))
}

func TestPartitionAddrs(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	listings, errCodes := dnsblquery.PartitionAddrs([]string{"127.0.0.2", "127.255.255.255", "127.0.0.4"})
	r.Equal([]string{"127.0.0.2", "127.0.0.4"}, listings)
	r.Equal([]string{"127.255.255.255"}, errCodes)

	listings, errCodes = dnsblquery.PartitionAddrs(nil)
	r.Empty(listings)
	r.Empty(errCodes)
}

func TestIsNotFound(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.False(dnsblquery.IsNotFound(nil))
	r.True(dnsblquery.IsNotFound(&net.DNSError{IsNotFound: true}))
	r.False(dnsblquery.IsNotFound(&net.DNSError{IsTimeout: true}))
	r.False(dnsblquery.IsNotFound(errServfail))
}

func TestQueryClassifiesEveryZone(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	lookuper := fakeLookuper{
		"4.3.2.1.listed.test":  {addrs: []string{"127.0.0.2"}},
		"4.3.2.1.refused.test": {addrs: []string{"127.255.255.254"}},
		"4.3.2.1.broken.test":  {err: errServfail},
		"4.3.2.1.empty.test":   {addrs: nil},
	}

	res := dnsblquery.Query(t.Context(), lookuper, "1.2.3.4",
		[]string{"listed.test", "clean.test", "refused.test", "broken.test", "empty.test"})

	r.Equal([]string{"listed.test"}, res.Listed)
	r.Equal([]string{"clean.test"}, res.Clean)
	r.Equal([]string{"broken.test", "empty.test", "refused.test"}, res.Inconclusive)
}

func TestQueryUnparsableIPIsInconclusive(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	res := dnsblquery.Query(t.Context(), fakeLookuper{}, "garbage", []string{"a.test", "b.test"})

	r.Empty(res.Listed)
	r.Empty(res.Clean)
	r.Equal([]string{"a.test", "b.test"}, res.Inconclusive)
}
