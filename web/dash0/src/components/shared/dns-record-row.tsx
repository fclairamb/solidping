import { useTranslation } from "react-i18next";
import type { DnsRecord } from "@/api/hooks";
import { CopyableInline } from "@/components/shared/copyable-code";

/**
 * A single DNS record the user must create, rendered as a labeled Type / Name /
 * Value grid with copy-to-clipboard buttons on the copyable values. Reused by
 * the status-page custom-domain section and the design reference.
 */
export function DnsRecordRow({ record }: { record: DnsRecord }) {
  const { t } = useTranslation("common");

  return (
    <div className="grid grid-cols-[4rem_minmax(0,1fr)] items-center gap-x-3 gap-y-2 rounded-md border p-3 text-sm">
      <span className="text-muted-foreground">{t("type")}</span>
      <span className="font-mono">{record.type}</span>

      <span className="text-muted-foreground">{t("name")}</span>
      <CopyableInline value={record.name} label={t("dnsRecord.recordName")} />

      <span className="text-muted-foreground">{t("value")}</span>
      <CopyableInline value={record.value} label={t("dnsRecord.recordValue")} />
    </div>
  );
}
