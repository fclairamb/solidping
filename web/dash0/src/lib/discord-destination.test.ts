import { describe, expect, it } from "vitest";
import { isDiscordDestinationSaved } from "./discord-destination";

const saved = { guild_id: "G", channel_id: "C1" };

describe("isDiscordDestinationSaved", () => {
  it("is saved when settings are equal", () => {
    expect(isDiscordDestinationSaved({ ...saved }, saved)).toBe(true);
  });
  it("is unsaved when channel_id changed", () => {
    expect(
      isDiscordDestinationSaved({ ...saved, channel_id: "C2" }, saved),
    ).toBe(false);
  });
  it("is unsaved when nothing bot-usable is stored", () => {
    expect(isDiscordDestinationSaved({ guild_id: "G", channel_id: "C1" }, {})).toBe(false);
    expect(isDiscordDestinationSaved({ channel_id: "C1" }, undefined)).toBe(false);
  });
  it("compares dm_user_id for the DM tab", () => {
    const savedDm = { ...saved, dm_user_id: "U1" };
    expect(isDiscordDestinationSaved({ ...savedDm }, savedDm)).toBe(true);
    expect(
      isDiscordDestinationSaved({ ...savedDm, dm_user_id: "U2" }, savedDm),
    ).toBe(false);
  });
});
