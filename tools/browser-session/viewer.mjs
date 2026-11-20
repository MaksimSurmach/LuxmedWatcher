import RFB from '/vendor/core/rfb.js';

const status = document.getElementById('status');
const done = document.getElementById('done');
const ticket = location.hash.slice(1);
history.replaceState(null, '', location.pathname);
if (/^[a-f0-9]{64}$/.test(ticket)) {
  const endpoint = new URL('/vnc', location.href);
  endpoint.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const viewer = new RFB(document.getElementById('screen'), endpoint.href, {
    wsProtocols: ['binary', `ticket-${ticket}`],
  });
  viewer.scaleViewport = true;
  viewer.addEventListener('connect', () => {
    status.textContent = 'Войди в LuxMed и заверши MFA.';
    viewer.focus();
  });
  viewer.addEventListener('disconnect', () => {
    status.textContent = 'Просмотр закрыт. Для повторного входа запроси новую ссылку в боте.';
    done.disabled = true;
  });
  done.addEventListener('click', () => viewer.disconnect());
} else {
  status.textContent = 'Запроси ссылку на вход в Telegram.';
  done.disabled = true;
}
