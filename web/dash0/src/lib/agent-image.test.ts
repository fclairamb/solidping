import { describe, expect, it } from "vitest";
import { agentImage, releaseVersion } from "./agent-image";

describe("agentImage", () => {
  it("uses the server registry on a tagged HTTPS server", () => {
    expect(agentImage("1.2.3", "https://ping.acme.com")).toEqual({
      image: "ping.acme.com/solidping:current",
      followsServer: true,
      pinnedImage: "ghcr.io/fclairamb/solidping:1.2.3",
    });
  });

  it("keeps a non-default port in the registry host", () => {
    expect(agentImage("1.2.3", "https://ping.acme.com:8443").image).toBe(
      "ping.acme.com:8443/solidping:current",
    );
  });

  it("pins the exact upstream tag on an http server", () => {
    expect(agentImage("v1.2.3", "http://localhost:4000")).toEqual({
      image: "ghcr.io/fclairamb/solidping:1.2.3",
      followsServer: false,
      pinnedImage: "ghcr.io/fclairamb/solidping:1.2.3",
    });
  });

  it("falls back to :latest on a dev or untagged build", () => {
    for (const version of [undefined, "", "dev", "1.2.3-4-gdeadbee", "1.2.3-4-gdeadbee-dirty"]) {
      expect(agentImage(version, "https://ping.acme.com")).toEqual({
        image: "ghcr.io/fclairamb/solidping:latest",
        followsServer: false,
        pinnedImage: null,
      });
    }
  });

  it("accepts release candidates", () => {
    expect(releaseVersion("1.2.3-rc.1")).toBe("1.2.3-rc.1");
  });
});
