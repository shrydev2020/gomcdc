import json
import re
import sys
from pathlib import Path


def evaluation_log(match):
    """Preserve table cells as tab-separated escaped text."""
    records = re.findall(rb"<tr>(.*?)</tr>", match[0], re.S)
    lines = []
    for row in records:
        cells = re.findall(rb"<t[hd]\b[^>]*>(.*?)</t[hd]>", row, re.S)
        lines.append(b"\t".join(re.sub(rb"<[^>]*>", b"", cell) for cell in cells))
    return b'<pre class="evaluation-log">' + b"\n".join(lines) + b"</pre>"


def element_count(content):
    return len(re.findall(rb"<[a-zA-Z][^>]*>", content))


def main():
    if len(sys.argv) != 3:
        raise SystemExit("Usage: diagnostic-transforms.py SINGLE_HTML OUTPUT_DIRECTORY")
    output = Path(sys.argv[2])
    output.mkdir(parents=True, exist_ok=True)
    original = Path(sys.argv[1]).read_bytes()
    pattern = rb"<table><caption>Observed evaluations;.*?</table>"
    matches = list(re.finditer(pattern, original, re.S))
    if not matches:
        raise ValueError("Input must contain the original observed-evaluation tables")
    summary = re.search(rb'<header class="panel" id="summary">.*?</header>', original, re.S)
    triage = re.search(rb'<section class="panel" id="triage">.*?</section>', original, re.S)
    if summary is None or triage is None:
        raise ValueError("Input must contain summary and package-triage sections")
    measurements = {
        "evaluation_tables": len(matches),
        "observations": sum(match[0].count(b"<tr>") - 1 for match in matches),
        "bytes": sum(len(match[0]) for match in matches),
        "elements": sum(element_count(match[0]) for match in matches),
        "largest_table_bytes": max(len(match[0]) for match in matches),
    }
    (output / "no-evaluations.html").write_bytes(re.sub(pattern, b"", original, flags=re.S))
    flat = re.sub(pattern, evaluation_log, original, flags=re.S)
    css = (
        b"<style>.package-card,.finding{content-visibility:auto;contain-intrinsic-size:auto 500px}"
        b".evaluation-log{white-space:pre;max-height:28rem;overflow:auto}</style>"
    )
    flat = flat.replace(b"</head>", css + b"</head>")
    (output / "text-log-approximation.html").write_bytes(flat)
    measurements.update({
        "text_log_approximation_bytes": len(flat),
        "text_log_approximation_elements": element_count(flat),
        "approximation_note": "Diagnostic transformation retaining each table row and cell as text.",
    })
    encoded = json.dumps(measurements, indent=2)
    print(encoded)
    (output / "ablations.json").write_text(encoded)
    head = original[:original.index(b"<body>")]
    (output / "overview-only.html").write_bytes(
        head + b'<body><main class="main">' + summary[0] + triage[0] + b"</main></body></html>"
    )


if __name__ == "__main__":
    main()
