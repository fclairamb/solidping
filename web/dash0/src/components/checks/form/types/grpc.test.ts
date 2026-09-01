import { describe, it, expect } from "vitest";
import { grpcModule, grpcAuthSummary, grpcAdvancedSummary, isValidGrpcMetadataKey } from "./messaging";
import type { GrpcState } from "./messaging";
import enChecks from "@/locales/en/checks.json";
import frChecks from "@/locales/fr/checks.json";
import deChecks from "@/locales/de/checks.json";
import esChecks from "@/locales/es/checks.json";

const { fromConfig, toConfig } = grpcModule;

function state(overrides: Partial<GrpcState> = {}): GrpcState {
  return {
    host: "grpc.example.com",
    port: "50051",
    serviceName: "",
    tls: false,
    tlsSkipVerify: false,
    metadata: [],
    secretMetadata: [],
    secretMetadataDirty: false,
    ...overrides,
  };
}

describe("grpc fromConfig", () => {
  it("seeds every field the form now owns", () => {
    const seeded = fromConfig({
      host: "grpc.example.com",
      port: 443,
      serviceName: "my.service.v1",
      tls: true,
      tlsSkipVerify: true,
      metadata: { "x-tenant": "acme" },
    });
    expect(seeded).toMatchObject({
      host: "grpc.example.com",
      port: "443",
      serviceName: "my.service.v1",
      tls: true,
      tlsSkipVerify: true,
      metadata: [{ key: "x-tenant", value: "acme" }],
      secretMetadata: [],
      secretMetadataDirty: false,
    });
  });

  it("marks the secret section dirty only when the config actually carried values", () => {
    // Encrypted deployments never return secretMetadata, so the section must
    // start clean — that is what stops an unrelated save from wiping it.
    expect(fromConfig({ host: "h" }).secretMetadataDirty).toBe(false);
    // The plaintext fallback does return them; they must round-trip.
    expect(
      fromConfig({ host: "h", secretMetadata: { authorization: "Bearer t" } })
        .secretMetadataDirty,
    ).toBe(true);
  });
});

describe("grpc toConfig", () => {
  it("writes the TLS options only when they are on", () => {
    expect(toConfig(state()).config).not.toHaveProperty("tls");
    expect(toConfig(state()).config).not.toHaveProperty("tlsSkipVerify");
    expect(
      toConfig(state({ tls: true, tlsSkipVerify: true })).config,
    ).toMatchObject({ tls: true, tlsSkipVerify: true });
  });

  it("never writes tlsSkipVerify for a plaintext check", () => {
    // A skip-verify flag on an h2c check does nothing but look alarming.
    expect(
      toConfig(state({ tls: false, tlsSkipVerify: true })).config,
    ).not.toHaveProperty("tlsSkipVerify");
  });

  it("serializes metadata rows into a map and drops keyless rows", () => {
    const { config } = toConfig(
      state({
        metadata: [
          { key: "x-tenant", value: "acme" },
          { key: "", value: "orphan" },
        ],
      }),
    );
    expect(config.metadata).toEqual({ "x-tenant": "acme" });
  });

  it("omits secretMetadata entirely while the section is untouched", () => {
    // THE regression this guard exists for: sending `secretMetadata: {}` on
    // every save is what would wipe the stored values, since they never come
    // back on GET.
    const { config } = toConfig(
      state({ secretMetadata: [{ key: "", value: "" }] }),
    );
    expect(config).not.toHaveProperty("secretMetadata");
  });

  it("writes an explicit empty map once the section is touched and emptied", () => {
    const { config } = toConfig(
      state({ secretMetadata: [], secretMetadataDirty: true }),
    );
    expect(config.secretMetadata).toEqual({});
  });

  it("blocks a save on a metadata key the server would reject", () => {
    const { errors } = toConfig(
      state({ metadata: [{ key: "X-Tenant", value: "acme" }] }),
    );
    expect(errors.map((e) => e.name)).toContain("metadata");
  });

  it("blocks a save on a reserved key in the SECRET map too", () => {
    const { errors } = toConfig(
      state({
        secretMetadata: [{ key: "grpc-timeout", value: "1s" }],
        secretMetadataDirty: true,
      }),
    );
    expect(errors.map((e) => e.name)).toContain("metadata");
  });

  it("still requires a host", () => {
    expect(toConfig(state({ host: "" })).errors.map((e) => e.name)).toContain(
      "host",
    );
  });
});

