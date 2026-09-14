# 0007: Response metadata budgets and V2 list fields

Status: qualified direction accepted; concrete metadata policy remains unresolved.

The concrete follow-up is [decision 0010](0010-response-header-design.md), proposed
after the streaming bridge implementation. It recommends a broader field audit,
weighted resource accounting, and separate encoded-output limits. Its details
still require user review; this record preserves the earlier qualification.

## Accepted qualification

The user accepted the qualified recommendation: keep the documented Lambda
envelope limit and semantics-preserving header translation, but do not freeze
the proposed 64 KiB/1,024-entry limits without stronger justification. The ten
fields below are an initial audit, not an AWS allowlist or an exhaustive set of
valid list-valued fields. Broader compatibility must be considered before fixing
that set as the library contract. No numeric metadata limit or exact joinable
set has been approved or implemented.

The numeric proposal was a library resource policy, not an AWS quota or a result
of header-allocation benchmarks. Its intended protection was to bound additional
snapshot allocations, not memory already allocated by the application. Current
AWS HTTP API quotas explicitly limit the request line and headers; that does not
establish the same limit for an edge response snapshot.
[HTTP API quotas](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-quotas.html)

## Question and evidence

Decision 0006 and the accepted plan review require bounded metadata snapshots
and an audited list of fields whose repeated values can be joined for V2. They
deliberately left the numeric budgets and initial field set unspecified.

The existing request helper preserves multiple values and canonicalizes keys;
the new response path must not inherit its unbounded copying or assume every
field is a list. AWS V2 provides a single string per header and a separate Cookies
array; V1 supports multiple values.
[AWS payload formats](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html)

HTTP does not define one universal field-size limit. A library metadata budget
is our resource policy, not a claim about gateway quotas.
[HTTP field limits](https://www.rfc-editor.org/rfc/rfc9110.html#section-5.4)

## Initial proposal retained for discussion (not accepted as written)

Before sorting, canonicalizing, or cloning a committed Header map, allow at most
64 KiB (65,536 bytes) of names plus values and 1,024 entries. Count each original
map key's bytes once and every value's bytes. Count max(1, len(values)) entries
per original key, including empty slices, so empty values cannot bypass the
entry bound. Include Set-Cookie and fields later stripped from the response.
Check incrementally without overflowing counters. Neither limit is configurable
in the first implementation.

The budget protects adapter-created copies; it cannot prevent an application
from allocating its own Header map. The separate 6 MiB serialized-envelope
limit still applies to Invoke. These metadata limits apply equally to raw and
typed entry points. Generated Content-Type/Content-Length have small fixed
overhead outside the application snapshot budget.

For V2, initially allow comma-space joining for exactly these repeated fields:

| Fields | Source |
| --- | --- |
| Accept-Ranges, Allow, Content-Encoding, Content-Language, Vary, WWW-Authenticate | [RFC 9110](https://www.rfc-editor.org/rfc/rfc9110.html) |
| Cache-Control | [RFC 9111 section 5.2](https://www.rfc-editor.org/rfc/rfc9111.html#section-5.2) |
| Access-Control-Allow-Headers, Access-Control-Allow-Methods, Access-Control-Expose-Headers | [Fetch field grammar](https://fetch.spec.whatwg.org/#http-new-header-syntax) |

Each has a list grammar. Preserve order, duplicates, and embedded commas without
parsing field-specific parameters. This authorizes joining, not semantic
validation of every field. A single value for any valid field passes through.
Set-Cookie keeps its separate Cookies treatment. Reject other repeated fields,
including Location, Content-Type, Access-Control-Allow-Origin, and unknown
extension fields; do not silently select the first or last value. Further list
fields can be added after checking their definitions and consumer requirements.

WWW-Authenticate allows multiple challenges, but some clients handle separate
lines better. V2 cannot promise separate lines; applications requiring them need
V1. Hop-by-hop fields are removed under decision 0006 before V2 joining.

## Alternatives and consequences

- Using the full payload budget for metadata permits disproportionate map/slice
  overhead. A byte limit alone does not constrain large slices of empty values.
- Much smaller limits could unnecessarily constrain cookies and application
  metadata. The proposed 64 KiB/1,024 bounds are library defaults chosen for a
  generous finite snapshot, not documented AWS allowances.
- An exhaustive registry or public join callback adds maintenance or API surface
  before there is a demonstrated need. The initial set covers common response,
  cache, authentication, and CORS fields; other repeated fields fail visibly.

## Resolution

The user agreed with the qualification above. Resolve evidence-based resource
budgets and broader field compatibility before implementing those policies.
Bounded envelope encoding and invocation lifetime work do not depend on them.
