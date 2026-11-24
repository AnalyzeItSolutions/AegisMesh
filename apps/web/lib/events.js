// Loading the event log for time travel.
import { parseEvents, Timeline } from './replay.js';

const PAGE = 5000; // events per request (the server allows up to 20000)
const MAX_EVENTS = 250_000; // beyond this the log is not loaded whole: say so instead of freezing the tab

/**
 * GETs `${baseUrl}/events?after=N&limit=M` page by page until a page comes back short, and builds a Timeline from the whole log.
 * Returns {status: 'ready', timeline} or {status: 'unavailable', reason}: a missing or broken endpoint is reported, never thrown, so the live map keeps working.
 */
export async function fetchTimeline(baseUrl, { fetchImpl = globalThis.fetch, headers = {}, every = 500, page = PAGE, maxEvents = MAX_EVENTS } = {}) {
  try {
    const events = [];
    let after = 0;
    for (;;) {
      const res = await fetchImpl(`${baseUrl}/events?after=${after}&limit=${page}`, { headers: typeof headers === 'function' ? await headers() : headers });
      if (!res.ok) return { status: 'unavailable', reason: `the events endpoint answered ${res.status}` };
      const batch = parseEvents(await res.text());
      events.push(...batch);
      if (batch.length < page) break;
      after = batch[batch.length - 1].sequence_id; // not a response header: a browser may not be allowed to read custom headers
      if (events.length >= maxEvents) return { status: 'unavailable', reason: `the log has more than ${maxEvents} events: too many to load whole` };
    }
    return { status: 'ready', timeline: new Timeline(events, { every }) };
  } catch (error) {
    return { status: 'unavailable', reason: error instanceof Error ? error.message : String(error) };
  }
}
