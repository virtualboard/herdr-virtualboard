#!/usr/bin/env bash
# Real terminal lifecycle and key decoding, with explicit stubs and no session.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
trap teardown EXIT
setup
# A named child project makes captured terminal previews readable; teardown
# still owns and removes the original scratch parent.
mkdir "$ROOT/virtualboard-demo"
mv "$ROOT/.virtualboard" "$ROOT/virtualboard-demo/.virtualboard"
ROOT="$ROOT/virtualboard-demo"
export VB_STUB_ROOT="$ROOT" HERDR_STUB_CWD="$ROOT"
add_spec "$ROOT" backlog FTR-0001 "Alpha active" '[backend]'
add_spec "$ROOT" review FTR-0002 "Beta backend" '[backend]'
add_spec "$ROOT" done FTR-0003 "Completed work" '[frontend]'
echo '11-terminal-ux: raw terminal, search, filters, completed collapse, and help'
if python3 - "$HVB" "$ROOT" <<'PY'
import fcntl,os,pty,select,struct,subprocess,sys,termios,time
from pathlib import Path
binary,root=sys.argv[1:]
# A missing explicit override must fail here rather than use ambient tools.
for name in ('HERDR_BIN_PATH','HVB_VB_BIN'):
    assert os.environ.get(name) and Path(os.environ[name]).is_file(), name
before={p:p.read_bytes() for p in (Path(root)/'.virtualboard/features').rglob('FTR-*.md')}
master,slave=pty.openpty()
fcntl.ioctl(slave,termios.TIOCSWINSZ,struct.pack('HHHH',35,140,0,0))
original=termios.tcgetattr(slave)
proc=None
pending=b''
transcript=bytearray()
def expect(needle):
    global pending
    deadline=time.monotonic()+5
    while needle not in pending:
        remaining=deadline-time.monotonic()
        if remaining<=0:raise AssertionError(f'no terminal frame containing {needle!r}; tail: {bytes(transcript[-1500:])!r}')
        ready,_,_=select.select([master],[],[],remaining)
        if not ready:continue
        chunk=os.read(master,65536)
        if not chunk:raise AssertionError(f'terminal closed before {needle!r}')
        transcript.extend(chunk);pending+=chunk
    pending=pending.split(needle,1)[1]
def key(value):os.write(master,value)
def capture(name):
    directory=os.environ.get('HVB_TUI_CAPTURE_DIR')
    if directory:
        output=Path(directory);output.mkdir(parents=True,exist_ok=True)
        (output/(name+'.ansi')).write_bytes(transcript)
try:
    env=dict(os.environ);env.pop('HERDR_PANE_ID',None)
    proc=subprocess.Popen([binary,'--root',root,'tui','--color=false','--refresh=1s'],stdin=slave,stdout=slave,stderr=slave,env=env,start_new_session=True)
    expect(b'Completed work')
    key(b'/');expect(b'Search and filters')
    key(b'Alpha');expect('Alpha▏'.encode());capture('filters')
    key(b'\r');expect(b'search: Alpha')
    key(b'c');expect(b'Filters cleared.')
    key(b'D');expect(b'D to expand completed');expect(b'Completed cards collapsed');capture('completed-collapsed')
    key(b'D');expect(b'Completed work')
    key(b'?');expect('hvb — VirtualBoard on Herdr'.encode())
    key(b'\x1b[F');expect(b'This board')
    key(b'q');expect(b'BACKLOG')
    key(b'q')
    # Keep draining while awaiting exit: otherwise the PTY's finite output
    # buffer can block the final Draw/restore before the queued q is read.
    deadline=time.monotonic()+5
    while proc.poll() is None:
        remaining=deadline-time.monotonic()
        if remaining<=0:raise AssertionError('TUI did not exit after q')
        ready,_,_=select.select([master],[],[],min(remaining,0.1))
        if ready:
            try:chunk=os.read(master,65536)
            except OSError:chunk=b''
            transcript.extend(chunk)
    assert proc.returncode==0, 'TUI exited unsuccessfully'
    assert termios.tcgetattr(slave)==original, 'raw terminal settings were not restored'
    assert all(p.read_bytes()==data for p,data in before.items()), 'view controls mutated feature files'
    print('terminal: search, clear, collapse/expand, scrollable help, quit, terminal restoration, and spec preservation passed')
finally:
    if proc is not None and proc.poll() is None:
        proc.terminate()
        try:proc.wait(timeout=4)
        except subprocess.TimeoutExpired:proc.kill();proc.wait()
    os.close(master);os.close(slave)
PY
then
  pass "real terminal controls and restoration preserve repository data"
else
  fail "real terminal workflow failed"
fi
finish
