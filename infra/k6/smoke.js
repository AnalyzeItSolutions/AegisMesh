import http from 'k6/http';
import { check } from 'k6';
export const options = { vus: 10, duration: '10s', thresholds: { http_req_failed: ['rate<0.01'] } };
export default function () { const r = http.get(__ENV.TARGET || 'http://localhost:8080/'); check(r, { 'status is 2xx': (x) => x.status >= 200 && x.status < 300 }); }
