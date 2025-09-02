package mcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSuccessResponse(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	resp := successResponse(1, map[string]string{"key": "value"})
	r.Equal("2.0", resp.JSONRPC)
	r.Equal(1, resp.ID)
	r.Nil(resp.Error)

	result, ok := resp.Result.(map[string]string)
	r.True(ok)
	r.Equal("value", result["key"])
}

func TestErrorResponse(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	resp := errorResponse(42, CodeParseError, "bad json")
	r.Equal("2.0", resp.JSONRPC)
	r.Equal(42, resp.ID)
	r.Nil(resp.Result)
	r.NotNil(resp.Error)
	r.Equal(CodeParseError, resp.Error.Code)
	r.Equal("bad json", resp.Error.Message)
}

func TestTextResult(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := textResult("hello world")
	r.False(result.IsError)
	r.Len(result.Content, 1)
	r.Equal("text", result.Content[0].Type)
	r.Equal("hello world", result.Content[0].Text)
}

func TestErrorResult(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := errorResult("something failed")
	r.True(result.IsError)
	r.Len(result.Content, 1)
	r.Equal("text", result.Content[0].Type)
	r.Equal("something failed", result.Content[0].Text)
}

func TestResponseJSONSerialization(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	t.Run("success response omits error", func(t *testing.T) {
		t.Parallel()
		resp := successResponse(1, "ok")
		data, err := json.Marshal(resp)
		r.NoError(err)

		var parsed map[string]any
		r.NoError(json.Unmarshal(data, &parsed))
		r.Equal("2.0", parsed["jsonrpc"])
		r.EqualValues(1, parsed["id"])
		r.Equal("ok", parsed["result"])
		_, hasError := parsed["error"]
		r.False(hasError, "error field should be omitted for success responses")
	})

	t.Run("error response omits result", func(t *testing.T) {
		t.Parallel()
		resp := errorResponse(1, CodeInternalError, "fail")
		data, err := json.Marshal(resp)
		r.NoError(err)

		var parsed map[string]any
		r.NoError(json.Unmarshal(data, &parsed))
		_, hasResult := parsed["result"]
		r.False(hasResult, "result field should be omitted for error responses")

		errObj, ok := parsed["error"].(map[string]any)
		r.True(ok)
		r.EqualValues(CodeInternalError, errObj["code"])
		r.Equal("fail", errObj["message"])
	})

	t.Run("notification response omits id", func(t *testing.T) {
		t.Parallel()
		resp := successResponse(nil, "ok")
		data, err := json.Marshal(resp)
		r.NoError(err)

		var parsed map[string]any
		r.NoError(json.Unmarshal(data, &parsed))
		_, hasID := parsed["id"]
		r.False(hasID, "id field should be omitted for notifications")
	})
}

func TestMarshalResult(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	t.Run("valid struct", func(t *testing.T) {
		t.Parallel()
		result := marshalResult(map[string]string{"name": "test"})
		r.False(result.IsError)
		r.Contains(result.Content[0].Text, `"name":"test"`)
	})

	t.Run("unmarshalable value", func(t *testing.T) {
		t.Parallel()
		// Channels cannot be marshaled to JSON
		result := marshalResult(make(chan int))
		r.True(result.IsError)
		r.Contains(result.Content[0].Text, "Failed to marshal")
	})
}
