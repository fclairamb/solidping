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

/** Placeholder dimensions for a capture whose real size is unknown until it
 * loads (the listing does not carry it). They only give the browser an aspect
 * ratio to reserve the box with: with `width: 100%; height: auto`, an <img>
 * carrying width/height attributes is laid out at that ratio BEFORE it loads
 * and switches to its intrinsic ratio once it has, so a full-page capture is
 * never squashed. Without them a lazy image below the fold keeps a 0-height
 * box, never intersects the viewport, and never loads. */
const PLACEHOLDER_WIDTH = 1280;
const PLACEHOLDER_HEIGHT = 800;

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
        width={PLACEHOLDER_WIDTH}
        height={PLACEHOLDER_HEIGHT}
        loading="lazy"
        className={cn("h-auto w-full max-w-full", imgClassName)}
        data-testid={imageTestId}
      />
    </a>
  );
}
