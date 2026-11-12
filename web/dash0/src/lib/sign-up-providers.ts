import type { AuthProvider } from "@/api/hooks";

/**
 * Provider types that sign in an existing account but never create one.
 * Telegram returns no email address, so the server only accepts a Telegram
 * account a user already linked as a notification contact.
 */
const SIGN_IN_ONLY_PROVIDERS = new Set(["telegram"]);

/** The providers /register offers: those whose first login creates the account. */
export function signUpProviders(
  providers: AuthProvider[] | undefined,
): AuthProvider[] | undefined {
  return providers?.filter((provider) => !SIGN_IN_ONLY_PROVIDERS.has(provider.type));
}
