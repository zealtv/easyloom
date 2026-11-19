// A small markdown renderer for instructions.md.
//
// It is deliberately not a general markdown implementation. It covers what a
// loom's instructions are actually written in — headings, paragraphs, lists,
// fenced code, blockquotes, rules, and inline emphasis, code and links — and
// ignores the rest, which renders as the plain text it already is.
//
// Safety is structural rather than a filter: every piece of text becomes a
// text node via `element`, and nothing here ever touches innerHTML. There is
// no markup to escape because no markup is ever built from a string. Link
// targets are the one exception, so those are checked against a scheme
// allowlist — a `javascript:` href in a file on disk must not become a live
// link.

import { element, selectable } from "./stitches.js";

const SAFE_LINK = /^(https?:|mailto:|#|\/|\.{0,2}\/)/i;

/** renderMarkdown returns a fragment for a markdown document.
 *
 * `wikilink(id)` resolves a [[stitch-id]] to a URL, or returns null for a
 * stitch this loom does not have — a link that goes nowhere is worse than
 * plain text.
 */
export function renderMarkdown(text, wikilink = () => null) {
  const fragment = document.createDocumentFragment();
  const lines = String(text ?? "").replace(/\r\n?/g, "\n").split("\n");

  let index = 0;
  const peek = () => lines[index];

  while (index < lines.length) {
    const line = peek();

    if (line.trim() === "") {
      index += 1;
      continue;
    }

    // Fenced code. An unterminated fence runs to the end of the document,
    // which is what every other renderer does and what a truncated file looks
    // like.
    const fence = /^\s*(```+|~~~+)(.*)$/.exec(line);
    if (fence) {
      index += 1;
      const body = [];
      while (index < lines.length && !new RegExp(`^\\s*${fence[1][0]}{${fence[1].length},}\\s*$`).test(lines[index])) {
        body.push(lines[index]);
        index += 1;
      }
      index += 1; // the closing fence, or the end
      const pre = element("pre", "code");
      const code = element("code", null, body.join("\n"));
      if (fence[2].trim()) code.className = `language-${fence[2].trim()}`;
      pre.append(code);
      fragment.append(pre);
      continue;
    }

    const heading = /^(#{1,6})\s+(.*)$/.exec(line);
    if (heading) {
      index += 1;
      // h1 in a document embedded in a page is a heading level too high, so
      // every level shifts down one and stops at h6.
      const node = element(`h${Math.min(heading[1].length + 1, 6)}`);
      node.append(...inline(heading[2], wikilink));
      fragment.append(node);
      continue;
    }

    if (/^\s*([-*_])(\s*\1){2,}\s*$/.test(line)) {
      index += 1;
      fragment.append(element("hr"));
      continue;
    }

    if (/^\s*>/.test(line)) {
      const body = [];
      while (index < lines.length && /^\s*>/.test(lines[index])) {
        body.push(lines[index].replace(/^\s*>\s?/, ""));
        index += 1;
      }
      const quote = element("blockquote");
      quote.append(renderMarkdown(body.join("\n"), wikilink));
      fragment.append(quote);
      continue;
    }

    const bullet = /^(\s*)([-*+]|\d+[.)])\s+/.exec(line);
    if (bullet) {
      const ordered = !/^[-*+]$/.test(bullet[2]);
      const list = element(ordered ? "ol" : "ul", "md-list");
      while (index < lines.length) {
        const item = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/.exec(lines[index]);
        if (!item) break;
        index += 1;
        // Continuation lines belong to the item they are indented under.
        const parts = [item[3]];
        while (index < lines.length && /^\s+\S/.test(lines[index]) && !/^(\s*)([-*+]|\d+[.)])\s+/.test(lines[index])) {
          parts.push(lines[index].trim());
          index += 1;
        }
        const entry = element("li");
        entry.append(...inline(parts.join(" "), wikilink));
        list.append(entry);
      }
      fragment.append(list);
      continue;
    }

    // Anything else is a paragraph, running until a blank line or a block that
    // starts on its own.
    const paragraph = [];
    while (index < lines.length && lines[index].trim() !== "" && !isBlockStart(lines[index])) {
      paragraph.push(lines[index].trim());
      index += 1;
    }
    const node = element("p");
    node.append(...inline(paragraph.join(" "), wikilink));
    fragment.append(node);
  }

  return fragment;
}

function isBlockStart(line) {
  return (
    /^\s*(```+|~~~+)/.test(line) ||
    /^#{1,6}\s/.test(line) ||
    /^\s*>/.test(line) ||
    /^(\s*)([-*+]|\d+[.)])\s+/.test(line) ||
    /^\s*([-*_])(\s*\1){2,}\s*$/.test(line)
  );
}

// Inline constructs, in the order they are recognised. Code wins over
// everything else, so `**not bold**` inside backticks stays literal.
const INLINE = [
  { name: "code", pattern: /`([^`]+)`/ },
  { name: "wikilink", pattern: /\[\[([^\]]+)\]\]/ },
  { name: "link", pattern: /\[([^\]]*)\]\(([^)\s]+)\)/ },
  { name: "bold", pattern: /\*\*([^*]+)\*\*|__([^_]+)__/ },
  { name: "italic", pattern: /(?<![\w*])\*([^*\n]+)\*(?![\w*])|(?<![\w_])_([^_\n]+)_(?![\w_])/ },
  { name: "autolink", pattern: /<(https?:\/\/[^>\s]+)>|(?<![(\w])(https?:\/\/[^\s<>)]+)/ },
];

/** inline turns one line of markdown into an array of nodes. */
export function inline(text, wikilink = () => null) {
  const source = String(text ?? "");
  let earliest = null;

  for (const rule of INLINE) {
    const match = rule.pattern.exec(source);
    if (match && (earliest === null || match.index < earliest.match.index)) {
      earliest = { rule, match };
    }
  }
  if (!earliest) return source ? [document.createTextNode(source)] : [];

  const { rule, match } = earliest;
  const before = source.slice(0, match.index);
  const after = source.slice(match.index + match[0].length);
  const nodes = before ? [document.createTextNode(before)] : [];
  nodes.push(node(rule, match, wikilink));
  return nodes.concat(inline(after, wikilink));
}

function node(rule, match, wikilink) {
  switch (rule.name) {
    case "code":
      return element("code", null, match[1]);
    case "wikilink": {
      // A loom's instructions link to other stitches by ID. Resolving them
      // makes the plan navigable; an unresolved one stays visible as text,
      // because a dangling ID is information too.
      const id = match[1].trim();
      const href = wikilink(id);
      if (!href) return element("span", "wikilink dangling", id);
      // Marked with the stitch it names, so the shell can select the target
      // rather than let the browser navigate away from what is being read.
      const link = selectable(element("a", "wikilink", id), id);
      link.href = href;
      return link;
    }
    case "link": {
      const label = match[1] || match[2];
      if (!SAFE_LINK.test(match[2])) return document.createTextNode(`${label} (${match[2]})`);
      const link = element("a", null, label);
      link.href = match[2];
      if (/^https?:/i.test(match[2])) {
        link.rel = "noreferrer";
        link.target = "_blank";
      }
      return link;
    }
    case "bold": {
      const strong = element("strong", null, match[1] ?? match[2]);
      return strong;
    }
    case "italic":
      return element("em", null, match[1] ?? match[2]);
    case "autolink": {
      const url = match[1] ?? match[2];
      const link = element("a", null, url);
      link.href = url;
      link.rel = "noreferrer";
      link.target = "_blank";
      return link;
    }
    default:
      return document.createTextNode(match[0]);
  }
}
