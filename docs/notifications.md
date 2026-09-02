# Control-plane notifications

Werkt emits durable control-plane events for decisions and terminal failures.
Routes fan each event out to one or more operator-managed destinations without
giving provider credentials to automation code or making delivery part of an
automation transaction.

Supported events:

- `approval.requested`
- `approval.expiring`
- `approval.resolved`
- `run.failed`

Supported providers are ntfy, Telegram Bot API, Pushbullet, and a signed generic
webhook. Delivery is at least once: every destination receives a stable delivery
ID, retries use exponential backoff, ntfy receives that ID as
`X-Sequence-ID`, and webhooks receive it as both `Idempotency-Key` and
`X-Werkt-Delivery`.
Providers without a deduplication primitive can occasionally show a duplicate
if Werkt loses its database lease after the provider accepted a request.

## Configuration

Set `WERKT_NOTIFICATIONS_FILE` to an operator-owned JSON file. An inline
`WERKT_NOTIFICATIONS_JSON` value is also supported for small development
setups; configure only one. Files may contain secret names but never secret
values.

```json
{
  "destinations": [
    {
      "id": "operator-phone",
      "provider": "ntfy",
      "server": "https://notify.example.com",
      "topic": "automation-approvals",
      "tokenSecret": "notifications/ntfy/token"
    },
    {
      "id": "operator-chat",
      "provider": "telegram",
      "botTokenSecret": "notifications/telegram/bot-token",
      "chatId": "123456789"
    },
    {
      "id": "operator-pushbullet",
      "provider": "pushbullet",
      "accessTokenSecret": "notifications/pushbullet/access-token"
    },
    {
      "id": "incident-router",
      "provider": "webhook",
      "url": "https://events.example.com/werkt",
      "signingSecret": "notifications/webhook/signing-secret"
    }
  ],
  "routes": [
    {
      "events": ["approval.requested", "approval.expiring"],
      "destinations": ["operator-phone", "operator-chat"]
    },
    {
      "events": ["run.failed"],
      "automations": ["backup", "certificate-renewal"],
      "destinations": ["incident-router"]
    }
  ]
}
```

Omitting `automations` matches every automation. `"*"` has the same meaning;
other entries are exact automation IDs. Overlapping routes are deduplicated per
event and destination.

Store provider credentials with `werkt secret set`. ntfy accepts either
`tokenSecret` for bearer authentication or `basicSecret` for a vault value in
`username:password` form. Telegram requires `botTokenSecret` and `chatId`.
Pushbullet requires `accessTokenSecret` and can optionally target one `deviceId`
or `channelTag`. A webhook may use `authorizationSecret`, `signingSecret`, or
both. Signed webhooks receive an HMAC-SHA256 signature over
`<timestamp>.<delivery-id>.<raw-body>`.

`WERKT_PUBLIC_URL` controls links in messages. Approval messages always open the
signed-in Werkt inbox; providers cannot resolve an approval directly. The
default expiry reminder is 24 hours before expiry and can be changed with
`WERKT_NOTIFICATION_EXPIRY_WARNING`. `WERKT_NOTIFICATION_POLL` controls the
outbox polling interval.

Delivery results are recorded as `notification.delivered` and
`notification.failed` audit events. Payloads intentionally omit approval field
values, runtime logs, and exception text. Provider credentials are resolved
only immediately before a request and are never stored in the outbox.
