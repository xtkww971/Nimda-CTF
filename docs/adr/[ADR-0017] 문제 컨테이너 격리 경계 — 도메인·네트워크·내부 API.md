* **Date:** 2026-09-18
* **Author:** 이도현
* **Status:** Proposed (design only, not implemented)

# ADR-0017: Challenge Container Isolation Boundaries — Domain, Network, Internal API

## 1. Context

I dropped the gateway and settled on **the website proxying instance traffic**.
ADR-0015 goes on hold; only the split from ADR-0016 survives — the website decides
questions about *people*, the CTF server decides questions about *resources*.
Enforcement stays where it already is, in `SubdomainInstanceProxyFilter`.

Simplifying the design was the right call, but it left behind the things I had been
quietly assuming the gateway would handle. There turned out to be three, and they are
all the same question wearing different clothes:

> **Where exactly is the boundary between a container the participant controls
> and the rest of the platform?**

ADR-0009 called the agent a "privileged process." A challenge container is the opposite
end of that: a **hostile process**. For a web challenge, the participant getting code
execution isn't a risk — it's the entire point of the challenge. The right framing isn't
"this might get compromised," it's "this is built to be compromised."

The code already says as much:

> CTF challenges are inherently hostile code and are not to be trusted, so platform
> cookies are stripped here and only the challenge's own cookies are passed through.
> — comment on `stripPlatformCookies`

The instinct is right. Only one direction is actually covered.

### Problem 1 — an instance can overwrite the platform's login cookie

`stripPlatformCookies` filters the **request** direction only. Responses pass straight
through.

`InstanceProxy.forward`:

```java
outHeaders.add(e.getKey(), v); // Set-Cookie/Location 등 요청을 보낸 클라이언트에게 전달
```

`SubdomainInstanceProxyFilter.writeResponse`:

```java
for (Map.Entry<String, List<String>> e : upstream.getHeaders().entrySet()) {
    for (String v : e.getValue()) {
        response.addHeader(e.getKey(), v);
    }
}
```

So a challenge container only has to answer with this:

```
Set-Cookie: Authorization=<attacker-chosen value>; Domain=.nimda.kr; Path=/
```

`inst-abc.nimda.kr` is allowed to set cookies on its parent `nimda.kr`. Cookie scope is
not the origin — it is the **registrable domain** ([RFC 6265 §5.1.3][rfc6265-domain]) —
and `nimda.kr` is not on the Public Suffix List, so the browser accepts it.

A participant opens one challenge and their platform login cookie is silently replaced.
That is session fixation.

**And filtering the response header does not fix it.** The challenge page's JavaScript
can set the cookie directly:

```js
document.cookie = "Authorization=...; domain=.nimda.kr; path=/";
```

That happens inside the browser, where **the proxy cannot see it**. Header filtering is
not a real defense here.

Worth noting: a gateway would not have helped either. This happens as long as the two
share a registrable domain. It is a boundary problem, independent of where the proxy sits.

### Problem 2 — instances share a network and can reach each other

From `agent/internal/runtime/docker.go`:

```go
created, err := d.cli.ContainerCreate(ctx, cfg, hostCfg, &network.NetworkingConfig{}, nil, name)
```

`NetworkingConfig` is empty, so containers land on the **default bridge network**.
The default bridge permits container-to-container traffic, which means a participant can
scan from inside their own instance and **reach someone else's instance**.

In a CTF this is not merely "weak isolation." Pulling a flag out of someone else's
instance counts as solving the challenge. There is a path here where you skip the
challenge and rob the person next to you instead.

The container itself is already tightened — `CapDrop: ALL`, `no-new-privileges`, memory
and PID limits. The network is the one thing left blank.

### Problem 2b — `127.0.0.1` means two different machines

Same issue I wrote up in ADR-0016. I repeat it here because **it has the same fix as
Problem 2.**

The agent puts `CTF_INSTANCE_HOST=127.0.0.1` in the result message, and the daemon
publishes the container on the *host's* loopback. But Spring runs inside a container
(`backend-blue`, `ports: 127.0.0.1:8081:8080`), so dialing that address makes it talk
to itself.

Right now this only works by accident, when Spring is launched straight from the IDE.

### Problem 3 — an endpoint that hands out addresses to anyone who knows a uuid

The website needs some way to ask the CTF server "is this uuid alive, and where is it?"
If that path is reachable from outside, **anyone holding a uuid gets the real container
address.**

One thing worth stating explicitly, because it is easy to get backwards:
**knowing a uuid is not authentication.** A uuid travels in plaintext through DNS queries
and TLS SNI (ADR-0016), so it cannot be a secret. This endpoint therefore cannot protect
itself with the uuid — **the reachability itself has to be closed.**

## 2. Decision

