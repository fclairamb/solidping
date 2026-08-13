import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link } from "@tanstack/react-router";
import {
  Bot,
  Eye,
  KeyRound,
  AlertTriangle,
  Compass,
  MessageSquare,
  Terminal,
  MousePointerClick,
  Code2,
  Puzzle,
} from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { CopyableCode, CollapsibleCode } from "@/components/shared/copyable-code";
import { DocsLink } from "@/components/shared/docs-link";

type McpSearch = {
  /** "get" when the user landed here from opening GET /api/v1/mcp in a browser. */
  from?: string;
};

export const Route = createFileRoute("/orgs/$org/account/mcp")({
  validateSearch: (search: Record<string, unknown>): McpSearch => ({
    from: typeof search.from === "string" ? search.from : undefined,
  }),
  component: McpPage,
});

/**
 * Builds the Cursor "Add to Cursor" deep link.
 * Format: cursor://anysphere.cursor-deeplink/mcp/install?name=<name>&config=<base64 json>
 * The config JSON mirrors Cursor's own mcp.json shape for a remote HTTP
 * server — just a "url" key, no "type" (type is only required for stdio
 * servers). Verified against Cursor's install-links docs at implementation
 * time; this is an unversioned client convention, not a formal spec.
 */
function cursorDeepLink(mcpUrl: string): string {
  const config = JSON.stringify({ url: mcpUrl });
  const base64 = btoa(config);
  return `cursor://anysphere.cursor-deeplink/mcp/install?name=solidping&config=${encodeURIComponent(base64)}`;
}

/**
 * Builds the VS Code "Add to VS Code" deep link.
 * Format: vscode:mcp/install?<url-encoded json>, json shape
 * {"name": ..., "type": "http", "url": ...}. VS Code Insiders uses the
 * vscode-insiders: scheme instead. Verified against VS Code's MCP developer
 * guide at implementation time; this is an unversioned client convention.
 */
function vscodeDeepLink(mcpUrl: string, insiders = false): string {
  const config = JSON.stringify({ name: "solidping", type: "http", url: mcpUrl });
  const scheme = insiders ? "vscode-insiders" : "vscode";
  return `${scheme}:mcp/install?${encodeURIComponent(config)}`;
}

