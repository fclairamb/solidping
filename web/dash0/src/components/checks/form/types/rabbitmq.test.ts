import { describe, expect, it } from "vitest";

import { rabbitmqModule, type RabbitmqState } from "./database";

function baseState(overrides: Partial<RabbitmqState> = {}): RabbitmqState {
  return {
    host: "rabbitmq.example.com",
    port: "",
    username: "guest",
    password: "",
    vhost: "",
    queue: "",
    tls: false,
    mode: "amqp",
    managementPort: "",
    memoryUsedWarning: "",
    memoryUsedCritical: "",
    diskFreeWarning: "",
    diskFreeCritical: "",
    ...overrides,
  };
}

describe("rabbitmqModule.toConfig", () => {
  it("serializes an amqp config with queue/vhost, no threshold keys", () => {
    const { config, errors } = rabbitmqModule.toConfig(
      baseState({ vhost: "/prod", queue: "my-queue", tls: true }),
    );
    expect(errors).toEqual([]);
    expect(config).toMatchObject({
      host: "rabbitmq.example.com",
      username: "guest",
      vhost: "/prod",
      queue: "my-queue",
      tls: true,
    });
    expect(config.mode).toBeUndefined();
    expect(config.memoryUsedWarning).toBeUndefined();
  });

  it("serializes a management config with thresholds and drops queue/vhost", () => {
    const { config, errors } = rabbitmqModule.toConfig(
      baseState({
        mode: "management",
        managementPort: "15672",
        memoryUsedWarning: "70%",
        memoryUsedCritical: "90%",
        diskFreeWarning: "20GiB",
        diskFreeCritical: "5GiB",
        vhost: "/should-not-be-sent",
        queue: "should-not-be-sent",
      }),
    );
    expect(errors).toEqual([]);
    expect(config).toMatchObject({
      mode: "management",
      managementPort: 15672,
      memoryUsedWarning: "70%",
      memoryUsedCritical: "90%",
      diskFreeWarning: "20GiB",
      diskFreeCritical: "5GiB",
    });
    expect(config.queue).toBeUndefined();
    expect(config.vhost).toBeUndefined();
  });

  it("requires a host", () => {
    expect(
      rabbitmqModule.toConfig(baseState({ host: "" })).errors.map((e) => e.name),
    ).toContain("host");
  });
});

describe("rabbitmqModule.fromConfig", () => {
  it("round-trips an amqp config", () => {
    const config = {
      host: "rabbitmq.example.com",
      port: 5672,
      username: "guest",
      vhost: "/prod",
      queue: "my-queue",
      tls: true,
    };
    const state = rabbitmqModule.fromConfig(config);
    expect(state.mode).toBe("amqp");
    expect(state.vhost).toBe("/prod");
    expect(state.queue).toBe("my-queue");
    expect(state.tls).toBe(true);

    expect(rabbitmqModule.toConfig(state).config).toMatchObject(config);
  });

  it("round-trips a management config with all four thresholds", () => {
    const config = {
      host: "rabbitmq.example.com",
      username: "guest",
      mode: "management",
      managementPort: 15672,
      memoryUsedWarning: "70%",
      memoryUsedCritical: "1.5GiB",
      diskFreeWarning: "20GiB",
      diskFreeCritical: "5GiB",
    };
    const state = rabbitmqModule.fromConfig(config);
    expect(state.mode).toBe("management");
    expect(state.managementPort).toBe("15672");
    expect(state.memoryUsedWarning).toBe("70%");
    expect(state.memoryUsedCritical).toBe("1.5GiB");
    expect(state.diskFreeWarning).toBe("20GiB");
    expect(state.diskFreeCritical).toBe("5GiB");

    expect(rabbitmqModule.toConfig(state).config).toMatchObject(config);
  });

  it("defaults mode to amqp for an empty config", () => {
    const state = rabbitmqModule.fromConfig({});
    expect(state.mode).toBe("amqp");
  });
});
