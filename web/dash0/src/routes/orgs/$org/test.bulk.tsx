import { createFileRoute } from "@tanstack/react-router";
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
        toast.success(`Created ${result.created} checks`);
      } else {
        toast.warning(
          `Created ${result.created}, failed ${result.failed} checks`,
        );
      }
    } catch (err) {
      const message =
        err instanceof Error ? err.message : "Bulk create failed";
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
      toast.success(`Deleted ${result.deleted} checks`);
    } catch (err) {
      const message =
        err instanceof Error ? err.message : "Bulk delete failed";
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
          <CardTitle className="text-base">Bulk check operations</CardTitle>
          <CardDescription>
            Create or delete up to 10,000 checks at once for performance
            testing. The <code className="text-xs">{"{nb}"}</code> placeholder
            in slug and URL is replaced with the check number (0, 1, 2, ...).
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label htmlFor="bulk-count">Count</Label>
              <Input
                id="bulk-count"
                type="number"
                min="1"
                max="10000"
                value={count}
                onChange={(e) => setCount(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                Number of checks (1-10,000)
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="bulk-period">Period</Label>
              <Input
                id="bulk-period"
                value={period}
                onChange={(e) => setPeriod(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                Check interval (e.g. 10s, 1m, 5m)
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="bulk-slug">Slug template</Label>
              <Input
                id="bulk-slug"
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                Must contain <code className="text-xs">{"{nb}"}</code>
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="bulk-url">URL template</Label>
              <Input
                id="bulk-url"
                value={url}
                onChange={(e) => setUrl(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                <code className="text-xs">{"{nb}"}</code> is replaced with the
                check number
              </p>
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
            Create {count} checks
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
            Delete {count} checks
          </Button>
        </CardFooter>
      </Card>

      {lastResult && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Result</CardTitle>
          </CardHeader>
          <CardContent>
            {lastResult.type === "create" && (
              <div className="space-y-2 text-sm">
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Created</span>
                  <span className="font-mono">{lastResult.data.created}</span>
                </div>
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Failed</span>
                  <span className="font-mono">{lastResult.data.failed}</span>
                </div>
                {lastResult.data.firstSlug && (
                  <div className="flex justify-between">
                    <span className="text-muted-foreground">Range</span>
                    <span className="font-mono">
                      {lastResult.data.firstSlug} ... {lastResult.data.lastSlug}
                    </span>
                  </div>
                )}
                {lastResult.data.errors && lastResult.data.errors.length > 0 && (
                  <div className="mt-2">
                    <p className="text-muted-foreground mb-1">Errors:</p>
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
                <span className="text-muted-foreground">Deleted</span>
                <span className="font-mono">{lastResult.data.deleted}</span>
              </div>
            )}
          </CardContent>
        </Card>
      )}
    </div>
  );
}
