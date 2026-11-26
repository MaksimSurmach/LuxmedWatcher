import { viewerCopy } from './viewer-copy.mjs';

const copy = viewerCopy(document.documentElement.lang);
const status = document.getElementById('status');
const done = document.getElementById('done');
const ticket = location.hash.slice(1);
history.replaceState(history.state, '', location.pathname + location.search);

async function connectViewer() {
  if (!/^[a-f0-9]{64}$/.test(ticket)) {
    status.textContent = copy.missing;
    return;
  }

  try {
    const { default: RFB } = await import('/vendor/core/rfb.js');
    const endpoint = new URL('/vnc', location.href);
    endpoint.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const viewer = new RFB(document.getElementById('screen'), endpoint.href, {
      wsProtocols: ['binary', `ticket-${ticket}`],
    });
    viewer.scaleViewport = true;
    done.disabled = false;
    viewer.addEventListener('connect', () => {
      status.textContent = copy.connected;
      viewer.focus();
    });
    viewer.addEventListener('disconnect', (event) => {
      status.textContent = event.detail.clean ? copy.closed : copy.failed;
      done.disabled = true;
    });
    done.addEventListener('click', () => viewer.disconnect());
  } catch {
    status.textContent = copy.failed;
  }
}

void connectViewer();
