import { useTranslation } from "react-i18next";
import { Loader2, Play } from "lucide-react";

import { useTestRunCheck, type TestRunConfig } from "@/api/hooks";
import { StepsList } from "@/components/checks/steps-list";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { parseSteps } from "@/lib/check-steps";

export interface JsTestRunProps {
  org: string;
  config: TestRunConfig;
  disabled?: boolean;
}

/** The "Test" button of the JS check editor: runs the unsaved script once and
 * shows the step list inline (spec 2026-11-17-04). Nothing is saved. */
export function JsTestRun({ org, config, disabled }: JsTestRunProps) {
  const { t } = useTranslation("checks");
  const testRun = useTestRunCheck(org);
  const result = testRun.data;
  const steps = result ? parseSteps(result.output) : [];
  const screenshotSrc = result?.screenshot
    ? `data:image/${result.screenshot.format};base64,${result.screenshot.data}`
    : undefined;

  return (
    <div className="space-y-2" data-testid="js-test-run">
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={disabled || testRun.isPending || !config.script}
        onClick={() => testRun.mutate(config)}
        data-testid="js-test-run-button"
      >
        {testRun.isPending ? (
          <Loader2 className="mr-2 h-4 w-4 animate-spin" />
        ) : (
          <Play className="mr-2 h-4 w-4" />
        )}
        {t("steps.testButton")}
      </Button>
      {testRun.isError && (
        <Alert variant="destructive" data-testid="js-test-run-error">
          <AlertDescription>{testRun.error.message}</AlertDescription>
        </Alert>
      )}
      {result && (
        <div className="space-y-2 rounded-md border p-3" data-testid="js-test-run-result">
          <div className="text-sm" data-testid="js-test-run-status">
            <span className="font-medium">{result.status}</span>
            <span className="ml-2 text-xs text-muted-foreground">{result.durationMs} ms</span>
            {result.message && <p className="mt-1 text-xs font-mono">{result.message}</p>}
          </div>
          {steps.length > 0 ? (
            <StepsList steps={steps} screenshotSrc={screenshotSrc} />
          ) : (
            <p className="text-xs text-muted-foreground">{t("steps.noSteps")}</p>
          )}
        </div>
      )}
    </div>
  );
}
