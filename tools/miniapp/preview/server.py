#!/usr/bin/env python3
"""Design preview for the Mini App: serves tools/miniapp/web with canned API data.

    python3 tools/miniapp/preview/server.py [port]

Open /?tab=history, /?tab=watches or add &scenario=empty|session|long|booking
(booking: the server offers booking; the 08:30 time answers "taken", others "confirmed") and &lang=pl|en|ru|uk|be.
No Telegram, database or LUX MED account is needed."""
import json, sys, pathlib
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse, parse_qs

ROOT = pathlib.Path(__file__).resolve().parents[3]
WEB = ROOT / 'tools/miniapp/web'
I18N = ROOT / 'internal/i18n/web'
STUB = '<script>window.Telegram={WebApp:{initData:%s,ready(){},expand(){},openLink(u){window.__opened=u}}}</script>'
TYPES = {'.js': 'text/javascript', '.css': 'text/css', '.html': 'text/html'}

def day(offset, hh, mm):
    d = (datetime.now(timezone.utc) + timedelta(days=offset)).replace(hour=hh - 1, minute=mm, second=0, microsecond=0)
    return d.isoformat().replace('+00:00', 'Z')

BOOKED = {}    # slot ID -> 'confirmed' | 'taken'
AUTOBOOK = {2: True}  # watch ID -> enabled, booking scenario only

def slots(scenario):
    if scenario == 'empty':
        return []
    long = scenario == 'long'
    f1 = 'Centrum Medyczne Warszawa' if not long else 'Centrum Medyczne Warszawa-Śródmieście Specjalistyczne Poradnie Kardiologiczne'
    a1 = 'ul. Postępu 21C' if not long else 'ul. Marszałkowska-Wspólna im. Generała Władysława Sikorskiego 121/123 lok. 4'
    d1 = 'Anna Nowak' if not long else 'dr hab. n. med. Małgorzata Wierzchowska-Przybyszewska'
    svc = 'Echo serca (USG serca)' if not long else 'Konsultacja kardiologiczna z zaleceniami po badaniu echokardiograficznym'
    row = lambda i, t, doc, fac, adr, st='new': dict(ID=i, WatchID=1, DateTime=t, Service=svc, Doctor=doc, Facility=fac, Address=adr, Status=st)
    rows = [row(1, day(5, 8, 15), d1, f1, a1), row(2, day(5, 8, 30), d1, f1, a1),
            dict(row(3, day(5, 9, 40), 'Jan Mazur', f1, a1), Service='Konsultacja ortopedyczna'),
            row(6, day(5, 11, 0), 'Jan Mazur', f1, 'ul. Prosta 51'),
            row(4, day(6, 12, 5), 'Jan Mazur', f1, 'ul. Prosta 51'),
            row(5, day(-1, 10, 0), d1, f1, a1, 'expired')]
    # Booking results persist for the server's lifetime: confirmed times leave the list, taken ones stay unavailable.
    rows = [dict(r, Status='expired') if BOOKED.get(r['ID']) == 'taken' else r for r in rows if BOOKED.get(r['ID']) != 'confirmed']
    return rows

def watches(scenario):
    base = dict(ID=1, Name='Echo serca', ServiceName='Echo serca (USG serca)', CityName='Warszawa', Status='active',
                NextDays=14, DateFrom=None, DateTo=None, DoctorName='', TimeWindows=[], CheckIntervalSeconds=600,
                LastCheckedAt=day(0, 12, 0), LastSuccessAt=day(0, 12, 0), LastError='')
    if scenario == 'long':
        base.update(ServiceName='Konsultacja kardiologiczna z zaleceniami po badaniu echokardiograficznym', CityName='Warszawa-Śródmieście',
                    DoctorName='dr hab. n. med. Małgorzata Wierzchowska-Przybyszewska')
    second = dict(base, ID=2, ServiceName='Konsultacja ortopedyczna', Status='paused', LastSuccessAt=None, LastCheckedAt=None)
    if scenario == 'session':
        base.update(LastError='luxmed_authentication', LastSuccessAt=day(-1, 9, 42))
    if scenario == 'booking':
        base['AutoBook'], second['AutoBook'] = AUTOBOOK.get(1, False), AUTOBOOK.get(2, False)
    return [base, second]

def book(body):
    sid = slot_id(body)
    BOOKED[sid] = 'taken' if sid == 2 else 'confirmed'
    return BOOKED[sid]

def autobook(body):
    data = json.loads(body or b'{}')
    AUTOBOOK[data.get('ID')] = bool(data.get('Enabled'))
    return {}

def slot_id(body):
    try:
        return json.loads(body or b'{}').get('ID')
    except ValueError:
        return None

class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def send(self, body, ctype, code=200):
        self.send_response(code); self.send_header('Content-Type', ctype); self.send_header('Content-Length', str(len(body))); self.end_headers(); self.wfile.write(body)
    def do_GET(self):
        u = urlparse(self.path); q = parse_qs(u.query)
        if u.path.startswith('/i18n/'):
            return self.send((I18N / (u.path[6:] + '.json')).read_bytes(), 'application/json')
        name = 'dashboard.html' if u.path == '/' else u.path.lstrip('/')
        f = (WEB / name).resolve()
        if WEB not in f.parents or not f.is_file(): return self.send(b'', 'text/plain', 404)
        body = f.read_bytes()
        if name == 'dashboard.html':
            # Scenario and language travel in initData, then in the session token, because the page
            # sends no Referer and the API calls carry no query string.
            params = json.dumps('&'.join(f'{k}={q[k][0]}' for k in ('scenario', 'lang') if k in q) or 'preview')
            body = body.replace(b'<script src="https://telegram.org/js/telegram-web-app.js?65"></script>', (STUB % params).encode())
        self.send(body, TYPES.get(f.suffix, 'application/octet-stream'))
    def do_POST(self):
        n = int(self.headers.get('Content-Length') or 0); body = self.rfile.read(n)
        action = self.path.rsplit('/', 1)[-1]
        raw = self.headers.get('X-Telegram-Init-Data') if action == 'auth' else self.headers.get('Authorization', '').removeprefix('Bearer ')
        params = parse_qs(raw or '')
        scenario, lang = params.get('scenario', [''])[0], params.get('lang', ['ru'])[0]
        booking = scenario == 'booking'
        handler = {'auth': lambda: {'token': raw or ''},
                'book': lambda: dict(Status=book(body)),
                'autobook': lambda: autobook(body),
                'load': lambda: dict(Watches=watches(scenario), Locale=lang, Booking=booking, CityID=1, CityName='Warszawa', MinimumMinutes=2, DefaultMinutes=10),
                'history': lambda: dict(Slots=slots(scenario), More=False),
                'account': lambda: dict(state='needs_login' if scenario == 'session' else 'connected'),
                'cities': lambda: [dict(ID=1, Name='Warszawa')]}.get(action)
        data = handler() if handler else {}
        self.send(json.dumps(data).encode(), 'application/json')

if __name__ == '__main__':
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8765
    ThreadingHTTPServer(('0.0.0.0', port), H).serve_forever()
