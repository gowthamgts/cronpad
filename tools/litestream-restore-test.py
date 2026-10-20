#!/usr/bin/env python3
"""Local-file disaster recovery exercise; no object store or Docker required."""
import http.cookiejar
import http.server
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import sqlite3
import subprocess
import sys
import threading
import time
import urllib.request

root, scratch = map(Path, sys.argv[1:])
data = scratch / 'data'
data.mkdir()

def port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]

api_port = port()
class Echo(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        self.rfile.read(int(self.headers.get('Content-Length', '0')))
        self.send_response(204)
        self.end_headers()
    def log_message(self, *_):
        pass

echo = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Echo)
threading.Thread(target=echo.serve_forever, daemon=True).start()
env = dict(os.environ, CRONPAD_AUTH__DEV_LOGIN='true', CRONPAD_SECRETS__ENCRYPTION_KEY='AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=',
           CRONPAD_SERVER__ADDR=f'127.0.0.1:{api_port}',
           CRONPAD_DATABASE__PATH=str(data / 'cronpad.db'),
           CRONPAD_DATABASE__HISTORY_PATH=str(data / 'history.db'),
           LITESTREAM_REPLICA_URL=f'file://{scratch}/replicas',
           LITESTREAM_CONFIG=str(root / 'deploy/litestream.yml'),
           LITESTREAM_HISTORY_RESTORE_TIMEOUT='5s')
processes = []
logs = open(scratch / 'processes.log', 'w+')

def spawn(args, **kw):
    p = subprocess.Popen(args, stdout=logs, stderr=logs, start_new_session=True, **kw)
    processes.append(p)
    return p

def stop(p):
    if p.poll() is None:
        os.killpg(p.pid, signal.SIGTERM)
        try:
            p.wait(timeout=20)
        except subprocess.TimeoutExpired:
            os.killpg(p.pid, signal.SIGKILL)
            p.wait()

def wait_for(predicate, label, seconds=25):
    until = time.monotonic() + seconds
    while time.monotonic() < until:
        try:
            if predicate():
                return
        except (OSError, sqlite3.Error):
            pass
        time.sleep(.1)
    raise RuntimeError(f'timed out: {label}')

def count():
    with sqlite3.connect(data / 'history.db') as db:
        return db.execute("SELECT count(*) FROM runs WHERE status='succeeded'").fetchone()[0]

def ready():
    return urllib.request.urlopen(f'http://127.0.0.1:{api_port}/readyz', timeout=1).status == 200

try:
    first = spawn(['cronpad', 'entrypoint', 'serve'], env=env)
    wait_for(ready, 'first readiness')
    jar = http.cookiejar.CookieJar()
    client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    client.open(urllib.request.Request(f'http://127.0.0.1:{api_port}/auth/dev/login', b'name=restore-test')).close()
    csrf = next(c.value for c in jar if c.name == 'cp_csrf')
    body = json.dumps({'name': 'restore-test', 'schedule': {'expr': '@every 1s'},
                       'sink': {'kind': 'webhook', 'config': {'url': f'http://127.0.0.1:{echo.server_port}'}}}).encode()
    response = client.open(urllib.request.Request(f'http://127.0.0.1:{api_port}/api/v1/jobs', body,
                            {'Content-Type': 'application/json', 'X-CSRF-Token': csrf}))
    job = json.load(response)['id']
    wait_for(lambda: count() >= 3, 'runs before backup')
    time.sleep(3)  # Allow the 0.3.x CLI sync interval; shutdown flushes the final WAL.
    stop(first)
    before = count()
    # The wipe also removes the queue file, which is never replicated: restore must
    # reconstruct scheduling from the control DB alone.
    assert any(data.glob('*-queue.db')), 'queue file missing before wipe'
    for path in list(data.iterdir()):
        if path.is_dir():
            shutil.rmtree(path)
        else:
            path.unlink()
    second = spawn(['cronpad', 'entrypoint', 'serve'], env=env)
    wait_for(ready, 'restored readiness')
    with sqlite3.connect(data / 'cronpad.db') as db:
        assert db.execute('SELECT count(*) FROM jobs WHERE id=?', (job,)).fetchone()[0] == 1
    restored = count()
    assert restored >= before, (restored, before)
    wait_for(lambda: count() > restored, 'scheduling resumes')
    print(f'PASS local file:// replica: control job restored, history runs={restored} (before={before}), scheduling resumed runs={count()}')
finally:
    for p in reversed(processes):
        stop(p)
    echo.shutdown()
    logs.flush()
    print((scratch / 'processes.log').read_text())
    logs.close()
