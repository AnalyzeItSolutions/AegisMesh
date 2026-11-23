// Local storage for the field app: key/value records and an ordered outbox of changes not yet sent. IndexedDB in a browser; an in-memory
// store with the same API when there is none (tests, private windows, old browsers), so the app keeps working, just not across restarts.

const clone = (v) => (v === undefined ? undefined : JSON.parse(JSON.stringify(v)));

export class MemoryStore {
  constructor() {
    this.kv = new Map();
    this.box = [];
    this.seq = 0;
  }

  async get(key) {
    return clone(this.kv.get(key));
  }

  async put(key, value) {
    this.kv.set(key, clone(value));
  }

  async delete(key) {
    this.kv.delete(key);
  }

  async keys() {
    return [...this.kv.keys()].sort();
  }

  /** Adds an entry to the end of the outbox; returns its id (ids only grow). */
  async outboxAdd(entry) {
    const id = ++this.seq;
    this.box.push({ ...clone(entry), id });
    return id;
  }

  async outboxAll() {
    return clone(this.box);
  }

  async outboxRemove(ids) {
    const gone = new Set(ids);
    this.box = this.box.filter((e) => !gone.has(e.id));
  }
}

const KV = 'kv';
const OUTBOX = 'outbox';

/** The IndexedDB store: object store `kv` (out-of-line keys) and `outbox` (auto-increment `id`). */
export class IdbStore {
  constructor(db) {
    this.db = db;
  }

  static open(name = 'aegismesh', factory = globalThis.indexedDB) {
    return new Promise((resolve, reject) => {
      const req = factory.open(name, 1);
      req.onupgradeneeded = () => {
        const db = req.result;
        db.createObjectStore(KV);
        db.createObjectStore(OUTBOX, { keyPath: 'id', autoIncrement: true });
      };
      req.onsuccess = () => resolve(new IdbStore(req.result));
      req.onerror = () => reject(req.error ?? new Error('could not open IndexedDB'));
    });
  }

  /** Runs fn(objectStore) in one transaction; resolves with the request's result once the transaction has completed. */
  #run(storeName, mode, fn) {
    return new Promise((resolve, reject) => {
      const tx = this.db.transaction(storeName, mode);
      let result;
      const req = fn(tx.objectStore(storeName));
      if (req) req.onsuccess = () => { result = req.result; };
      tx.oncomplete = () => resolve(result);
      tx.onerror = () => reject(tx.error ?? new Error('IndexedDB transaction failed'));
      tx.onabort = () => reject(tx.error ?? new Error('IndexedDB transaction aborted'));
    });
  }

  get(key) {
    return this.#run(KV, 'readonly', (s) => s.get(key));
  }

  async put(key, value) {
    await this.#run(KV, 'readwrite', (s) => s.put(clone(value), key));
  }

  async delete(key) {
    await this.#run(KV, 'readwrite', (s) => s.delete(key));
  }

  async keys() {
    return [...(await this.#run(KV, 'readonly', (s) => s.getAllKeys()))].sort();
  }

  outboxAdd(entry) {
    return this.#run(OUTBOX, 'readwrite', (s) => s.add(clone(entry)));
  }

  async outboxAll() {
    return [...(await this.#run(OUTBOX, 'readonly', (s) => s.getAll()))].sort((a, b) => a.id - b.id);
  }

  async outboxRemove(ids) {
    await this.#run(OUTBOX, 'readwrite', (s) => {
      for (const id of ids) s.delete(id);
    });
  }
}

/** IndexedDB when the environment has it and it opens, otherwise the in-memory store. */
export async function openStore({ name = 'aegismesh', indexedDB = globalThis.indexedDB } = {}) {
  if (indexedDB) {
    try {
      return await IdbStore.open(name, indexedDB);
    } catch {
      /* private window or blocked storage: fall through */
    }
  }
  return new MemoryStore();
}
