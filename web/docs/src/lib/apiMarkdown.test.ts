import { describe, expect, test } from "bun:test";

import { renderInfo, renderOperation, renderSchema, renderTag, typeLabel } from "./apiMarkdown";

const spec = {
  info: { title: "Acme API", description: "Does things", version: "1" },
  tags: [{ name: "Widgets", description: "Widget management" }],
  components: {
    securitySchemes: { bearerAuth: { type: "http", scheme: "bearer" } },
    parameters: {
      OrgPath: { name: "org", in: "path", required: true, schema: { type: "string" }, description: "Org slug" },
    },
    responses: {
      NotFound: {
        description: "Not found",
        content: { "application/json": { schema: { $ref: "#/components/schemas/Error" } } },
      },
    },
    schemas: {
      Error: { type: "object", properties: { title: { type: "string" } } },
      Widget: {
        type: "object",
        required: ["name"],
        properties: {
          name: { type: "string", description: "Widget name" },
          kind: { type: "string", enum: ["", "big"] },
          createdAt: { type: "string", format: "date-time", nullable: true },
          parent: { $ref: "#/components/schemas/Widget" },
          tags: { type: "array", items: { $ref: "#/components/schemas/Tag" } },
        },
      },
      Tag: { type: "object", properties: { key: { type: "string" } } },
      Named: { allOf: [{ $ref: "#/components/schemas/Tag" }, { type: "object", properties: { extra: { type: "integer" } } }] },
    },
  },
  paths: {
    "/orgs/{org}/widgets": {
      parameters: [{ $ref: "#/components/parameters/OrgPath" }],
      post: {
        tags: ["Widgets"],
        summary: "Create a widget",
        description: "Creates it.",
        security: [{ bearerAuth: [] }],
        requestBody: {
          required: true,
          content: { "application/json": { schema: { $ref: "#/components/schemas/Widget" } } },
        },
        responses: {
          "201": {
            description: "Created",
            content: { "application/json": { schema: { $ref: "#/components/schemas/Widget" } } },
          },
          "404": { $ref: "#/components/responses/NotFound" },
        },
      },
    },
    "/health": { get: { summary: "Health", responses: { "200": { description: "OK" } } } },
  },
};

describe("typeLabel", () => {
  test("names refs, arrays, formats and nullability", () => {
    const widget = spec.components.schemas.Widget.properties;
    expect(typeLabel(spec, widget.parent)).toBe("Widget");
    expect(typeLabel(spec, widget.tags)).toBe("array of Tag");
    expect(typeLabel(spec, widget.createdAt)).toBe("string (date-time), nullable");
    expect(typeLabel(spec, { type: "object", additionalProperties: { type: "integer" } })).toBe("map of integer");
    expect(typeLabel(spec, { oneOf: [{ type: "string" }, { type: "integer" }] })).toBe("one of string, integer");
  });
});

describe("renderSchema", () => {
  const table = renderSchema(spec, { $ref: "#/components/schemas/Widget" });

  test("lists fields with nested paths and stops on a recursive ref", () => {
    expect(table).toContain("| `name` | string | yes | Widget name. |");
    expect(table).toContain("| `tags[].key` | string | no |  |");
    expect(table).toContain("| `parent` | Widget | no |  |");
    expect(table).not.toContain("parent.name");
  });

  test("shows an empty enum value", () => {
    expect(table).toContain('One of: `""`, `big`.');
  });

  test("merges allOf members", () => {
    const merged = renderSchema(spec, { $ref: "#/components/schemas/Named" });
    expect(merged).toContain("`key`");
    expect(merged).toContain("`extra`");
  });

  test("escapes pipes and backslashes in cells", () => {
    const t = renderSchema(spec, { type: "object", properties: { re: { type: "string", description: "a|b \\d" } } });
    expect(t).toContain("| a\\|b \\\\d. |");
  });

  test("a schema without fields gives its type", () => {
    expect(renderSchema(spec, { type: "string" })).toBe("Type: `string`");
  });
});

describe("renderOperation", () => {
  const page = renderOperation(spec, "post", "/orgs/{org}/widgets");

  test("is plain markdown, no MDX left", () => {
    expect(page).not.toMatch(/import |<[A-Z]|require\(/);
    expect(page.startsWith("# Create a widget\n\n`POST /orgs/{org}/widgets`\n\nCreates it.")).toBe(true);
  });

  test("covers auth, path-level parameters, body and responses", () => {
    expect(page).toContain("## Authentication\n\n`Authorization: Bearer <token>`.");
    expect(page).toContain("| `org` | path | string | yes | Org slug |");
    expect(page).toContain("## Request body\n\nRequired.");
    expect(page).toContain("### 201\n\nCreated");
    expect(page).toContain("### 404\n\nNot found\n\nBody: an `Error` object");
  });

  test("an operation without security is public", () => {
    expect(renderOperation(spec, "get", "/health")).toContain("None, public endpoint.");
  });

  test("an unknown operation throws", () => {
    expect(() => renderOperation(spec, "get", "/nope")).toThrow();
  });
});

test("renderTag links each operation", () => {
  const page = renderTag(spec, "Widgets", [
    { id: "create-widget", title: "Create a widget", method: "post", path: "/orgs/{org}/widgets" },
  ], "/docs/api/");
  expect(page).toBe(
    "# Widgets\n\nWidget management\n\n- [Create a widget](/docs/api/create-widget.md): `POST /orgs/{org}/widgets`\n",
  );
});

test("renderInfo lists the sections", () => {
  const page = renderInfo(spec, [{ id: "widgets", name: "Widgets" }], "/docs/api/");
  expect(page).toContain("# Acme API\n\nDoes things");
  expect(page).toContain("- [Widgets](/docs/api/widgets.md)");
});
