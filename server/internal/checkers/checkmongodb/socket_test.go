package checkmongodb_test

import (
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkmongodb"
)

const (
	opReply = 1
	opQuery = 2004
	opMsg   = 2013
)

// helloReply is the one document the fake server answers to every command
// (hello, isMaster, ping): a standalone, writable server.
func helloReply(tb testing.TB) []byte {
	tb.Helper()

	doc, err := bson.Marshal(bson.D{
		{Key: "ok", Value: 1.0},
		{Key: "ismaster", Value: true},
		{Key: "isWritablePrimary", Value: true},
		{Key: "helloOk", Value: true},
		{Key: "maxBsonObjectSize", Value: int32(16777216)},
		{Key: "maxMessageSizeBytes", Value: int32(48000000)},
		{Key: "maxWriteBatchSize", Value: int32(100000)},
		{Key: "minWireVersion", Value: int32(0)},
		{Key: "maxWireVersion", Value: int32(17)},
	})
	require.NoError(tb, err)

	return doc
}

// serveMongo answers each OP_QUERY (legacy handshake) with an OP_REPLY and
// each OP_MSG with an OP_MSG, both carrying the hello document.
func serveMongo(conn net.Conn, doc []byte) {
	defer func() { _ = conn.Close() }()

	for {
		header := make([]byte, 16)
		if _, err := io.ReadFull(conn, header); err != nil {
			return
		}

		length := binary.LittleEndian.Uint32(header[0:4])
		requestID := binary.LittleEndian.Uint32(header[4:8])
		opCode := binary.LittleEndian.Uint32(header[12:16])

		if _, err := io.CopyN(io.Discard, conn, int64(length)-16); err != nil {
			return
		}

		var body []byte

		switch opCode {
		case opQuery:
			// responseFlags, cursorID, startingFrom, numberReturned, documents
			body = binary.LittleEndian.AppendUint32(nil, 8)
			body = binary.LittleEndian.AppendUint64(body, 0)
			body = binary.LittleEndian.AppendUint32(body, 0)
			body = binary.LittleEndian.AppendUint32(body, 1)
			body = append(body, doc...)
		case opMsg:
			// flagBits, section kind 0, document
			body = binary.LittleEndian.AppendUint32(nil, 0)
			body = append(body, 0)
			body = append(body, doc...)
		default:
			return
		}

		out := binary.LittleEndian.AppendUint32(nil, uint32(16+len(body)))
		out = binary.LittleEndian.AppendUint32(out, 1)
		out = binary.LittleEndian.AppendUint32(out, requestID)
		out = binary.LittleEndian.AppendUint32(out, map[uint32]uint32{opQuery: opReply, opMsg: opMsg}[opCode])
		out = append(out, body...)

		if _, err := conn.Write(out); err != nil {
			return
		}
	}
}

func mongoSocketServer(t *testing.T) (string, *atomic.Int32) {
	t.Helper()

	dir, err := os.MkdirTemp("", "mgo")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "m.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	require.NoError(t, err)

	t.Cleanup(func() { _ = listener.Close() })

	doc := helloReply(t)

	var accepted atomic.Int32

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			accepted.Add(1)

			go serveMongo(conn, doc)
		}
	}()

	return path, &accepted
}

func runMongoSocket(t *testing.T, path string, allow []string) *checkerdef.Result {
	t.Helper()

	cfg := &checkmongodb.MongoDBConfig{}
	require.NoError(t, cfg.FromMap(map[string]any{"socket": path, "timeout": "3s"}))

	ctx := checkerdef.WithLocalSocketAllowlist(t.Context(), allow)

	result, err := (&checkmongodb.MongoDBChecker{}).Execute(ctx, cfg)
	require.NoError(t, err)

	return result
}

func TestMongoDBSocket(t *testing.T) {
	t.Parallel()

	path, _ := mongoSocketServer(t)
	allow := []string{filepath.Dir(path) + "/"}

	t.Run("allowlisted socket is up", func(t *testing.T) {
		t.Parallel()

		result := runMongoSocket(t, path, allow)
		require.Equal(t, checkerdef.StatusUp, result.Status, result.Output)
		require.Equal(t, path, result.Output["socket"])
	})

	t.Run("path outside the allowlist is refused without dialing", func(t *testing.T) {
		t.Parallel()

		// Its own server: the count must not see the other subtests' dials.
		own, accepted := mongoSocketServer(t)

		result := runMongoSocket(t, own, []string{"/run/other/"})
		require.NotEqual(t, checkerdef.StatusUp, result.Status)

		result = runMongoSocket(t, own, nil)
		require.NotEqual(t, checkerdef.StatusUp, result.Status, "an empty allowlist refuses every path")
		require.Zero(t, accepted.Load(), "a refused path must not be dialed")
	})
}

func TestMongoDBSocketConfig(t *testing.T) {
	t.Parallel()

	validate := func(cfg map[string]any) (*checkerdef.CheckSpec, error) {
		spec := &checkerdef.CheckSpec{Config: cfg}

		return spec, (&checkmongodb.MongoDBChecker{}).Validate(spec)
	}

	spec, err := validate(map[string]any{"socket": "/tmp/mongodb-27017.sock"})
	require.NoError(t, err)
	require.Equal(t, "mongodb-tmp-mongodb-27017-sock", spec.Slug)

	_, err = validate(map[string]any{"socket": "/tmp/mongodb-27017.sock", "host": "acme.com"})
	require.ErrorContains(t, err, "mutually exclusive")

	_, err = validate(map[string]any{"socket": "mongodb.sock"})
	require.Error(t, err, "relative path")

	cfg := &checkmongodb.MongoDBConfig{}
	require.NoError(t, cfg.FromMap(map[string]any{"url": "mongodb+unix:///tmp/mongodb-27017.sock?db=app&user=monitor"}))
	require.Equal(t, "/tmp/mongodb-27017.sock", cfg.Socket)
	require.Equal(t, "app", cfg.Database)
	require.Equal(t, "monitor", cfg.Username)
}
