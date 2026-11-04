// Constant 30 req/s for 60 s: run.sh reads the pod's CPU use (cgroup
// cpu.stat) around it to measure the CPU each request really costs, HTTP
// handling and the Go runtime included.
import http from 'k6/http'

export const options = {
  discardResponseBodies: true,
  scenarios: {
    steady: {
      executor: 'constant-arrival-rate',
      rate: 30,
      timeUnit: '1s',
      duration: '60s',
      preAllocatedVUs: 20,
      maxVUs: 50,
    },
  },
}

export default function () {
  http.get(__ENV.TARGET, { timeout: '2s' })
}
