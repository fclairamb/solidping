import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { CheckCircle2, XCircle } from "lucide-react";

interface AssertionResult {
  type: "assertion" | "and" | "or";
  pass: boolean;
  path?: string;
  operator?: string;
  expected?: string;
  actual?: string;
  error?: string;
  children?: AssertionResult[];
}

interface JsonAssertionResultsProps {
  result: AssertionResult;
}

export function JsonAssertionResults({ result }: JsonAssertionResultsProps) {
  const { t } = useTranslation("checks");

  return (
    <div className="space-y-1">
      <div className="flex items-center gap-2 text-sm font-medium">
        <span>{t("jsonAssertions")}</span>
        <PassBadge pass={result.pass} />
      </div>
      <div className="pl-2">
        <ResultNode result={result} />
      </div>
    </div>
  );
}

function ResultNode({ result }: { result: AssertionResult }) {
  if (result.type === "assertion") {
    return (
      <div className="flex items-center gap-2 text-xs py-0.5">
        <StatusIcon pass={result.pass} />
        <code className="text-muted-foreground">{result.path}</code>
        <span className="text-muted-foreground">{result.operator}</span>
        {result.expected && (
          <span className="text-muted-foreground">
            &quot;{result.expected}&quot;
          </span>
        )}
        {result.actual !== undefined && (
          <span className={result.pass ? "text-green-600" : "text-red-600"}>
            = &quot;{result.actual}&quot;
          </span>
        )}
        {result.error && (
          <span className="text-red-600 italic">{result.error}</span>
        )}
      </div>
    );
  }

  // Group node
  return (
    <div className="space-y-0.5">
      <div className="flex items-center gap-2 text-xs font-medium">
        <StatusIcon pass={result.pass} />
        <span className="uppercase text-muted-foreground">{result.type}</span>
        <PassBadge pass={result.pass} />
      </div>
      <div className="pl-4 border-l border-muted space-y-0.5">
        {result.children?.map((child, i) => (
          <ResultNode key={i} result={child} />
        ))}
      </div>
    </div>
  );
}

function StatusIcon({ pass }: { pass: boolean }) {
  return pass ? (
    <CheckCircle2 className="h-3 w-3 text-green-600 flex-shrink-0" />
  ) : (
    <XCircle className="h-3 w-3 text-red-600 flex-shrink-0" />
  );
}

function PassBadge({ pass }: { pass: boolean }) {
  const { t } = useTranslation("checks");
  return (
    <Badge variant={pass ? "outline" : "destructive"} className="text-xs">
      {pass ? t("passed") : t("failed")}
    </Badge>
  );
}
