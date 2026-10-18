// Gopher (RFC 1436) check: the reference SolidPing JS plugin.
//
// `config` is the check config, already validated against schema.json.
// The script returns the same shape as a js check: { status, output, metrics }.

var port = config.port === undefined ? 70 : config.port;
var selector = config.selector || "";
var host = config.host.indexOf(":") >= 0 ? "[" + config.host + "]" : config.host;

var conn = tcp.connect(host + ":" + port);
if (!conn.ok) {
  return { status: "down", output: { message: "connect failed", error: conn.error } };
}

var sent = conn.write(selector + "\r\n");
if (!sent.ok) {
  return { status: "down", output: { message: "write failed", error: sent.error } };
}

// A menu ends with "." on its own line; a text document may also end with the
// server closing the connection.
var res = conn.read({ until: "\r\n.\r\n", maxBytes: 1048576 });
var data = res.data || "";

if (!res.ok && !res.eof) {
  if (res.timedOut) {
    return { status: "timeout", output: { message: "no complete answer", error: res.error } };
  }
  return { status: "down", output: { message: "read failed", error: res.error } };
}

if (data.length === 0) {
  return { status: "down", output: { message: "empty response" } };
}

var body = data;
var end = body.indexOf("\r\n.\r\n");
if (end >= 0) {
  body = body.substring(0, end + 2);
}

var lines = body.split(/\r?\n/);
var items = 0;
for (var i = 0; i < lines.length; i++) {
  if (lines[i].split("\t").length - 1 >= 3) {
    items++;
  }
}

var first = lines[0].replace(/^\s+|\s+$/g, "");
var metrics = { bytes: res.bytes, items: items };

if (first.charAt(0) === "3") {
  return { status: "down", output: { message: first, error: "server returned an error item" }, metrics: metrics };
}

if (config.contains !== undefined && data.indexOf(config.contains) < 0) {
  return {
    status: "down",
    output: { message: first, error: "response does not contain " + JSON.stringify(config.contains) },
    metrics: metrics,
  };
}

return { status: "up", output: { message: first }, metrics: metrics };
