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
import {
  AnnotationCanvas,
  renderAnnotations,
} from "./AnnotationCanvas";
import {
  AnnotationToolbar,
  type AnnotationTool,
} from "./AnnotationToolbar";
import type { Annotation } from "./types";
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
  const [tool, setTool] = useState<AnnotationTool>("select");
  const [annotations, setAnnotations] = useState<Annotation[]>([]);
  const previewURL = useRef<string | null>(null);

  // Reset comment + annotations when the dialog opens fresh.
  useEffect(() => {
    if (!open) {
      setComment("");
      setAnnotations([]);
      setTool("select");
    }
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
      const baked = await bakeAnnotations(screenshot, annotations);
      await onSubmit({ comment, screenshot: baked });
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
            <div className="hidden sm:block space-y-2">
              <AnnotationToolbar
                tool={tool}
                onToolChange={setTool}
                onUndo={() => setAnnotations((prev) => prev.slice(0, -1))}
                canUndo={annotations.length > 0}
              />
              <AnnotationCanvas
                imageURL={previewURL.current}
                tool={tool}
                annotations={annotations}
                onAnnotationsChange={setAnnotations}
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

// bakeAnnotations rasterizes the annotation overlay onto a copy of the
// captured screenshot and returns a fresh PNG blob. Returns the original
// blob unchanged when there are no annotations or no screenshot.
async function bakeAnnotations(
  screenshot: Blob | null,
  annotations: Annotation[],
): Promise<Blob | null> {
  if (!screenshot || annotations.length === 0) return screenshot;

  const bitmap = await createImageBitmap(screenshot);
  const canvas = document.createElement("canvas");
  canvas.width = bitmap.width;
  canvas.height = bitmap.height;

  const ctx = canvas.getContext("2d");
  if (!ctx) return screenshot;

  ctx.drawImage(bitmap, 0, 0);
  renderAnnotations(ctx, annotations, {
    width: bitmap.width,
    height: bitmap.height,
  });

  return new Promise<Blob | null>((resolve) => {
    canvas.toBlob((blob) => resolve(blob || screenshot), "image/png");
  });
}
