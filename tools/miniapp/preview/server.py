#!/usr/bin/env python3
"""Design preview for the Mini App: serves tools/miniapp/web with canned API data.

    python3 tools/miniapp/preview/server.py [port] [host]

Listens on 127.0.0.1 unless a host is given.

Open /?tab=history, /?tab=watches or add &scenario=empty|session|long|booking
(booking: the server offers booking; the 08:30 time answers "taken", others "confirmed") and &lang=pl|en|ru|uk|be.
No Telegram, database or LUX MED account is needed."""
import json, sys, pathlib
from zoneinfo import ZoneInfo
from datetime import datetime, timedelta, timezone
from html import escape
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse, parse_qs

ROOT = pathlib.Path(__file__).resolve().parents[3]
WEB = ROOT / 'tools/miniapp/web'
I18N = ROOT / 'internal/i18n/web'
STUB = '<script>window.Telegram={WebApp:{initDataUnsafe:{user:{first_name:"Anna",last_name:"Kowalska"}},initData:%s,ready(){},expand(){},openLink(u){window.__opened=u}}}</script>'
SCENARIOS = {'', 'empty', 'session', 'long', 'booking'}
LANGS = {'pl', 'en', 'ru', 'uk', 'be'}
RESTORED = set()  # scenarios whose LUX MED sign-in was restored through connect
TYPES = {'.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.html': 'text/html', '.woff2': 'font/woff2'}

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
    now = datetime.now(timezone.utc)
    return sorted((r for r in rows if r['Status'] != 'expired' and datetime.fromisoformat(r['DateTime']) > now),
                  key=lambda r: r['DateTime'])

def available_count(scenario):
    return len(slots(scenario))

def history(scenario, body):
    groups = {}
    for row in slots(scenario):
        day = datetime.fromisoformat(row['DateTime']).astimezone(ZoneInfo('Europe/Warsaw')).date().isoformat()
        groups.setdefault(day, []).append(row)
    params = json.loads(body)
    requested = params.get('Day', '')
    day = requested if requested in groups else next(iter(groups), '')
    linked = next((row for row in groups.get(day, []) if row['ID'] == params.get('SlotID')), None)
    return dict(Day=day, Linked=linked, Days=[dict(Date=k, Count=len(v)) for k,v in groups.items()],
                Slots=groups.get(day, []) if requested else slots(scenario), More=False)

def watches(scenario):
    base = dict(ID=1, Name='Echo serca', ServiceID=11, ServiceName='Echo serca (USG serca)', CityID=1, CityName='Warszawa', Status='active',
                NextDays=14, DateFrom=None, DateTo=None, DoctorName='', TimeWindows=[], CheckIntervalSeconds=600,
                LastCheckedAt=day(0, 12, 0), LastSuccessAt=day(0, 12, 0), LastError='')
    if scenario == 'long':
        base.update(ServiceName='Konsultacja kardiologiczna z zaleceniami po badaniu echokardiograficznym', CityName='Warszawa-Śródmieście',
                    DoctorName='dr hab. n. med. Małgorzata Wierzchowska-Przybyszewska')
    second = dict(base, ID=2, ServiceName='Konsultacja ortopedyczna', Status='paused', LastSuccessAt=None, LastCheckedAt=None)
    if scenario == 'session' and scenario not in RESTORED:
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

def pick(params, key, allowed, default):
    value = params.get(key, [default])[0]
    return value if value in allowed else default

def choose(q):
    values = [(k, pick(q, k, allowed, '')) for k, allowed in (('scenario', SCENARIOS), ('lang', LANGS))]
    return '&'.join(f'{k}={v}' for k, v in values if v) or 'preview'

