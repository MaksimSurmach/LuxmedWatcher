import { viewerCopy } from './viewer-copy.mjs';

const copy = viewerCopy(document.documentElement.lang);
const status = document.getElementById('status');
const done = document.getElementById('done');
const screen = document.getElementById('screen');
const ticket = location.hash.slice(1);
history.replaceState(history.state, '', location.pathname + location.search);

async function connectViewer() {
  if (!/^[a-f0-9]{64}$/.test(ticket)) {
    status.textContent = copy.missing;
    screen.hidden = true;
    return;
  }

  try {
    const { default: RFB } = await import('/vendor/core/rfb.js');
    const endpoint = new URL('/vnc', location.href);
    endpoint.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const viewer = new RFB(screen, endpoint.href, {
      wsProtocols: ['binary', `ticket-${ticket}`],
    });
    viewer.scaleViewport = true;
    let connected = false;
    let stopped = false;
    done.disabled = false;
    viewer.addEventListener('connect', () => {
      connected = true;
      status.textContent = copy.connected;
      viewer.focus();
    });
    viewer.addEventListener('disconnect', (event) => {
      status.textContent = stopped || (connected && event.detail.clean) ? copy.closed : copy.failed;
      screen.hidden = true;
      done.disabled = true;
    });
    done.addEventListener('click', () => {
      stopped = true;
      viewer.disconnect();
    });
  } catch {
    status.textContent = copy.failed;
    screen.hidden = true;
  }
}

void connectViewer();
