import re

body = open("body.html").read()

# Cover page: title, subtitle, intro, contents.
m = re.search(r"<h1>(.*?)</h1>", body, re.S)
title = m.group(1).strip() if m else "Encryl"
rest = body[m.end():] if m else body
first_h2 = rest.find("<h2")
intro = rest[:first_h2] if first_h2 >= 0 else rest
sections = rest[first_h2:] if first_h2 >= 0 else ""

headings = re.findall(r"<h2>(.*?)</h2>", sections, re.S)


def clean_heading(h):
    text = re.sub(r"<.*?>", "", h).strip()
    return re.sub(r"^\d+\.\s*", "", text)


contents = "\n".join("<li>%s</li>" % clean_heading(h) for h in headings)

cover = f"""
<section class="cover">
  <div class="kicker">TECHNICAL GUIDE</div>
  <h1 class="cover-title">{title}</h1>
  <div class="rule"></div>
  <p class="subtitle">The v1 design in simple English, with diagrams.<br/>
  Includes the link to the earlier <code>uring-io-rewrite</code> design.</p>
  <div class="intro">{intro}</div>
  <div class="contents">
    <div class="contents-title">Contents</div>
    <ol>{contents}</ol>
  </div>
  <div class="cover-art"><img src="guide-01.svg" alt=""/></div>
</section>
"""

css = """
@page { size: A4; margin: 18mm 16mm 20mm 16mm; }
* { box-sizing: border-box; }
body {
  font-family: "Helvetica Neue", Helvetica, Arial, sans-serif;
  font-size: 10.6pt; line-height: 1.55; color: #1b1f24; margin: 0;
}
h1, h2, h3, h4 { color: #0f4c81; page-break-after: avoid; }
h1 { font-size: 20pt; }
h2 {
  font-size: 15.5pt; margin: 0 0 6pt; padding-bottom: 4pt;
  border-bottom: 2px solid #0f4c81; page-break-before: always;
}
h3 { font-size: 12pt; margin: 14pt 0 4pt; }
p { margin: 0 0 7pt; }
ul, ol { margin: 0 0 8pt 16pt; padding: 0; }
li { margin-bottom: 3pt; }
strong { color: #0b3a61; }
code {
  font-family: "SFMono-Regular", Menlo, Consolas, monospace;
  font-size: 9.2pt; background: #f1f4f8; padding: 1pt 3pt; border-radius: 3pt;
  color: #7a2e0e;
}
table {
  width: 100%; border-collapse: collapse; margin: 8pt 0 12pt;
  font-size: 9.4pt; page-break-inside: avoid;
}
th {
  background: #e8f0f7; color: #0f4c81; text-align: left;
  border: 1px solid #c7d7e5; padding: 5pt 6pt;
}
td { border: 1px solid #d5dfe8; padding: 5pt 6pt; vertical-align: top; }
tr:nth-child(even) td { background: #fafcfe; }
img {
  display: block; margin: 8pt auto 12pt; max-width: 100%; max-height: 168mm;
  page-break-inside: avoid;
}
blockquote {
  margin: 8pt 0; padding: 6pt 10pt; border-left: 3px solid #9fc0dd;
  background: #f5f9fc; color: #33414f;
}
hr { border: 0; border-top: 1px solid #d5dfe8; margin: 12pt 0; }

/* Cover */
.cover { page-break-after: always; }
.kicker {
  letter-spacing: 3pt; font-size: 8.5pt; font-weight: 700;
  color: #5b7fa3; margin-top: 6mm;
}
.cover-title {
  font-size: 34pt; line-height: 1.08; margin: 6pt 0 10pt; color: #0f4c81;
  border: 0;
}
.rule { width: 42mm; height: 4pt; background: #d9822b; margin: 0 0 10pt; }
.subtitle { font-size: 12.5pt; color: #45566a; margin-bottom: 14pt; }
.intro { font-size: 10pt; color: #33414f; }
.contents { margin-top: 12pt; }
.contents-title {
  font-size: 10pt; font-weight: 700; letter-spacing: 1.5pt;
  color: #0f4c81; margin-bottom: 4pt;
}
.contents ol { columns: 2; column-gap: 12mm; font-size: 9.4pt; margin-left: 14pt; }
.cover-art { margin-top: 12pt; }
.cover-art img { max-height: 58mm; }

.content h2:first-of-type { page-break-before: always; }
"""

html = f"""<!doctype html>
<html lang="en"><head><meta charset="utf-8"/>
<title>{re.sub(r'<.*?>', '', title)}</title>
<style>{css}</style></head>
<body>
{cover}
<section class="content">
{sections}
</section>
</body></html>
"""

open("VISUAL-GUIDE.html", "w").write(html)
print("wrote VISUAL-GUIDE.html", len(html))