Draw all three boundaries explicitly.

### 2-1. Put instances on a different registrable domain from the platform

Not `inst-{uuid}.nimda.kr` but a **separately registered domain** —
say `inst-{uuid}.nimda-ctf.kr`.

Because cookie scope is per registrable domain, splitting the domain makes Problem 1
disappear **structurally**. Nothing the challenge container does can touch a platform
cookie. No response inspection, no worrying about what the JavaScript might do.

This is the same reasoning behind Google serving user content from
`googleusercontent.com` ([Content hosting for the modern web][google-hosting]).

**Stripping `Set-Cookie` on responses is a stopgap until the domain split lands, nothing
more.** As shown above it cannot cover the JavaScript path, so it must not be treated as
the final defense.

Splitting the domain has two side effects:

* Platform cookies stop being attached to instance requests at all. `stripPlatformCookies`
  drops from "a safety net that fails silently if you forget it" to defense in depth.
* A wildcard certificate is needed on the new domain. A wildcard was always required, and
  **the current certbot setup (webroot = HTTP-01) cannot issue one.** Moving to DNS-01
  becomes part of this work.

### 2-2. Put instances on a dedicated network and reach them by container name

Create a `ctf-instances` network and attach containers to it explicitly at creation time.
The proxy (`backend-blue` / `green` today) joins the same network.

```
proxy ──▶ http://{uuid}:{container port}
```

The container name *is* the uuid, so Docker's embedded DNS resolves it directly.
**No host port is published at all.**

That single change resolves three things:

* The `127.0.0.1` mismatch is gone, because loopback is no longer involved.
* **Most of ADR-0013 becomes moot.** With no host ports there is no pool to allocate from,
  no exhaustion, no reuse collision, no firewall range. (pwn is a separate matter.)
* ADR-0015's goal — "80/443 are the only externally open ports" — is achieved
  **without a gateway.**

The network is created with **`internal: true`**, so challenge containers cannot reach the
internet. That closes reverse shells and outbound attacks in the same stroke. Image builds
run in a separate phase and are unaffected.

**Isolating instances from each other happens in stages.**

On a single `ctf-instances` network, instances can still see each other. That is the same
exposure as today's default bridge, so nothing gets worse — but Problem 2 remains open.
Cutting it off completely means **a separate network per instance**.

I am not doing that yet, because of cost. Docker's default address pool only yields on the
order of a few dozen user-defined bridges; going beyond that means editing
`default-address-pools` in `daemon.json`. The proxy would also have to attach and detach
from a network every time an instance comes and goes.

**I'll decide once the expected concurrent-instance count is known.** For a competition,
where the incentive to rob a neighbor is obvious, per-instance networks are going to be
necessary.

### 2-3. Keep the CTF lookup path inside the internal network

The path that answers "is this uuid alive, and where" stays reachable **only from within
`ctf-internal`**. The provisioner already straddles `ctf-internal` and `nimda-network`
(ADR-0012), so the website asks through the provisioner. No host port, no nginx route.

It does not try to defend itself with the uuid. As established above, the uuid is not a
secret.

## 3. Consequences

### 🟢 Upsides

* **The cookie problem disappears structurally.** Once the domains differ, it is not a
  check that blocks the attack — the attack stops being possible. That removes one more
  "silently broken if you forget it" defense.
* **Port management goes away entirely.** The pools, exhaustion, and firewall ranges from
  ADR-0013 become irrelevant for web challenges, because containers are addressed by name.
* **The `127.0.0.1` mismatch is fixed.** The bug that keeps the proxy from working in the
  deployed configuration is resolved by the same change.
* **"Only 80/443 exposed" without a gateway.** ADR-0015's goal, achieved without adding a
  moving part — back in line with ADR-0009.
* **Instances lose internet access.** `internal: true` blocks reverse shells and outbound
  attacks. Today that is wide open.

### 🔴 Downsides / open items

* **Instances can still reach each other.** As written in 2-2, stage one does not fix this.
  **The path for pulling a flag out of someone else's instance stays open.** It is the
  status quo, but this document turns it into something knowingly deferred. The switch to
  per-instance networks needs a decision point tied to concurrent-instance count.
* **The proxy joins the instance network.** Challenge containers can then hit
  `backend-blue:8080` directly, bypassing nginx and whatever rate limiting or inspection
  sits in front. Removing that risk means isolating the proxy on its own across both
  networks — **which is a gateway.** This is the price of dropping the gateway, and I am
  writing it down rather than pretending it isn't there.
* **Another domain to buy and operate.** Registration, DNS, and a wildcard certificate.
  The certbot move to DNS-01 has to land first — the current setup cannot issue one.
