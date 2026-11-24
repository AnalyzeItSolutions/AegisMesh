// The live WebSocket to the telemetry service: connects, reconnects with growing pauses, and always tells the server the CURRENT viewport after
// a (re)connect, because the server forgets a viewport when the connection drops.

const STATES = ['connecting', 'open', 'closed'];

export class LiveConnection {
  /**
   * @param url            ws:// or wss:// address, or a function returning one (or a promise of one), called on EVERY connect so a renewed token is used
   * @param WebSocketImpl  the WebSocket class (injectable for tests)
   * @param onMessage      (text) => void
   * @param onState        (state) => void, state is 'connecting' | 'open' | 'closed'
   * @param delays         pauses before successive reconnect attempts (ms); the last one repeats
   * @param timers         {set(fn, ms) -> handle, clear(handle)} (injectable for tests)
   */
  constructor({ url, WebSocketImpl = globalThis.WebSocket, onMessage, onState = () => {}, delays = [500, 1000, 2000, 5000, 10_000], timers = { set: setTimeout, clear: clearTimeout } }) {
    this.url = url;
    this.WS = WebSocketImpl;
    this.onMessage = onMessage;
    this.onState = onState;
    this.delays = delays;
    this.timers = timers;
    this.socket = null;
    this.state = 'closed';
    this.attempt = 0;
    this.timer = null;
    this.viewport = null; // the latest subscription message
    this.stopped = true;
    this.generation = 0;
  }

  #set(state) {
    if (!STATES.includes(state)) throw new Error(`bad state ${state}`);
    this.state = state;
    this.onState(state);
  }

  start() {
    this.stopped = false;
    this.#connect();
  }

  #connect() {
    this.timer = null;
    this.#set('connecting');
    if (typeof this.url !== 'function') return this.#open(this.url);
    const attempt = ++this.generation;
    Promise.resolve()
      .then(() => this.url())
      .then((u) => {
        if (!this.stopped && attempt === this.generation) this.#open(u);
      })
      .catch(() => {
        if (!this.stopped && attempt === this.generation) this.#retry(); // could not get an address (no session): try again later
      });
  }

  #retry() {
    this.#set('closed');
    const pause = this.delays[Math.min(this.attempt, this.delays.length - 1)];
    this.attempt++;
    this.timer = this.timers.set(() => this.#connect(), pause);
  }

  #open(url) {
    const ws = new this.WS(url);
    this.socket = ws;
    ws.onopen = () => {
      if (this.socket !== ws) return;
      this.attempt = 0;
      this.#set('open');
      if (this.viewport) ws.send(this.viewport); // the server forgot it
    };
    ws.onmessage = (e) => {
      if (this.socket === ws) this.onMessage(typeof e.data === 'string' ? e.data : String(e.data));
    };
    ws.onclose = () => {
      if (this.socket !== ws) return;
      this.socket = null;
      this.#set('closed');
      if (!this.stopped) {
        const pause = this.delays[Math.min(this.attempt, this.delays.length - 1)];
        this.attempt++;
        this.timer = this.timers.set(() => this.#connect(), pause);
      }
    };
    ws.onerror = () => {}; // an error is always followed by a close, which handles it
  }

  /** Sends the viewport message now if connected; either way it is remembered and sent again after a reconnect. */
  send(viewportMessage) {
    this.viewport = viewportMessage;
    if (this.state === 'open' && this.socket) this.socket.send(viewportMessage);
  }

  stop() {
    this.stopped = true;
    this.generation++;
    if (this.timer) this.timers.clear(this.timer);
    this.timer = null;
    const ws = this.socket;
    this.socket = null;
    if (ws) ws.close();
    this.#set('closed');
  }
}
