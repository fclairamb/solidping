// A small JSON Schema checker for the plugin check config editor (spec
// 2026-10-17-05). It covers the keywords a plugin config schema uses — type,
// required, properties, additionalProperties: false, enum, minimum/maximum,
// minLength/maxLength — so the editor can flag a mistake as it is typed. The
// server validates with a full draft 2020-12 implementation and stays the
// authority: anything this does not understand is simply not checked here.

export interface SchemaIssue {
  /** Dotted path of the offending key ("" for the config itself). */
  path: string;
  message: string;
}

type Schema = Record<string, unknown>;

function typeOf(value: unknown): string {
  if (value === null) return "null";
  if (Array.isArray(value)) return "array";
  if (typeof value === "number") return Number.isInteger(value) ? "integer" : "number";
  return typeof value;
}

function matchesType(value: unknown, expected: string): boolean {
  const actual = typeOf(value);
  if (expected === "number") return actual === "number" || actual === "integer";
  return actual === expected;
}

function join(path: string, key: string): string {
  return path ? `${path}.${key}` : key;
}

function check(value: unknown, schema: Schema, path: string, issues: SchemaIssue[]) {
  const types = schema.type === undefined ? [] : ([] as unknown[]).concat(schema.type);
  if (types.length > 0 && !types.some((t) => typeof t === "string" && matchesType(value, t))) {
    issues.push({ path, message: `must be of type ${types.join(" or ")}` });
    return;
  }

  if (Array.isArray(schema.enum) && !schema.enum.some((e) => JSON.stringify(e) === JSON.stringify(value))) {
    issues.push({ path, message: `must be one of ${schema.enum.map((e) => JSON.stringify(e)).join(", ")}` });
  }

  if (typeof value === "number") {
    if (typeof schema.minimum === "number" && value < schema.minimum) {
      issues.push({ path, message: `must be >= ${schema.minimum}` });
    }
    if (typeof schema.maximum === "number" && value > schema.maximum) {
      issues.push({ path, message: `must be <= ${schema.maximum}` });
    }
  }

  if (typeof value === "string") {
    if (typeof schema.minLength === "number" && value.length < schema.minLength) {
      issues.push({ path, message: `must be at least ${schema.minLength} characters` });
    }
    if (typeof schema.maxLength === "number" && value.length > schema.maxLength) {
      issues.push({ path, message: `must be at most ${schema.maxLength} characters` });
    }
  }

  if (typeOf(value) !== "object") return;

  const object = value as Record<string, unknown>;
  const properties = (schema.properties ?? {}) as Record<string, Schema>;

  if (Array.isArray(schema.required)) {
    for (const key of schema.required) {
      if (typeof key === "string" && !(key in object)) {
        issues.push({ path: join(path, key), message: "is required" });
      }
    }
  }

  for (const [key, child] of Object.entries(object)) {
    const childSchema = properties[key];
    if (childSchema) {
      check(child, childSchema, join(path, key), issues);
    } else if (schema.additionalProperties === false) {
      issues.push({ path: join(path, key), message: "is not an allowed key" });
    }
  }
}

/** Checks a value against a JSON Schema, returning every issue found. */
export function lintAgainstSchema(value: unknown, schema: Schema | undefined): SchemaIssue[] {
  if (!schema) return [];
  const issues: SchemaIssue[] = [];
  check(value, schema, "", issues);
  return issues;
}
