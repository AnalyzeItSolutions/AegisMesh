# LinkedIn post (draft, for you to edit and post yourself)

I wanted to see how far a staged builder could take a hard brief, so I gave it one page: an offline-first, multi-tenant disaster-response platform. Then I checked everything against the real stack instead of trusting it.

What came out:
- A field app that works with no signal and merges reports (CRDT) when the network returns
- A command center with live unit positions and a time-travel slider over the real event log
- Event-sourced dispatch with a tamper-evident hash chain in Postgres
- An Envoy gateway that validates sign-in tokens and keeps each organisation's data apart, with row-level security as a second wall

What checking found, which no unit test had:
- The chaos test (kill the broker, kill the service, under load) lost 11 acknowledged messages. Redpanda's dev mode caches writes. One setting fixed it; 2,000 of 2,000 now survive.
- A late edit from a phone that had been offline could be skipped by the sync cursor, because it ordered by clock instead of arrival.
- A "secure" gateway config that routed gRPC to the wrong paths and had no token check at all.

Not done, and the README says so: no cloud deployment yet, no mTLS between services, service worker only tested in simulation.

`make demo` runs the whole thing locally. [link to the repo / case study]

#systemsdesign #offlinefirst #disasterresponse #golang #nextjs
