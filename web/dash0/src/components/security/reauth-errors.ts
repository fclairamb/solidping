import { useTranslation } from "react-i18next";
import { ApiError } from "@/api/client";

// Errors of the re-authentication a new sign-in factor needs
// (spec 2026-10-31-02).

/** The server refused because the session's sign-in is too old. */
export function isReauthRequired(err: unknown): boolean {
  return err instanceof ApiError && err.code === "REAUTHENTICATION_REQUIRED";
}

/** Returns a function giving the translated message for a refused current
 * password, or null when the error is not a re-authentication refusal. */
export function useReauthErrorMessage(): (err: unknown) => string | null {
  const { t } = useTranslation(["account"]);
  return (err: unknown) => {
    if (!(err instanceof ApiError)) return null;
    if (err.code === "INVALID_CURRENT_PASSWORD") {
      return t("account:security.reauth.invalidPassword");
    }
    if (err.status === 429) return t("account:security.reauth.rateLimited");
    return null;
  };
}
