import { describe, expect, it } from "vitest";

import { signUpProviders } from "@/lib/sign-up-providers";

describe("signUpProviders", () => {
  it("drops Telegram, which signs in linked accounts only", () => {
    expect(
      signUpProviders([
        { name: "GitHub", type: "github" },
        { name: "Telegram", type: "telegram" },
      ]),
    ).toEqual([{ name: "GitHub", type: "github" }]);
  });

  it("passes undefined through", () => {
    expect(signUpProviders(undefined)).toBeUndefined();
  });
});
