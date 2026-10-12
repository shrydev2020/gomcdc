import re,json,sys
from pathlib import Path
output=Path(sys.argv[2]); output.mkdir(parents=True,exist_ok=True)
b=Path(sys.argv[1]).read_bytes()
pattern=rb'<table><caption>Observed evaluations;.*?</table>'
matches=list(re.finditer(pattern,b,re.S))
rows=sum(m[0].count(b'<tr>')-1 for m in matches)
out={'evaluation_tables':len(matches),'observations':rows,'bytes':sum(len(m[0]) for m in matches),'elements':sum(len(re.findall(rb'<[a-zA-Z][^>]*>',m[0])) for m in matches),'largest_table_bytes':max(len(m[0]) for m in matches)}
(output/'no-evaluations.html').write_bytes(re.sub(pattern,b'',b,flags=re.S))
# Controlled diagnostic approximation of local pending template: same table cells become tab-separated escaped text, no observations removed.
def log(m):
 table=m[0]; records=re.findall(rb'<tr>(.*?)</tr>',table,re.S)
 lines=[]
 for row in records:
  cells=re.findall(rb'<t[hd]\b[^>]*>(.*?)</t[hd]>',row,re.S)
  lines.append(b'\t'.join(re.sub(rb'<[^>]*>',b'',c) for c in cells))
 return b'<pre class="evaluation-log">'+b'\n'.join(lines)+b'</pre>'
flat=re.sub(pattern,log,b,flags=re.S)
css=b'<style>.package-card,.finding{content-visibility:auto;contain-intrinsic-size:auto 500px}.evaluation-log{white-space:pre;max-height:28rem;overflow:auto}</style>'
flat=flat.replace(b'</head>',css+b'</head>')
(output/'text-log-approximation.html').write_bytes(flat)
out['text_log_approximation_bytes']=len(flat);out['text_log_approximation_elements']=len(re.findall(rb'<[a-zA-Z][^>]*>',flat));out['approximation_note']='Diagnostic transform of existing HTML, not regenerated current report; same rows/columns but current template separates run/package/process.'
print(json.dumps(out,indent=2));(output/'ablations.json').write_text(json.dumps(out,indent=2))

head=b[:b.index(b'<body>')]
summary=re.search(rb'<header class="panel" id="summary">.*?</header>',b,re.S)[0]
triage=re.search(rb'<section class="panel" id="triage">.*?</section>',b,re.S)[0]
(output/'overview-only.html').write_bytes(head+b'<body><main class="main">'+summary+triage+b'</main></body></html>')
