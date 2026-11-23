// The sync batch as it travels (see CONTRACT.md):
//   {replica_id, clock, lww:[{key, value, hlc, replica_id, deleted?}], orset_adds:[{set, element, tag}], orset_removes:[{set, tag}]}
// `cursor` (pull replies) is the server's arrival-order position: ask for since_seq=cursor next time. It is NOT derived from clocks, because a device
// that was offline can push a write with an old clock after everyone else has pulled past it.
// `hlc` and `clock` are 64-bit integers (wall << 16 | counter, about 1.1e17), more than a JS number holds exactly (2^53, about 9e15). JSON.parse would
// round them and order writes wrongly, so they are read and written as exact digits and handled as BigInt.
import { hlcToStamp, stampToHlc } from './crdt.js';

const BIG_FIELDS = /"(hlc|clock)":\s*(-?\d+)/g; // inside a string value the quotes are escaped (\"), so a value cannot fake this pattern

/** Parses a batch; hlc and clock become BigInt. Throws on anything that is not a well-formed batch. */
export function decodeBatch(text) {
  const doc = JSON.parse(text.replace(BIG_FIELDS, '"$1":"$2"'));
  if (doc === null || typeof doc !== 'object' || Array.isArray(doc)) throw new TypeError('batch must be a JSON object');
  const lww = (doc.lww ?? []).map((e) => {
    if (typeof e.key !== 'string' || e.key === '' || typeof e.replica_id !== 'string' || e.replica_id === '' || typeof e.hlc !== 'string') throw new TypeError('bad lww entry');
    return { key: e.key, value: String(e.value ?? ''), hlc: BigInt(e.hlc), replica_id: e.replica_id, deleted: !!e.deleted };
  });
  const adds = (doc.orset_adds ?? []).map((a) => {
    if (!a.set || !a.element || !a.tag) throw new TypeError('bad orset add');
    return { set: a.set, element: a.element, tag: a.tag };
  });
  const removes = (doc.orset_removes ?? []).map((r) => {
    if (!r.set || !r.tag) throw new TypeError('bad orset remove');
    return { set: r.set, tag: r.tag };
  });
  return { replica_id: String(doc.replica_id ?? ''), clock: BigInt(doc.clock ?? '0'), cursor: Number(doc.cursor ?? 0), lww, orset_adds: adds, orset_removes: removes };
}

/** Serialises a batch with hlc and clock as exact integer literals. */
export function encodeBatch(batch) {
  const text = JSON.stringify({ ...batch, clock: String(batch.clock ?? 0n), lww: batch.lww.map((e) => ({ ...e, hlc: String(e.hlc) })) });
  return text.replace(/"(hlc|clock)":"(-?\d+)"/g, '"$1":$2');
}

/** A local delta (stamps as {wall, counter, node}) -> a wire batch. */
export function toBatch(replicaId, delta, clock = 0n) {
  return {
    replica_id: replicaId,
    clock,
    lww: delta.lww.map(({ key, reg }) => ({ key, value: reg.value, hlc: stampToHlc(reg.stamp), replica_id: reg.stamp.node, deleted: !!reg.deleted })),
    orset_adds: delta.adds,
    orset_removes: delta.removes,
  };
}

/** A wire batch -> a local delta (LWW entries with stamps, set adds and removes). */
export function fromBatch(batch) {
  return {
    lww: batch.lww.map((e) => ({ key: e.key, reg: { value: e.value, stamp: hlcToStamp(e.hlc, e.replica_id), deleted: e.deleted } })),
    adds: batch.orset_adds,
    removes: batch.orset_removes,
  };
}
