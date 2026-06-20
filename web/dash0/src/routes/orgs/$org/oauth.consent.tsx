import { createFileRoute } from "@tanstack/react-router";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { ShieldCheck, Eye, Pencil, AlertCircle } from "lucide-react";

// Consent screen for the embedded MCP OAuth 2.1 authorization server
// (spec 2026-06-20-03). The server-side /authorize endpoint validates every
// parameter and redirects a logged-in user here with the validated values in the
// query string. The user reviews the requested scope and either approves —
// submitting a native POST form to /api/v1/oauth/authorize, which mints the
// single-use code and redirects back to the client — or denies, returning to the
// client's redirect_uri with an access_denied error.
//
// The form is a native browser submit (not an apiFetch) so the access_token
// cookie rides along and the browser follows the server's 302 to the client.
// Auth is enforced by the parent /orgs/$org layout, which redirects to login
// with returnTo when there is no session (frontend 401 convention).

interface ConsentSearch {
  client_id?: string;
  redirect_uri?: string;
  response_type?: string;
  scope?: string;
  state?: string;
  resource?: string;
  code_challenge?: string;
  code_challenge_method?: string;
}

function str(v: unknown): string | undefined {
  return typeof v === "string" ? v : undefined;
}

export const Route = createFileRoute("/orgs/$org/oauth/consent")({
  validateSearch: (search: Record<string, unknown>): ConsentSearch => ({
    client_id: str(search.client_id),
    redirect_uri: str(search.redirect_uri),
    response_type: str(search.response_type),
    scope: str(search.scope),
    state: str(search.state),
    resource: str(search.resource),
    code_challenge: str(search.code_challenge),
    code_challenge_method: str(search.code_challenge_method),
  }),
  component: ConsentPage,
});

const AUTHORIZE_ENDPOINT = "/api/v1/oauth/authorize";

function ConsentPage() {
  const search = Route.useSearch();
  const { org } = Route.useParams();

  const scope = search.scope ?? "mcp";
  const isReadOnly = scope.split(/\s+/).every((s) => s === "mcp:read");
  const clientName = search.client_id ?? "an application";

  // Missing required params means the user landed here without going through the
  // validated /authorize endpoint — refuse rather than render a misleading form.
  const missingRequired =
    !search.client_id ||
    !search.redirect_uri ||
    !search.code_challenge ||
    !search.resource;

  if (missingRequired) {
    return (
      <div className="mx-auto flex max-w-lg flex-col gap-4 p-4">
        <Alert variant="destructive">
          <AlertCircle className="h-4 w-4" />
          <AlertTitle>Invalid authorization request</AlertTitle>
          <AlertDescription>
            This consent request is missing required parameters. Start the
            connection again from your MCP client.
          </AlertDescription>
        </Alert>
      </div>
    );
  }

  // Hidden fields mirror the validated query params straight back to the server,
  // which re-validates them — the consent UI cannot alter the grant.
  const hidden: Array<[string, string | undefined]> = [
    ["client_id", search.client_id],
    ["redirect_uri", search.redirect_uri],
    ["response_type", search.response_type ?? "code"],
    ["scope", scope],
    ["state", search.state],
    ["resource", search.resource],
    ["code_challenge", search.code_challenge],
    ["code_challenge_method", search.code_challenge_method ?? "S256"],
  ];

  return (
    <div className="mx-auto flex max-w-lg flex-col gap-4 p-4">
      <Card>
        <CardHeader>
          <div className="flex items-center gap-2">
            <ShieldCheck className="h-5 w-5 text-primary" />
            <CardTitle>Authorize MCP access</CardTitle>
          </div>
          <CardDescription>
            <span className="font-medium">{clientName}</span> is requesting access
            to your SolidPing organization <span className="font-medium">{org}</span>{" "}
            over the Model Context Protocol.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          <Alert>
            {isReadOnly ? (
              <Eye className="h-4 w-4" />
            ) : (
              <Pencil className="h-4 w-4" />
            )}
            <AlertTitle>
              {isReadOnly ? "Read-only access" : "Read-write access"}
            </AlertTitle>
            <AlertDescription>
              {isReadOnly
                ? "This connection can read your monitoring data but cannot create, update, or delete anything."
                : "This connection can read your monitoring data and create, update, or delete resources via MCP tools."}
            </AlertDescription>
          </Alert>

          <div className="flex flex-col gap-2 sm:flex-row sm:justify-end">
            {/* Deny: POST with deny=1 so the server redirects back with access_denied. */}
            <form method="POST" action={AUTHORIZE_ENDPOINT} className="w-full sm:w-auto">
              {hidden.map(([name, value]) => (
                <input key={name} type="hidden" name={name} value={value ?? ""} />
              ))}
              <input type="hidden" name="decision" value="deny" />
              <Button type="submit" variant="outline" className="w-full">
                Deny
              </Button>
            </form>
            {/* Approve: POST mints the auth code and redirects to the client. */}
            <form method="POST" action={AUTHORIZE_ENDPOINT} className="w-full sm:w-auto">
              {hidden.map(([name, value]) => (
                <input key={name} type="hidden" name={name} value={value ?? ""} />
              ))}
              <input type="hidden" name="decision" value="approve" />
              <Button type="submit" className="w-full">
                Approve
              </Button>
            </form>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