describe("isValidGrpcMetadataKey", () => {
  it("mirrors the server rules", () => {
    expect(isValidGrpcMetadataKey("x-tenant")).toBe(true);
    expect(isValidGrpcMetadataKey("acme.tenant")).toBe(true);
    expect(isValidGrpcMetadataKey("x_tenant")).toBe(true);
    // gRPC lowercases keys on the wire, so an uppercase key is a silent rename.
    expect(isValidGrpcMetadataKey("X-Tenant")).toBe(false);
    expect(isValidGrpcMetadataKey("grpc-timeout")).toBe(false);
    expect(isValidGrpcMetadataKey("trace-bin")).toBe(false);
    expect(isValidGrpcMetadataKey("x tenant")).toBe(false);
  });
});

describe("grpc section summaries", () => {
  it("summarizes the advanced section", () => {
    expect(grpcAdvancedSummary(state())).toEqual({
      text: "",
      customized: false,
    });
    expect(
      grpcAdvancedSummary(
        state({
          tls: true,
          tlsSkipVerify: true,
          metadata: [{ key: "x-tenant", value: "acme" }],
        }),
      ),
    ).toEqual({
      text: "TLS verification off · 1 metadata entry",
      customized: true,
    });
  });

  it("reports stored secret metadata the form state cannot see", () => {
    // configPrivateKeys is the only evidence the form has that an encrypted
    // value exists — without it the section would read "none" for a check that
    // very much carries a credential.
    expect(grpcAuthSummary(state(), ["secretMetadata"])).toEqual({
      text: "secret metadata",
      customized: true,
    });
    expect(grpcAuthSummary(state())).toEqual({ text: "none", customized: false });
    expect(
      grpcAuthSummary(
        state({
          secretMetadata: [{ key: "authorization", value: "Bearer t" }],
          secretMetadataDirty: true,
        }),
      ),
    ).toEqual({ text: "1 secret metadata entry", customized: true });
  });
});

// ---------------------------------------------------------------------------
// Locale completeness. A key missing from one bundle renders the raw key path
// in that language — a visible defect no type check catches.
// ---------------------------------------------------------------------------

const LOCALES = {
  en: enChecks,
  fr: frChecks,
  de: deChecks,
  es: esChecks,
};

const REQUIRED_GRPC_KEYS = [
  "tlsSkipVerify",
  "tlsSkipVerifyWarning",
  "metadata",
  "metadataDescription",
  "addMetadata",
  "secretMetadata",
  "secretMetadataDescription",
  "secretMetadataEncrypted",
  "addSecretMetadata",
  "metadataKeyPlaceholder",
  "metadataValuePlaceholder",
];

describe("grpc locale completeness", () => {
  for (const [locale, bundle] of Object.entries(LOCALES)) {
    it(`${locale} defines every grpc form key`, () => {
      const grpc = (bundle as Record<string, unknown>).grpc as
        | Record<string, unknown>
        | undefined;
      expect(grpc, `${locale}/checks.json has no "grpc" section`).toBeTruthy();
      for (const key of REQUIRED_GRPC_KEYS) {
        expect(typeof grpc?.[key], `${locale} grpc.${key}`).toBe("string");
        expect((grpc?.[key] as string).length, `${locale} grpc.${key}`).toBeGreaterThan(0);
      }
    });

    it(`${locale} adds no grpc key the form does not use`, () => {
      const grpc = (bundle as Record<string, unknown>).grpc as Record<
        string,
        unknown
      >;
      expect(Object.keys(grpc).sort()).toEqual([...REQUIRED_GRPC_KEYS].sort());
    });
  }
});
