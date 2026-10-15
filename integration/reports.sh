#!/usr/bin/env bash
# Exercise the shipped HTML catalog using the real renderer, with no server,
# remote service, browser session, or AI-generated substitutions.
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
template="${TEMPLATE_SOURCE:-$here/../template-base}"
: "${VB_REAL_BIN:?Set VB_REAL_BIN to the vb binary under test}"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT
python3 - "$VB_REAL_BIN" "$template" "$scratch" <<'PY'
import json,re,subprocess,sys
from pathlib import Path
binary,template,scratch=sys.argv[1:]
root=Path(template)/'templates/reports/html'
work=Path(scratch)
def expanded(path):
    text=re.sub(r'<!--.*?-->','',path.read_text(),flags=re.S)
    return re.sub(r'{{INCLUDE:\s*([^{}]+)}}',lambda m:expanded(root/m.group(1).strip()),text)
count=0
for path in sorted(root.glob('*.html')):
    text=expanded(path)
    lists=set(re.findall(r'{{#([A-Z][A-Z0-9_]*)}}',text))
    names=set(re.findall(r'{{([A-Z][A-Z0-9_]*)}}',text))
    data={name:([] if name.endswith('_JSON') else 0 if name.startswith(('KPI_','PCT_')) else '') for name in names}
    data.update({name:[] for name in lists})
    for name in ('BRAND_NAME','BRAND_TAGLINE','BRAND_LOGO_DATAURI','PROJECT_NAME'):data.pop(name,None)
    data['REPORT_TITLE']='Catalog render <safety>'
    data_file=work/(path.stem+'.json')
    data_file.write_text(json.dumps(data))
    output=work/path.name
    args=[binary,'--root',template,'report','render','--template',path.name,'--data',str(data_file),'--output',str(output)]
    subprocess.run(args,check=True,capture_output=True)
    first=output.read_bytes()
    assert b'{{' not in first, path
    assert b'Catalog render &lt;safety&gt;' in first, path
    assert b'data:image/jpeg;base64,' in first, path
    subprocess.run(args+['--force'],check=True,capture_output=True)
    assert output.read_bytes()==first, path
    count+=1
# The authored sample exercises nonempty lists and reviewed HTML, not only the
# empty-state smoke data above. Its output must also be reproducible.
sample=Path(template)/'templates/reports/examples/pm-progress-report.data.json'
if sample.is_file():
    output=work/'progress-example.html'
    args=[binary,'--root',template,'report','render','--template','pm-progress-report.html','--data',str(sample),'--output',str(output),'--allow-html']
    subprocess.run(args,check=True,capture_output=True)
    html=output.read_text()
    assert 'FTR-0042' in html and 'Progress report renderer' in html
    assert '<span class="green">Progress</span>' in html
    assert '{{' not in html
print(f'report catalog: {count} templates rendered deterministically with escaped values and default logos')
PY
