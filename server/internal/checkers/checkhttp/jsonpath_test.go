package checkhttp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAssertionNode_Evaluate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		jsonData string
		node     AssertionNode
		wantPass bool
	}{
		{
			name:     "eq pass",
			jsonData: `{"status":"healthy"}`,
			node:     AssertionNode{Type: NodeTypeAssertion, Path: "$.status", Operator: "eq", Value: "healthy"},
			wantPass: true,
		},
		{
			name:     "eq fail",
			jsonData: `{"status":"degraded"}`,
			node:     AssertionNode{Type: NodeTypeAssertion, Path: "$.status", Operator: "eq", Value: "healthy"},
			wantPass: false,
		},
		{
			name:     "neq pass",
			jsonData: `{"status":"healthy"}`,
			node:     AssertionNode{Type: NodeTypeAssertion, Path: "$.status", Operator: "neq", Value: "down"},
			wantPass: true,
		},
		{
			name:     "neq fail",
			jsonData: `{"status":"down"}`,
			node:     AssertionNode{Type: NodeTypeAssertion, Path: "$.status", Operator: "neq", Value: "down"},
			wantPass: false,
		},
		{
			name:     "gt pass",
			jsonData: `{"count":10}`,
			node:     AssertionNode{Type: NodeTypeAssertion, Path: "$.count", Operator: "gt", Value: "5"},
			wantPass: true,
		},
		{
			name:     "gt fail",
			jsonData: `{"count":3}`,
			node:     AssertionNode{Type: NodeTypeAssertion, Path: "$.count", Operator: "gt", Value: "5"},
			wantPass: false,
		},
		{
			name:     "gte pass equal",
			jsonData: `{"count":5}`,
			node:     AssertionNode{Type: NodeTypeAssertion, Path: "$.count", Operator: "gte", Value: "5"},
			wantPass: true,
		},
		{
			name:     "lt pass",
			jsonData: `{"count":3}`,
			node:     AssertionNode{Type: NodeTypeAssertion, Path: "$.count", Operator: "lt", Value: "5"},
			wantPass: true,
		},
		{
			name:     "lt fail",
			jsonData: `{"count":10}`,
			node:     AssertionNode{Type: NodeTypeAssertion, Path: "$.count", Operator: "lt", Value: "5"},
			wantPass: false,
		},
		{
			name:     "lte pass equal",
			jsonData: `{"count":5}`,
			node:     AssertionNode{Type: NodeTypeAssertion, Path: "$.count", Operator: "lte", Value: "5"},
			wantPass: true,
		},
		{
			name:     "contains pass",
			jsonData: `{"message":"all ok"}`,
			node:     AssertionNode{Type: NodeTypeAssertion, Path: "$.message", Operator: "contains", Value: "ok"},
			wantPass: true,
		},
		{
			name:     "contains fail",
			jsonData: `{"message":"error"}`,
			node:     AssertionNode{Type: NodeTypeAssertion, Path: "$.message", Operator: "contains", Value: "ok"},
			wantPass: false,
		},
		{
			name:     "regex pass",
			jsonData: `{"version":"v1.2.3"}`,
			node:     AssertionNode{Type: NodeTypeAssertion, Path: "$.version", Operator: "regex", Value: `^v\d+\.\d+`},
			wantPass: true,
		},
		{
			name:     "regex fail",
			jsonData: `{"version":"1.2.3"}`,
			node:     AssertionNode{Type: NodeTypeAssertion, Path: "$.version", Operator: "regex", Value: `^v\d+`},
			wantPass: false,
		},
		{
			name:     "exists pass",
			jsonData: `{"data":null}`,
			node:     AssertionNode{Type: NodeTypeAssertion, Path: "$.data", Operator: "exists"},
			wantPass: true,
		},
		{
			name:     "exists fail",
			jsonData: `{}`,
			node:     AssertionNode{Type: NodeTypeAssertion, Path: "$.data", Operator: "exists"},
			wantPass: false,
		},
		{
			name:     "not_exists pass",
			jsonData: `{}`,
			node:     AssertionNode{Type: NodeTypeAssertion, Path: "$.missing", Operator: "not_exists"},
			wantPass: true,
		},
		{
			name:     "not_exists fail",
			jsonData: `{"missing":"present"}`,
			node:     AssertionNode{Type: NodeTypeAssertion, Path: "$.missing", Operator: "not_exists"},
			wantPass: false,
		},
		{
			name:     "path not found",
			jsonData: `{"other":"value"}`,
			node:     AssertionNode{Type: NodeTypeAssertion, Path: "$.missing", Operator: "eq", Value: "x"},
			wantPass: false,
		},
		{
			name:     "AND all pass",
			jsonData: `{"status":"healthy","count":10}`,
			node: AssertionNode{Type: NodeTypeAnd, Children: []AssertionNode{
				{Type: NodeTypeAssertion, Path: "$.status", Operator: "eq", Value: "healthy"},
				{Type: NodeTypeAssertion, Path: "$.count", Operator: "gt", Value: "5"},
			}},
			wantPass: true,
		},
		{
			name:     "AND one fails",
			jsonData: `{"status":"down","count":10}`,
			node: AssertionNode{Type: NodeTypeAnd, Children: []AssertionNode{
				{Type: NodeTypeAssertion, Path: "$.status", Operator: "eq", Value: "healthy"},
				{Type: NodeTypeAssertion, Path: "$.count", Operator: "gt", Value: "5"},
			}},
			wantPass: false,
		},
		{
			name:     "OR one passes",
			jsonData: `{"region":"eu-west-1"}`,
			node: AssertionNode{Type: NodeTypeOr, Children: []AssertionNode{
				{Type: NodeTypeAssertion, Path: "$.region", Operator: "eq", Value: "eu-west-1"},
				{Type: NodeTypeAssertion, Path: "$.region", Operator: "eq", Value: "us-east-1"},
			}},
			wantPass: true,
		},
		{
			name:     "OR all fail",
			jsonData: `{"region":"ap-southeast-1"}`,
			node: AssertionNode{Type: NodeTypeOr, Children: []AssertionNode{
				{Type: NodeTypeAssertion, Path: "$.region", Operator: "eq", Value: "eu-west-1"},
				{Type: NodeTypeAssertion, Path: "$.region", Operator: "eq", Value: "us-east-1"},
			}},
			wantPass: false,
		},
		{
			name:     "nested AND/OR",
			jsonData: `{"status":"healthy","region":"eu-west-1"}`,
			node: AssertionNode{Type: NodeTypeAnd, Children: []AssertionNode{
				{Type: NodeTypeAssertion, Path: "$.status", Operator: "eq", Value: "healthy"},
				{Type: NodeTypeOr, Children: []AssertionNode{
					{Type: NodeTypeAssertion, Path: "$.region", Operator: "eq", Value: "eu-west-1"},
					{Type: NodeTypeAssertion, Path: "$.region", Operator: "eq", Value: "us-east-1"},
				}},
			}},
			wantPass: true,
		},
		{
			name:     "invalid JSONPath",
			jsonData: `{"status":"ok"}`,
			node:     AssertionNode{Type: NodeTypeAssertion, Path: "$[invalid", Operator: "eq", Value: "ok"},
			wantPass: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			var data any
			r.NoError(json.Unmarshal([]byte(tc.jsonData), &data))

			result := tc.node.Evaluate(data)
			r.Equal(tc.wantPass, result.Pass,
				"expected pass=%v, got pass=%v (error: %s)", tc.wantPass, result.Pass, result.Error)
		})
	}
}

