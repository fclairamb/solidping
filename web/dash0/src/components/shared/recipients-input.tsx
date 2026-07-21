import { useTranslation } from "react-i18next";

import { isValidEmail } from "@/lib/email";
import { TokenChipsInput } from "@/components/shared/token-chips-input";

export interface RecipientsInputProps {
  value: string[];
  onChange: (next: string[]) => void;
  placeholder?: string;
  id?: string;
  "data-testid"?: string;
}

// RecipientsInput is the email-flavored configuration of the generic
// TokenChipsInput (spec 2026-07-21-02): a chip/tag input for a list of
// free-form addresses (currently used for email recipients). Each entry
// renders as a removable Badge chip — destructive-red when it fails
// isValidEmail, so invalid entries are visibly flagged without being
// silently dropped. No `normalize` is passed: the local part of an email
// address is technically case-sensitive, so entries are kept exactly as
// typed (only trimmed).
export function RecipientsInput({
  value,
  onChange,
  placeholder,
  id,
  "data-testid": testid,
}: RecipientsInputProps) {
  const { t } = useTranslation("integrations");

  return (
    <TokenChipsInput
      value={value}
      onChange={onChange}
      validate={isValidEmail}
      placeholder={placeholder}
      id={id}
      data-testid={testid}
      invalidTitle={t("form.recipientInvalid", "Not a valid email address")}
      getRemoveLabel={(email) =>
        t("form.removeRecipient", "Remove {{email}}", { email })
      }
    />
  );
}
