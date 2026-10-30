import { useTranslation } from "react-i18next";

/**
 * Honeypot field of the sign-up and password-reset forms (auth.form_guard).
 * A human never sees it, a keyboard never reaches it and a screen reader never
 * announces it; a bot that fills every input does, and the server then drops
 * the submission while answering like a success.
 *
 * Moved off-screen rather than `display: none`, which naive bots skip.
 * `autoComplete="off"` plus the password-manager opt-outs keep autofill from
 * filling it on a real visitor's behalf.
 */
export function FormHoneypot({
  value,
  onChange,
}: {
  value: string;
  onChange: (value: string) => void;
}) {
  const { t } = useTranslation("auth");

  return (
    <div
      aria-hidden="true"
      className="pointer-events-none absolute -left-[10000px] top-auto h-px w-px overflow-hidden"
      data-testid="form-honeypot"
    >
      <label htmlFor="website">{t("honeypotLabel")}</label>
      <input
        id="website"
        name="website"
        type="text"
        tabIndex={-1}
        autoComplete="off"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        data-1p-ignore=""
        data-lpignore="true"
        data-bwignore="true"
        data-form-type="other"
        data-testid="form-honeypot-input"
      />
    </div>
  );
}