func TestAssertionNode_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		node    AssertionNode
		wantErr bool
	}{
		{
			name:    "valid leaf",
			node:    AssertionNode{Type: NodeTypeAssertion, Path: "$.status", Operator: "eq", Value: "ok"},
			wantErr: false,
		},
		{
			name:    "missing path",
			node:    AssertionNode{Type: NodeTypeAssertion, Path: "", Operator: "eq", Value: "ok"},
			wantErr: true,
		},
		{
			name:    "invalid operator",
			node:    AssertionNode{Type: NodeTypeAssertion, Path: "$.x", Operator: "invalid", Value: "ok"},
			wantErr: true,
		},
		{
			name:    "exists with value",
			node:    AssertionNode{Type: NodeTypeAssertion, Path: "$.x", Operator: "exists", Value: "bad"},
			wantErr: true,
		},
		{
			name:    "numeric operator with non-numeric value",
			node:    AssertionNode{Type: NodeTypeAssertion, Path: "$.x", Operator: "gt", Value: "abc"},
			wantErr: true,
		},
		{
			name:    "invalid regex value",
			node:    AssertionNode{Type: NodeTypeAssertion, Path: "$.x", Operator: "regex", Value: "[invalid"},
			wantErr: true,
		},
		{
			name: "valid AND group",
			node: AssertionNode{Type: NodeTypeAnd, Children: []AssertionNode{
				{Type: NodeTypeAssertion, Path: "$.x", Operator: "eq", Value: "ok"},
			}},
			wantErr: false,
		},
		{
			name:    "empty AND group",
			node:    AssertionNode{Type: NodeTypeAnd, Children: []AssertionNode{}},
			wantErr: true,
		},
		{
			name:    "unknown type",
			node:    AssertionNode{Type: "unknown"},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			err := tc.node.Validate()
			if tc.wantErr {
				r.Error(err)
			} else {
				r.NoError(err)
			}
		})
	}
}
