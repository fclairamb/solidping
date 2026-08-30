import { Fragment } from "react";

// Tiny hand-rolled renderer for incident comment bodies. Comments arrive
// from dash0, Slack, Telegram and the API, so treat the text as untrusted:
// scope formatting to exactly three things (autolinked URLs, `inline code`,
// and ```fenced``` code blocks), and never hand raw HTML to the DOM — every
// piece of text always goes through React's normal text-node escaping.
//
// Deliberately NOT react-markdown / any markdown lib: comments are short
// freeform chat, not the trusted operator-authored markdown status0 embeds
// (see status0's renderer) — headings, images, and raw HTML are explicitly
// out of scope here, so a general markdown parser would be both overkill
// and a bigger attack surface than this needs.

type InlineNode =
  | { type: "text"; content: string }
  | { type: "link"; url: string }
  | { type: "code"; content: string };

type Segment =
  | { type: "text"; nodes: InlineNode[] }
  | { type: "code-block"; content: string };

// Lazy match so an unclosed fence simply never matches — the whole
// remainder falls through to the plain-text path instead of being eaten.
const FENCE_REGEX = /```([\s\S]*?)```/g;

// Alternation of URL / `inline code`. Backticks and whitespace are excluded
// from the URL char class so a URL never swallows an adjacent code span or
// spans a newline.
const INLINE_REGEX = /(https?:\/\/[^\s`]+)|`([^`\n]+)`/g;

const TRAILING_PUNCTUATION = new Set([
  ".",
  ",",
  ";",
  ":",
  "!",
  "?",
  "'",
  '"',
  ")",
  "]",
  "}",
]);

// Strip trailing punctuation that's almost always sentence structure, not
// part of the URL ("see https://acme.com." shouldn't linkify the period).
// A trailing ")" is kept when the URL itself contains a balancing "(" —
// e.g. a Wikipedia-style "https://en.wikipedia.org/wiki/Foo_(bar)".
function trimTrailingPunctuation(url: string): {
  url: string;
  trailing: string;
} {
  let end = url.length;
  while (end > 0 && TRAILING_PUNCTUATION.has(url[end - 1]!)) {
    if (url[end - 1] === ")") {
      const opens = (url.slice(0, end).match(/\(/g) ?? []).length;
      const closes = (url.slice(0, end).match(/\)/g) ?? []).length;
      if (opens >= closes) break;
    }
    end--;
  }
  return { url: url.slice(0, end), trailing: url.slice(end) };
}

function parseInline(text: string): InlineNode[] {
  const nodes: InlineNode[] = [];
  let lastIndex = 0;
  INLINE_REGEX.lastIndex = 0;
  let match: RegExpExecArray | null;
  while ((match = INLINE_REGEX.exec(text)) !== null) {
    if (match.index > lastIndex) {
      nodes.push({ type: "text", content: text.slice(lastIndex, match.index) });
    }
    const urlMatch = match[1];
    const codeMatch = match[2];
    if (urlMatch !== undefined) {
      const { url, trailing } = trimTrailingPunctuation(urlMatch);
      if (url.length > 0) {
        nodes.push({ type: "link", url });
        if (trailing.length > 0) {
          nodes.push({ type: "text", content: trailing });
        }
      } else {
        // Nothing left after trimming (e.g. a bare punctuation run) — keep
        // it as plain text rather than emitting an empty link.
        nodes.push({ type: "text", content: urlMatch });
      }
    } else if (codeMatch !== undefined) {
      nodes.push({ type: "code", content: codeMatch });
    }
    lastIndex = INLINE_REGEX.lastIndex;
  }
  if (lastIndex < text.length) {
    nodes.push({ type: "text", content: text.slice(lastIndex) });
  }
  return nodes;
}

function parseComment(text: string): Segment[] {
  const segments: Segment[] = [];
  let lastIndex = 0;
  FENCE_REGEX.lastIndex = 0;
  let match: RegExpExecArray | null;
  while ((match = FENCE_REGEX.exec(text)) !== null) {
    if (match.index > lastIndex) {
      segments.push({
        type: "text",
        nodes: parseInline(text.slice(lastIndex, match.index)),
      });
    }
    // Fenced content is rendered verbatim, on purpose: it is never re-run
    // through parseInline, so backticks inside a fenced block don't get
    // double-parsed as inline code.
    segments.push({ type: "code-block", content: match[1]! });
    lastIndex = FENCE_REGEX.lastIndex;
  }
  if (lastIndex < text.length) {
    segments.push({ type: "text", nodes: parseInline(text.slice(lastIndex)) });
  }
  return segments;
}

/** Renders an incident comment body: autolinked URLs, `inline code`, and
 * ```fenced``` code blocks, everything else as plain wrapped text. Always
 * emits React elements — never dangerouslySetInnerHTML — so untrusted
 * comment text (Slack, Telegram, API) can never inject markup. */
export function CommentBody({ text }: { text: string }) {
  const segments = parseComment(text);
  return (
    <div className="text-sm" data-testid="comment-body">
      {segments.map((segment, i) => {
        if (segment.type === "code-block") {
          return (
            <pre
              key={i}
              className="my-1 overflow-x-auto rounded bg-muted px-2 py-1.5 font-mono text-xs"
            >
              <code>{segment.content}</code>
            </pre>
          );
        }
        return (
          <span key={i} className="whitespace-pre-wrap break-words">
            {segment.nodes.map((node, j) => {
              if (node.type === "link") {
                return (
                  <a
                    key={j}
                    href={node.url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="break-all text-primary underline underline-offset-2 hover:no-underline"
                  >
                    {node.url}
                  </a>
                );
              }
              if (node.type === "code") {
                return (
                  <code
                    key={j}
                    className="rounded bg-muted px-1 py-0.5 font-mono text-xs"
                  >
                    {node.content}
                  </code>
                );
              }
              return <Fragment key={j}>{node.content}</Fragment>;
            })}
          </span>
        );
      })}
    </div>
  );
}