def connect(host, scenario, lang):
    RESTORED.add(scenario)
    return {'url': f'http://{host}/?ticket=preview'}


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
        if u.path == '/i18n.js':
            dictionaries = {lang: json.loads((I18N / f'{lang}.json').read_bytes()) for lang in sorted(LANGS)}
            return self.send(('export default ' + json.dumps(dictionaries)).encode(), TYPES['.js'])
        if u.path.startswith('/i18n/'):
            locale = u.path[6:]
            if locale not in LANGS: return self.send(b'', 'text/plain', 404)
            return self.send((I18N / f'{locale}.json').read_bytes(), 'application/json')
        if u.path == '/' and q.get('embedded') == ['1']:
            locale = pick(q, 'lang', LANGS, 'pl')
            labels = json.loads((I18N / f'{locale}.json').read_bytes())
            html = ('<main><h1>LUX MED · Preview</h1><button>' + escape(labels['viewer.finish']) +
                    '</button></main><script>document.querySelector("button").onclick=async()=>{'
                    'await frameElement.loginSession.issueTicket();frameElement.loginSession.finish()}</script>')
            return self.send(html.encode(), 'text/html; charset=utf-8')
        name = 'dashboard.html' if u.path == '/' else u.path.lstrip('/')
        f = (WEB / name).resolve()
        if WEB not in f.parents or not f.is_file(): return self.send(b'', 'text/plain', 404)
        body = f.read_bytes()
        if name in ('dashboard.html', 'index.html'):
            locale = pick(q, 'lang', LANGS, 'pl')
            labels = json.loads((I18N / f'{locale}.json').read_bytes())
            for field, value in {
                'Locale': locale, 'JavaScript': labels['common.javascript_required'],
                'Loading': labels['dashboard.loading'], 'Unavailable': labels['error.unavailable'],
            }.items():
                body = body.replace(('{{.' + field + '}}').encode(), escape(value).encode())
            # Scenario and language travel in initData, then in the session token, because the page
            # sends no Referer and the API calls carry no query string.
            # Only allow-listed values reach the inline script, and '<' is escaped for the HTML context.
            params = json.dumps(choose(q)).replace('<', '\\u003c')
            body = body.replace(b'<script defer src="https://telegram.org/js/telegram-web-app.js?65"></script>', (STUB % params).encode())
        self.send(body, TYPES.get(f.suffix, 'application/octet-stream'))
    def do_POST(self):
        n = int(self.headers.get('Content-Length') or 0); body = self.rfile.read(n)
        action = self.path.rsplit('/', 1)[-1]
        raw = self.headers.get('X-Telegram-Init-Data') if action == 'auth' else self.headers.get('Authorization', '').removeprefix('Bearer ')
        params = parse_qs(raw or '')
        scenario, lang = pick(params, 'scenario', SCENARIOS, ''), pick(params, 'lang', LANGS, 'ru')
        booking = scenario == 'booking'
        handler = {'auth': lambda: {'token': raw or ''},
                'book': lambda: dict(Status=book(body)),
                'autobook': lambda: autobook(body),
                'connect': lambda: connect(self.headers.get('Host', 'localhost'), scenario, lang),
                'load': lambda: dict(Watches=watches(scenario), AvailableSlots=available_count(scenario), AccountStatus='needs_login' if scenario == 'session' and scenario not in RESTORED else 'connected', Locale=lang, Booking=booking, CityID=1, CityName='Warszawa', MinimumMinutes=2, DefaultMinutes=10),
                'history': lambda: history(scenario, body),
                'account': lambda: dict(state='needs_login' if scenario == 'session' and scenario not in RESTORED else 'connected'),
                'cities': lambda: [dict(ID=1, Name='Warszawa')],
                'procedures': lambda: dict(Items=[dict(ID=11, Name='Echo serca (USG serca)'), dict(ID=12, Name='Konsultacja dermatologiczna')], More=False),
                'procedure-picks': lambda: dict(Recent=[dict(ID=11, Name='Echo serca (USG serca)')], Popular=[dict(ID=12, Name='Konsultacja dermatologiczna'), dict(ID=13, Name='Konsultacja ortopedyczna')]),
                'providers': lambda: dict(Doctors=[dict(ID=1, FirstName='Anna', LastName='Nowak')], Facilities=[dict(ID=1, Name='Centrum Medyczne Warszawa', Address='ul. Postępu 21C')])}.get(action)
        data = handler() if handler else {}
        self.send(json.dumps(data).encode(), 'application/json')

if __name__ == '__main__':
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8765
    host = sys.argv[2] if len(sys.argv) > 2 else '127.0.0.1'
    ThreadingHTTPServer((host, port), H).serve_forever()
