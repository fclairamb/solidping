import { describe, expect, it } from "vitest";
import { backoffDelay, createSSEParser, parseHintKinds } from "./live-events";

describe("createSSEParser", () => {
  it("parses a complete event frame", () => {
    const events: Array<[string, string]> = [];
    const feed = createSSEParser((event, data) => events.push([event, data]));

    feed('event: hint\ndata: {"kinds":["results"]}\n\n');

    expect(events).toEqual([["hint", '{"kinds":["results"]}']]);
  });

  it("handles frames split across chunk boundaries", () => {
    const events: Array<[string, string]> = [];
    const feed = createSSEParser((event, data) => events.push([event, data]));

    feed("event: hel");
    feed("lo\nda");
    feed('ta: {"protocol":1}\n');
    feed("\n");

    expect(events).toEqual([["hello", '{"protocol":1}']]);
  });

  it("parses multiple frames and ignores comment pings", () => {
    const events: Array<[string, string]> = [];
    const feed = createSSEParser((event, data) => events.push([event, data]));

    feed(
      'event: hello\ndata: {"protocol":1}\n\n' +
        ": ping\n\n" +
        "event: resync\ndata: {}\n\n" +
        'event: hint\ndata: {"kinds":["checks","incidents"]}\n\n',
    );

    expect(events).toEqual([
      ["hello", '{"protocol":1}'],
      ["resync", "{}"],
      ["hint", '{"kinds":["checks","incidents"]}'],
    ]);
  });

  it("handles CRLF line endings", () => {
    const events: Array<[string, string]> = [];
    const feed = createSSEParser((event, data) => events.push([event, data]));

    feed("event: hint\r\ndata: {}\r\n\r\n");

    expect(events).toEqual([["hint", "{}"]]);
  });
});

describe("parseHintKinds", () => {
  it("extracts kinds from a hint payload", () => {
    expect(parseHintKinds('{"kinds":["results","jobs"]}')).toEqual([
      "results",
      "jobs",
    ]);
  });

  it("returns [] for malformed payloads", () => {
    expect(parseHintKinds("{not json")).toEqual([]);
    expect(parseHintKinds("{}")).toEqual([]);
    expect(parseHintKinds('{"kinds":"nope"}')).toEqual([]);
    expect(parseHintKinds('{"kinds":[1,2]}')).toEqual([]);
  });
});

describe("backoffDelay", () => {
  it("grows exponentially and caps at 30s", () => {
    const noJitter = () => 0.5; // jitter factor 1.0
    expect(backoffDelay(0, noJitter)).toBe(1_000);
    expect(backoffDelay(1, noJitter)).toBe(2_000);
    expect(backoffDelay(3, noJitter)).toBe(8_000);
    expect(backoffDelay(10, noJitter)).toBe(30_000);
    expect(backoffDelay(30, noJitter)).toBe(30_000);
  });

  it("applies bounded jitter", () => {
    expect(backoffDelay(0, () => 0)).toBe(700);
    expect(backoffDelay(0, () => 1)).toBe(1_300);
  });
});