* **Local development gets awkward.** Faking two domains with `nip.io` doesn't work:
  `127.0.0.1.nip.io` has `nip.io` as its registrable domain, so it **cannot reproduce the
  cookie boundary**. Meaning this particular defense goes unverified locally. Two fake
  domains via the hosts file would work, but it's fiddly.
* **Routing depends on the container name being the uuid.** Change the naming rule and
  routing breaks. Reading the name out of the registry (`uuid:`) would be safer, at the
  cost of one more lookup.
* **pwn is still outside all of this.** Raw TCP can't traverse the proxy, so nothing here
  covers it. Opening ports for it breaks the "80/443 only" property above. That's why
  ADR-0013's port pool survives for pwn, and whether to go the web-terminal route is still
  undecided.

## 4. References

### Internal documents

* [ADR-0016](<./[ADR-0016] 인스턴스 접근 인가 — 게이트웨이는 판정하지 않는다.md>) —
  the decision split (people → website, resources → CTF) still holds. **Only the
  enforcement point moves, from the gateway to the web proxy.** The reasoning for "a uuid
  is not a secret" is there too.
* [ADR-0015](<./[ADR-0015] 인스턴스 접근 경로 — 포트 노출 대신 게이트웨이.md>) — **on hold.**
  Its goal of "80/443 only" is achieved by 2-2 here without a gateway. The pwn discussion
  and the single-point-of-failure analysis remain valid, so it is not withdrawn.
* [ADR-0013](<./[ADR-0013] 인스턴스 포트 관리 — 임의 할당 대신 용도별 분리와 포트 풀.md>) —
  **largely moot for web challenges**, since host ports are no longer used. The pwn port
  pool stays.
* [ADR-0014](<./[ADR-0014] 인스턴스 수명(TTL) 정책 — 선언 대신 집행.md>) — TTL enforcement.
  With the website proxying, an expired instance can be refused right at that point.
* [ADR-0012](<./[ADR-0012] ctf 서버 컨테이너 간 통신 방식 확정.md>) —
  the `ctf-internal` / `nimda-network` split that 2-3 depends on.
* [ADR-0009](<./[ADR-0009] 조율자·에이전트 배포 구조 — 컨테이너 분리 대신 프로세스 관리.md>) —
  where "privileged process" comes from. The "hostile process" framing here is the
  boundary on the opposite side.

### Code

* Web `InstanceProxy.forward` — copies response headers through verbatim; the comment
  even spells out `Set-Cookie/Location 등 ... 전달`. This is where Problem 1 lives.
* Web `SubdomainInstanceProxyFilter.writeResponse` — the last step of that same path.
* Web `SubdomainInstanceProxyFilter.stripPlatformCookies` — covers the request direction
  only. Evidence that the instinct is right but the coverage is half.
* `agent/internal/runtime/docker.go` — `&network.NetworkingConfig{}` is empty, so
  containers land on the default bridge (Problem 2). `PortBindings` with
  `HostIP: "127.0.0.1"` is in the same file.
* `agent/cmd/agent/main.go` — `CTF_INSTANCE_HOST` defaults to `127.0.0.1`.
* Web `docker-compose.yml` — `backend-blue` is a container with
  `ports: 127.0.0.1:8081:8080` (evidence for Problem 2b), and the nginx volume
  `./certbot/www` shows the HTTP-01 setup.

### External references

* [RFC 6265 §5.1.3 — Domain Matching][rfc6265-domain] — the conditions under which a
  cookie may be set on a parent domain. The basis for a subdomain being able to write
  cookies on the registrable domain.
* [Public Suffix List](https://publicsuffix.org/) — what browsers use to decide where
  cookie-setting stops. `nimda.kr` is not on it, so it is not a boundary.
* [Content hosting for the modern web][google-hosting] — why Google serves user content
  from a separate domain. **The basis for treating the domain split as the real fix.**
* [OWASP Session Fixation](https://owasp.org/www-community/attacks/Session_fixation) —
  an attacker planting a session identifier on the victim. Problem 1 is exactly this shape.
* [Docker — bridge network driver](https://docs.docker.com/engine/network/drivers/bridge/) —
  embedded DNS on user-defined bridges (name resolution) and container-to-container
  behavior. Both why 2-2 works and why a single shared network doesn't isolate instances.
* [Docker — internal networks](https://docs.docker.com/reference/compose-file/networks/) —
  `internal: true` to block outbound egress.
* [Let's Encrypt — Challenge Types](https://letsencrypt.org/docs/challenge-types/) —
  wildcard certificates are issued only via DNS-01. Evidence that the current webroot
  setup cannot produce one.

[rfc6265-domain]: https://datatracker.ietf.org/doc/html/rfc6265#section-5.1.3
[google-hosting]: https://security.googleblog.com/2012/08/content-hosting-for-modern-web.html
