const boot = document.querySelector('#boot');
boot.querySelector('a').addEventListener('click', (event) => {
  event.preventDefault();
  location.reload();
});

window.Telegram?.WebApp?.ready();
try {
  if (boot.dataset.entry === 'dashboard') {
    await import('/dashboard.js');
  } else {
    await import('/app.js');
  }
  boot.hidden = true;
} catch {
  boot.querySelector('[role="status"]').textContent = boot.dataset.bootError;
}
