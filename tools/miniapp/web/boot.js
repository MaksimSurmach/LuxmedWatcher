const boot = document.querySelector('#boot');
window.Telegram?.WebApp?.ready();

try {
  if (boot.dataset.entry === 'dashboard') {
    const { ready } = await import('/dashboard.js');
    await ready;
  } else {
    await import('/app.js');
  }
  boot.hidden = true;
} catch {
  boot.dataset.failed = '';
  boot.querySelector('[role="status"]').textContent = boot.dataset.bootError;
}
