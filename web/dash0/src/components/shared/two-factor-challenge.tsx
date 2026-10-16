import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ApiError } from "@/api/client";
import { useAuth, type LoginResult } from "@/contexts/AuthContext";

export interface TwoFactorChallengeProps {
  /** The 2FA temp token a login answered with `requires2Fa`. */
  tempToken: string;
  /** Called with the completed login once the code (or recovery code) is accepted. */
  onVerified: (result: LoginResult) => void;
  /**
   * Called when an attempt fails. `message` is set when the component already
   * has a translated reason to show (the per-user attempt throttle's 429);
   * otherwise the caller reports `err` its own way.
   */
  onError: (err: unknown, message?: string) => void;
  /** Called when an attempt starts, so the caller can clear a previous error. */
  onAttempt?: () => void;
  /** "Back to login": drop the challenge. */
  onBack: () => void;
}

/**
 * The second step of a sign-in for a user with TOTP enabled: a 6-digit
 * authenticator code, or one of the recovery codes. Shared by the password
 * login page and the federated-login handoff route (spec 2026-10-16-14), so
 * both ask the same way and both go through AuthContext's verify2FA /
 * useRecoveryCode.
 */
export function TwoFactorChallenge({
  tempToken,
  onVerified,
  onError,
  onAttempt,
  onBack,
}: TwoFactorChallengeProps) {
  const { t } = useTranslation("auth");
  const auth = useAuth();
  const { verify2FA } = auth;
  // Rename to skip the react-hooks/rules-of-hooks linter: auth.useRecoveryCode
  // is a service method, not a React hook, but its name triggers the rule.
  const submitRecoveryCode = auth.useRecoveryCode;
  const [code, setCode] = useState("");
  const [showRecovery, setShowRecovery] = useState(false);
  const [isLoading, setIsLoading] = useState(false);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    onAttempt?.();
    setIsLoading(true);
    try {
      const result = showRecovery
        ? await submitRecoveryCode(tempToken, code)
        : await verify2FA(tempToken, code);
      setCode("");
      onVerified(result);
    } catch (err) {
      // The per-user attempt throttle (spec
      // 2026-09-25-29-totp-attempt-throttle) answers with a 429 whose body is
      // accurate but always in English. The user already proved their first
      // factor at this point, so give them a translated, actionable reason to
      // wait instead of the raw API string.
      if (err instanceof ApiError && err.status === 429) {
        onError(err, t("twoFactor.tooManyAttempts"));
      } else {
        onError(err);
      }
    } finally {
      setIsLoading(false);
    }
  };

  const fieldId = showRecovery ? "2fa-login-recovery-code" : "2fa-login-code";

  return (
    <form onSubmit={handleSubmit} className="space-y-4" data-testid="2fa-challenge">
      <p className="text-sm text-muted-foreground text-center">
        {showRecovery ? t("twoFactor.recoveryPrompt") : t("twoFactor.codePrompt")}
      </p>
      <div className="space-y-2">
        <Label htmlFor={fieldId}>
          {showRecovery ? t("twoFactor.recoveryLabel") : t("twoFactor.codeLabel")}
        </Label>
        <Input
          id={fieldId}
          data-testid={fieldId}
          inputMode={showRecovery ? "text" : "numeric"}
          autoComplete="one-time-code"
          maxLength={showRecovery ? 32 : 6}
          value={code}
          onChange={(e) =>
            setCode(showRecovery ? e.target.value : e.target.value.replace(/\D/g, ""))
          }
          required
          autoFocus
          className="font-mono"
        />
      </div>
      <Button
        type="submit"
        className="w-full"
        disabled={isLoading || (!showRecovery && code.length !== 6)}
        data-testid="2fa-login-verify"
      >
        {isLoading ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : null}
        {t("twoFactor.verify")}
      </Button>
      <div className="flex justify-between gap-4 text-sm">
        <button
          type="button"
          className="text-muted-foreground hover:underline py-2"
          data-testid="2fa-login-back"
          onClick={() => {
            setCode("");
            setShowRecovery(false);
            onBack();
          }}
        >
          {t("twoFactor.back")}
        </button>
        <button
          type="button"
          className="text-muted-foreground hover:underline py-2"
          data-testid="2fa-login-recovery-link"
          onClick={() => {
            setShowRecovery((v) => !v);
            setCode("");
          }}
        >
          {showRecovery ? t("twoFactor.useCode") : t("twoFactor.useRecovery")}
        </button>
      </div>
    </form>
  );
}
