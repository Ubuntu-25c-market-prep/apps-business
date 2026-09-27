# resume-builder

A static resume site: two files of content, served by nginx. No JavaScript, no
framework, no build step. Editing it is editing HTML.

```
resume-builder/
├── src/
│   ├── index.html      the content - this is the file you edit
│   └── style.css       the only stylesheet, no external requests
├── nginx.conf          server block: port 8080, /healthz
├── Dockerfile          copies src/ into nginx-unprivileged
└── README.md
```

## Editing it

Open `src/index.html` and replace the placeholder words. Everything is
placeholder: name, headline, contact details, both jobs, the skills and the
education entry.

**Keep the tags, change the words.** `style.css` selects on semantic elements -
`header`, `section`, `article`, `dl`, `ul` - and not on class names, so:

- renaming a heading or rewording a bullet is always safe;
- adding another job means copying an `<article>` block inside
  `<section id="experience">`;
- replacing a `<section>` with a `<div>` will silently lose its styling.

Dates use `<time datetime="YYYY-MM">`. The `datetime` attribute is the machine
-readable form and the text inside the element is what a reader sees; keep them
saying the same thing.

To preview, open `src/index.html` in a browser - `file://` is enough, because
there is nothing to serve and nothing to compile. Print preview is worth a look
too: the stylesheet has print rules that flatten the colours, print link URLs
after the link text, and keep entries from splitting across pages.

### Things deliberately not here

No web font, no CDN, no analytics. The page makes **no external requests at
all**, which is what lets `nginx.conf` set a `default-src 'self'` Content
Security Policy. If you add an external asset, that header has to change in the
same commit or the asset will be blocked.

## How the image gets built

Pushing to a branch and opening a pull request builds the image and pushes
nothing. Merging to `main` builds it again and pushes exactly one tag:

```
<ECR_REGISTRY>/dev-ecr-us-east-1/resume-builder:<commit sha>
```

`.github/workflows/resume-builder.yml` is the trigger; the work is in the
reusable `.github/workflows/build-push.yml`. The full image reference is
echoed at the end of the job and repeated in the run summary, because the next
step is pasting it into `gitops-argocd`.

**The tag is always the commit SHA, never `latest`.** The ECR repositories are
created `IMMUTABLE`, so a moving tag is not a style preference - the registry
rejects a second push to an existing tag outright. It is also what makes
rollback mean anything: the tag a deployment names keeps pointing at the same
bytes forever.

Registry and role come from repository variables, `ECR_REGISTRY` and
`AWS_PUSH_ROLE_ARN`. Credentials are obtained through GitHub OIDC at job time;
there is no access key in this repository.

`linux/amd64` only. The cluster's node group is `t3.medium`, which is x86_64.
Add `linux/arm64` to `platforms:` in `build-push.yml` when an arm64 node pool
exists - the Dockerfile needs no change.

### Building locally

```bash
docker build -t resume-builder:dev ./resume-builder
docker run --rm -p 8080:8080 resume-builder:dev
# http://localhost:8080  and  http://localhost:8080/healthz
```

## Running it in the cluster

The image runs as **uid 101** on **port 8080** and writes nothing outside
`/tmp`, which is what the unprivileged nginx base is for.

That satisfies Pod Security Admission `baseline`, which `app-dev` enforces. To
also pass the `restricted` audit that namespace runs, the Deployment must
supply what an image cannot declare for itself:

```yaml
securityContext:              # pod
  runAsNonRoot: true
  runAsUser: 101
  seccompProfile:
    type: RuntimeDefault
containers:
  - name: resume-builder
    securityContext:          # container
      allowPrivilegeEscalation: false
      readOnlyRootFilesystem: true
      capabilities:
        drop: ["ALL"]
    ports:
      - containerPort: 8080
    livenessProbe:
      httpGet: { path: /healthz, port: 8080 }
    readinessProbe:
      httpGet: { path: /healthz, port: 8080 }
```

`readOnlyRootFilesystem: true` works because the base image keeps its pid file
and every temp path under `/tmp`; mount an `emptyDir` at `/tmp` when you set
it.

Those manifests live in `gitops-argocd` under
`apps/resume-builder/overlays/dev/`, not here. The ApplicationSet generates an
Argo CD Application when `apps/*/overlays/dev/kustomization.yaml` exists, and
for no other reason - creating that file is the whole registration step.

Sizing note: `app-dev`'s ResourceQuota is built on roughly 100m CPU / 128Mi of
requests and 400m / 256Mi of limits per pod, **including the Istio sidecar**.
That namespace is labelled for sidecar injection, so the pod template must pin
the sidecar with `sidecar.istio.io/proxyCPU`, `proxyMemory`, `proxyCPULimit`
and `proxyMemoryLimit` annotations, or the injected proxy takes Istio's
defaults (2000m/1Gi limits) and eats the namespace quota on its own.
