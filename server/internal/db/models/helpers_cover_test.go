package models

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSettingsToJSONMapRoundTrips(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	twilio := &TwilioSettings{Region: "au1"}
	m, err := twilio.ToJSONMap()
	r.NoError(err)
	r.Equal("au1", m["region"])

	teams := &MSTeamsBotSettings{TenantID: "t1", Destinations: []MSTeamsDestination{{ID: "c1", Name: "ops"}}}
	m, err = teams.ToJSONMap()
	r.NoError(err)
	r.Equal("t1", m["tenant_id"])
	r.NotNil(teams.FindDestination("c1"))
	r.Nil(teams.FindDestination("missing"))
}

func TestGetEffectiveSettings(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	conn := &Integration{Settings: JSONMap{"a": 1, "b": 2}}
	r.Equal(JSONMap{"a": 1, "b": 2}, GetEffectiveSettings(conn, nil))
	r.Equal(JSONMap{"a": 1, "b": 2}, GetEffectiveSettings(conn, &CheckConnection{}))

	override := JSONMap{"b": 3, "c": 4}
	r.Equal(JSONMap{"a": 1, "b": 3, "c": 4}, GetEffectiveSettings(conn, &CheckConnection{Settings: &override}))
}

func TestSupportReplyWindow(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	now := time.Now()

	r.True((&SupportThread{Channel: SupportChannelSMS}).ReplyWindow(now).CostsMoney)
	r.False((&SupportThread{Channel: SupportChannelEmail}).ReplyWindow(now).Open)
	r.True((&SupportThread{Channel: SupportChannelTelegram}).ReplyWindow(now).Open)

	wa := &SupportThread{Channel: SupportChannelWhatsApp}
	r.False(wa.ReplyWindow(now).Open)

	recent := now.Add(-time.Hour)
	wa.LastInboundAt = &recent
	window := wa.ReplyWindow(now)
	r.True(window.Open)
	r.True(window.Expires)
	r.NotNil(window.ExpiresAt)

	old := now.Add(-48 * time.Hour)
	wa.LastInboundAt = &old
	r.False(wa.ReplyWindow(now).Open)
}

func TestScannersAcceptEveryShape(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var payload EntitlementsPayload
	r.NoError(payload.Scan(nil))
	r.Equal(EntitlementsPayloadVersion, payload.Version)
	r.NoError(payload.Scan(42))
	r.NoError(payload.Scan(`{"version":1}`))
	r.NoError(payload.Scan([]byte(`{"version":1}`)))
	r.Error(payload.Scan(`{`))

	var m JSONMap
	r.NoError(m.Scan(nil))
	r.NotNil(m)
	r.NoError(m.Scan(42))
	r.NoError(m.Scan([]byte(`{"a":1}`)))
	r.InDelta(1, m["a"], 1e-9)
	r.NoError(m.Scan(`{"b":2}`))
	r.Error(m.Scan(`{`))

	var settings StatusPageSettings
	r.NoError(settings.Scan(nil))
	r.NoError(settings.Scan(`{}`))
	r.NoError(settings.Scan([]byte(`{}`)))
	_ = settings.Scan(42)
	r.Error(settings.Scan(`{`))
}

func TestCounterAndFixedWindowState(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Zero(CounterValue(nil))
	r.Zero(CounterValue(&StateEntry{}))
	for _, v := range []any{float64(3), 3, int64(3)} {
		r.Equal(3, CounterValue(&StateEntry{Value: &JSONMap{CounterValueKey: v}}))
	}
	r.Zero(CounterValue(&StateEntry{Value: &JSONMap{CounterValueKey: "x"}}))

	now := time.Now()
	w := FixedWindow{Key: "k", Limit: 5, Window: time.Hour}
	count, start := w.FixedWindowState(nil, now)
	r.Zero(count)
	r.Equal(now, start)

	deleted := now
	_, _ = w.FixedWindowState(&StateEntry{DeletedAt: &deleted, Value: &JSONMap{}}, now)
	count, _ = w.FixedWindowState(&StateEntry{Value: &JSONMap{"windowStart": 7}}, now)
	r.Zero(count)
	count, _ = w.FixedWindowState(&StateEntry{Value: &JSONMap{"windowStart": "not a time"}}, now)
	r.Zero(count)

	began := now.Add(-time.Minute)
	for _, v := range []any{float64(2), 2, int64(2)} {
		value := FixedWindowValue(0, began)
		value["count"] = v
		count, start = w.FixedWindowState(&StateEntry{Value: &value}, now)
		r.Equal(2, count)
		r.WithinDuration(began, start, time.Millisecond)
	}

	elapsed := FixedWindowValue(4, now.Add(-2*time.Hour))
	count, _ = w.FixedWindowState(&StateEntry{Value: &elapsed}, now)
	r.Zero(count)
}

func TestDecodeStatusCountsAndKinds(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	counts, err := DecodeStatusCounts(nil)
	r.NoError(err)
	r.Empty(counts)
	empty := ""
	counts, err = DecodeStatusCounts(&empty)
	r.NoError(err)
	r.Empty(counts)
	good := `{"3": 58, "4": 2}`
	counts, err = DecodeStatusCounts(&good)
	r.NoError(err)
	r.Equal(map[int]int{3: 58, 4: 2}, counts)
	bad := `{`
	_, err = DecodeStatusCounts(&bad)
	r.Error(err)
	badKey := `{"x": 1}`
	_, err = DecodeStatusCounts(&badKey)
	r.Error(err)

	r.Equal(StatusUpdateKindIdentified, PublicationStateIdentified.UpdateKind())
	r.Equal(StatusUpdateKindMonitoring, PublicationStateMonitoring.UpdateKind())
	r.Equal(StatusUpdateKindResolved, PublicationStateResolved.UpdateKind())
	r.Equal(StatusUpdateKindInvestigating, PublicationStateInvestigating.UpdateKind())
	r.Equal(StatusUpdateKindInvestigating, PublicationState("zzz").UpdateKind())

	r.True(SubscriberScopePage.IsValid())
	r.True(SubscriberScopeIncident.IsValid())
	r.False(SubscriberScope("x").IsValid())
}
