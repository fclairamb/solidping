import { useTranslation } from "react-i18next";
import { CollapsibleSection } from "@/components/ui/collapsible-section";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  TRANSLATION_LANGUAGES,
  type TranslationEntry,
  type Translations,
} from "@/lib/localized-text";

interface TranslationsEditorProps {
  value: Translations;
  onChange: (next: Translations) => void;
  /** A status update has a markdown body to translate; a publication title does not. */
  withBody: boolean;
  /** The page's own language: the literal text is that language, so it has no group. */
  pageLanguage?: string;
}

/**
 * The "Other languages" block of the status update and publication editors:
 * one collapsible group per supported language with a title and, for updates,
 * a markdown body. An empty group means no translation. Groups stack, so the
 * block works on a phone.
 */
export function TranslationsEditor({
  value,
  onChange,
  withBody,
  pageLanguage,
}: TranslationsEditorProps) {
  const { t } = useTranslation("statusUpdates");
  const own = (pageLanguage || "en").toLowerCase().split(/[-_]/)[0];

  const setField = (lang: string, field: keyof TranslationEntry, text: string) => {
    const current = value[lang] ?? { title: "" };
    onChange({ ...value, [lang]: { ...current, [field]: text } });
  };

  return (
    <div className="space-y-3" data-testid="translations-editor">
      <div className="space-y-1">
        <h3 className="text-sm font-medium">{t("translations.heading")}</h3>
        <p className="text-xs text-muted-foreground">
          {t("translations.description")}
        </p>
      </div>
      {TRANSLATION_LANGUAGES.filter((lang) => lang !== own).map((lang) => {
        const entry = value[lang] ?? { title: "" };
        const filled = Boolean(entry.title || entry.body);
        return (
          <CollapsibleSection
            key={lang}
            title={t(`translations.languages.${lang}`)}
            customized={filled}
            defaultOpen={filled}
            data-testid={`translation-${lang}-toggle`}
          >
            <div className="space-y-1">
              <Label htmlFor={`translation-${lang}-title`}>
                {t("translations.titleLabel")}
              </Label>
              <Input
                id={`translation-${lang}-title`}
                value={entry.title}
                maxLength={200}
                onChange={(e) => setField(lang, "title", e.target.value)}
                data-testid={`translation-${lang}-title`}
              />
            </div>
            {withBody && (
              <div className="space-y-1">
                <Label htmlFor={`translation-${lang}-body`}>
                  {t("translations.bodyLabel")}
                </Label>
                <Textarea
                  id={`translation-${lang}-body`}
                  value={entry.body ?? ""}
                  rows={4}
                  onChange={(e) => setField(lang, "body", e.target.value)}
                  data-testid={`translation-${lang}-body`}
                />
              </div>
            )}
          </CollapsibleSection>
        );
      })}
    </div>
  );
}
