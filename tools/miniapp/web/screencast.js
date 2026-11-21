const width = 1180;
const height = 728;
const limit = (value, size) => Math.max(0, Math.min(size - 1, Math.floor(value)));

export class Screencast extends EventTarget {
  constructor(screen, address, protocols) {
    super();
    this.screen = screen;
    this.canvas = document.createElement('canvas');
    this.canvas.width = width;
    this.canvas.height = height;
    this.canvas.tabIndex = 0;
    this.context = this.canvas.getContext('2d', { alpha: false });
    this.socket = new WebSocket(address, protocols);
    this.socket.addEventListener('message', (event) => {
      if (!(event.data instanceof Blob)) return;
      this.frame = event.data;
      if (!this.decoding) void this.drawFrames();
    });
    this.socket.addEventListener('close', () => {
      this.dispatchEvent(new Event('disconnect'));
    });
    screen.classList.add('screencast');
    screen.append(this.canvas);
    this.bindInput();
  }

  async drawFrames() {
    this.decoding = true;
    while (this.frame && this.socket.readyState === WebSocket.OPEN) {
      const url = URL.createObjectURL(this.frame);
      this.frame = undefined;
      const image = new Image();
      image.src = url;
      try {
        await image.decode();
        if (this.socket.readyState !== WebSocket.OPEN) break;
        this.context.drawImage(image, 0, 0, width, height);
        if (!this.connected) {
          this.connected = true;
          this.dispatchEvent(new Event('connect'));
        }
      } catch {
        // A damaged frame can be replaced by the next frame.
      } finally {
        URL.revokeObjectURL(url);
      }
    }
    this.decoding = false;
  }

  disconnect() {
    this.socket.close();
    this.frame = undefined;
    this.canvas.remove();
  }

  focus(options) {
    this.canvas.focus(options);
  }

  setZoom(zoomed) {
    this.zoomed = zoomed;
    this.screen.classList.toggle('screencast-zoomed', zoomed);
  }

  send(message) {
    if (this.connected && this.socket.readyState === WebSocket.OPEN) {
      this.socket.send(JSON.stringify(message));
    }
  }

  sendText(text) {
    if (text) this.send({ kind: 'text', text });
  }

  sendKey(key, code) {
    for (const phase of ['down', 'up']) this.send({ kind: 'key', phase, key, code });
  }

  point(clientX, clientY) {
    const rect = this.canvas.getBoundingClientRect();
    return {
      x: limit((clientX - rect.left) * width / rect.width, width),
      y: limit((clientY - rect.top) * height / rect.height, height),
    };
  }

  pointer(phase, event, button) {
    this.send({ kind: 'pointer', phase, ...this.point(event.clientX, event.clientY), button });
  }

  bindInput() {
    const canvas = this.canvas;
    let press;
    canvas.addEventListener('pointerdown', (event) => {
      if (press || event.button > 2 || !this.connected) return;
      event.preventDefault();
      if (event.pointerType !== 'touch') this.focus({ preventScroll: true });
      canvas.setPointerCapture(event.pointerId);
      const { pointerId, clientX, clientY, button } = event;
      const deferred = event.pointerType === 'touch' || (this.zoomed && button === 0);
      press = { pointerId, button, clientX, clientY, lastX: clientX, lastY: clientY, deferred, moved: false };
      if (!deferred) this.pointer('down', event, button);
    });
    canvas.addEventListener('pointermove', (event) => {
      if (press && event.pointerId !== press.pointerId) return;
      if (!press?.deferred) {
        if (event.pointerType !== 'touch') this.pointer('move', event, press?.button ?? 0);
        return;
      }
      const { clientX, clientY } = event;
      if (!press.moved && Math.hypot(clientX - press.clientX, clientY - press.clientY) < 8) return;
      press.moved = true;
      const dx = press.lastX - clientX;
      const dy = press.lastY - clientY;
      if (this.zoomed) {
        this.screen.scrollBy(dx, dy);
      } else {
        const scale = width / canvas.getBoundingClientRect().width;
        this.send({ kind: 'wheel', ...this.point(press.clientX, press.clientY), dx: dx * scale, dy: dy * scale });
      }
      press.lastX = clientX;
      press.lastY = clientY;
    });
    const release = (event) => {
      if (event.pointerId !== press?.pointerId) return;
      if (!press.deferred) this.pointer('up', event, press.button);
      else if (!press.moved && event.type === 'pointerup') {
        this.pointer('down', event, press.button);
        this.pointer('up', event, press.button);
      }
      press = undefined;
    };
    canvas.addEventListener('pointerup', release);
    canvas.addEventListener('pointercancel', release);
    canvas.addEventListener('lostpointercapture', release);
    canvas.addEventListener('contextmenu', (event) => event.preventDefault());
    canvas.addEventListener('wheel', (event) => {
      event.preventDefault();
      const unit = event.deltaMode === WheelEvent.DOM_DELTA_LINE ? 16 :
        event.deltaMode === WheelEvent.DOM_DELTA_PAGE ? height : 1;
      this.send({ kind: 'wheel', ...this.point(event.clientX, event.clientY), dx: event.deltaX * unit, dy: event.deltaY * unit });
    }, { passive: false });

    const pressedKeys = new Map();
    for (const type of ['keydown', 'keyup']) {
      canvas.addEventListener(type, (event) => {
        if (!this.connected || event.isComposing) return;
        event.preventDefault();
        const { key, code } = event;
        const phase = type === 'keydown' ? 'down' : 'up';
        const message = { kind: 'key', phase, key, code };
        if (phase === 'down') {
          pressedKeys.set(code, key);
          if (!event.ctrlKey && !event.metaKey && !event.altKey && Array.from(key).length === 1) message.text = key;
        } else {
          pressedKeys.delete(code);
        }
        this.send(message);
      });
    }
    canvas.addEventListener('blur', () => {
      for (const [code, key] of pressedKeys) this.send({ kind: 'key', phase: 'up', key, code });
      pressedKeys.clear();
    });
  }
}
