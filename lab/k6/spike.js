// Spike load matching ../design.json: 30 req/s, ramp to 120 req/s from
// t=60 s over 30 s, hold 240 s, ramp back over 30 s, 120 s at 30 req/s.
// Arrival-rate (open model): requests keep coming while the service is
// slow, as in ArchSim. The 2 s timeout matches the design's edge timeout
// and bounds in-flight requests (and k6's memory) to ~rate x 2 s.
import http from 'k6/http'

export const options = {
  discardResponseBodies: true,
  scenarios: {
    spike: {
      executor: 'ramping-arrival-rate',
      startRate: 30,
      timeUnit: '1s',
      preAllocatedVUs: 50,
      maxVUs: 300,
      stages: [
        { target: 30, duration: '60s' },
        { target: 120, duration: '30s' },
        { target: 120, duration: '240s' },
        { target: 30, duration: '30s' },
        { target: 30, duration: '120s' },
      ],
    },
  },
}

export default function () {
  http.get(__ENV.TARGET, { timeout: '2s' })
}
