import CodeMirror from "@uiw/react-codemirror";
import { javascript } from "@codemirror/lang-javascript";
import { ExternalLink } from "lucide-react";
import type { Check } from "@/api/hooks";
import { Label } from "@/components/ui/label";
import { aiBlockOf } from "./ai-authored-detail";

// Read-only rendering of a check's config, laid out like the edit form: short
// values as label/value rows, the js script in the same CodeMirror editor
// (read-only), maps such as env as key/value rows, multi-line text as a code
// block. The `ai` block of an AI-authored check is left to AIAuthoredDetail.

type Scalar = string | number | boolean | null;

function isScalar(value: unknown): value is Scalar {
  return value === null || ["string", "number", "boolean"].includes(typeof value);
}

function isScalarMap(value: unknown): value is Record<string, Scalar> {
  return (
    typeof value === "object" &&
    value !== null &&
    !Array.isArray(value) &&
    Object.values(value).every(isScalar)
  );
}

function ScalarValue({ value }: { value: Scalar }) {
  if (typeof value === "string" && /^https?:\/\//.test(value)) {
    return (
      <a
        href={value}
        target="_blank"
        rel="noopener noreferrer"
        className="inline-flex items-center gap-1 break-all text-primary hover:underline"
      >
        {value}
        <ExternalLink className="h-3 w-3 shrink-0" />
      </a>
    );
  }
  return <span className="break-all">{String(value)}</span>;
}

function ScriptView({ value }: { value: string }) {
  return (
    <CodeMirror
      value={value}
      extensions={[javascript()]}
      editable={false}
      readOnly
      theme={document.documentElement.classList.contains("dark") ? "dark" : "light"}
      maxHeight="320px"
      className="overflow-hidden rounded-md border text-sm"
      data-testid="check-config-script"
    />
  );
}

function Block({ name, children }: { name: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5" data-testid={`check-config-${name}`}>
      <Label className="font-mono text-xs text-muted-foreground">{name}</Label>
      {children}
    </div>
  );
}

export function CheckConfigView({ check }: { check: Check }) {
  const config = check.config ?? {};
  const skipAI = aiBlockOf(check) !== undefined;
  const entries = Object.entries(config).filter(([key]) => !(skipAI && key === "ai"));
  // The script leads, as in the edit form.
  entries.sort(([a], [b]) => Number(b === "script") - Number(a === "script"));

  const rows = entries.filter(
    ([key, value]) =>
      key !== "script" && isScalar(value) && !(typeof value === "string" && value.includes("\n")),
  );
  const blocks = entries.filter((entry) => !rows.includes(entry));

  return (
    <div className="space-y-4" data-testid="check-config-view">
      {blocks
        .filter(([key]) => key === "script")
        .map(([key, value]) => (
          <Block key={key} name={key}>
            <ScriptView value={String(value)} />
          </Block>
        ))}

      {rows.length > 0 && (
        <dl className="grid grid-cols-[minmax(0,auto)_minmax(0,1fr)] gap-x-4 gap-y-1 rounded-md border bg-muted/40 p-3 font-mono text-xs">
          {rows.map(([key, value]) => (
            <div key={key} className="contents">
              <dt className="text-muted-foreground">{key}</dt>
              <dd className="min-w-0">
                <ScalarValue value={value as Scalar} />
              </dd>
            </div>
          ))}
        </dl>
      )}

      {blocks
        .filter(([key]) => key !== "script")
        .map(([key, value]) => (
          <Block key={key} name={key}>
            {typeof value === "string" ? (
              <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words rounded-md border bg-muted/40 p-3 font-mono text-xs">
                {value}
              </pre>
            ) : isScalarMap(value) ? (
              <dl className="grid grid-cols-[minmax(0,auto)_minmax(0,1fr)] gap-x-4 gap-y-1 rounded-md border bg-muted/40 p-3 font-mono text-xs">
                {Object.entries(value).map(([innerKey, innerValue]) => (
                  <div key={innerKey} className="contents">
                    <dt className="text-muted-foreground">{innerKey}</dt>
                    <dd className="min-w-0">
                      <ScalarValue value={innerValue} />
                    </dd>
                  </div>
                ))}
              </dl>
            ) : Array.isArray(value) && value.every(isScalar) ? (
              <ul className="list-disc space-y-0.5 rounded-md border bg-muted/40 py-2 pl-7 pr-3 font-mono text-xs">
                {value.map((item, idx) => (
                  <li key={idx}>
                    <ScalarValue value={item} />
                  </li>
                ))}
              </ul>
            ) : (
              <pre className="max-h-80 overflow-auto rounded-md border bg-muted/40 p-3 font-mono text-xs">
                {JSON.stringify(value, null, 2)}
              </pre>
            )}
          </Block>
        ))}
    </div>
  );
}
