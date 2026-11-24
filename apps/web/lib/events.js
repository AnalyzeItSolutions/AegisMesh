// Loading the event log for time travel.
import { parseEvents, Timeline } from './replay.js';

/**
 * GETs `${baseUrl}/events` (a JSON array of event-store events) and builds a Timeline.
 * Returns {status: 'ready', timeline} or {status: 'unavailable', reason}: a missing or broken endpoint is reported, never thrown, so the live map keeps working.
 */
export async function fetchTimeline(baseUrl, { fetchImpl = globalThis.fetch, headers = {}, every = 500 } = {}) {
  try {
    const res = await fetchImpl(`${baseUrl}/events`, { headers });
    if (!res.ok) return { status: 'unavailable', reason: `the events endpoint answered ${res.status}` };
    return { status: 'ready', timeline: new Timeline(parseEvents(await res.text()), { every }) };
  } catch (error) {
    return { status: 'unavailable', reason: error instanceof Error ? error.message : String(error) };
  }
}
