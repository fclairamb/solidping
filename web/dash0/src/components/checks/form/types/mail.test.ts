import { describe, expect, it } from "vitest";

import { smtpModule, type SmtpState } from "./mail";

function baseState(overrides: Partial<SmtpState> = {}): SmtpState {
  return {
    host: "mail.example.com",
    port: "",
    startTLS: false,
    tlsVerify: false,
    checkAuth: false,
    ehloDomain: "",
    expectGreeting: "",
    username: "",
    password: "",
    sendEmail: false,
    mailFrom: "",
    deliveryCheckUid: "",
    ...overrides,
  };
}

describe("smtpModule — send mode", () => {
  it("omits send-mode fields entirely when sendEmail is off (existing checks untouched)", () => {
    const { config, errors } = smtpModule.toConfig(baseState());
    expect(config.send_email).toBeUndefined();
    expect(config.mail_from).toBeUndefined();
    expect(config.delivery_check_uid).toBeUndefined();
    expect(errors).toEqual([]);
  });

  it("serializes send_email/mail_from/delivery_check_uid when sendEmail is on", () => {
    const { config, errors } = smtpModule.toConfig(
      baseState({ sendEmail: true, mailFrom: "prober@example.com", deliveryCheckUid: "check-uid-1" }),
    );
    expect(config).toMatchObject({
      send_email: true,
      mail_from: "prober@example.com",
      delivery_check_uid: "check-uid-1",
    });
    expect(errors).toEqual([]);
  });

  it("requires mail_from and delivery_check_uid when sendEmail is on", () => {
    const { errors } = smtpModule.toConfig(baseState({ sendEmail: true }));
    const fields = errors.map((e) => e.name);
    expect(fields).toContain("mail_from");
    expect(fields).toContain("delivery_check_uid");
  });

  it("does not require mail_from/delivery_check_uid when sendEmail is off", () => {
    const { errors } = smtpModule.toConfig(baseState());
    const fields = errors.map((e) => e.name);
    expect(fields).not.toContain("mail_from");
    expect(fields).not.toContain("delivery_check_uid");
  });

  it("still requires host regardless of send mode", () => {
    const { errors } = smtpModule.toConfig(baseState({ host: "" }));
    expect(errors.map((e) => e.name)).toContain("host");
  });

  it("round-trips send-mode config through fromConfig", () => {
    const state = smtpModule.fromConfig({
      host: "mail.example.com",
      send_email: true,
      mail_from: "prober@example.com",
      delivery_check_uid: "check-uid-1",
    });
    expect(state.sendEmail).toBe(true);
    expect(state.mailFrom).toBe("prober@example.com");
    expect(state.deliveryCheckUid).toBe("check-uid-1");
  });

  it("fromConfig defaults send mode off for a plain (pre-existing) SMTP config", () => {
    const state = smtpModule.fromConfig({ host: "mail.example.com", starttls: "true" });
    expect(state.sendEmail).toBe(false);
    expect(state.mailFrom).toBe("");
    expect(state.deliveryCheckUid).toBe("");
  });
});
