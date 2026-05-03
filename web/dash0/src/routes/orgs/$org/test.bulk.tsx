import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { useState } from "react";
import { Loader2, Plus, Trash2 } from "lucide-react";
import {
  useBulkCreateChecks,
  useBulkDeleteChecks,
  type BulkCreateChecksResponse,
  type BulkDeleteChecksResponse,
} from "@/api/hooks";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export const Route = createFileRoute("/orgs/$org/test/bulk")({
  component: BulkChecksTab,
});

function BulkChecksTab() {
  const { t } = useTranslation("nav");
  const { org } = Route.useParams();
  const bulkCreate = useBulkCreateChecks();
  const bulkDelete = useBulkDeleteChecks();

  const [count, setCount] = useState("100");
  const [slug, setSlug] = useState("http-{nb}");
  const [url, setUrl] = useState(
    `${window.location.origin}/api/v1/fake?nb={nb}`,
  );
  const [period, setPeriod] = useState("10s");

  const [lastResult, setLastResult] = useState<
    | { type: "create"; data: BulkCreateChecksResponse }
    | { type: "delete"; data: BulkDeleteChecksResponse }
    | null
  >(null);

  const handleCreate = async () => {
    try {
      const result = await bulkCreate.mutateAsync({
        org,
        type: "http",
        slug,
        url,
        period,
        count: Number(count),
      });
      setLastResult({ type: "create", data: result });
      if (result.failed === 0) {
        toast.success(t("test.bulk.createSuccess", { created: result.created }));
      } else {
        toast.warning(
          t("test.bulk.createPartial", { created: result.created, failed: result.failed }),
        );
      }
    } catch (err) {
      const message =
        err instanceof Error ? err.message : t("test.bulk.createFailed");
      toast.error(message);
    }
  };

  const handleDelete = async () => {
    try {
      const result = await bulkDelete.mutateAsync({
        org,
        slug,
        count: Number(count),
      });
      setLastResult({ type: "delete", data: result });
      toast.success(t("test.bulk.deleteSuccess", { deleted: result.deleted }));
    } catch (err) {
      const message =
        err instanceof Error ? err.message : t("test.bulk.deleteFailed");
      toast.error(message);
    }
  };

  const isLoading = bulkCreate.isPending || bulkDelete.isPending;
  const countNum = Number(count);
  const isValid =
    countNum >= 1 && countNum <= 10000 && slug.includes("{nb}");

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("test.bulk.title")}</CardTitle>
          <CardDescription>{t("test.bulk.description")}</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label htmlFor="bulk-count">{t("test.bulk.count")}</Label>
              <Input
                id="bulk-count"
                type="number"
                min="1"
                max="10000"
                value={count}
                onChange={(e) => setCount(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">{t("test.bulk.countHelp")}</p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="bulk-period">{t("test.bulk.period")}</Label>
              <Input
                id="bulk-period"
                value={period}
                onChange={(e) => setPeriod(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">{t("test.bulk.periodHelp")}</p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="bulk-slug">{t("test.bulk.slugTemplate")}</Label>
              <Input
                id="bulk-slug"
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">{t("test.bulk.slugTemplateHelp")}</p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="bulk-url">{t("test.bulk.urlTemplate")}</Label>
              <Input
                id="bulk-url"
                value={url}
                onChange={(e) => setUrl(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">{t("test.bulk.urlTemplateHelp")}</p>
            </div>
          </div>
        </CardContent>
        <CardFooter className="gap-2">
          <Button onClick={handleCreate} disabled={isLoading || !isValid}>
            {bulkCreate.isPending ? (
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
            ) : (
              <Plus className="mr-2 h-4 w-4" />
            )}
            {t("test.bulk.createButton", { count })}
          </Button>
          <Button
            variant="destructive"
            onClick={handleDelete}
            disabled={isLoading || !isValid}
          >
            {bulkDelete.isPending ? (
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
            ) : (
              <Trash2 className="mr-2 h-4 w-4" />
            )}
            {t("test.bulk.deleteButton", { count })}
          </Button>
        </CardFooter>
      </Card>

      {lastResult && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("test.bulk.result")}</CardTitle>
          </CardHeader>
          <CardContent>
            {lastResult.type === "create" && (
              <div className="space-y-2 text-sm">
                <div className="flex justify-between">
                  <span className="text-muted-foreground">{t("test.bulk.created")}</span>
                  <span className="font-mono">{lastResult.data.created}</span>
                </div>
                <div className="flex justify-between">
                  <span className="text-muted-foreground">{t("test.bulk.failed")}</span>
                  <span className="font-mono">{lastResult.data.failed}</span>
                </div>
                {lastResult.data.firstSlug && (
                  <div className="flex justify-between">
                    <span className="text-muted-foreground">{t("test.bulk.range")}</span>
                    <span className="font-mono">
                      {lastResult.data.firstSlug} ... {lastResult.data.lastSlug}
                    </span>
                  </div>
                )}
                {lastResult.data.errors && lastResult.data.errors.length > 0 && (
                  <div className="mt-2">
                    <p className="text-muted-foreground mb-1">{t("test.bulk.errors")}</p>
                    <ul className="list-disc list-inside text-xs text-destructive">
                      {lastResult.data.errors.map((error, i) => (
                        <li key={i}>{error}</li>
                      ))}
                    </ul>
                  </div>
                )}
              </div>
            )}
            {lastResult.type === "delete" && (
              <div className="flex justify-between text-sm">
                <span className="text-muted-foreground">{t("test.bulk.deleted")}</span>
                <span className="font-mono">{lastResult.data.deleted}</span>
              </div>
            )}
          </CardContent>
        </Card>
      )}
    </div>
  );
}
