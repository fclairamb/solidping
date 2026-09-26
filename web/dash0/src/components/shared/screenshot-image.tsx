import { cn } from "@/lib/utils";

export interface ScreenshotImageLinkProps {
  /** The image URL — for a stored capture, its signed `downloadUrl`. */
  src: string;
  alt: string;
  /** Classes for the link frame (sizing, aspect ratio). */
  className?: string;
  /** Classes for the image itself (e.g. `object-cover` for a thumbnail). */
  imgClassName?: string;
  /** Hover text, e.g. the capture time of a thumbnail. */
  title?: string;
  "data-testid"?: string;
  /** Test id of the <img>. */
  imageTestId?: string;
}

/**
 * A captured screenshot as a link to its full-size self (spec 2026-08-21-01,
 * shared since spec 2026-09-25-34). Opening the image in a new tab IS the
 * full-size view: no lightbox dependency, and the browser's own image viewer
 * zooms and saves. Used by the incident screenshot card and the check page's
 * Screenshots card, so both render a capture the same way.
 */
export function ScreenshotImageLink({
  src,
  alt,
  className,
  imgClassName,
  title,
  "data-testid": testId,
  imageTestId,
}: ScreenshotImageLinkProps) {
  return (
    <a
      href={src}
      target="_blank"
      rel="noopener noreferrer"
      title={title}
      className={cn("block overflow-hidden rounded-md border bg-muted", className)}
      data-testid={testId}
    >
      <img
        src={src}
        alt={alt}
        loading="lazy"
        className={cn("h-auto w-full max-w-full", imgClassName)}
        data-testid={imageTestId}
      />
    </a>
  );
}
