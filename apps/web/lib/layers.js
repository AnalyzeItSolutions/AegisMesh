// Deck.gl layer descriptions as plain objects: {type, props}. The page turns each into `new deck[type](props)`. Nothing here imports deck.gl,
// so the colour, position and refresh rules are testable without a browser. Draw order: hazards, incidents, units (units on top).

export const SEVERITY_COLOR = {
  black: [30, 30, 30, 255],
  red: [220, 38, 38, 255],
  yellow: [234, 179, 8, 255],
  green: [34, 197, 94, 255],
};
export const UNIT_COLOR = [37, 99, 235, 255];
export const STALE_UNIT_COLOR = [140, 140, 140, 200];
export const STALE_AFTER_MS = 60_000; // a unit not heard from for this long is drawn grey
const COLOUR_BUCKET_MS = 10_000; // staleness is re-evaluated at most this often, not on every frame

export function unitLayer(units, { now = Date.now(), selectedId = null } = {}) {
  return {
    type: 'ScatterplotLayer',
    props: {
      id: 'units',
      data: units, // the marker store's array: same object until a unit changes, so deck.gl skips unchanged data
      pickable: true,
      radiusUnits: 'pixels',
      getPosition: (u) => [u.lon, u.lat],
      getRadius: (u) => (u.unit_id === selectedId ? 9 : 5),
      getFillColor: (u) => (now - u.ts > STALE_AFTER_MS ? STALE_UNIT_COLOR : UNIT_COLOR),
      updateTriggers: { getFillColor: [Math.floor(now / COLOUR_BUCKET_MS)], getRadius: [selectedId] },
    },
  };
}

export function incidentLayer(incidents) {
  return {
    type: 'ScatterplotLayer',
    props: {
      id: 'incidents',
      data: incidents,
      pickable: true,
      radiusUnits: 'pixels',
      getPosition: (i) => [i.lon, i.lat],
      getRadius: (i) => (i.status === 'dispatched' ? 7 : 10),
      getFillColor: (i) => SEVERITY_COLOR[i.severity] ?? [120, 120, 120, 255],
      updateTriggers: { getFillColor: [incidents.length], getRadius: [incidents.length] },
    },
  };
}

export function hazardLayer(hazards) {
  return {
    type: 'PolygonLayer',
    props: {
      id: 'hazards',
      data: hazards,
      pickable: true,
      stroked: true,
      filled: true,
      getPolygon: (h) => h.polygon, // [[lon, lat], ...]
      getFillColor: (h) => (h.active ? [220, 38, 38, 70] : [150, 150, 150, 40]),
      getLineColor: (h) => (h.active ? [220, 38, 38, 255] : [150, 150, 150, 200]),
      lineWidthMinPixels: 1,
    },
  };
}

/** All layers for the current state; ids are stable so deck.gl updates them instead of recreating them. */
export function buildLayers({ units = [], incidents = [], hazards = [], now = Date.now(), selectedId = null } = {}) {
  return [hazardLayer(hazards), incidentLayer(incidents), unitLayer(units, { now, selectedId })];
}
