FROM ubuntu:24.04@sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    ca-certificates curl git python3 unzip procps openssh-client gcc libc6-dev \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://awscli.amazonaws.com/awscli-exe-linux-x86_64-2.17.44.zip -o /tmp/aws.zip \
    && echo 'ed46356f6358c0dfa30416899bf3efe0001689520ffce22c128f5bc617e8f4c3  /tmp/aws.zip' | sha256sum -c - \
    && unzip -q /tmp/aws.zip -d /tmp && /tmp/aws/install \
    && rm -rf /tmp/aws /tmp/aws.zip
RUN curl -fsSL https://nodejs.org/dist/v22.20.0/node-v22.20.0-linux-x64.tar.gz -o /tmp/node.tar.gz \
    && echo 'eeaccb0378b79406f2208e8b37a62479c70595e20be6b659125eb77dd1ab2a29  /tmp/node.tar.gz' | sha256sum -c - \
    && tar -C /usr/local --strip-components=1 -xzf /tmp/node.tar.gz && rm /tmp/node.tar.gz
ARG GO_VERSION=1.26.11
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tar.gz \
    && curl -fsSL 'https://go.dev/dl/?mode=json&include=all' -o /tmp/go.json \
    && python3 -c 'import json,hashlib; x=json.load(open("/tmp/go.json")); v=next(v for v in x if v["version"]=="go1.26.11"); f=next(f for f in v["files"] if f["filename"]=="go1.26.11.linux-amd64.tar.gz"); assert hashlib.sha256(open("/tmp/go.tar.gz","rb").read()).hexdigest()==f["sha256"]' \
    && tar -C /usr/local -xzf /tmp/go.tar.gz && rm /tmp/go.tar.gz /tmp/go.json
RUN useradd --create-home --uid 1001 --shell /bin/bash runner
ENV PATH=/usr/local/go/bin:$PATH GOTOOLCHAIN=local CGO_ENABLED=1 HOME=/home/runner
USER runner
RUN mkdir -p /home/runner/.cache/go-build /home/runner/go/pkg/mod
RUN git config --global --add safe.directory /src
WORKDIR /src
