import { describe, expect, it } from "vitest";
import { lintAgainstSchema } from "./json-schema-lint";

const gopherSchema = {
  type: "object",
  properties: {
    host: { type: "string", minLength: 1 },
    port: { type: "integer", minimum: 1, maximum: 65535 },
    selector: { type: "string" },
  },
  required: ["host"],
  additionalProperties: false,
};

describe("lintAgainstSchema", () => {
  it("accepts a valid config", () => {
    expect(lintAgainstSchema({ host: "gopher.acme.com", port: 70 }, gopherSchema)).toEqual([]);
  });

  it("reports a missing required key", () => {
    expect(lintAgainstSchema({ port: 70 }, gopherSchema)).toEqual([
      { path: "host", message: "is required" },
    ]);
  });

  it("reports bounds, types and unknown keys", () => {
    const issues = lintAgainstSchema(
      { host: "", port: 70000, selector: 3, command: "rm" },
      gopherSchema,
    );
    expect(issues.map((i) => i.path)).toEqual(["host", "port", "selector", "command"]);
  });

  it("rejects a non-object config", () => {
    expect(lintAgainstSchema([], gopherSchema)).toEqual([
      { path: "", message: "must be of type object" },
    ]);
  });

  it("checks nothing without a schema", () => {
    expect(lintAgainstSchema({ anything: true }, undefined)).toEqual([]);
  });

  it("treats an integer as a number", () => {
    expect(lintAgainstSchema(3, { type: "number" })).toEqual([]);
    expect(lintAgainstSchema(3.5, { type: "integer" })).toHaveLength(1);
    expect(lintAgainstSchema("b", { enum: ["a"] })).toHaveLength(1);
  });
});
