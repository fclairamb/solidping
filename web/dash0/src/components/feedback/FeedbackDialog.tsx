import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import type { SubmitPayload } from "./useFeedback";

interface FeedbackDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  screenshot: Blob | null;
  isCapturing: boolean;
  onSubmit: (payload: SubmitPayload) => Promise<void>;
}

export function FeedbackDialog({
  open,
  onOpenChange,
  screenshot,
  isCapturing,
  onSubmit,
}: FeedbackDialogProps) {
  const { t } = useTranslation("feedback");
  const [comment, setComment] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const previewURL = useRef<string | null>(null);

  // Reset comment when the dialog opens fresh.
  useEffect(() => {
    if (!open) setComment("");
  }, [open]);

  // Build (and revoke) an object URL for the screenshot preview.
  useEffect(() => {
    if (previewURL.current) {
      URL.revokeObjectURL(previewURL.current);
      previewURL.current = null;
    }
    if (screenshot) {
      previewURL.current = URL.createObjectURL(screenshot);
    }
    return () => {
      if (previewURL.current) {
        URL.revokeObjectURL(previewURL.current);
        previewURL.current = null;
      }
    };
  }, [screenshot]);

  async function handleSubmit() {
    setSubmitting(true);
    try {
      await onSubmit({ comment, screenshot });
      toast.success(t("success"));
      onOpenChange(false);
    } catch {
      toast.error(t("error"));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-sm w-[calc(100vw-1rem)] sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t("dialog_title")}</DialogTitle>
          <DialogDescription className="hidden sm:block">
            {isCapturing ? t("capturing") : ""}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          {previewURL.current && (
            <div className="hidden sm:block max-h-64 overflow-hidden rounded-md border">
              <img
                src={previewURL.current}
                alt="Screenshot preview"
                className="w-full object-contain"
              />
            </div>
          )}

          <label className="block">
            <span className="text-sm font-medium">{t("comment_label")}</span>
            <Textarea
              autoFocus
              value={comment}
              onChange={(event) => setComment(event.target.value)}
              placeholder={t("comment_placeholder")}
              rows={5}
              className="mt-1"
              data-testid="feedback-comment"
            />
          </label>
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={submitting}
          >
            {t("cancel")}
          </Button>
          <Button
            type="button"
            onClick={handleSubmit}
            disabled={submitting || isCapturing}
            data-testid="feedback-submit"
          >
            {submitting ? t("sending") : t("submit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
