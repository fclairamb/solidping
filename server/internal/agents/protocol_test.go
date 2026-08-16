package agents_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestToAgentJobNeverShipsConfigPrivate is the load-bearing security assertion
// for the agent dispatch path: the job's own config_private symmetric envelope
// must never cross to an agent. ToAgentJob ships only the public config and the
// region-sealed blob.
func TestToAgentJobNeverShipsConfigPrivate(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	secretEnvelope := "SUPER-SECRET-SYMMETRIC-ENVELOPE"
	sealed := "SEALED-TO-REGION-AGENTS"
	region := "@acme/paris"

	job := &models.CheckJob{
		UID:             "job-1",
		CheckUID:        "check-1",
		OrganizationUID: "org-1",
		Type:            "http",
		Config:          models.JSONMap{"url": "http://internal.private/health"},
		ConfigPrivate:   &secretEnvelope,
		ConfigSealed:    &sealed,
		Region:          &region,
	}

	wire := agents.ToAgentJob(job)
	raw, err := json.Marshal(wire)
	r.NoError(err)

	r.NotContains(string(raw), secretEnvelope, "config_private must never cross to an agent")
	r.Contains(string(raw), sealed, "the region-sealed envelope is shipped verbatim")
	// The wire shape has no field that could carry the symmetric envelope.
	r.NotContains(string(raw), "configPrivate")
}

// TestAgentJobTunnelRoundTripsAndHidesPrivate proves the tunnel block survives
// the wire round-trip and structurally cannot carry config_private: only the
// SSH check's public config plus its region-sealed envelope cross.
func TestAgentJobTunnelRoundTripsAndHidesPrivate(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sealed := "SSH-BASTION-SEALED-ENVELOPE"
	region := "@acme/paris"

	job := agents.AgentJob{
		UID:      "job-1",
		CheckUID: "check-1",
		Type:     "http",
		Config: map[string]any{
			"url":                "http://internal.private/health",
			"tunnelCheckUid":     "ssh-1",
			"someUnrelatedField": true,
		},
		Region: &region,
		Period: "1m0s",
		Tunnel: &agents.AgentJobTunnel{
			CheckUID: "ssh-1",
			// PUBLIC config only — a real secret value must never be placed here.
			Config: map[string]any{
				"host":                 "bastion.internal",
				"port":                 float64(22),
				"username":             "probe",
				"expected_fingerprint": "SHA256:abc",
			},
			ConfigSealed: &sealed,
		},
	}

	raw, err := json.Marshal(job)
	r.NoError(err)
	r.Contains(string(raw), "\"tunnel\"")
	r.Contains(string(raw), sealed)

	var back agents.AgentJob
	r.NoError(json.Unmarshal(raw, &back))
	r.NotNil(back.Tunnel)
	r.Equal("ssh-1", back.Tunnel.CheckUID)
	r.Equal("bastion.internal", back.Tunnel.Config["host"])
	r.NotNil(back.Tunnel.ConfigSealed)
	r.Equal(sealed, *back.Tunnel.ConfigSealed)

	// A common untunneled job serializes with no tunnel block at all.
	plain := agents.ToAgentJob(&models.CheckJob{UID: "j", CheckUID: "c", Type: "http", Period: 0})
	plainRaw, err := json.Marshal(plain)
	r.NoError(err)
	r.NotContains(string(plainRaw), "\"tunnel\"")
}

// TestClaimFrameCapabilitiesSurviveThreeStates is the wire half of spec
// 2026-08-16-02's central invariant.
//
// The claim frame must round-trip THREE distinct answers: an agent that did not
// report (nil), one that reported an empty set ("I have none"), and one that
// reported a set. This is why the field deliberately carries no `omitempty` —
// omitempty would erase an empty non-nil slice and make "none" indistinguishable
// from "not reported", collapsing the three states to two on the wire.
func TestClaimFrameCapabilitiesSurviveThreeStates(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		caps []string
		json string
	}{
		{"not-reported", nil, `"capabilities":null`},
		{"reported-none", []string{}, `"capabilities":[]`},
		{"reported-set", []string{"ipv4", "ipv6"}, `"capabilities":["ipv4","ipv6"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			encoded, err := json.Marshal(&agents.ClientFrame{
				Type: agents.MsgTypeClaim, MaxJobs: 4, Capabilities: tc.caps,
			})
			r.NoError(err)
			r.Contains(string(encoded), tc.json,
				"the empty set must stay on the wire — omitempty here would lose a state")

			var back agents.ClientFrame
			r.NoError(json.Unmarshal(encoded, &back))

			if tc.caps == nil {
				r.Nil(back.Capabilities)

				return
			}

			r.NotNil(back.Capabilities, "a reported empty set must not decode as nil")
			r.Equal(tc.caps, back.Capabilities)
		})
	}
}

// TestClaimFrameFromAgentPredatingCapabilities: a frame with no capabilities
// field at all decodes to nil, i.e. "unknown", which is what leaves the stored
// column untouched and makes the agent/server rollout order irrelevant.
func TestClaimFrameFromAgentPredatingCapabilities(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var frame agents.ClientFrame
	r.NoError(json.Unmarshal([]byte(`{"type":"claim","maxJobs":4}`), &frame))
	r.Nil(frame.Capabilities)
}
