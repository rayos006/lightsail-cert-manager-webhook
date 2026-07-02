# lightsail-cert-manager-webhook

A cert-manager ACME DNS-01 solver webhook for AWS Lightsail DNS.

Why: cert-manager has built-in solvers for Route53, Cloudflare, and many
others — but not AWS Lightsail's DNS service (which has its own control
plane distinct from Route53). No maintained community webhook existed at
the time of writing, so this is one.

## How it works

At challenge time cert-manager calls this webhook's `Present` method with
the domain and the challenge value. We:

1. Load AWS credentials from a Secret referenced in the ClusterIssuer config.
2. Look up the existing DomainEntries via `GetDomain` — if the challenge
   TXT already exists (idempotency), return without doing anything.
3. Call `CreateDomainEntry` with Type=`TXT`, Name=the FQDN (e.g.
   `_acme-challenge.beszel.example.com`), Target=the quoted challenge value.

`CleanUp` reverses this via `DeleteDomainEntry`. Both operations only touch
records whose value matches — concurrent challenges for the same name are
safe.

## Requirements

- AWS Lightsail-managed domain (not a Route53-hosted zone that shares the
  same name). Domain must be listed under Lightsail → Domains.
- IAM user or role with:
  - `lightsail:GetDomain`
  - `lightsail:CreateDomainEntry`
  - `lightsail:DeleteDomainEntry`
- Lightsail's DNS control plane is only in `us-east-1` — the region config
  defaults there.

## Installation

Container image is published to Docker Hub as
`rayos006/lightsail-cert-manager-webhook:vX.Y.Z`.

The Helm chart lives in `deploy/lightsail-webhook/`. Install with:

```bash
helm install lightsail-webhook \
  ./deploy/lightsail-webhook \
  --namespace cert-manager \
  --set groupName=acme.example.com
```

or via a Flux HelmRelease (see `homie-lab/infrastructure/controllers/`).

## Usage in a ClusterIssuer

Create a Secret in the cert-manager namespace with the IAM creds:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: lightsail-dns-creds
  namespace: cert-manager
type: Opaque
stringData:
  access-key-id: AKIA...
  secret-access-key: ...
```

Then reference it in a ClusterIssuer:

```yaml
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-lightsail
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: you@example.com
    privateKeySecretRef:
      name: letsencrypt-lightsail-account
    solvers:
      - dns01:
          webhook:
            groupName: acme.example.com
            solverName: lightsail
            config:
              region: us-east-1
              accessKeyIDSecretRef:
                name: lightsail-dns-creds
                key: access-key-id
              secretAccessKeySecretRef:
                name: lightsail-dns-creds
                key: secret-access-key
```

## Local development

```bash
go mod tidy    # populate go.sum
go build ./...
go vet ./...
```

To test end-to-end, cert-manager provides a conformance test suite. Point
it at real Lightsail creds + a test domain and run `go test ./...`.

## License

MIT (same as the cert-manager webhook-example base).