function McpPage() {
  const { t } = useTranslation(["org", "common"]);
  const { org } = Route.useParams();
  const { from } = Route.useSearch();

  const mcpUrl = useMemo(() => `${window.location.origin}/api/v1/mcp`, []);
  const genericConfig = useMemo(
    () =>
      JSON.stringify(
        {
          mcpServers: {
            solidping: {
              url: mcpUrl,
            },
          },
        },
        null,
        2,
      ),
    [mcpUrl],
  );
  const genericConfigWithToken = useMemo(
    () =>
      JSON.stringify(
        {
          mcpServers: {
            solidping: {
              url: mcpUrl,
              headers: {
                Authorization: "Bearer <your-token>",
              },
            },
          },
        },
        null,
        2,
      ),
    [mcpUrl],
  );

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">{t("ai.title")}</h1>
          <p className="text-muted-foreground">{t("ai.description")}</p>
        </div>
        <DocsLink href="/docs/features/mcp" />
      </div>

      {from === "get" && (
        <Alert data-testid="mcp-from-get-hint">
          <Compass className="h-4 w-4" />
          <AlertTitle>{t("ai.fromGet.title")}</AlertTitle>
          <AlertDescription>{t("ai.fromGet.description")}</AlertDescription>
        </Alert>
      )}

      <Card>
        <CardHeader>
          <div className="flex items-center gap-2">
            <Bot className="h-5 w-5 text-primary" />
            <CardTitle>{t("ai.connectTitle")}</CardTitle>
          </div>
          <CardDescription>{t("ai.description")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-6">
          <div className="space-y-2">
            <div className="text-sm font-medium">{t("ai.mcpUrl")}</div>
            <CopyableCode code={mcpUrl} />
          </div>

          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {/* Claude (claude.ai / desktop): no public deep link exists today —
                copy-URL + paste-into-Settings instructions. */}
            <Card>
              <CardHeader>
                <div className="flex items-center gap-2">
                  <MessageSquare className="h-4 w-4 text-muted-foreground" />
                  <CardTitle className="text-base">{t("ai.claude.title")}</CardTitle>
                </div>
                <CardDescription>{t("ai.claude.description")}</CardDescription>
              </CardHeader>
              <CardContent className="space-y-3">
                <CopyableCode code={mcpUrl} />
                <ol className="list-decimal space-y-1 pl-4 text-sm text-muted-foreground">
                  <li>{t("ai.claude.step1")}</li>
                  <li>{t("ai.claude.step2")}</li>
                  <li>{t("ai.claude.step3")}</li>
                </ol>
              </CardContent>
            </Card>

            {/* Claude Code: one-liner CLI command. */}
            <Card>
              <CardHeader>
                <div className="flex items-center gap-2">
                  <Terminal className="h-4 w-4 text-muted-foreground" />
                  <CardTitle className="text-base">{t("ai.claudeCode.title")}</CardTitle>
                </div>
                <CardDescription>{t("ai.claudeCode.description")}</CardDescription>
              </CardHeader>
              <CardContent>
                <CopyableCode code={`claude mcp add --transport http solidping ${mcpUrl}`} />
              </CardContent>
            </Card>

            {/* Cursor: true one-click deep link. */}
            <Card>
              <CardHeader>
                <div className="flex items-center gap-2">
                  <MousePointerClick className="h-4 w-4 text-muted-foreground" />
                  <CardTitle className="text-base">{t("ai.cursor.title")}</CardTitle>
                </div>
                <CardDescription>{t("ai.cursor.description")}</CardDescription>
              </CardHeader>
              <CardContent>
                <a
                  href={cursorDeepLink(mcpUrl)}
                  className="inline-flex items-center justify-center gap-2 rounded-md border bg-primary px-4 py-2 text-sm font-medium text-primary-foreground shadow-sm transition-colors hover:bg-primary/90"
                  data-testid="cursor-install-link"
                >
                  {t("ai.cursor.button")}
                </a>
              </CardContent>
            </Card>

            {/* VS Code: true one-click deep link (+ Insiders variant). */}
            <Card>
              <CardHeader>
                <div className="flex items-center gap-2">
                  <Code2 className="h-4 w-4 text-muted-foreground" />
                  <CardTitle className="text-base">{t("ai.vscode.title")}</CardTitle>
                </div>
                <CardDescription>{t("ai.vscode.description")}</CardDescription>
              </CardHeader>
              <CardContent className="flex flex-wrap items-center gap-2">
                <a
                  href={vscodeDeepLink(mcpUrl)}
                  className="inline-flex items-center justify-center gap-2 rounded-md border bg-primary px-4 py-2 text-sm font-medium text-primary-foreground shadow-sm transition-colors hover:bg-primary/90"
                  data-testid="vscode-install-link"
                >
                  {t("ai.vscode.button")}
                </a>
                <a
                  href={vscodeDeepLink(mcpUrl, true)}
                  className="text-sm text-muted-foreground underline-offset-2 hover:underline"
                  data-testid="vscode-insiders-install-link"
                >
                  {t("ai.vscode.insiders")}
                </a>
              </CardContent>
            </Card>

            {/* Generic / other clients: collapsible JSON config, URL-only first. */}
            <Card className="sm:col-span-2 lg:col-span-1">
              <CardHeader>
                <div className="flex items-center gap-2">
                  <Puzzle className="h-4 w-4 text-muted-foreground" />
                  <CardTitle className="text-base">{t("ai.generic.title")}</CardTitle>
                </div>
                <CardDescription>{t("ai.generic.description")}</CardDescription>
              </CardHeader>
              <CardContent className="space-y-2">
                <CollapsibleCode
                  label={t("ai.generic.urlOnly")}
                  value={genericConfig}
                  defaultOpen
                />
                <CollapsibleCode
                  label={t("ai.generic.withToken")}
                  value={genericConfigWithToken}
                />
              </CardContent>
            </Card>
          </div>

          <Alert>
            <Eye className="h-4 w-4" />
            <AlertTitle>{t("ai.readOnly.title")}</AlertTitle>
            <AlertDescription className="space-y-1">
              <p>{t("ai.readOnly.oauth")}</p>
              <p className="flex flex-wrap items-center gap-1">
                {t("ai.readOnly.pat")}{" "}
                <Link
                  to="/orgs/$org/account/tokens"
                  params={{ org }}
                  className="inline-flex items-center gap-1 font-medium text-primary hover:underline"
                >
                  <KeyRound className="h-3.5 w-3.5" />
                  {t("ai.readOnly.tokensLink")}
                </Link>
              </p>
            </AlertDescription>
          </Alert>

          <Alert variant="warning">
            <AlertTriangle className="h-4 w-4" />
            <AlertTitle>{t("ai.selfHosted.title")}</AlertTitle>
            <AlertDescription>{t("ai.selfHosted.description")}</AlertDescription>
          </Alert>
        </CardContent>
      </Card>
    </div>
  );
}
