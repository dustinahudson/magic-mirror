#!/usr/bin/env python3
"""Render SETUP.md to a printable PDF.

The setup guide is written as markdown because that is what stays readable in
the repo and in review. What people actually hand to someone with a mirror is
paper, and markdown printed straight from a browser breaks in the wrong places:
a screenshot split across two sheets, a heading stranded at the foot of a page.

So the markdown is converted here with print rules attached — figures and
tables kept whole, headings kept with the text they introduce — and rendered by
headless Chrome, which is already a dependency of taking the screenshots.

Images are embedded as data URIs rather than linked. A PDF that depends on
files next to it is one email away from being a document full of broken boxes.

Usage:  scripts/make-setup-pdf.py [--md SETUP.md] [--out docs/magic-mirror-setup.pdf]
"""

import argparse
import base64
import mimetypes
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile

try:
    import markdown
except ImportError:
    sys.exit("error: python-markdown not installed — pip install markdown")

ROOT = pathlib.Path(__file__).resolve().parent.parent

# Letter, not A4: the mirrors and the people printing this are in the US.
CSS = """
@page { size: letter; margin: 16mm 18mm; }

html { -webkit-print-color-adjust: exact; print-color-adjust: exact; }
body {
  font: 10.5pt/1.55 -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  color: #16161a; max-width: none; margin: 0;
}

h1 {
  font-size: 22pt; margin: 0 0 .3em; letter-spacing: -.01em;
  border-bottom: 2px solid #16161a; padding-bottom: .25em;
}
h2 {
  font-size: 14pt; margin: 1.6em 0 .5em;
  /* Keep a step's heading with at least the start of that step. A heading
     alone at the foot of a page is the single most common way a printed
     instruction sheet becomes hard to follow. */
  break-after: avoid; page-break-after: avoid; break-inside: avoid;
}
h1 + p { font-size: 11pt; color: #45454f; }

p, li { orphans: 3; widows: 3; }
ul, ol { padding-left: 1.3em; }
li { margin: .22em 0; }
li > strong:first-child { color: #000; }

/* Screenshots are of a dark UI, so they need a defined edge on white paper or
   they read as holes in the page. */
img {
  max-width: 100%; height: auto; display: block;
  margin: 1em auto; border: 1px solid #c8c8d0; border-radius: 4px;
  break-inside: avoid; page-break-inside: avoid;
}

table {
  border-collapse: collapse; width: 100%; margin: 1em 0; font-size: 9.5pt;
  break-inside: avoid; page-break-inside: avoid;
}
th, td { border: 1px solid #c8c8d0; padding: .45em .6em; text-align: left; vertical-align: top; }
th { background: #f0f0f4; }

blockquote {
  margin: 1.1em 0; padding: .7em 1em; border-left: 3px solid #8a8a99;
  background: #f5f5f8; break-inside: avoid; page-break-inside: avoid;
}
blockquote p { margin: .2em 0; }

code {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: .9em; background: #f0f0f4; padding: .1em .3em; border-radius: 3px;
}

hr { border: 0; border-top: 1px solid #d8d8e0; margin: 1.8em 0; }

.footer {
  margin-top: 2.5em; padding-top: .8em; border-top: 1px solid #d8d8e0;
  font-size: 8.5pt; color: #6a6a76; break-inside: avoid;
}
"""


def embed_images(html: str, base: pathlib.Path) -> str:
    """Rewrite every <img> source to a data URI, so the PDF stands alone.

    Matches the whole tag and finds src inside it, rather than assuming where
    the attribute sits: python-markdown emits alt before src, so a pattern
    anchored on `<img src=` silently matches nothing and produces a document
    of broken-image boxes that still renders, still weighs a plausible number
    of kilobytes, and is wrong on every page.
    """

    def repl(m):
        tag = m.group(0)
        found = re.search(r'src="([^"]+)"', tag)
        if not found:
            return tag
        src = found.group(1)
        if src.startswith(("data:", "http://", "https://")):
            return tag
        path = (base / src).resolve()
        if not path.is_file():
            sys.exit(f"error: SETUP.md references a missing image: {src}")
        mime = mimetypes.guess_type(path.name)[0] or "image/png"
        b64 = base64.b64encode(path.read_bytes()).decode("ascii")
        return tag.replace(f'src="{src}"', f'src="data:{mime};base64,{b64}"')

    html = re.sub(r"<img[^>]*>", repl, html)

    # Assert the rewrite actually happened. Every failure mode above is
    # invisible in the finished PDF unless someone opens it and looks.
    stragglers = [t for t in re.findall(r"<img[^>]*>", html) if 'src="data:' not in t]
    if stragglers:
        sys.exit(f"error: {len(stragglers)} image(s) were not embedded: {stragglers[:2]}")
    return html


def version() -> str:
    try:
        out = subprocess.run(
            ["git", "describe", "--tags", "--always"],
            cwd=ROOT, capture_output=True, text=True, timeout=10,
        )
        return out.stdout.strip() or "dev"
    except Exception:
        return "dev"


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--md", default=str(ROOT / "SETUP.md"))
    ap.add_argument("--out", default=str(ROOT / "docs" / "magic-mirror-setup.pdf"))
    args = ap.parse_args()

    md_path = pathlib.Path(args.md).resolve()
    out_path = pathlib.Path(args.out).resolve()
    if not md_path.is_file():
        sys.exit(f"error: no such file: {md_path}")

    chrome = next(
        (c for c in ("google-chrome", "google-chrome-stable", "chromium", "chromium-browser")
         if shutil.which(c)), None,
    )
    if not chrome:
        sys.exit("error: no Chrome/Chromium found to render the PDF")

    body = markdown.markdown(
        md_path.read_text(encoding="utf-8"),
        extensions=["tables", "sane_lists", "attr_list"],
    )
    body = embed_images(body, md_path.parent)

    footer = (
        f'<p class="footer">Magic Mirror setup guide — {version()}. '
        f"The current version of this guide lives in the project repository.</p>"
    )
    html = (
        "<!doctype html><html><head><meta charset='utf-8'>"
        "<title>Magic Mirror — setup</title>"
        f"<style>{CSS}</style></head><body>{body}{footer}</body></html>"
    )

    out_path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory() as tmp:
        src = pathlib.Path(tmp) / "setup.html"
        src.write_text(html, encoding="utf-8")
        cmd = [
            chrome, "--headless", "--disable-gpu", "--no-sandbox",
            "--no-pdf-header-footer",
            f"--print-to-pdf={out_path}",
            "--virtual-time-budget=10000",
            src.as_uri(),
        ]
        res = subprocess.run(cmd, capture_output=True, text=True, timeout=180)
        if not out_path.is_file():
            sys.exit(f"error: Chrome produced no PDF\n{res.stderr[-2000:]}")

    kb = out_path.stat().st_size / 1024
    print(f"wrote {out_path.relative_to(ROOT)} ({kb:.0f} KB)")


if __name__ == "__main__":
    main()
